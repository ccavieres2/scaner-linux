package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/google/uuid"

	"arsop-scanner/internal/app"
	"arsop-scanner/internal/catalog"
	"arsop-scanner/internal/config"
	"arsop-scanner/internal/detection"
	"arsop-scanner/internal/extract"
	"arsop-scanner/internal/persistence"
	"arsop-scanner/internal/pipeline"
)

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

func openRuntime(ctx context.Context, cfg *config.Config, stderr io.Writer) (*app.Runtime, int) {
	rt, err := app.Open(ctx, cfg, app.NewLogger(stderr))
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return nil, 1
	}
	return rt, 0
}

func cmdInit(args []string, stdout, stderr io.Writer) int {
	cfg, code := newFlags("init", stderr).load(args, stderr)
	if cfg == nil {
		return code
	}
	ctx, cancel := signalContext()
	defer cancel()
	rt, code := openRuntime(ctx, cfg, stderr)
	if rt == nil {
		return code
	}
	defer rt.Close()
	if err := rt.Store.Migrate(ctx); err != nil {
		fmt.Fprintln(stderr, "migraciones:", err)
		return 1
	}
	_, sources, err := rt.EnsureCatalog(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "catálogo:", err)
		return 1
	}
	v, _ := rt.Store.SchemaVersion(ctx)
	fmt.Fprintf(stdout, "inicializado: esquema v%d, %d fuentes registradas\n", v, len(sources))
	return 0
}

