package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"arsop-scanner/internal/testpg"
)

var baseDSN string

func TestMain(m *testing.M) {
	if runtime.GOOS == "windows" {
		os.Setenv("ARSOP_SKIP_PG", "1") // la config exige rutas Linux; ver TestCLIIntegration
	}
	os.Exit(testpg.Main(m, &baseDSN))
}

func secret(t *testing.T, dir, name string, data []byte) string {
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestCLIIntegration ejecuta init → scan start → report → resume con la CLI real.
func TestCLIIntegration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requiere rutas Linux absolutas (se ejecuta en CI Linux)")
	}
	py := ""
	for _, c := range []string{"python3", "python"} {
		if p, err := exec.LookPath(c); err == nil && exec.Command(p, "-I", "-c", "import openpyxl, pypdfium2").Run() == nil {
			py = p
		}
	}
	if py == "" {
		t.Skip("Python con dependencias no disponible")
	}
	dsn := testpg.FreshDB(t, baseDSN)
	pc, _ := pgx.ParseConfig(dsn)
	dir := t.TempDir()
	sec := filepath.Join(dir, "secrets")
	os.Mkdir(sec, 0o700)
	pw := secret(t, sec, "db", []byte(pc.Password))
	hk := secret(t, sec, "hmac", bytes.Repeat([]byte("h"), 32))
	ek := secret(t, sec, "enc", bytes.Repeat([]byte("e"), 32))
	pf := secret(t, sec, "principals.yaml", []byte("principals:\n  - {id: x, token_sha256: "+strings.Repeat("0", 64)+", scopes: [scans:read]}\n"))
	root := filepath.Join(dir, "data", "rrhh")
	fixtures, _ := filepath.Abs(filepath.Join("..", "..", "tests", "fixtures", "synthetic", "tree"))
	os.MkdirAll(filepath.Dir(root), 0o755)
	if out, err := exec.Command("cp", "-r", fixtures, root).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v %s", err, out)
	}
	// Archivo grande (>= 1 MiB): con large_files: defer queda pendiente.
	big := "rut 5.126.663-3\n" + strings.Repeat("texto de relleno\n", 70000)
	os.WriteFile(filepath.Join(root, "grande.txt"), []byte(big), 0o644)
	// Fuente en solo lectura, como exige `sources check` (se restaura para borrar el TempDir).
	exec.Command("chmod", "-R", "a-w", root).Run()
	t.Cleanup(func() { exec.Command("chmod", "-R", "u+w", root).Run() })
	script, _ := filepath.Abs(filepath.Join("..", "..", "workers", "extractor", "extract.py"))
	cfg := fmt.Sprintf(`schema_version: 1
tenant: {name: cli-test}
sources: [{id: rrhh, root_path: %s}]
scanner: {max_extract_workers: 2, extractor_command: [%s, -I, -B, %s]}
api: {auth: {principals_file: %s}}
database: {host: %s, port: %d, name: %s, user: %s, password_file: %s, sslmode: disable}
secrets: {hmac_key_file: %s, encryption_key_file: %s}
`, root, py, script, pf, pc.Host, pc.Port, pc.Database, pc.User, pw, hk, ek)
	cfgPath := filepath.Join(dir, "scanner.yaml")
	os.WriteFile(cfgPath, []byte(cfg), 0o600)

	runOK := func(args ...string) string {
		var out, errb bytes.Buffer
		if code := run(append(args, "--file", cfgPath), &out, &errb); code != 0 {
			t.Fatalf("%v: código %d\n%s\n%s", args, code, out.String(), errb.String())
		}
		return out.String()
	}
	runOK("init")
	runOK("init") // idempotente
	if out := runOK("sources", "check"); !strings.Contains(out, "rrhh\t") {
		t.Fatalf("sources check: %s", out)
	}
	out := runOK("scan", "start", "--source", "rrhh")
	id := regexp.MustCompile(`escaneo ([0-9a-f-]{36}) iniciado`).FindStringSubmatch(out)
	if id == nil || !strings.Contains(out, "finalizado: partial") {
		t.Fatalf("scan start: %s", out)
	}
	var rep map[string]any
	if err := json.Unmarshal([]byte(runOK("scan", "report", "--scan", id[1])), &rep); err != nil {
		t.Fatal(err)
	}
	prog := rep["progress"].(map[string]any)
	if rep["status"] != "partial" || prog["files_seen"].(float64) != 5 || prog["deferred_files"].(float64) != 1 {
		t.Fatalf("reporte: %v", rep)
	}
	if out := runOK("scan", "pending", "--scan", id[1]); !strings.Contains(out, "grande.txt") || !strings.Contains(out, "1 archivos pendientes") {
		t.Fatalf("scan pending: %s", out)
	}
	if out := runOK("scan", "process-pending", "--scan", id[1]); !strings.Contains(out, "1 archivos pendientes enviados") {
		t.Fatalf("scan process-pending: %s", out)
	}
	if out := runOK("scan", "pending", "--scan", id[1]); !strings.Contains(out, "0 archivos pendientes") {
		t.Fatalf("tras procesar: %s", out)
	}
	if out := runOK("scan", "process-pending", "--scan", id[1]); !strings.Contains(out, "no hay archivos pendientes") {
		t.Fatalf("segunda vez: %s", out)
	}
	// Un escaneo finalizado no puede reanudarse.
	var o, e bytes.Buffer
	if code := run([]string{"scan", "resume", "--scan", id[1], "--file", cfgPath}, &o, &e); code != 1 {
		t.Fatalf("resume de escaneo final debe fallar: %d", code)
	}
}
