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

// pendingSet: archivos que esperan lectura por tamaño (scan process-pending).
// No depende de la fila: PostgreSQL lo calcula una vez (índice parcial
// file_tasks_deferred) en vez de una subconsulta por archivo.
const pendingSet = `(SELECT v.file_id FROM file_tasks t JOIN file_versions v ON v.id = t.file_version_id
	WHERE t.state = 'deferred')`

type SourceSummary struct {
	Name         string     `json:"source"`
	Enabled      bool       `json:"enabled"`
	Files        int        `json:"files"`
	Scanned      int        `json:"files_scanned"`
	Pending      int        `json:"files_pending"`
	WithFindings int        `json:"files_with_findings"`
	Partial      int        `json:"files_partial"`
	Failed       int        `json:"files_failed"`
	Skipped      int        `json:"files_skipped"`
	LastScanAt   *time.Time `json:"last_completed_at"`
}

// SourceSummaries resume cada fuente (o solo una, si source no está vacío) en
// una sola pasada sobre los archivos: con millones de archivos, una subconsulta
// por archivo superaba el tiempo máximo de respuesta de la API.
func (s *Store) SourceSummaries(ctx context.Context, tenant uuid.UUID, source string) ([]SourceSummary, error) {
	rows, err := s.pool.Query(ctx, `WITH pend AS (SELECT DISTINCT file_id FROM `+pendingSet+` x),
		hit AS (SELECT DISTINCT file_version_id FROM findings
			WHERE tenant_id = $1 AND status <> 'rejected_by_reviewer')
		SELECT src.logical_name, src.enabled,
			count(f.id),
			count(f.id) FILTER (WHERE f.state IN ('completed','partial') AND p.file_id IS NULL),
			count(p.file_id),
			count(h.file_version_id),
			count(f.id) FILTER (WHERE f.state = 'partial'),
			count(f.id) FILTER (WHERE f.state = 'failed'),
			count(f.id) FILTER (WHERE f.state = 'skipped'),
			(SELECT max(sc.completed_at) FROM scans sc WHERE sc.source_id = src.id AND sc.status IN ('completed','partial'))
		FROM sources src
		LEFT JOIN files f ON f.source_id = src.id AND f.active
		LEFT JOIN pend p ON p.file_id = f.id
		LEFT JOIN hit h ON h.file_version_id = f.last_success_version
		WHERE src.tenant_id = $1 AND ($2 = '' OR src.logical_name = $2)
		GROUP BY src.id ORDER BY src.logical_name`, tenant, source)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SourceSummary, error) {
		var x SourceSummary
		err := r.Scan(&x.Name, &x.Enabled, &x.Files, &x.Scanned, &x.Pending, &x.WithFindings, &x.Partial, &x.Failed, &x.Skipped, &x.LastScanAt)
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
// fuente no existe. filter: "" (todos), findings, scanned, pending o problems
// (omitidos y fallidos).
func (s *Store) ListSourceFiles(ctx context.Context, tenant uuid.UUID, source, filter string, offset, limit int) ([]SourceFile, string, bool, error) {
	var sid uuid.UUID
	var root string
	err := s.pool.QueryRow(ctx, `SELECT id, root_path FROM sources WHERE tenant_id = $1 AND logical_name = $2`, tenant, source).Scan(&sid, &root)
	if err == pgx.ErrNoRows {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	// Primero se elige la página (recorriendo el índice por ruta y parando al
	// completarla) y recién después se cuentan hallazgos de esos archivos.
	rows, err := s.pool.Query(ctx, `WITH page AS (
			SELECT f.id, f.relative_path, f.size_bytes, f.mime_type, f.state, f.skip_reason, f.last_success_version
			FROM files f
			WHERE f.tenant_id = $1 AND f.source_id = $2 AND f.active AND CASE $3::text
				WHEN 'findings' THEN EXISTS (SELECT 1 FROM findings fi
					WHERE fi.file_version_id = f.last_success_version AND fi.status <> 'rejected_by_reviewer')
				WHEN 'scanned' THEN f.state IN ('completed','partial') AND f.id NOT IN `+pendingSet+`
				WHEN 'pending' THEN f.id IN `+pendingSet+`
				WHEN 'problems' THEN f.state IN ('failed','skipped')
				ELSE true END
			ORDER BY f.relative_path OFFSET $4 LIMIT $5)
		SELECT p.id, p.relative_path, p.size_bytes, p.mime_type, p.state,
			count(fd.id) FILTER (WHERE i.type = 'rut'),
			count(fd.id) FILTER (WHERE i.type = 'email'),
			COALESCE(bool_or(fd.detection_method = 'ocr'), false),
			p.id IN `+pendingSet+`,
			COALESCE(CASE p.state
				WHEN 'skipped' THEN p.skip_reason
				WHEN 'failed' THEN (SELECT t.last_error_code FROM file_tasks t JOIN file_versions v ON v.id = t.file_version_id
				                    WHERE v.file_id = p.id AND t.last_error_code IS NOT NULL
				                    ORDER BY v.observed_at DESC LIMIT 1)
			END, '')
		FROM page p
		LEFT JOIN findings fd ON fd.file_version_id = p.last_success_version AND fd.status <> 'rejected_by_reviewer'
		LEFT JOIN identifiers i ON i.id = fd.identifier_id
		GROUP BY p.id, p.relative_path, p.size_bytes, p.mime_type, p.state, p.skip_reason
		ORDER BY p.relative_path`, tenant, sid, filter, offset, limit)
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