// cmdSourcesCheck no requiere base de datos: verifica que cada raíz exista,
// sea un directorio legible y no sea escribible por este proceso.
func cmdSourcesCheck(args []string, stdout, stderr io.Writer) int {
	cfg, code := newFlags("sources check", stderr).load(args, stderr)
	if cfg == nil {
		return code
	}
	bad := 0
	for _, s := range cfg.Sources {
		status := "ok"
		fi, err := os.Lstat(s.RootPath)
		switch {
		case err != nil:
			status = "unavailable"
		case !fi.IsDir():
			status = "not_a_directory"
		default:
			if f, err := os.Open(s.RootPath); err != nil {
				status = "not_readable"
			} else {
				f.Close()
				if writable(s.RootPath) {
					status = "WARNING_writable" // las fuentes deben montarse en solo lectura
				}
			}
		}
		if status != "ok" {
			bad++
		}
		fmt.Fprintf(stdout, "%s\t%s\n", s.ID, status)
	}
	if bad > 0 {
		return 1
	}
	return 0
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".arsop-rw-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

func newPipeline(rt *app.Runtime, tenant uuid.UUID) *pipeline.Pipeline {
	c := rt.Cfg.Scanner
	host, _ := os.Hostname()
	return &pipeline.Pipeline{
		Store: rt.Store, Key: rt.Key, Sealer: rt.Sealer, Engine: detection.NewEngine(detection.DefaultOptions()),
		Python: &extract.Python{Command: c.ExtractorCommand, Timeout: time.Duration(c.MaxExtractSecondsPerFile) * time.Second,
			MaxPages: c.MaxPDFPages, MemoryMB: c.WorkerMemoryMB, TempDir: c.TempDir,
			OCRLanguages: c.OCRLanguages, OCRPageTimeout: time.Duration(c.MaxOCRSecondsPerPage) * time.Second},
		Log: rt.Log, Tenant: tenant, LightWorkers: c.MaxExtractWorkers, HeavyWorkers: c.MaxHeavyWorkers,
		WorkerMemoryMB: c.WorkerMemoryMB, MemoryBudgetMB: c.MemoryBudgetMB, LargeFileBytes: c.LargeFileBytes,
		DeferLarge: c.LargeFiles == "defer",
		RetryMax:   c.RetryMax, MaxBytes: c.MaxFileBytes,
		FileTimeout: time.Duration(c.MaxExtractSecondsPerFile) * time.Second,
		Extract:     extract.Options{Window: 64 << 10, Overlap: c.ChunkOverlapChars},
		Owner:       fmt.Sprintf("%s/%d/%s", host, os.Getpid(), uuid.NewString()[:8]),
	}
}

func findSource(cfg *config.Config, id string) *config.SourceConfig {
	for i := range cfg.Sources {
		if cfg.Sources[i].ID == id {
			return &cfg.Sources[i]
		}
	}
	return nil
}

func runScan(ctx context.Context, rt *app.Runtime, tenant uuid.UUID, sc *persistence.Scan, src *config.SourceConfig, sourceID uuid.UUID, stdout, stderr io.Writer) int {
	p := newPipeline(rt, tenant)
	status, err := p.Run(ctx, sc, pipeline.Source{ID: sourceID, Name: src.ID, Root: src.RootPath})
	switch {
	case errors.Is(err, pipeline.ErrPaused):
		fmt.Fprintf(stdout, "escaneo %s pausado; reanudar con: scanner scan resume --scan %s\n", sc.ID, sc.ID)
		return 0
	case errors.Is(err, catalog.ErrRootUnavailable):
		fmt.Fprintf(stderr, "escaneo %s: raíz no disponible (no se borró ningún resultado previo)\n", sc.ID)
		return 1
	case errors.Is(err, context.Canceled):
		fmt.Fprintf(stderr, "interrumpido; reanudar con: scanner scan resume --scan %s\n", sc.ID)
		return 130
	case err != nil:
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	fmt.Fprintf(stdout, "escaneo %s finalizado: %s\n", sc.ID, status)
	return 0
}

func cmdScanStart(args []string, stdout, stderr io.Writer) int {
	f := newFlags("scan start", stderr)
	source := f.String("source", "", "id de la fuente autorizada")
	full := f.Bool("full", false, "reprocesar todo aunque no haya cambios")
	cfg, code := f.load(args, stderr)
	if cfg == nil {
		return code
	}
	src := findSource(cfg, *source)
	if src == nil || !*src.Enabled {
		fmt.Fprintln(stderr, "--source debe ser una fuente configurada y habilitada")
		return 2
	}
	ctx, cancel := signalContext()
	defer cancel()
	rt, code := openRuntime(ctx, cfg, stderr)
	if rt == nil {
		return code
	}
	defer rt.Close()
	tenant, sources, err := rt.EnsureCatalog(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "catálogo (¿ejecutó scanner init?):", err)
		return 1
	}
	typ := cfg.Scanner.ScanMode
	if *full {
		typ = "full"
	}
	id, err := rt.Store.CreateScan(ctx, tenant, sources[src.ID], typ, detection.RulesVersion, version)
	if errors.Is(err, persistence.ErrScanActive) {
		fmt.Fprintln(stderr, "ya hay un escaneo activo para esta fuente; use scan status / scan resume")
		return 1
	}
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	fmt.Fprintf(stdout, "escaneo %s iniciado (%s)\n", id, typ)
	sc, err := rt.Store.GetScan(ctx, tenant, id)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return runScan(ctx, rt, tenant, sc, src, sources[src.ID], stdout, stderr)
}

func scanFlags(name string, args []string, stderr io.Writer, extra func(*flags)) (*config.Config, uuid.UUID, int) {
	f := newFlags(name, stderr)
	scan := f.String("scan", "", "id del escaneo")
	if extra != nil {
		extra(f)
	}
	cfg, code := f.load(args, stderr)
	if cfg == nil {
		return nil, uuid.Nil, code
	}
	id, err := uuid.Parse(*scan)
	if err != nil {
		fmt.Fprintln(stderr, "--scan debe ser un UUID")
		return nil, uuid.Nil, 2
	}
	return cfg, id, 0
}

func cmdScanStatus(args []string, stdout, stderr io.Writer, report bool) int {
	var asJSON *bool
	cfg, id, code := scanFlags("scan status", args, stderr, func(f *flags) { asJSON = f.Bool("json", false, "salida JSON") })
	if cfg == nil {
		return code
	}
	ctx, cancel := signalContext()
	defer cancel()
	rt, code := openRuntime(ctx, cfg, stderr)
	if rt == nil {
		return code
	}
	defer rt.Close()
	tenant, _, err := rt.EnsureCatalog(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	sc, err := rt.Store.GetScan(ctx, tenant, id)
	if err != nil {
		fmt.Fprintln(stderr, "escaneo no encontrado")
		return 1
	}
	prog, err := rt.Store.ScanProgress(ctx, id)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	if report || *asJSON {
		out := map[string]any{"scan_id": sc.ID, "source": sc.SourceName, "type": sc.Type, "status": sc.Status,
			"started_at": sc.StartedAt, "inventory_completed_at": sc.InventoryCompletedAt, "completed_at": sc.CompletedAt,
			"rules_version": sc.RulesVersion, "scanner_version": sc.ScannerVersion, "progress": prog}
		if report {
			errs, _ := rt.Store.ListScanErrors(ctx, tenant, id, 0, 1000)
			byCode := map[string]int{}
			for _, e := range errs {
				byCode[e.Stage+":"+e.Code] += e.Attempts
			}
			out["errors_by_code"] = byCode
			out["notice"] = "Ausencia de hallazgos no implica ausencia de datos personales si hay archivos parciales, fallidos u omitidos."
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return 0
	}
	fmt.Fprintf(stdout, "escaneo %s  fuente=%s  tipo=%s  estado=%s\n", sc.ID, sc.SourceName, sc.Type, sc.Status)
	fmt.Fprintf(stdout, "archivos vistos=%d  parciales=%d  fallidos=%d  omitidos=%v\n", prog.FilesSeen, prog.FilesPartial, prog.FilesFailed, prog.FilesSkipped)
	fmt.Fprintf(stdout, "tareas=%v  eventos de error=%d\n", prog.Tasks, prog.Errors)
	return 0
}

func cmdScanPause(args []string, stdout, stderr io.Writer) int {
	cfg, id, code := scanFlags("scan pause", args, stderr, nil)
	if cfg == nil {
		return code
	}
	ctx, cancel := signalContext()
	defer cancel()
	rt, code := openRuntime(ctx, cfg, stderr)
	if rt == nil {
		return code
	}
	defer rt.Close()
	tenant, _, err := rt.EnsureCatalog(ctx)
	if err == nil {
		err = rt.Store.SetScanStatus(ctx, tenant, id, []string{"running"}, "paused")
	}
	if err != nil {
		fmt.Fprintln(stderr, "no se pudo pausar (¿el escaneo está en ejecución?)")
		return 1
	}
	fmt.Fprintf(stdout, "escaneo %s pausado; los workers terminan el archivo en curso\n", id)
	return 0
}

func cmdScanResume(args []string, stdout, stderr io.Writer) int {
	cfg, id, code := scanFlags("scan resume", args, stderr, nil)
	if cfg == nil {
		return code
	}
	ctx, cancel := signalContext()
	defer cancel()
	rt, code := openRuntime(ctx, cfg, stderr)
	if rt == nil {
		return code
	}
	defer rt.Close()
	tenant, sources, err := rt.EnsureCatalog(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	// 'running' también se acepta: reanudación tras caída del proceso (leases vencidos).
	if err := rt.Store.SetScanStatus(ctx, tenant, id, []string{"paused", "running"}, "running"); err != nil {
		fmt.Fprintln(stderr, "el escaneo no puede reanudarse (estado final o inexistente)")
		return 1
	}
	sc, err := rt.Store.GetScan(ctx, tenant, id)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	src := findSource(cfg, sc.SourceName)
	if src == nil {
		fmt.Fprintln(stderr, "la fuente del escaneo ya no está configurada")
		return 1
	}
	return runScan(ctx, rt, tenant, sc, src, sources[src.ID], stdout, stderr)
}

// cmdTokenNew genera un token aleatorio de 256 bits. El token se muestra una
// sola vez; en el archivo de principals solo se guarda su hash.
func cmdTokenNew(stdout io.Writer) int {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return 1
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(tok))
	fmt.Fprintf(stdout, "token (entregar por canal seguro, no se vuelve a mostrar): %s\ntoken_sha256 (para principals_file): %s\n", tok, hex.EncodeToString(h[:]))
	return 0
}

// cmdScanPending lista los archivos grandes que el escaneo dejó pendientes
// (scanner.large_files: defer). Muestra rutas: es una herramienta del operador.
func cmdScanPending(args []string, stdout, stderr io.Writer) int {
	var asJSON *bool
	cfg, id, code := scanFlags("scan pending", args, stderr, func(f *flags) { asJSON = f.Bool("json", false, "salida JSON") })
	if cfg == nil {
		return code
	}
	ctx, cancel := signalContext()
	defer cancel()
	rt, code := openRuntime(ctx, cfg, stderr)
	if rt == nil {
		return code
	}
	defer rt.Close()
	tenant, _, err := rt.EnsureCatalog(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	if _, err := rt.Store.GetScan(ctx, tenant, id); err != nil {
		fmt.Fprintln(stderr, "escaneo no encontrado")
		return 1
	}
	var all []persistence.DeferredFile
	after := uuid.Nil
	for {
		page, err := rt.Store.ListDeferred(ctx, tenant, id, after, 1000)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		all = append(all, page...)
		if len(page) < 1000 {
			break
		}
		after = page[len(page)-1].TaskID
	}
	if *asJSON {
		out := make([]map[string]any, 0, len(all))
		for _, d := range all {
			out = append(out, map[string]any{"file_id": d.FileID, "relative_path": d.RelPath, "size_bytes": d.Size,
				"class": d.Class, "deferred_at": d.DeferredAt})
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"scan_id": id, "pending": out})
		return 0
	}
	var total int64
	fmt.Fprintf(stdout, "%-10s  %-6s  %s\n", "TAMAÑO", "COLA", "ARCHIVO")
	for _, d := range all {
		total += d.Size
		fmt.Fprintf(stdout, "%-10s  %-6s  %s\n", humanBytes(d.Size), d.Class, d.RelPath)
	}
	fmt.Fprintf(stdout, "%d archivos pendientes (%s). Procesar con: scanner scan process-pending --scan %s\n",
		len(all), humanBytes(total), id)
	return 0
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// cmdScanProcessPending libera los archivos diferidos y los procesa con los
// mismos workers y presupuesto de memoria que un escaneo normal.
func cmdScanProcessPending(args []string, stdout, stderr io.Writer) int {
	var maxBytes *int64
	cfg, id, code := scanFlags("scan process-pending", args, stderr, func(f *flags) {
		maxBytes = f.Int64("max-bytes", 0, "procesar solo diferidos de hasta N bytes (0 = todos)")
	})
	if cfg == nil {
		return code
	}
	ctx, cancel := signalContext()
	defer cancel()
	rt, code := openRuntime(ctx, cfg, stderr)
	if rt == nil {
		return code
	}
	defer rt.Close()
	tenant, sources, err := rt.EnsureCatalog(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	sc, err := rt.Store.GetScan(ctx, tenant, id)
	if err != nil {
		fmt.Fprintln(stderr, "escaneo no encontrado")
		return 1
	}
	src := findSource(cfg, sc.SourceName)
	if src == nil || !*src.Enabled {
		fmt.Fprintln(stderr, "la fuente del escaneo ya no está configurada o está deshabilitada")
		return 1
	}
	n, err := rt.Store.ReleaseDeferred(ctx, tenant, id, *maxBytes)
	switch {
	case errors.Is(err, persistence.ErrScanActive):
		fmt.Fprintln(stderr, "la fuente tiene otro escaneo en curso; espere a que termine")
		return 1
	case err != nil:
		fmt.Fprintln(stderr, "no se pudieron liberar los pendientes (estado del escaneo no válido)")
		return 1
	case n == 0:
		fmt.Fprintln(stdout, "no hay archivos pendientes que procesar")
		return 0
	}
	fmt.Fprintf(stdout, "%d archivos pendientes enviados a proceso\n", n)
	sc, err = rt.Store.GetScan(ctx, tenant, id)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return runScan(ctx, rt, tenant, sc, src, sources[src.ID], stdout, stderr)
}
