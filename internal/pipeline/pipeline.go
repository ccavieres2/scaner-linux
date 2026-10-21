// Package pipeline orquesta inventario → tareas → extracción → detección →
// publicación transaccional (spec §20). Los logs solo contienen
// identificadores opacos y códigos sanitizados (spec §19.5).
package pipeline

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/semaphore"

	"arsop-scanner/internal/catalog"
	"arsop-scanner/internal/detection"
	"arsop-scanner/internal/extract"
	"arsop-scanner/internal/identity"
	"arsop-scanner/internal/persistence"
	"arsop-scanner/internal/security"
)

// MaxStoredLocations por hallazgo y versión (acota almacenamiento).
const MaxStoredLocations = 100

// Clases de cola. Cada una tiene su propio grupo de workers.
const (
	ClassLight = "light" // TXT/CSV/imágenes: Go, lectura por partes, memoria acotada
	ClassHeavy = "heavy" // PDF/XLSX: proceso Python aislado con tope de memoria
)

// LightReserveMB es la memoria que reserva cada worker liviano del presupuesto.
const LightReserveMB = 64

type Pipeline struct {
	Store        *persistence.Store
	Key          *identity.Key
	Sealer       *security.Sealer
	Engine       *detection.Engine
	Python       *extract.Python
	Log          *slog.Logger
	Tenant       uuid.UUID
	LightWorkers int
	HeavyWorkers int
	// WorkerMemoryMB es lo que reserva cada worker pesado; coincide con el tope
	// RLIMIT_AS del proceso Python, de modo que el uso real no supera el presupuesto.
	WorkerMemoryMB int
	MemoryBudgetMB int
	LargeFileBytes int64
	// DeferLarge: los archivos >= LargeFileBytes no se procesan en el escaneo;
	// quedan como pendientes (deferred) hasta `scan process-pending`.
	DeferLarge  bool
	RetryMax    int
	MaxBytes    int64
	FileTimeout time.Duration
	Extract     extract.Options
	Owner       string // identificador de este proceso para leases
	Hooks       *Hooks // solo pruebas
}

// Hooks permite observar el planificador en pruebas (orden y concurrencia).
type Hooks struct {
	OnStart func(class, relPath string, reservedMB int64)
	OnEnd   func(class, relPath string, reservedMB int64)
}

// Source es una raíz autorizada ya registrada.
type Source struct {
	ID   uuid.UUID
	Name string
	Root string
}

// ErrPaused indica que el escaneo fue pausado durante la ejecución.
var ErrPaused = errors.New("escaneo pausado")

// Run ejecuta (o reanuda) un escaneo hasta completarlo, pausarlo o fallar.
func (p *Pipeline) Run(ctx context.Context, scan *persistence.Scan, src Source) (string, error) {
	log := p.Log.With("scan_id", scan.ID.String(), "source", src.Name)
	if scan.InventoryCompletedAt == nil {
		log.Info("inventario iniciado")
		n, err := p.inventory(ctx, scan, src)
		if errors.Is(err, catalog.ErrRootUnavailable) {
			// Nunca interpretar raíz caída como borrado (spec §10, REL-002).
			log.Error("raíz no disponible; escaneo marcado unavailable", "error_code", "root_unavailable")
			_ = p.Store.RecordScanError(ctx, scan.ID, "inventory", "root_unavailable")
			_ = p.Store.SetScanStatus(ctx, p.Tenant, scan.ID, []string{"running"}, "unavailable")
			return "unavailable", err
		}
		if err != nil {
			return "", err
		}
		if err := p.Store.MarkInventoryDone(ctx, scan.ID); err != nil {
			return "", err
		}
		log.Info("inventario completado", "files_observed", n)
	}
	if err := p.process(ctx, scan, src, log); err != nil {
		return "", err
	}
	status, err := p.Store.FinishScan(ctx, p.Tenant, scan.ID)
	if err != nil {
		return "", err
	}
	log.Info("escaneo finalizado", "status", status)
	return status, nil
}

func (p *Pipeline) checkRunning(ctx context.Context, scan uuid.UUID) error {
	st, err := p.Store.ScanStatus(ctx, scan)
	if err != nil {
		return err
	}
	if st != "running" {
		return ErrPaused
	}
	return nil
}

