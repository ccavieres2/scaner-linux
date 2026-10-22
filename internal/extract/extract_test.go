package extract

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"arsop-scanner/internal/detection"
)

var opt = Options{Window: 4096, Overlap: 256}

func eng() *detection.Engine { return detection.NewEngine(detection.DefaultOptions()) }

func keys(fs []detection.Finding) map[string]detection.Location {
	m := map[string]detection.Location{}
	for _, f := range fs {
		if f.Status != detection.StatusInvalidChecksum {
			m[string(f.Type)+"|"+f.Normalized] = f.Location
		}
	}
	return m
}

func TestTextEncodingsAndLines(t *testing.T) {
	latin1 := []byte("Se\xf1ora Ficticia\nRUT 12.345.678-5 correo uno@example.cl\n")
	r, err := Text(bytes.NewReader(latin1), eng(), opt)
	if err != nil {
		t.Fatal(err)
	}
	k := keys(r.Findings)
	if k["rut|12345678-5"].Line != 2 || k["email|uno@example.cl"].Line != 2 || r.Units != 2 {
		t.Fatalf("latin1: %+v units=%d", k, r.Units)
	}
	// UTF-16LE con BOM
	var u16 bytes.Buffer
	u16.Write([]byte{0xFF, 0xFE})
	for _, c := range "x\r\n11.111.111-1\r\n" {
		u16.Write([]byte{byte(c), 0})
	}
	r, err = Text(&u16, eng(), opt)
	if err != nil || keys(r.Findings)["rut|11111111-1"].Line != 2 {
		t.Fatalf("utf16: %v %+v", err, r)
	}
}

func TestTextHugeLineIsStreamed(t *testing.T) {
	line := strings.Repeat("relleno ", 200_000) + " 22.222.222-2 " + strings.Repeat("z", 5000) + " fin@example.net"
	r, err := Text(strings.NewReader(line), eng(), opt)
	if err != nil {
		t.Fatal(err)
	}
	k := keys(r.Findings)
	if len(k) != 2 || k["rut|22222222-2"].Line != 1 {
		t.Fatalf("línea enorme: %+v", k)
	}
}

func TestCSVLocations(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "tests", "fixtures", "synthetic", "tree", "clientes.csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := CSV(f, eng(), opt)
	if err != nil {
		t.Fatal(err)
	}
	k := keys(r.Findings)
	if l := k["rut|10000013-K"]; l.Row != 4 || l.Column != "B" {
		t.Fatalf("ubicación: %+v", l)
	}
	if l := k["email|cinco@xn--ejmplo-cva.test"]; l.Row != 6 || l.Column != "C" {
		t.Fatalf("ubicación email: %+v", l)
	}
	if _, ok := k["rut|12345678-9"]; ok {
		t.Fatal("DV inválido no debe indexarse")
	}
	// Separador ';'
	r, _ = CSV(strings.NewReader("a;b\nx;12.345.678-5\n"), eng(), opt)
	if l := keys(r.Findings)["rut|12345678-5"]; l.Row != 2 || l.Column != "B" {
		t.Fatalf("separador ;: %+v", l)
	}
}

func TestColumnRef(t *testing.T) {
	for i, want := range map[int]string{0: "A", 25: "Z", 26: "AA", 27: "AB", 701: "ZZ", 702: "AAA"} {
		if got := ColumnRef(i); got != want {
			t.Errorf("ColumnRef(%d)=%s want %s", i, got, want)
		}
	}
}

// pythonCmd devuelve el comando del worker o salta el test si no hay Python
// con las dependencias instaladas.
func pythonCmd(t *testing.T) []string {
	t.Helper()
	script, _ := filepath.Abs(filepath.Join("..", "..", "workers", "extractor", "extract.py"))
	for _, py := range []string{"python3", "python"} {
		p, err := exec.LookPath(py)
		// En Windows, "python3" suele ser el alias de WindowsApps que instala Python
		// por su cuenta; nunca usarlo desde pruebas.
		if err != nil || strings.Contains(p, "WindowsApps") {
			continue
		}
		if exec.Command(p, "-I", "-c", "import openpyxl, pypdfium2").Run() == nil {
			return []string{p, "-I", "-B", script}
		}
	}
	t.Skip("Python con openpyxl y pypdfium2 no disponible")
	return nil
}

