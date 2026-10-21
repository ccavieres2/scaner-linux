// Package persistence implementa el acceso a PostgreSQL. Solo SQL
// parametrizado; nunca concatenación de valores (spec §9.3, T-04).
package persistence

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"arsop-scanner/migrations"
)

var (
	ErrNotFound     = errors.New("no encontrado")
	ErrScanActive   = errors.New("ya existe un escaneo activo para la fuente")
	ErrLeaseLost    = errors.New("lease de tarea perdido")
	ErrInvalidState = errors.New("transición de estado no permitida")
)

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, dsn string, maxConns int32) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("DSN de base de datos inválido") // no devolver el DSN (contiene credenciales)
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("conexión a base de datos: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("base de datos no disponible")
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Migrate aplica migraciones pendientes bajo un advisory lock.
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	const lockID = 727172
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", lockID)
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	names, err := fs.Glob(migrations.FS, "*.up.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, n := range names {
		v, err := strconv.Atoi(strings.SplitN(n, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("nombre de migración inválido: %s", n)
		}
		var exists bool
		if err := conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)", v).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		sql, _ := migrations.FS.ReadFile(n)
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migración %d: %w", v, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations(version) VALUES ($1)", v); err != nil {
			tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// SchemaVersion devuelve la última migración aplicada (para /health/ready).
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.pool.QueryRow(ctx, "SELECT COALESCE(max(version),0) FROM schema_migrations").Scan(&v)
	return v, err
}

func (s *Store) EnsureTenant(ctx context.Context, name string) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `INSERT INTO tenants(id, name) VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name RETURNING id`, uuid.New(), name).Scan(&id)
	return id, err
}

func (s *Store) EnsureSource(ctx context.Context, tenant uuid.UUID, logical, root string, enabled bool) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `INSERT INTO sources(id, tenant_id, logical_name, root_path, enabled) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (tenant_id, logical_name) DO UPDATE SET root_path = EXCLUDED.root_path, enabled = EXCLUDED.enabled
		RETURNING id`, uuid.New(), tenant, logical, root, enabled).Scan(&id)
	return id, err
}

func (s *Store) EnsureKeyVersion(ctx context.Context, purpose string, version int) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO key_versions(purpose, version, status) VALUES ($1,$2,'active')
		ON CONFLICT DO NOTHING`, purpose, version)
	return err
}

type Scan struct {
	ID                   uuid.UUID
	TenantID, SourceID   uuid.UUID
	SourceName           string
	Type, Status         string
	StartedAt            time.Time
	InventoryCompletedAt *time.Time
	CompletedAt          *time.Time
	RulesVersion         string
	ScannerVersion       string
	Coverage             map[string]any
}

func (s *Store) CreateScan(ctx context.Context, tenant, source uuid.UUID, typ, rules, scannerVersion string) (uuid.UUID, error) {
	id := uuid.New()
	_, err := s.pool.Exec(ctx, `INSERT INTO scans(id, tenant_id, source_id, type, status, rules_version, scanner_version)
		VALUES ($1,$2,$3,$4,'running',$5,$6)`, id, tenant, source, typ, rules, scannerVersion)
	if err != nil && strings.Contains(err.Error(), "scans_one_active_per_source") {
		return uuid.Nil, ErrScanActive
	}
	return id, err
}

func (s *Store) GetScan(ctx context.Context, tenant, id uuid.UUID) (*Scan, error) {
	var sc Scan
	err := s.pool.QueryRow(ctx, `SELECT s.id, s.tenant_id, s.source_id, src.logical_name, s.type, s.status, s.started_at,
			s.inventory_completed_at, s.completed_at, s.rules_version, s.scanner_version, s.coverage_summary
		FROM scans s JOIN sources src ON src.id = s.source_id
		WHERE s.id = $1 AND s.tenant_id = $2`, id, tenant).Scan(
		&sc.ID, &sc.TenantID, &sc.SourceID, &sc.SourceName, &sc.Type, &sc.Status, &sc.StartedAt,
		&sc.InventoryCompletedAt, &sc.CompletedAt, &sc.RulesVersion, &sc.ScannerVersion, &sc.Coverage)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &sc, err
}

// SetScanStatus aplica transiciones permitidas: running<->paused, y finales.
func (s *Store) SetScanStatus(ctx context.Context, tenant, id uuid.UUID, from []string, to string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE scans SET status = $3,
			completed_at = CASE WHEN $3 IN ('completed','partial','failed','unavailable') THEN now() ELSE completed_at END
		WHERE id = $1 AND tenant_id = $2 AND status = ANY($4)`, id, tenant, to, from)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidState
	}
	return nil
}

func (s *Store) MarkInventoryDone(ctx context.Context, scan uuid.UUID) error {
	_, err := s.pool.Exec(ctx, "UPDATE scans SET inventory_completed_at = now() WHERE id = $1", scan)
	return err
}

func (s *Store) ScanStatus(ctx context.Context, scan uuid.UUID) (string, error) {
	var st string
	err := s.pool.QueryRow(ctx, "SELECT status FROM scans WHERE id=$1", scan).Scan(&st)
	return st, err
}
