package pipeline_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"arsop-scanner/internal/detection"
	"arsop-scanner/internal/persistence"
	"arsop-scanner/internal/pipeline"
)

// large_files: defer — los archivos >= large_file_bytes no se procesan en el
// escaneo: quedan listados como pendientes y se procesan después a pedido.
func TestDeferLargeFiles(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, 1000)
	root := e.src.Root
	os.WriteFile(filepath.Join(root, "g2.pdf"), bigPDF(t, 2000), 0o644)
	os.WriteFile(filepath.Join(root, "g1.pdf"), bigPDF(t, 1100), 0o644)
	os.WriteFile(filepath.Join(root, "g3.txt"), []byte("rut 5.126.663-3\n"+strings.Repeat("texto de relleno\n", 1200<<10/17)), 0o644)
	tr := &tracker{}
	e.pipe.Hooks = tr.hooks()
	e.pipe.DeferLarge = true

	if st := e.scan(t, "incremental"); st != "partial" {
		t.Fatalf("con pendientes el escaneo debe quedar partial, quedó %s", st)
	}
	scan := e.lastScan
	for _, rel := range append(tr.order[pipeline.ClassHeavy], tr.order[pipeline.ClassLight]...) {
		if rel == "g1.pdf" || rel == "g2.pdf" || rel == "g3.txt" {
			t.Fatalf("un archivo grande se procesó en vez de diferirse: %s", rel)
		}
	}
	list, err := e.store.ListDeferred(ctx, e.tenant, scan, uuid.Nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range list {
		names = append(names, d.RelPath)
	}
	t.Logf("pendientes: %v", names)
	if len(names) != 3 || names[0] != "g1.pdf" || names[1] != "g3.txt" || names[2] != "g2.pdf" {
		t.Fatalf("lista de pendientes (por tamaño): %v", names)
	}
	prog, _ := e.store.ScanProgress(ctx, scan)
	if prog.DeferredFiles != 3 || prog.DeferredBytes < 3<<20 {
		t.Fatalf("progreso: %+v", prog)
	}
	// Todavía no se buscaron: solo contrato.pdf tiene el RUT.
	if n := ruts(t, e); n != 1 {
		t.Fatalf("antes de procesar pendientes: %d", n)
	}

	// Paginación de la API y nombres solo con files:names.
	code, body := e.get(t, e.tokens["full"], "/api/v1/scans/"+scan.String()+"/pending")
	if code != http.StatusOK || len(body["pending"].([]any)) != 3 ||
		body["pending"].([]any)[0].(map[string]any)["relative_path"] != "g1.pdf" {
		t.Fatalf("API pendientes: %d %v", code, body)
	}
	code, body = e.get(t, e.tokens["scansonly"], "/api/v1/scans/"+scan.String()+"/pending")
	if code != http.StatusOK || body["pending"].([]any)[0].(map[string]any)["relative_path"] != nil {
		t.Fatalf("sin files:names no debe ver rutas: %d %v", code, body)
	}
	if code, _ = e.get(t, e.tokens["noname"], "/api/v1/scans/"+scan.String()+"/pending"); code != http.StatusForbidden {
		t.Fatalf("sin scans:read: %d", code)
	}

	// No se puede reabrir mientras otro escaneo de la fuente está en curso.
	other, _ := e.store.CreateScan(ctx, e.tenant, e.src.ID, "incremental", detection.RulesVersion, "test")
	if _, err := e.store.ReleaseDeferred(ctx, e.tenant, scan, 0); !errors.Is(err, persistence.ErrScanActive) {
		t.Fatalf("esperaba ErrScanActive: %v", err)
	}
	if _, err := e.store.ReleaseDeferred(ctx, e.tenant, other, 0); !errors.Is(err, persistence.ErrInvalidState) {
		t.Fatalf("un escaneo en curso no se libera: %v", err)
	}
	e.store.SetScanStatus(ctx, e.tenant, other, []string{"running"}, "failed")

	// Procesar solo los de hasta 1.5 MB.
	if n, err := e.store.ReleaseDeferred(ctx, e.tenant, scan, 1536<<10); err != nil || n != 2 {
		t.Fatalf("liberar <= 1.5MB: n=%d err=%v", n, err)
	}
	run(t, e, scan)
	if n := ruts(t, e); n != 3 { // + g1.pdf + g3.txt
		t.Fatalf("tras primer lote: %d", n)
	}
	// El resto.
	if n, err := e.store.ReleaseDeferred(ctx, e.tenant, scan, 0); err != nil || n != 1 {
		t.Fatalf("liberar resto: n=%d err=%v", n, err)
	}
	run(t, e, scan)
	if n := ruts(t, e); n != 4 {
		t.Fatalf("tras procesar todo: %d", n)
	}
	if prog, _ := e.store.ScanProgress(ctx, scan); prog.DeferredFiles != 0 {
		t.Fatalf("quedan pendientes: %+v", prog)
	}
	if n, _ := e.store.ReleaseDeferred(ctx, e.tenant, scan, 0); n != 0 {
		t.Fatalf("no deberían quedar pendientes: %d", n)
	}
}

func run(t *testing.T, e *env, scan uuid.UUID) {
	sc, _ := e.store.GetScan(context.Background(), e.tenant, scan)
	if _, err := e.pipe.Run(context.Background(), sc, e.src); err != nil {
		t.Fatal(err)
	}
}

func ruts(t *testing.T, e *env) int {
	_, body := e.post(t, e.tokens["full"], map[string]any{"type": "rut", "value": "5.126.663-3", "case_reference": "C-1"})
	return int(body["result_count"].(float64))
}

func (e *env) get(t *testing.T, token, p string) (int, map[string]any) {
	req, _ := http.NewRequest("GET", e.srv.URL+p, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	return do(t, req)
}
