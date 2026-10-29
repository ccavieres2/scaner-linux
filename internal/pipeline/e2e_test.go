package pipeline_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"arsop-scanner/internal/api"
	"arsop-scanner/internal/app"
	"arsop-scanner/internal/detection"
	"arsop-scanner/internal/extract"
	"arsop-scanner/internal/identity"
	"arsop-scanner/internal/persistence"
	"arsop-scanner/internal/pipeline"
	"arsop-scanner/internal/security"
	"arsop-scanner/internal/testpg"
)

var baseDSN string

func TestMain(m *testing.M) { os.Exit(testpg.Main(m, &baseDSN)) }

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, b, 0o644); err != nil {
			return err
		}
		return os.Chtimes(target, info.ModTime(), info.ModTime())
	})
	if err != nil {
		t.Fatal(err)
	}
}

func pythonCmd(t *testing.T) []string {
	script, _ := filepath.Abs(filepath.Join("..", "..", "workers", "extractor", "extract.py"))
	for _, py := range []string{"python3", "python"} {
		if p, err := exec.LookPath(py); err == nil && !strings.Contains(p, "WindowsApps") && exec.Command(p, "-I", "-c", "import openpyxl, pypdfium2").Run() == nil {
			return []string{p, "-I", "-B", script}
		}
	}
	t.Skip("Python con openpyxl y pypdfium2 no disponible")
	return nil
}

type env struct {
	store    *persistence.Store
	tenant   uuid.UUID
	src      pipeline.Source
	pipe     *pipeline.Pipeline
	srv      *httptest.Server
	tokens   map[string]string
	lastScan uuid.UUID
	logs     *bytes.Buffer
}

func newEnv(t *testing.T, rate int) *env {
	ctx := context.Background()
	st, err := persistence.Open(ctx, testpg.FreshDB(t, baseDSN), 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "rrhh")
	copyTree(t, filepath.Join("..", "..", "tests", "fixtures", "synthetic", "tree"), root)
	ten, _ := st.EnsureTenant(ctx, "cliente-prueba")
	srcID, _ := st.EnsureSource(ctx, ten, "rrhh", root, true)
	key, _ := identity.NewKey(1, bytes.Repeat([]byte{0x42}, 32))
	sealer, _ := security.NewSealer(1, bytes.Repeat([]byte{0x24}, 32))
	logs := &bytes.Buffer{}
	log := app.NewLogger(logs)
	p := &pipeline.Pipeline{
		Store: st, Key: key, Sealer: sealer, Engine: detection.NewEngine(detection.DefaultOptions()),
		Python: &extract.Python{Command: pythonCmd(t), Timeout: time.Minute, MaxPages: 100},
		Log:    log, Tenant: ten, LightWorkers: 2, HeavyWorkers: 2, WorkerMemoryMB: 512, MemoryBudgetMB: 2048,
		LargeFileBytes: 1 << 20, RetryMax: 1, MaxBytes: 50 << 20, FileTimeout: time.Minute,
		Extract: extract.Options{Window: 4096, Overlap: 256}, Owner: "test",
	}
	tokens := map[string]string{"full": strings.Repeat("a", 40), "noname": strings.Repeat("b", 40), "scansonly": strings.Repeat("c", 40)}
	h := func(s string) string { x := sha256.Sum256([]byte(s)); return hex.EncodeToString(x[:]) }
	pf := fmt.Sprintf(`principals:
  - {id: arsop-app, token_sha256: %s, scopes: [findings:search, files:read, files:names, scans:read, scans:diagnose]}
  - {id: sin-nombres, token_sha256: %s, scopes: [findings:search]}
  - {id: solo-scans, token_sha256: %s, scopes: [scans:read]}
`, h(tokens["full"]), h(tokens["noname"]), h(tokens["scansonly"]))
	ps, err := api.ParsePrincipals(strings.NewReader(pf), false)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.New(st, key, sealer, ten, ps, api.Options{MaxBodyBytes: 65536, MaxPageSize: 100,
		SearchRatePerMinute: rate, SearchBurst: rate}, log).Handler())
	t.Cleanup(srv.Close)
	return &env{store: st, tenant: ten, src: pipeline.Source{ID: srcID, Name: "rrhh", Root: root}, pipe: p, srv: srv, tokens: tokens, logs: logs}
}

