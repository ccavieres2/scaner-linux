package persistence

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Consultas de solo lectura para la interfaz web: resumen por fuente, lista
// de escaneos y archivos de una fuente con su cantidad de hallazgos. Nunca
// devuelven valores de identificadores (solo conteos).

type ScanSummary struct {
	ID          uuid.UUID  `json:"scan_id"`
	SourceName  string     `json:"source"`
	Type        string     `json:"type"`
	Status      string     `json:"status"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

func (s *Store) ListScans(ctx context.Context, tenant uuid.UUID, limit int) ([]ScanSummary, error) {
	rows, err := s.pool.Query(ctx, `SELECT sc.id, src.logical_name, sc.type, sc.status, sc.started_at, sc.completed_at
		FROM scans sc JOIN sources src ON src.id = sc.source_id
		WHERE sc.tenant_id = $1 ORDER BY sc.started_at DESC LIMIT $2`, tenant, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ScanSummary, error) {
		var x ScanSummary
		err := r.Scan(&x.ID, &x.SourceName, &x.Type, &x.Status, &x.StartedAt, &x.CompletedAt)
		return x, err
	})
}

type SourceSummary struct {
	Name         string     `json:"source"`
	Enabled      bool       `json:"enabled"`
	Files        int        `json:"files"`
	WithFindings int        `json:"files_with_findings"`
	Partial      int        `json:"files_partial"`
	Failed       int        `json:"files_failed"`
	Skipped      int        `json:"files_skipped"`
	LastScanAt   *time.Time `json:"last_completed_at"`
}

func (s *Store) SourceSummaries(ctx context.Context, tenant uuid.UUID) ([]SourceSummary, error) {
	rows, err := s.pool.Query(ctx, `SELECT src.logical_name, src.enabled,
			count(f.id),
			count(f.id) FILTER (WHERE EXISTS (SELECT 1 FROM findings fi
				WHERE fi.file_version_id = f.last_success_version AND fi.status <> 'rejected_by_reviewer')),
			count(f.id) FILTER (WHERE f.state = 'partial'),
			count(f.id) FILTER (WHERE f.state = 'failed'),
			count(f.id) FILTER (WHERE f.state = 'skipped'),
			(SELECT max(sc.completed_at) FROM scans sc WHERE sc.source_id = src.id AND sc.status IN ('completed','partial'))
		FROM sources src LEFT JOIN files f ON f.source_id = src.id AND f.active
		WHERE src.tenant_id = $1
		GROUP BY src.id ORDER BY src.logical_name`, tenant)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SourceSummary, error) {
		var x SourceSummary
		err := r.Scan(&x.Name, &x.Enabled, &x.Files, &x.WithFindings, &x.Partial, &x.Failed, &x.Skipped, &x.LastScanAt)
		return x, err
	})
}

type SourceFile struct {
	ID       uuid.UUID
	RelPath  string
	Size     int64
	Kind     *string
	State    string
	RUTs     int
	Emails   int
	OCR      bool
	Deferred bool
	// Reason: código del motivo si fue omitido (skip_reason) o falló (último
	// error de la tarea). Nunca contiene datos del archivo.
	Reason string
}

// ListSourceFiles pagina (por offset, ordenado por ruta) los archivos activos
// de una fuente y devuelve también la raíz de la fuente. ok=false si la
// fuente no existe.
func (s *Store) ListSourceFiles(ctx context.Context, tenant uuid.UUID, source string, onlyFindings bool, offset, limit int) ([]SourceFile, string, bool, error) {
	var sid uuid.UUID
	var root string
	err := s.pool.QueryRow(ctx, `SELECT id, root_path FROM sources WHERE tenant_id = $1 AND logical_name = $2`, tenant, source).Scan(&sid, &root)
	if err == pgx.ErrNoRows {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	rows, err := s.pool.Query(ctx, `SELECT f.id, f.relative_path, f.size_bytes, f.mime_type, f.state,
			count(fd.id) FILTER (WHERE i.type = 'rut'),
			count(fd.id) FILTER (WHERE i.type = 'email'),
			COALESCE(bool_or(fd.detection_method = 'ocr'), false),
			EXISTS (SELECT 1 FROM file_tasks t JOIN file_versions v ON v.id = t.file_version_id
			        WHERE v.file_id = f.id AND t.state = 'deferred'),
			COALESCE(CASE f.state
				WHEN 'skipped' THEN f.skip_reason
				WHEN 'failed' THEN (SELECT t.last_error_code FROM file_tasks t JOIN file_versions v ON v.id = t.file_version_id
				                    WHERE v.file_id = f.id AND t.last_error_code IS NOT NULL
				                    ORDER BY v.observed_at DESC LIMIT 1)
			END, '')
		FROM files f
		LEFT JOIN findings fd ON fd.file_version_id = f.last_success_version AND fd.status <> 'rejected_by_reviewer'
		LEFT JOIN identifiers i ON i.id = fd.identifier_id
		WHERE f.tenant_id = $1 AND f.source_id = $2 AND f.active
		GROUP BY f.id
		HAVING NOT $3 OR count(fd.id) > 0
		ORDER BY f.relative_path OFFSET $4 LIMIT $5`, tenant, sid, onlyFindings, offset, limit)
	if err != nil {
		return nil, root, true, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (SourceFile, error) {
		var x SourceFile
		err := r.Scan(&x.ID, &x.RelPath, &x.Size, &x.Kind, &x.State, &x.RUTs, &x.Emails, &x.OCR, &x.Deferred, &x.Reason)
		return x, err
	})
	return out, root, true, err
}
