package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Observation es un archivo visto durante el inventario.
type Observation struct {
	Tenant, Source, Scan uuid.UUID
	RelPath              string
	Size                 int64
	MTime                time.Time
	Identity             string
	SkipReason           string
	RulesVersion         string
	Full                 bool   // escaneo completo: reprocesa aunque la versión no cambie
	Class                string // light | heavy (cola de workers)
	Priority             int    // menor = antes; los archivos grandes van con prioridad mayor
	Deferred             bool   // archivo grande: queda pendiente hasta `scan process-pending`
}

// ObserveFile registra el archivo y, si su versión es nueva (o el escaneo es
// completo), crea la tarea de extracción. Idempotente: repetir el inventario
// tras un reinicio no duplica versiones ni tareas.
func (s *Store) ObserveFile(ctx context.Context, o Observation) (queued bool, err error) {
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var fileID uuid.UUID
		var lastSuccess *uuid.UUID
		state := "discovered"
		if o.SkipReason != "" {
			state = "skipped"
		}
		err := tx.QueryRow(ctx, `INSERT INTO files(id, tenant_id, source_id, relative_path, size_bytes, mtime, file_identity,
				state, skip_reason, last_observed_at, last_scan_id, active)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,now(),$10,true)
			ON CONFLICT (tenant_id, source_id, relative_path) DO UPDATE SET
				size_bytes = EXCLUDED.size_bytes, mtime = EXCLUDED.mtime, file_identity = EXCLUDED.file_identity,
				skip_reason = EXCLUDED.skip_reason, last_observed_at = now(), last_scan_id = EXCLUDED.last_scan_id, active = true,
				state = CASE WHEN EXCLUDED.state = 'skipped' THEN 'skipped' ELSE files.state END
			RETURNING id, last_success_version`,
			uuid.New(), o.Tenant, o.Source, o.RelPath, o.Size, o.MTime, o.Identity, state, nullable(o.SkipReason), o.Scan,
		).Scan(&fileID, &lastSuccess)
		if err != nil || o.SkipReason != "" {
			return err
		}
		var versionID uuid.UUID
		err = tx.QueryRow(ctx, `INSERT INTO file_versions(id, file_id, scan_id, size_bytes, mtime, file_identity, rules_version, state)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'queued')
			ON CONFLICT (file_id, size_bytes, mtime, file_identity, rules_version) DO UPDATE SET file_id = EXCLUDED.file_id
			RETURNING id`, uuid.New(), fileID, o.Scan, o.Size, o.MTime, o.Identity, o.RulesVersion).Scan(&versionID)
		if err != nil {
			return err
		}
		if lastSuccess != nil && *lastSuccess == versionID && !o.Full {
			return nil // sin cambios desde el último procesamiento exitoso (incremental)
		}
		class, prio := o.Class, o.Priority
		if class == "" {
			class = "light"
		}
		if prio == 0 {
			prio = 100
		}
		taskState := "pending"
		var deferredAt *time.Time
		if o.Deferred {
			now := time.Now()
			taskState, deferredAt = "deferred", &now
		}
		tag, err := tx.Exec(ctx, `INSERT INTO file_tasks(id, scan_id, file_version_id, kind, state, class, priority, size_bytes, deferred_at)
			VALUES ($1,$2,$3,'extract',$7,$4,$5,$6,$8)
			ON CONFLICT (file_version_id, kind) DO UPDATE SET
				scan_id = EXCLUDED.scan_id, state = EXCLUDED.state, attempt_count = 0, available_at = now(),
				lease_owner = NULL, lease_expires_at = NULL, last_error_code = NULL,
				class = EXCLUDED.class, priority = EXCLUDED.priority, size_bytes = EXCLUDED.size_bytes,
				deferred_at = EXCLUDED.deferred_at
			WHERE file_tasks.scan_id <> EXCLUDED.scan_id AND file_tasks.state <> 'processing'`,
			uuid.New(), o.Scan, versionID, class, prio, o.Size, taskState, deferredAt)
		if err != nil {
			return err
		}
		queued = tag.RowsAffected() > 0
		if queued {
			_, err = tx.Exec(ctx, "UPDATE files SET state='queued' WHERE id=$1", fileID)
		}
		return err
	})
	return queued, err
}