func (e *env) scan(t *testing.T, typ string) string {
	ctx := context.Background()
	id, err := e.store.CreateScan(ctx, e.tenant, e.src.ID, typ, detection.RulesVersion, "test")
	if err != nil {
		t.Fatal(err)
	}
	e.lastScan = id
	sc, _ := e.store.GetScan(ctx, e.tenant, id)
	status, err := e.pipe.Run(ctx, sc, e.src)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func (e *env) post(t *testing.T, token string, body any) (int, map[string]any) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/identifiers/search", bytes.NewReader(b))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	return do(t, req)
}

func do(t *testing.T, req *http.Request) (int, map[string]any) {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("falta Cache-Control: no-store")
	}
	return resp.StatusCode, m
}

func locString(l map[string]any) string {
	n := func(k string) int { f, _ := l[k].(float64); return int(f) }
	switch {
	case l["sheet"] != nil:
		return fmt.Sprintf("sheet=%s,row=%d,col=%s", l["sheet"], n("row"), l["column"])
	case l["page"] != nil:
		return fmt.Sprintf("page=%d", n("page"))
	case l["line"] != nil:
		return fmt.Sprintf("line=%d", n("line"))
	default:
		return fmt.Sprintf("row=%d,col=%s", n("row"), l["column"])
	}
}

type expectedFile struct {
	Indexed    map[string][]string `json:"indexed"`
	NotIndexed []string            `json:"not_indexed_invalid_checksum"`
}

// Puerta de Fase 1: resultados exactos, deduplicados y estables tras reproceso.
func TestEndToEnd(t *testing.T) {
	e := newEnv(t, 1000)
	if status := e.scan(t, "incremental"); status != "partial" { // contrato.pdf p.2 requiere OCR
		t.Fatalf("estado del escaneo: %s", status)
	}
	raw, _ := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "synthetic", "expected.json"))
	var exp expectedFile
	if err := json.Unmarshal(raw, &exp); err != nil {
		t.Fatal(err)
	}
	check := func(round string) {
		for k, want := range exp.Indexed {
			typ, val, _ := strings.Cut(k, "|")
			code, body := e.post(t, e.tokens["full"], map[string]any{"type": typ, "value": val, "case_reference": "ARSOP-2026-001"})
			if code != 200 {
				t.Fatalf("%s %s: código %d %v", round, k, code, body)
			}
			var got []string
			for _, r := range body["results"].([]any) {
				res := r.(map[string]any)
				fid, _ := uuid.Parse(res["file_id"].(string))
				fi, err := e.store.GetFile(context.Background(), e.tenant, fid)
				if err != nil {
					t.Fatal(err)
				}
				for _, l := range res["locations"].([]any) {
					got = append(got, fi.RelPath+"@"+locString(l.(map[string]any)))
				}
			}
			sort.Strings(got)
			sort.Strings(want)
			if strings.Join(got, ";") != strings.Join(want, ";") {
				t.Errorf("%s %s:\n  obtenido %v\n  esperado %v", round, k, got, want)
			}
			if int(body["result_count"].(float64)) != len(body["results"].([]any)) {
				t.Errorf("%s %s: result_count inconsistente", round, k)
			}
		}
		for _, k := range exp.NotIndexed {
			typ, val, _ := strings.Cut(k, "|")
			code, _ := e.post(t, e.tokens["full"], map[string]any{"type": typ, "value": val, "case_reference": "C-1"})
			if code != 400 { // DV inválido: la API ni siquiera acepta la consulta
				t.Errorf("%s: esperado 400, got %d", k, code)
			}
		}
	}
	check("primer escaneo")

	// Reproceso completo (las listas exactas de check detectan cualquier duplicado) de las mismas versiones: resultados idénticos, sin duplicados.
	if status := e.scan(t, "full"); status != "partial" {
		t.Fatalf("reproceso: %s", status)
	}
	check("reproceso full")
	// Incremental sin cambios: nada que procesar.
	e.scan(t, "incremental")
	check("incremental sin cambios")

	// Cobertura parcial expuesta.
	_, body := e.post(t, e.tokens["full"], map[string]any{"type": "rut", "value": "12.345.678-5", "case_reference": "C-2"})
	if cov := body["coverage"].(map[string]any); cov["scan_status"] != "partial" {
		t.Errorf("cobertura: %v", cov)
	}

	// Sin scope files:names se enmascaran nombres de archivo y hoja.
	_, body = e.post(t, e.tokens["noname"], map[string]any{"type": "rut", "value": "10000013-K", "case_reference": "C-3"})
	for _, r := range body["results"].([]any) {
		res := r.(map[string]any)
		if _, ok := res["display_name"]; ok {
			t.Error("display_name expuesto sin files:names")
		}
		for _, l := range res["locations"].([]any) {
			if s, ok := l.(map[string]any)["sheet"]; ok && s != "[redacted]" {
				t.Errorf("nombre de hoja expuesto: %v", s)
			}
		}
	}

	// SEC-006: ningún RUT, correo ni nombre de archivo en logs.
	logs := e.logs.String()
	for k := range exp.Indexed {
		_, val, _ := strings.Cut(k, "|")
		if strings.Contains(logs, val) || strings.Contains(logs, strings.ReplaceAll(val, "-", "")) {
			t.Errorf("PII en logs: %s", val)
		}
	}
	for _, name := range []string{"clientes.csv", "notas.txt", "planilla", "contrato", "Oculta", "ARSOP-2026-001"} {
		if strings.Contains(logs, name) {
			t.Errorf("dato sensible en logs: %s", name)
		}
	}
}