func (p *Pipeline) inventory(ctx context.Context, scan *persistence.Scan, src Source) (int, error) {
	n := 0
	err := catalog.Walk(src.Root, p.MaxBytes, func(e catalog.Entry) error {
		if !catalog.ValidRelPath(e.RelPath) {
			return nil
		}
		n++
		if n%1000 == 0 {
			if err := p.checkRunning(ctx, scan.ID); err != nil {
				return err
			}
		}
		_, err := p.Store.ObserveFile(ctx, persistence.Observation{
			Tenant: p.Tenant, Source: src.ID, Scan: scan.ID, RelPath: e.RelPath, Size: e.Size,
			MTime: e.ModTime.Truncate(time.Microsecond), Identity: identityOf(e), SkipReason: e.SkipReason,
			RulesVersion: detection.RulesVersion, Full: scan.Type == "full",
			Class: classByName(e.RelPath), Priority: p.priority(e.Size),
			Deferred: p.DeferLarge && p.isLarge(e.Size),
		})
		return err
	})
	return n, err
}

// classByName asigna la cola por extensión (pista barata, sin abrir el
// archivo). El tipo real se verifica al procesar y se reclasifica si difiere.
func classByName(rel string) string {
	switch strings.ToLower(path.Ext(rel)) {
	case ".pdf", ".xlsx", ".xlsm":
		return ClassHeavy
	}
	return ClassLight
}

func classByKind(kind string) string {
	switch kind {
	case catalog.KindPDF, catalog.KindXLSX:
		return ClassHeavy
	}
	return ClassLight
}

// priority: los archivos grandes se procesan después de todos los pequeños.
func (p *Pipeline) priority(size int64) int {
	if p.isLarge(size) {
		return 200
	}
	return 100
}

func (p *Pipeline) isLarge(size int64) bool { return p.LargeFileBytes > 0 && size >= p.LargeFileBytes }

func identityOf(e catalog.Entry) string {
	return strconv.FormatUint(e.Dev, 10) + ":" + strconv.FormatUint(e.Inode, 10)
}

type pool struct {
	class    string
	workers  int
	reserveM int64
}

// process ejecuta un grupo de workers por clase. Todos comparten un
// presupuesto de memoria: cada worker reserva su cuota ANTES de reclamar una
// tarea, así nunca retiene un lease mientras espera memoria.
func (p *Pipeline) process(ctx context.Context, scan *persistence.Scan, src Source, log *slog.Logger) error {
	lease := p.FileTimeout + time.Minute
	budget := semaphore.NewWeighted(int64(p.MemoryBudgetMB))
	pools := []pool{
		{ClassLight, max(p.LightWorkers, 1), LightReserveMB},
		{ClassHeavy, max(p.HeavyWorkers, 1), int64(p.WorkerMemoryMB)},
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error
	for _, pl := range pools {
		for w := 0; w < pl.workers; w++ {
			wg.Add(1)
			go func(pl pool, w int) {
				defer wg.Done()
				owner := fmt.Sprintf("%s/%s/%d", p.Owner, pl.class, w)
				if err := p.workerLoop(ctx, scan.ID, src, pl, owner, lease, budget, log); err != nil {
					once.Do(func() { firstErr = err; cancel() })
				}
			}(pl, w)
		}
	}
	wg.Wait()
	return firstErr
}

func (p *Pipeline) workerLoop(ctx context.Context, scan uuid.UUID, src Source, pl pool, owner string,
	lease time.Duration, budget *semaphore.Weighted, log *slog.Logger) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := p.checkRunning(ctx, scan); err != nil {
			return err
		}
		if err := budget.Acquire(ctx, pl.reserveM); err != nil {
			return err
		}
		tasks, err := p.Store.ClaimTasks(ctx, scan, pl.class, owner, 1, lease)
		if err != nil {
			budget.Release(pl.reserveM)
			return err
		}
		if len(tasks) == 0 {
			budget.Release(pl.reserveM)
			// Termina solo cuando no queda nada pendiente en ninguna cola: una
			// tarea puede llegar a esta cola por reclasificación o reintento.
			pending, err := p.Store.PendingTasks(ctx, scan)
			if err != nil {
				return err
			}
			if pending == 0 {
				return nil
			}
			select {
			case <-ctx.Done():
			case <-time.After(300 * time.Millisecond):
			}
			continue
		}
		t := tasks[0]
		if p.Hooks != nil && p.Hooks.OnStart != nil {
			p.Hooks.OnStart(pl.class, t.RelPath, pl.reserveM)
		}
		p.handle(ctx, src, pl.class, t, log)
		if p.Hooks != nil && p.Hooks.OnEnd != nil {
			p.Hooks.OnEnd(pl.class, t.RelPath, pl.reserveM)
		}
		budget.Release(pl.reserveM)
	}
}

