package persistence

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// MaxLocationsPerFinding acota la exposición y el tamaño de respuesta.
const MaxLocationsPerFinding = 20

type SearchLocation struct {
	Page     int
	SheetEnc []byte
	Row      int
	Column   string
	Line     int
}

type SearchResult struct {
	FileID        uuid.UUID
	FileVersionID uuid.UUID
	RelPath       string
	VersionState  string
	ProcessedAt   *time.Time
	Status        string
	Method        string
	Confidence    float64
	Occurrences   int
	Locations     []SearchLocation
}

// Search busca por tokens HMAC (uno por versión de clave activa) entre las
// últimas versiones publicadas. Paginación por file_id (cursor keyset).
func (s *Store) Search(ctx context.Context, tenant uuid.UUID, typ string, tokens [][]byte, after uuid.UUID, limit int) ([]SearchResult, int, error) {
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(DISTINCT f.id)
		FROM identifiers i
		JOIN findings fd ON fd.identifier_id = i.id AND fd.tenant_id = i.tenant_id
		JOIN files f ON f.last_success_version = fd.file_version_id AND f.tenant_id = i.tenant_id AND f.active
		WHERE i.tenant_id = $1 AND i.type = $2 AND i.lookup_hmac = ANY($3)
		  AND fd.status <> 'rejected_by_reviewer'`, tenant, typ, tokens).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `SELECT f.id, fv.id, f.relative_path, fv.state, fv.processed_at, fd.id, fd.status,
			fd.detection_method, fd.confidence, fd.occurrences
		FROM identifiers i
		JOIN findings fd ON fd.identifier_id = i.id AND fd.tenant_id = i.tenant_id
		JOIN file_versions fv ON fv.id = fd.file_version_id
		JOIN files f ON f.last_success_version = fv.id AND f.tenant_id = i.tenant_id AND f.active
		WHERE i.tenant_id = $1 AND i.type = $2 AND i.lookup_hmac = ANY($3) AND f.id > $4
		  AND fd.status <> 'rejected_by_reviewer'
		ORDER BY f.id
		LIMIT $5`, tenant, typ, tokens, after, limit)
	if err != nil {
		return nil, 0, err
	}
	var out []SearchResult
	var findingIDs []uuid.UUID
	for rows.Next() {
		var r SearchResult
		var fid uuid.UUID
		var conf float32
		if err := rows.Scan(&r.FileID, &r.FileVersionID, &r.RelPath, &r.VersionState, &r.ProcessedAt, &fid, &r.Status,
			&r.Method, &conf, &r.Occurrences); err != nil {
			rows.Close()
			return nil, 0, err
		}
		r.Confidence = float64(conf)
		out = append(out, r)
		findingIDs = append(findingIDs, fid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(findingIDs) == 0 {
		return out, total, nil
	}
	idx := map[uuid.UUID]int{}
	for i, id := range findingIDs {
		idx[id] = i
	}
	lrows, err := s.pool.Query(ctx, `SELECT finding_id, page_number, sheet_name_encrypted, row_number, column_ref, line_number FROM (
			SELECT l.*, row_number() OVER (PARTITION BY finding_id ORDER BY page_number, row_number, line_number, id) AS rn
			FROM finding_locations l WHERE finding_id = ANY($1)) x
		WHERE rn <= $2 ORDER BY finding_id, rn`, findingIDs, MaxLocationsPerFinding)
	if err != nil {
		return nil, 0, err
	}
	defer lrows.Close()
	for lrows.Next() {
		var fid uuid.UUID
		var page, row, line *int
		var col *string
		var l SearchLocation
		if err := lrows.Scan(&fid, &page, &l.SheetEnc, &row, &col, &line); err != nil {
			return nil, 0, err
		}
		l.Page, l.Row, l.Line, l.Column = deref(page), deref(row), deref(line), derefS(col)
		i := idx[fid]
		out[i].Locations = append(out[i].Locations, l)
	}
	return out, total, lrows.Err()
}

type FileInfo struct {
	ID           uuid.UUID
	SourceName   string
	RelPath      string
	SizeBytes    int64
	MimeKind     *string
	State        string
	SkipReason   *string
	LastObserved time.Time
	ProcessedAt  *time.Time
}

func (s *Store) GetFile(ctx context.Context, tenant, id uuid.UUID) (*FileInfo, error) {
	var f FileInfo
	err := s.pool.QueryRow(ctx, `SELECT f.id, src.logical_name, f.relative_path, f.size_bytes, f.mime_type, f.state, f.skip_reason,
			f.last_observed_at, fv.processed_at
		FROM files f JOIN sources src ON src.id = f.source_id
		LEFT JOIN file_versions fv ON fv.id = f.last_success_version
		WHERE f.id = $1 AND f.tenant_id = $2`, id, tenant).Scan(
		&f.ID, &f.SourceName, &f.RelPath, &f.SizeBytes, &f.MimeKind, &f.State, &f.SkipReason, &f.LastObserved, &f.ProcessedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &f, err
}

// FilesWithFindings devuelve las rutas de los archivos activos de una fuente cuya
// última versión procesada tiene al menos un hallazgo (comando inspect --todos).
func (s *Store) FilesWithFindings(ctx context.Context, tenant, source uuid.UUID, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT f.relative_path FROM files f
		WHERE f.tenant_id = $1 AND f.source_id = $2 AND f.active
		  AND EXISTS (SELECT 1 FROM findings fi WHERE fi.file_version_id = f.last_success_version)
		ORDER BY f.relative_path LIMIT $3`, tenant, source, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// FileWithoutFindings es un archivo activo cuya última versión no tiene hallazgos.
type FileWithoutFindings struct {
	RelPath    string
	State      string
	SkipReason string
	Deferred   bool // pendiente por tamaño (scan process-pending)
}

// FilesWithoutFindings lista los archivos de una fuente que inspect --todos no
// muestra, con el motivo, para que ninguno quede oculto en silencio.
func (s *Store) FilesWithoutFindings(ctx context.Context, tenant, source uuid.UUID, limit int) ([]FileWithoutFindings, error) {
	rows, err := s.pool.Query(ctx, `SELECT f.relative_path, f.state, COALESCE(f.skip_reason, ''),
			EXISTS (SELECT 1 FROM file_tasks t JOIN file_versions v ON v.id = t.file_version_id
			        WHERE v.file_id = f.id AND t.state = 'deferred')
		FROM files f
		WHERE f.tenant_id = $1 AND f.source_id = $2 AND f.active
		  AND NOT EXISTS (SELECT 1 FROM findings fi WHERE fi.file_version_id = f.last_success_version)
		ORDER BY f.relative_path LIMIT $3`, tenant, source, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (FileWithoutFindings, error) {
		var f FileWithoutFindings
		err := r.Scan(&f.RelPath, &f.State, &f.SkipReason, &f.Deferred)
		return f, err
	})
}

// Coverage describe la frescura y completitud del índice (spec §10: no
// encontrar un dato no implica que no exista).
type Coverage struct {
	Status          string     `json:"scan_status"` // completed | partial | never_scanned | in_progress
	LastCompletedAt *time.Time `json:"last_completed_at"`
	FilesNotCovered int        `json:"files_not_covered"`
	SourcesTotal    int        `json:"sources_total"`
	SourcesScanned  int        `json:"sources_scanned"`
}

func (s *Store) Coverage(ctx context.Context, tenant uuid.UUID) (*Coverage, error) {
	c := &Coverage{}
	var anyPartial *bool
	var active int
	err := s.pool.QueryRow(ctx, `
		WITH last AS (
			SELECT DISTINCT ON (source_id) source_id, status, completed_at FROM scans
			WHERE tenant_id = $1 AND status IN ('completed','partial')
			ORDER BY source_id, completed_at DESC)
		SELECT (SELECT count(*) FROM sources WHERE tenant_id = $1 AND enabled),
		       (SELECT count(*) FROM last),
		       (SELECT bool_or(status = 'partial') FROM last),
		       (SELECT min(completed_at) FROM last),
		       (SELECT count(*) FROM files WHERE tenant_id = $1 AND active AND state IN ('failed','partial','skipped','stale','queued','discovered')),
		       (SELECT count(*) FROM scans WHERE tenant_id = $1 AND status IN ('running','paused'))`,
		tenant).Scan(&c.SourcesTotal, &c.SourcesScanned, &anyPartial, &c.LastCompletedAt, &c.FilesNotCovered, &active)
	if err != nil {
		return nil, err
	}
	switch {
	case c.SourcesScanned == 0:
		c.Status = "never_scanned"
	case active > 0:
		c.Status = "in_progress"
	case c.SourcesScanned < c.SourcesTotal || c.FilesNotCovered > 0 || (anyPartial != nil && *anyPartial):
		c.Status = "partial"
	default:
		c.Status = "completed"
	}
	return c, nil
}

type ScanError struct {
	ID        int64
	FileID    *uuid.UUID
	Stage     string
	Code      string
	Attempts  int
	FirstSeen time.Time
	LastSeen  time.Time
}

func (s *Store) ListScanErrors(ctx context.Context, tenant, scan uuid.UUID, afterID int64, limit int) ([]ScanError, error) {
	rows, err := s.pool.Query(ctx, `SELECT e.id, e.file_id, e.stage, e.error_code, e.attempts, e.first_seen_at, e.last_seen_at
		FROM scan_errors e JOIN scans s ON s.id = e.scan_id
		WHERE e.scan_id = $1 AND s.tenant_id = $2 AND e.id > $3 ORDER BY e.id LIMIT $4`, scan, tenant, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScanError
	for rows.Next() {
		var e ScanError
		if err := rows.Scan(&e.ID, &e.FileID, &e.Stage, &e.Code, &e.Attempts, &e.FirstSeen, &e.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AuditEvent nunca contiene valores de identificadores.
type AuditEvent struct {
	Tenant        *uuid.UUID
	Principal     string
	Action        string
	Outcome       string
	CaseReference string
	RequestID     string
	ResultCount   *int
	Details       map[string]any
}

func (s *Store) Audit(ctx context.Context, e AuditEvent) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO audit_events(tenant_id, principal_id, action, outcome, case_reference, request_id, result_count, details)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, e.Tenant, e.Principal, e.Action, e.Outcome, nullable(e.CaseReference),
		nullable(e.RequestID), e.ResultCount, e.Details)
	return err
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func derefS(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