func TestAPISecurity(t *testing.T) {
	e := newEnv(t, 3)
	e.scan(t, "incremental")
	q := map[string]any{"type": "rut", "value": "12.345.678-5", "case_reference": "C-1"}

	// SEC-001
	if code, body := e.post(t, "", q); code != 401 || body["results"] != nil {
		t.Fatalf("sin token: %d %v", code, body)
	}
	if code, _ := e.post(t, strings.Repeat("z", 40), q); code != 401 {
		t.Fatalf("token inválido: %d", code)
	}
	// SEC-002
	if code, _ := e.post(t, e.tokens["scansonly"], q); code != 403 {
		t.Fatalf("sin scope: %d", code)
	}
	// Validaciones: campos desconocidos, case_reference, identificador.
	if code, _ := e.post(t, e.tokens["full"], map[string]any{"type": "rut", "value": "x", "case_reference": "C", "extra": 1}); code != 400 {
		t.Fatalf("campo desconocido: %d", code)
	}
	// SEC-007: el límite de frecuencia corta la enumeración (burst 3; ya se usaron 2 de este principal).
	codes := []int{}
	for i := 0; i < 5; i++ {
		c, _ := e.post(t, e.tokens["full"], q)
		codes = append(codes, c)
	}
	if codes[len(codes)-1] != 429 {
		t.Fatalf("rate limit no aplicado: %v", codes)
	}
	// SEC-003 / BOLA: id inexistente u otro tenant → 404 idéntico.
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/files/"+uuid.NewString(), nil)
	req.Header.Set("Authorization", "Bearer "+e.tokens["full"])
	if code, _ := do(t, req); code != 404 {
		t.Fatalf("archivo inexistente: %d", code)
	}
	req, _ = http.NewRequest("GET", e.srv.URL+"/api/v1/files/../../etc/passwd", nil)
	req.Header.Set("Authorization", "Bearer "+e.tokens["full"])
	if code, _ := do(t, req); code != 404 {
		t.Fatalf("traversal en URL: %d", code)
	}
	// Salud sin autenticación y sin detalles.
	resp, _ := http.Get(e.srv.URL + "/api/v1/health/ready")
	if resp.StatusCode != 200 {
		t.Fatalf("ready: %d", resp.StatusCode)
	}
	resp.Body.Close()
	// Cuerpo excesivo.
	big := bytes.Repeat([]byte("a"), 70000)
	req, _ = http.NewRequest("POST", e.srv.URL+"/api/v1/identifiers/search", bytes.NewReader(big))
	req.Header.Set("Authorization", "Bearer "+e.tokens["noname"])
	if code, _ := do(t, req); code != 400 {
		t.Fatalf("cuerpo excesivo: %d", code)
	}
}