func (p *Pipeline) handle(ctx context.Context, src Source, class string, t persistence.Task, log *slog.Logger) {
	start := time.Now()
	res, kind, ferr := p.processFile(ctx, src, class, t)
	log = log.With("file_id", t.FileID.String(), "attempt", t.Attempt, "class", class)
	if ferr != nil {
		var err error
		switch ferr.state {
		case "reclassify":
			err = p.Store.ReclassifyTask(ctx, t, ClassHeavy)
			log.Info("archivo reclasificado por tipo real", "kind", kind, "to", ClassHeavy)
		case "stale":
			err = p.Store.FailTask(ctx, t, "stability", ferr.code, false, p.RetryMax, "stale")
		case "skipped":
			err = p.Store.FailTask(ctx, t, "extract", ferr.code, false, p.RetryMax, "skipped")
		default:
			err = p.Store.FailTask(ctx, t, "extract", ferr.code, ferr.retryable, p.RetryMax, "failed")
		}
		if ferr.state != "reclassify" {
			log.Warn("archivo no procesado", "error_code", ferr.code, "kind", kind)
		}
		if err != nil && !errors.Is(err, persistence.ErrLeaseLost) {
			log.Error("no se pudo registrar el resultado", "error_code", "db_error")
		}
		return
	}
	if err := p.Store.CompleteTask(ctx, p.Tenant, t, *res); err != nil {
		if errors.Is(err, persistence.ErrLeaseLost) {
			log.Warn("lease perdido; resultado descartado", "error_code", "lease_lost")
			return
		}
		log.Error("error al publicar resultados", "error_code", "db_error")
		_ = p.Store.FailTask(ctx, t, "persist", "db_error", true, p.RetryMax, "failed")
		return
	}
	log.Info("archivo procesado", "kind", kind, "identifiers", len(res.Findings), "partial", res.Partial,
		"ms", time.Since(start).Milliseconds())
}

type fileErr struct {
	code      string
	retryable bool
	state     string // failed | skipped | stale | reclassify
}