// Task es una tarea reclamada con lease.
type Task struct {
	ID, FileVersionID, FileID uuid.UUID
	Owner                     string
	Attempt                   int
	RelPath                   string
	Size                      int64
	MTime                     time.Time
	Identity                  string
}

// ClaimTasks reclama hasta n tareas de la clase indicada, disponibles o con
// lease vencido, con FOR UPDATE SKIP LOCKED (ADR-0002). Orden: prioridad
// (archivos grandes al final) y luego tamaño ascendente.
func (s *Store) ClaimTasks(ctx context.Context, scan uuid.UUID, class, owner string, n int, lease time.Duration) ([]Task, error) {
	rows, err := s.pool.Query(ctx, `
		WITH c AS (
			SELECT id FROM file_tasks
			WHERE scan_id = $1 AND class = $5 AND (
				(state IN ('pending','retry_wait') AND available_at <= now()) OR
				(state = 'processing' AND lease_expires_at < now()))
			ORDER BY priority, size_bytes, available_at
			FOR UPDATE SKIP LOCKED
			LIMIT $2)
		UPDATE file_tasks t SET state = 'processing', lease_owner = $3,
			lease_expires_at = now() + make_interval(secs => $4), attempt_count = t.attempt_count + 1
		FROM c, file_versions v, files f
		WHERE t.id = c.id AND v.id = t.file_version_id AND f.id = v.file_id
		RETURNING t.id, t.file_version_id, f.id, t.attempt_count, f.relative_path, v.size_bytes, v.mtime, v.file_identity`,
		scan, n, owner, lease.Seconds(), class)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t := Task{Owner: owner}
		if err := rows.Scan(&t.ID, &t.FileVersionID, &t.FileID, &t.Attempt, &t.RelPath, &t.Size, &t.MTime, &t.Identity); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ReclassifyTask devuelve la tarea a la cola de otra clase (p. ej. un ".txt"
// que en realidad es PDF). No consume un intento.
func (s *Store) ReclassifyTask(ctx context.Context, t Task, class string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE file_tasks SET class=$3, state='pending', lease_owner=NULL, lease_expires_at=NULL,
			attempt_count = GREATEST(attempt_count - 1, 0), available_at = now()
		WHERE id=$1 AND lease_owner=$2 AND state='processing'`, t.ID, t.Owner, class)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

// PendingTasks cuenta tareas que aún pueden procesarse en el escaneo.
func (s *Store) PendingTasks(ctx context.Context, scan uuid.UUID) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM file_tasks WHERE scan_id=$1 AND state IN ('pending','retry_wait','processing')`, scan).Scan(&n)
	return n, err
}

// DeferredFile es un archivo grande que quedó pendiente (no procesado).
type DeferredFile struct {
	TaskID     uuid.UUID
	FileID     uuid.UUID
	RelPath    string
	Size       int64
	Class      string
	DeferredAt time.Time
}

// ListDeferred lista los archivos diferidos de un escaneo, del más pequeño al
// más grande. Paginación por clave: after es el TaskID del último de la página
// anterior (uuid.Nil para empezar).
func (s *Store) ListDeferred(ctx context.Context, tenant, scan, after uuid.UUID, limit int) ([]DeferredFile, error) {
	rows, err := s.pool.Query(ctx, `SELECT t.id, f.id, f.relative_path, t.size_bytes, t.class, COALESCE(t.deferred_at, now())
		FROM file_tasks t JOIN scans s ON s.id = t.scan_id
			JOIN file_versions v ON v.id = t.file_version_id JOIN files f ON f.id = v.file_id
		WHERE t.scan_id = $1 AND s.tenant_id = $2 AND t.state = 'deferred'
			AND ($3 = '00000000-0000-0000-0000-000000000000'::uuid OR
				(t.size_bytes, t.id) > (SELECT size_bytes, id FROM file_tasks WHERE id = $3))
		ORDER BY t.size_bytes, t.id LIMIT $4`, scan, tenant, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeferredFile
	for rows.Next() {
		var d DeferredFile
		if err := rows.Scan(&d.TaskID, &d.FileID, &d.RelPath, &d.Size, &d.Class, &d.DeferredAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ReleaseDeferred pasa a la cola los archivos diferidos del escaneo (todos, o
// solo los de tamaño <= maxBytes si es > 0) y reabre el escaneo terminado
// para que los workers los procesen. Devuelve cuántos liberó; con 0 el escaneo no cambia.
// Falla con ErrScanActive si la fuente ya tiene otro escaneo en curso.
func (s *Store) ReleaseDeferred(ctx context.Context, tenant, scan uuid.UUID, maxBytes int64) (int, error) {
	var n int
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, "SELECT status FROM scans WHERE id=$1 AND tenant_id=$2 FOR UPDATE", scan, tenant).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		// Solo escaneos terminados: uno en curso ya tiene sus propios workers.
		if status != "completed" && status != "partial" {
			return ErrInvalidState
		}
		tag, err := tx.Exec(ctx, `UPDATE file_tasks SET state='pending', available_at=now(), attempt_count=0
			WHERE scan_id=$1 AND state='deferred' AND ($2 <= 0 OR size_bytes <= $2)`, scan, maxBytes)
		if err != nil {
			return err
		}
		n = int(tag.RowsAffected())
		if n == 0 {
			return nil
		}
		_, err = tx.Exec(ctx, "UPDATE scans SET status='running', completed_at=NULL WHERE id=$1", scan)
		if err != nil && strings.Contains(err.Error(), "scans_one_active_per_source") {
			return ErrScanActive
		}
		return err
	})
	return n, err
}

// LocationRow es una ubicación lista para persistir (sheet ya cifrada).
type LocationRow struct {
	Key      []byte
	Page     int
	SheetEnc []byte
	Row      int
	Column   string
	Line     int
}

// FindingRow agrupa las ocurrencias de un identificador en una versión.
type FindingRow struct {
	Type        string
	KeyVersion  int
	Token       []byte
	Status      string
	Method      string
	Confidence  float64
	Detector    string
	Occurrences int
	Locations   []LocationRow
}

type FileResult struct {
	MimeKind string
	Partial  bool
	Findings []FindingRow
	Warnings map[string]int
}

// CompleteTask publica atómicamente hallazgos + estado de versión + última
// versión exitosa. Si el lease se perdió (otro worker lo reclamó) no escribe.
func (s *Store) CompleteTask(ctx context.Context, tenant uuid.UUID, t Task, r FileResult) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE file_tasks SET state='completed', lease_owner=NULL, lease_expires_at=NULL, last_error_code=NULL
			WHERE id=$1 AND lease_owner=$2 AND state='processing'`, t.ID, t.Owner)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrLeaseLost
		}
		for _, f := range r.Findings {
			var identID uuid.UUID
			if err := tx.QueryRow(ctx, `INSERT INTO identifiers(id, tenant_id, type, key_version, lookup_hmac) VALUES ($1,$2,$3,$4,$5)
				ON CONFLICT (tenant_id, type, key_version, lookup_hmac) DO UPDATE SET type = EXCLUDED.type
				RETURNING id`, uuid.New(), tenant, f.Type, f.KeyVersion, f.Token).Scan(&identID); err != nil {
				return err
			}
			var findingID uuid.UUID
			if err := tx.QueryRow(ctx, `INSERT INTO findings(id, tenant_id, identifier_id, file_version_id, detection_method,
					confidence, status, detector_version, occurrences)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
				ON CONFLICT (file_version_id, identifier_id, detector_version) DO UPDATE SET
					occurrences = EXCLUDED.occurrences, confidence = EXCLUDED.confidence, detection_method = EXCLUDED.detection_method,
					status = CASE WHEN findings.status IN ('confirmed_by_authorized_reviewer','rejected_by_reviewer')
						THEN findings.status ELSE EXCLUDED.status END
				RETURNING id`, uuid.New(), tenant, identID, t.FileVersionID, f.Method, f.Confidence, f.Status, f.Detector,
				f.Occurrences).Scan(&findingID); err != nil {
				return err
			}
			batch := &pgx.Batch{}
			for _, l := range f.Locations {
				batch.Queue(`INSERT INTO finding_locations(finding_id, location_key, page_number, sheet_name_encrypted, row_number, column_ref, line_number)
					VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (finding_id, location_key) DO NOTHING`,
					findingID, l.Key, nullInt(l.Page), l.SheetEnc, nullInt(l.Row), nullable(l.Column), nullInt(l.Line))
			}
			if err := tx.SendBatch(ctx, batch).Close(); err != nil {
				return err
			}
		}
		state := "completed"
		if r.Partial {
			state = "partial"
		}
		if _, err := tx.Exec(ctx, `UPDATE file_versions SET state=$2, processed_at=now() WHERE id=$1`, t.FileVersionID, state); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE files SET state=$2, last_success_version=$3, mime_type=$4 WHERE id=$1`,
			t.FileID, state, t.FileVersionID, r.MimeKind)
		if err != nil {
			return err
		}
		if len(r.Warnings) > 0 {
			for code := range r.Warnings {
				if err := upsertError(ctx, tx, t, "extract", "warn_"+code); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func upsertError(ctx context.Context, tx pgx.Tx, t Task, stage, code string) error {
	_, err := tx.Exec(ctx, `INSERT INTO scan_errors(scan_id, file_id, stage, error_code)
		SELECT scan_id, $2, $3, $4 FROM file_tasks WHERE id = $1
		ON CONFLICT (scan_id, file_id, stage, error_code) DO UPDATE SET attempts = scan_errors.attempts + 1, last_seen_at = now()`,
		t.ID, t.FileID, stage, code)
	return err
}

// FailTask registra el error y programa reintento con backoff exponencial o
// marca la tarea como fallida (DLQ lógica) al superar retryMax.
// Los resultados de versiones anteriores se conservan (quedan como stale).
func (s *Store) FailTask(ctx context.Context, t Task, stage, code string, retryable bool, retryMax int, fileState string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		retry := retryable && t.Attempt <= retryMax
		taskState, verState := "failed", "failed"
		if fileState == "stale" {
			taskState, verState = "skipped", "stale"
		} else if fileState == "skipped" {
			taskState, verState = "skipped", "skipped"
		}
		backoff := time.Duration(0)
		if retry {
			taskState = "retry_wait"
			backoff = time.Duration(1<<min(t.Attempt, 8)) * 5 * time.Second
		}
		tag, err := tx.Exec(ctx, `UPDATE file_tasks SET state=$3, last_error_code=$4, lease_owner=NULL, lease_expires_at=NULL,
				available_at = now() + make_interval(secs => $5)
			WHERE id=$1 AND lease_owner=$2 AND state='processing'`, t.ID, t.Owner, taskState, code, backoff.Seconds())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrLeaseLost
		}
		if err := upsertError(ctx, tx, t, stage, code); err != nil {
			return err
		}
		if retry {
			return nil
		}
		if _, err := tx.Exec(ctx, "UPDATE file_versions SET state=$2, processed_at=now() WHERE id=$1", t.FileVersionID, verState); err != nil {
			return err
		}
		// Un archivo omitido al procesar (p. ej. cifrado) guarda el motivo para el reporte de cobertura.
		_, err = tx.Exec(ctx, `UPDATE files SET state=$2, skip_reason = CASE WHEN $2 = 'skipped' THEN $3 ELSE skip_reason END
			WHERE id=$1`, t.FileID, fileState, code)
		return err
	})
}

// RecordScanError registra errores sin archivo asociado (p. ej. raíz caída).
func (s *Store) RecordScanError(ctx context.Context, scan uuid.UUID, stage, code string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO scan_errors(scan_id, file_id, stage, error_code) VALUES ($1, NULL, $2, $3)
		ON CONFLICT (scan_id, file_id, stage, error_code) DO UPDATE SET attempts = scan_errors.attempts + 1, last_seen_at = now()`,
		scan, stage, code)
	return err
}

// Progress resume el avance de un escaneo.
type Progress struct {
	Tasks        map[string]int `json:"tasks"`
	FilesSeen    int            `json:"files_seen"`
	FilesSkipped map[string]int `json:"files_skipped"`
	FilesPartial int            `json:"files_partial"`
	FilesFailed  int            `json:"files_failed"`
	Errors       int            `json:"error_events"`
	// Archivos grandes diferidos: no procesados, pendientes de `scan process-pending`.
	DeferredFiles int   `json:"deferred_files"`
	DeferredBytes int64 `json:"deferred_bytes"`
}

func (s *Store) ScanProgress(ctx context.Context, scan uuid.UUID) (*Progress, error) {
	p := &Progress{Tasks: map[string]int{}, FilesSkipped: map[string]int{}}
	rows, err := s.pool.Query(ctx, "SELECT state, count(*) FROM file_tasks WHERE scan_id=$1 GROUP BY state", scan)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return nil, err
		}
		p.Tasks[st] = n
	}
	rows.Close()
	rows, err = s.pool.Query(ctx, `SELECT state, COALESCE(skip_reason,''), count(*) FROM files WHERE last_scan_id=$1 GROUP BY 1,2`, scan)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var st, reason string
		var n int
		if err := rows.Scan(&st, &reason, &n); err != nil {
			rows.Close()
			return nil, err
		}
		p.FilesSeen += n
		switch st {
		case "skipped":
			p.FilesSkipped[reason] += n
		case "partial":
			p.FilesPartial += n
		case "failed":
			p.FilesFailed += n
		}
	}
	rows.Close()
	err = s.pool.QueryRow(ctx, `SELECT count(*), COALESCE(sum(size_bytes),0) FROM file_tasks WHERE scan_id=$1 AND state='deferred'`,
		scan).Scan(&p.DeferredFiles, &p.DeferredBytes)
	if err != nil {
		return nil, err
	}
	err = s.pool.QueryRow(ctx, "SELECT COALESCE(sum(attempts),0) FROM scan_errors WHERE scan_id=$1 AND error_code NOT LIKE 'warn_%'", scan).Scan(&p.Errors)
	return p, err
}

// FinishScan cierra el escaneo como completed o partial según cobertura.
func (s *Store) FinishScan(ctx context.Context, tenant, scan uuid.UUID) (string, error) {
	p, err := s.ScanProgress(ctx, scan)
	if err != nil {
		return "", err
	}
	status := "completed"
	skippedUncovered := 0
	for _, n := range p.FilesSkipped {
		skippedUncovered += n
	}
	// Con archivos diferidos la cobertura es incompleta: el escaneo queda partial.
	if p.FilesPartial > 0 || p.FilesFailed > 0 || skippedUncovered > 0 || p.Tasks["skipped"] > 0 || p.DeferredFiles > 0 {
		status = "partial"
	}
	cov, _ := json.Marshal(p)
	tag, err := s.pool.Exec(ctx, `UPDATE scans SET status=$3, completed_at=now(), coverage_summary=$4
		WHERE id=$1 AND tenant_id=$2 AND status='running'`, scan, tenant, status, cov)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", ErrInvalidState
	}
	return status, nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullInt(i int) *int {
	if i == 0 {
		return nil
	}
	return &i
}