// REL-002: raíz no disponible no borra el catálogo.
func TestRootUnavailable(t *testing.T) {
	e := newEnv(t, 100)
	e.scan(t, "incremental")
	if err := os.RemoveAll(e.src.Root); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, _ := e.store.CreateScan(ctx, e.tenant, e.src.ID, "incremental", detection.RulesVersion, "test")
	sc, _ := e.store.GetScan(ctx, e.tenant, id)
	status, _ := e.pipe.Run(ctx, sc, e.src)
	if status != "unavailable" {
		t.Fatalf("estado: %s", status)
	}
	code, body := e.post(t, e.tokens["full"], map[string]any{"type": "rut", "value": "12.345.678-5", "case_reference": "C-9"})
	if code != 200 || int(body["result_count"].(float64)) != 2 {
		t.Fatalf("los resultados previos deben conservarse: %d %v", code, body["result_count"])
	}
}

// Interfaz web: archivos estáticos con CSP propia y endpoints de lectura con
// scopes; sin files:names no se exponen rutas.
func TestUIAndBrowseEndpoints(t *testing.T) {
	e := newEnv(t, 1000)
	e.scan(t, "incremental")
	get := func(token, path string) (int, map[string]any) {
		req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return do(t, req)
	}
	resp, err := http.Get(e.srv.URL + "/ui/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(page), "app.js") ||
		!strings.Contains(resp.Header.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("ui: %d %q", resp.StatusCode, resp.Header.Get("Content-Security-Policy"))
	}
	if code, _ := get("", "/api/v1/overview"); code != 401 {
		t.Errorf("overview sin token: %d", code)
	}
	code, me := get(e.tokens["scansonly"], "/api/v1/me")
	if code != 200 || me["principal"] != "solo-scans" {
		t.Errorf("me: %d %v", code, me)
	}
	if code, _ := get(e.tokens["scansonly"], "/api/v1/sources/rrhh/files"); code != 403 {
		t.Errorf("files sin scope: %d", code)
	}
	code, ov := get(e.tokens["scansonly"], "/api/v1/overview")
	srcs, _ := ov["sources"].([]any)
	if code != 200 || len(srcs) != 1 || srcs[0].(map[string]any)["files_with_findings"].(float64) < 1 {
		t.Fatalf("overview: %d %v", code, ov)
	}
	if code, sc := get(e.tokens["scansonly"], "/api/v1/scans"); code != 200 || len(sc["scans"].([]any)) != 1 {
		t.Errorf("scans: %d %v", code, sc)
	}
	code, fl := get(e.tokens["full"], "/api/v1/sources/rrhh/files?with_findings=true")
	files, _ := fl["files"].([]any)
	if code != 200 || len(files) == 0 {
		t.Fatalf("files: %d %v", code, fl)
	}
	var csv map[string]any
	for _, f := range files {
		m := f.(map[string]any)
		if m["relative_path"] == "clientes.csv" {
			csv = m
		}
		if m["ruts"].(float64)+m["emails"].(float64) == 0 {
			t.Errorf("with_findings devolvió archivo sin hallazgos: %v", m)
		}
	}
	if csv == nil || csv["ruts"].(float64) < 1 || csv["full_path"] != path.Join(e.src.Root, "clientes.csv") {
		t.Errorf("clientes.csv sin conteo de RUT: %v", files)
	}
	if code, _ := get(e.tokens["full"], "/api/v1/sources/no-existe/files"); code != 404 {
		t.Errorf("fuente inexistente: %d", code)
	}
}