func (p *Pipeline) processFile(ctx context.Context, src Source, class string, t persistence.Task) (*persistence.FileResult, string, *fileErr) {
	f, err := catalog.OpenBeneath(src.Root, t.RelPath)
	if err != nil {
		switch {
		case errors.Is(err, catalog.ErrOutsideRoot):
			return nil, "", &fileErr{code: "outside_root", state: "skipped"}
		case errors.Is(err, os.ErrNotExist):
			return nil, "", &fileErr{code: "vanished", state: "stale"}
		case errors.Is(err, os.ErrPermission):
			return nil, "", &fileErr{code: "permission_denied", state: "skipped"}
		case errors.Is(err, catalog.ErrRootUnavailable):
			return nil, "", &fileErr{code: "root_unavailable", retryable: true, state: "failed"}
		}
		return nil, "", &fileErr{code: "open_failed", retryable: true, state: "failed"}
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, "", &fileErr{code: "stat_failed", retryable: true, state: "failed"}
	}
	if before.Size() != t.Size || !before.ModTime().UTC().Truncate(time.Microsecond).Equal(t.MTime.UTC()) {
		return nil, "", &fileErr{code: "changed_since_inventory", state: "stale"}
	}
	kind := catalog.Sniff(f, before.Size(), t.RelPath)
	// Un worker liviano nunca procesa formatos pesados (p. ej. un PDF con extensión .txt).
	if class == ClassLight && classByKind(kind) == ClassHeavy {
		return nil, kind, &fileErr{code: "reclassified", state: "reclassify"}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, kind, &fileErr{code: "seek_failed", retryable: true, state: "failed"}
	}
	ctx, cancel := context.WithTimeout(ctx, p.FileTimeout)
	defer cancel()
	r := io.LimitReader(f, p.MaxBytes)
	var res *extract.Result
	switch kind {
	case catalog.KindText:
		res, err = extract.Text(r, p.Engine, p.Extract)
	case catalog.KindCSV:
		res, err = extract.CSV(r, p.Engine, p.Extract)
	case catalog.KindPDF, catalog.KindXLSX:
		res, err = p.Python.Extract(ctx, kind, r, p.Engine, p.Extract)
	case catalog.KindImage:
		// OCR llega en Fase 2: cobertura parcial explícita, sin hallazgos.
		res = &extract.Result{Warnings: map[string]int{extract.WarnImageNeedsOCR: 1}, Partial: true}
	default:
		return nil, kind, &fileErr{code: "unsupported_" + kind, state: "skipped"}
	}
	if err != nil {
		var ce *extract.CodedError
		if errors.As(err, &ce) {
			state := "failed"
			if ce.Code == extract.ErrEncrypted {
				state = "skipped" // nunca se intenta romper cifrado
			}
			return nil, kind, &fileErr{code: ce.Code, retryable: ce.Retryable, state: state}
		}
		return nil, kind, &fileErr{code: "extract_failed", retryable: true, state: "failed"}
	}
	// Estabilidad: si el archivo cambió durante la lectura, no se publica (REL-003).
	after, err := os.Lstat(joinRoot(src.Root, t.RelPath))
	if err != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || !os.SameFile(before, after) {
		return nil, kind, &fileErr{code: "changed_during_read", state: "stale"}
	}
	fr, err := p.build(t, kind, res)
	if err != nil {
		return nil, kind, &fileErr{code: "seal_failed", state: "failed"}
	}
	return fr, kind, nil
}

// build agrupa hallazgos por identificador, calcula HMAC y cifra hojas.
func (p *Pipeline) build(t persistence.Task, kind string, res *extract.Result) (*persistence.FileResult, error) {
	type agg struct {
		row  *persistence.FindingRow
		seen map[string]bool
	}
	groups := map[string]*agg{}
	var order []string
	invalid := 0
	aad := t.FileVersionID[:]
	for _, f := range res.Findings {
		if f.Status == detection.StatusInvalidChecksum {
			invalid++
			continue
		}
		k := string(f.Type) + "\x00" + f.Normalized
		g := groups[k]
		if g == nil {
			g = &agg{seen: map[string]bool{}, row: &persistence.FindingRow{
				Type: string(f.Type), KeyVersion: p.Key.Version, Token: p.Key.LookupToken(f.Type, f.Normalized),
				Status: f.Status, Method: f.Method, Confidence: f.Confidence, Detector: f.Detector,
			}}
			groups[k] = g
			order = append(order, k)
		}
		row := g.row
		row.Occurrences++
		if f.Status == detection.StatusValidatedFormat {
			row.Status = detection.StatusValidatedFormat
		}
		if f.Confidence > row.Confidence {
			row.Confidence = f.Confidence
		}
		if row.Method != f.Method {
			row.Method = "hybrid"
		}
		l := f.Location
		lk := fmt.Sprintf("%d|%s|%d|%s|%d", l.Page, l.Sheet, l.Row, l.Column, l.Line)
		if g.seen[lk] || len(row.Locations) >= MaxStoredLocations {
			continue
		}
		g.seen[lk] = true
		h := sha256.Sum256(append(append([]byte{}, aad...), lk...))
		loc := persistence.LocationRow{Key: h[:], Page: l.Page, Row: l.Row, Column: l.Column, Line: l.Line}
		if l.Sheet != "" {
			enc, err := p.Sealer.Seal([]byte(l.Sheet), aad)
			if err != nil {
				return nil, err
			}
			loc.SheetEnc = enc
		}
		row.Locations = append(row.Locations, loc)
	}
	fr := &persistence.FileResult{MimeKind: kind, Partial: res.Partial, Warnings: res.Warnings}
	for _, k := range order {
		fr.Findings = append(fr.Findings, *groups[k].row)
	}
	if invalid > 0 {
		if fr.Warnings == nil {
			fr.Warnings = map[string]int{}
		}
		fr.Warnings["invalid_checksum_candidates"] = invalid
	}
	return fr, nil
}
