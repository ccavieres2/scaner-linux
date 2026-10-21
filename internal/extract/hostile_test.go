package extract

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func codeOf(t *testing.T, err error) string {
	t.Helper()
	var ce *CodedError
	if !errors.As(err, &ce) {
		t.Fatalf("se esperaba CodedError, got %v", err)
	}
	return ce.Code
}

func hostile(t *testing.T, name string) []byte {
	b, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "hostile", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// REL-004: PDF con contraseña → encrypted (nunca se intenta abrir).
func TestPDFEncrypted(t *testing.T) {
	py := &Python{Command: pythonCmd(t), Timeout: 30 * time.Second, MaxPages: 10}
	_, err := py.Extract(context.Background(), "pdf", bytes.NewReader(hostile(t, "con_clave.pdf")), eng(), opt)
	if c := codeOf(t, err); c != ErrEncrypted {
		t.Fatalf("código %s", c)
	}
}

// PDF truncado (descarga incompleta / copia cortada).
func TestPDFTruncated(t *testing.T) {
	py := &Python{Command: pythonCmd(t), Timeout: 30 * time.Second, MaxPages: 10}
	data := fixture(t, "contrato.pdf")
	_, err := py.Extract(context.Background(), "pdf", bytes.NewReader(data[:len(data)/3]), eng(), opt)
	if c := codeOf(t, err); c != ErrCorrupt {
		t.Fatalf("código %s", c)
	}
}

// PDF con más páginas que max_pages → se procesa hasta el límite y queda parcial.
func TestPDFPageLimit(t *testing.T) {
	py := &Python{Command: pythonCmd(t), Timeout: 30 * time.Second, MaxPages: 1}
	r, err := py.Extract(context.Background(), "pdf", bytes.NewReader(fixture(t, "contrato.pdf")), eng(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Partial || r.Warnings[WarnTruncated] != 1 || r.Units != 1 {
		t.Fatalf("límite de páginas: %+v", r)
	}
}

// PDF grande (> 1 MB): se procesa por streaming desde temporal, sin errores.
func TestPDFLarge(t *testing.T) {
	py := &Python{Command: pythonCmd(t), Timeout: 60 * time.Second, MaxPages: 10, TempDir: t.TempDir()}
	data := append(fixture(t, "contrato.pdf"), bytes.Repeat([]byte("% relleno de prueba\n"), 150_000)...) // ~3 MB
	r, err := py.Extract(context.Background(), "pdf", bytes.NewReader(data), eng(), opt)
	if err != nil {
		t.Fatal(err)
	}
	if keys(r.Findings)["rut|5126663-3"].Page != 3 {
		t.Fatalf("PDF grande: %+v", keys(r.Findings))
	}
	// El temporal es anónimo: no debe quedar ningún archivo.
	if left, _ := os.ReadDir(py.TempDir); len(left) != 0 && runtime.GOOS != "windows" {
		t.Fatalf("quedaron temporales: %v", left)
	}
}

// Zip bomb disfrazada de XLSX: se rechaza sin descomprimir.
func TestXLSXZipBomb(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("xl/workbook.xml")
	w.Write([]byte("<workbook/>"))
	w, _ = zw.Create("xl/worksheets/sheet1.xml")
	zeros := make([]byte, 1<<20)
	for i := 0; i < 64; i++ { // 64 MiB descomprimidos, ~64 KiB comprimidos
		w.Write(zeros)
	}
	zw.Close()
	py := &Python{Command: pythonCmd(t), Timeout: 30 * time.Second, MaxPages: 10}
	_, err := py.Extract(context.Background(), "xlsx", bytes.NewReader(buf.Bytes()), eng(), opt)
	if c := codeOf(t, err); c != ErrZipBomb {
		t.Fatalf("código %s", c)
	}
}

// Parser colgado → timeout y proceso terminado (reintentable).
func TestWorkerTimeout(t *testing.T) {
	cmd := pythonCmd(t)
	py := &Python{Command: []string{cmd[0], "-I", "-c", "import time; time.sleep(60)"}, Timeout: time.Second, MaxPages: 1}
	start := time.Now()
	_, err := py.Extract(context.Background(), "pdf", bytes.NewReader([]byte("%PDF-1.4")), eng(), opt)
	var ce *CodedError
	if !errors.As(err, &ce) || ce.Code != ErrTimeout || !ce.Retryable {
		t.Fatalf("esperado timeout reintentable, got %v", err)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatalf("el proceso colgado no se terminó a tiempo: %s", time.Since(start))
	}
}

// Worker que excede su tope de memoria → falla clasificada, sin afectar al
// proceso padre. RLIMIT_AS solo existe en Linux.
func TestWorkerMemoryLimit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("RLIMIT_AS solo se aplica en Linux")
	}
	cmd := pythonCmd(t)
	// El "parser" intenta reservar 2 GiB con un tope de 256 MiB.
	script := `import sys, resource
lim=256*1024*1024
resource.setrlimit(resource.RLIMIT_AS,(lim,lim))
try:
    b=bytearray(2*1024*1024*1024)
    print('{"t":"done","units":0}')
except MemoryError:
    print('{"t":"error","code":"out_of_memory"}')
`
	py := &Python{Command: []string{cmd[0], "-I", "-c", script}, Timeout: 30 * time.Second, MaxPages: 1}
	_, err := py.Extract(context.Background(), "pdf", bytes.NewReader([]byte("%PDF")), eng(), opt)
	if c := codeOf(t, err); c != ErrOutOfMemory {
		t.Fatalf("código %s", c)
	}
	// Y el worker real arranca y procesa dentro de un tope razonable.
	real := &Python{Command: cmd, Timeout: 60 * time.Second, MaxPages: 10, MemoryMB: 1024}
	if _, err := real.Extract(context.Background(), "pdf", bytes.NewReader(fixture(t, "contrato.pdf")), eng(), opt); err != nil {
		t.Fatalf("worker real con tope de 1024 MiB: %v", err)
	}
}