func fixture(t *testing.T, name string) []byte {
	b, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "synthetic", "tree", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPythonPDFAndXLSX(t *testing.T) {
	py := &Python{Command: pythonCmd(t), Timeout: 60 * time.Second, MaxPages: 100}
	r, err := py.Extract(context.Background(), "pdf", bytes.NewReader(fixture(t, "contrato.pdf")), eng(), opt)
	if err != nil {
		t.Fatal(err)
	}
	k := keys(r.Findings)
	if k["rut|5126663-3"].Page != 3 || k["email|siete.ficticia@example.org"].Page != 3 {
		t.Fatalf("pdf: %+v", k)
	}
	if !r.Partial || r.Warnings[WarnPageNeedsOCR] != 1 {
		t.Fatalf("la página sin texto debe marcar parcial: %+v", r)
	}

	r, err = py.Extract(context.Background(), "xlsx", bytes.NewReader(fixture(t, "planilla.xlsx")), eng(), opt)
	if err != nil {
		t.Fatal(err)
	}
	k = keys(r.Findings)
	if l := k["rut|10000013-K"]; l.Sheet != "Oculta" || l.Row != 3 || l.Column != "B" {
		t.Fatalf("hoja oculta: %+v", l)
	}
	if r.Warnings[WarnHiddenSheet] != 1 || r.Partial {
		t.Fatalf("advertencias xlsx: %+v", r)
	}
}

// REL-004: archivo corrupto devuelve error clasificado.
func TestPythonCorrupt(t *testing.T) {
	py := &Python{Command: pythonCmd(t), Timeout: 30 * time.Second, MaxPages: 10}
	_, err := py.Extract(context.Background(), "pdf", strings.NewReader("%PDF-1.4 basura"), eng(), opt)
	ce, ok := err.(*CodedError)
	if !ok || ce.Code != ErrCorrupt {
		t.Fatalf("esperado corrupt, got %v", err)
	}
	_, err = py.Extract(context.Background(), "xlsx", strings.NewReader("PK\x03\x04 no es zip"), eng(), opt)
	if ce, ok := err.(*CodedError); !ok || ce.Code != ErrCorrupt {
		t.Fatalf("esperado corrupt xlsx, got %v", err)
	}
}

// ocrCmd devuelve el worker si además hay Pillow y tesseract con español.
func ocrCmd(t *testing.T) []string {
	t.Helper()
	cmd := pythonCmd(t)
	if exec.Command(cmd[0], "-I", "-c", "import PIL").Run() != nil {
		t.Skip("Pillow no disponible")
	}
	out, err := exec.Command("tesseract", "--list-langs").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "spa") {
		t.Skip("tesseract con idioma spa no disponible")
	}
	return cmd
}

func ocrFixture(t *testing.T, name string) []byte {
	b, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "synthetic", "ocr", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Fase 2: imágenes y PDF escaneados se leen con OCR; los hallazgos quedan con
// método "ocr" y requieren revisión (nunca se presentan como certeza).
func TestPythonOCR(t *testing.T) {
	py := &Python{Command: ocrCmd(t), Timeout: 120 * time.Second, MaxPages: 100,
		OCRLanguages: []string{"spa", "eng"}, OCRPageTimeout: 60 * time.Second}
	cases := []struct {
		kind, file string
		want       map[string]int // identificador -> página
	}{
		{"image", "escaneado.png", map[string]int{"rut|7654321-6": 1, "email|nueve.ficticia@example.cl": 1}},
		{"image", "foto.jpg", map[string]int{"rut|7654321-6": 1}},
		// Foto chica tipo cédula, fondo con trama: requiere ampliar y varias pasadas.
		{"image", "carnet_ficticio.png", map[string]int{"rut|11222333-9": 1}},
		{"image", "carnet_descargado.jpg", map[string]int{"rut|11222333-9": 1}}, // en realidad WEBP
		{"pdf", "escaneado.pdf", map[string]int{"rut|15555555-6": 2, "email|diez.ficticio@example.org": 2}},
		// Página con texto digital + escaneo pegado: el RUT solo está en la imagen.
		{"pdf", "mixto.pdf", map[string]int{"rut|9876543-3": 1}},
	}
	for _, tc := range cases {
		r, err := py.Extract(context.Background(), tc.kind, bytes.NewReader(ocrFixture(t, tc.file)), eng(), opt)
		if err != nil {
			t.Fatalf("%s: %v", tc.file, err)
		}
		if r.Partial {
			t.Errorf("%s: no debe quedar parcial con OCR: %+v", tc.file, r.Warnings)
		}
		k := keys(r.Findings)
		for id, page := range tc.want {
			if l, ok := k[id]; !ok || l.Page != page {
				t.Errorf("%s: falta %s en página %d (obtenido %+v)", tc.file, id, page, k)
			}
		}
		for _, f := range r.Findings {
			if f.Method != "ocr" || f.Status != detection.StatusRequiresReview {
				t.Errorf("%s: hallazgo OCR debe ser method=ocr y requires_review: %+v", tc.file, f)
			}
		}
	}
	// La página sin texto de contrato.pdf ahora se cubre con OCR (en blanco): sin parcial.
	r, err := py.Extract(context.Background(), "pdf", bytes.NewReader(fixture(t, "contrato.pdf")), eng(), opt)
	if err != nil || r.Partial || r.Warnings[WarnPageNeedsOCR] != 0 {
		t.Fatalf("contrato.pdf con OCR: %v %+v", err, r)
	}
	// Imagen inválida: error clasificado.
	_, err = py.Extract(context.Background(), "image", strings.NewReader("\x89PNG\r\n\x1a\nbasura"), eng(), opt)
	if ce, ok := err.(*CodedError); !ok || ce.Code != ErrCorrupt {
		t.Fatalf("imagen corrupta: %v", err)
	}
}

// Sin OCR configurado (o sin tesseract) una imagen queda parcial, sin error.
func TestPythonImageWithoutOCR(t *testing.T) {
	py := &Python{Command: pythonCmd(t), Timeout: 30 * time.Second, MaxPages: 10}
	r, err := py.Extract(context.Background(), "image", bytes.NewReader(ocrFixture(t, "escaneado.png")), eng(), opt)
	if err != nil || !r.Partial || r.Warnings[WarnImageNeedsOCR] != 1 || len(r.Findings) != 0 {
		t.Fatalf("sin OCR: %v %+v", err, r)
	}
}
