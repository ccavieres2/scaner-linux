package detection

import (
	"sort"
	"strings"
	"testing"

	"arsop-scanner/internal/identity"
)

func values(fs []Finding, t identity.Type, status string) []string {
	var out []string
	for _, f := range fs {
		if f.Type == t && f.Status == status {
			out = append(out, f.Normalized)
		}
	}
	sort.Strings(out)
	return out
}

func eq(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

func TestDetectRUTAndEmail(t *testing.T) {
	e := NewEngine(DefaultOptions())
	text := "RUT 12.345.678-5, otro 11111111-1 y 10.000.013-k; malo 12.345.678-9. " +
		"Correo: uno.ficticio@example.cl, (dos+tag@Example.COM). Fin."
	fs := e.Detect(text, Location{Line: 1}, "digital")
	if got := values(fs, identity.TypeRUT, StatusValidatedFormat); !eq(got, []string{"10000013-K", "11111111-1", "12345678-5"}) {
		t.Fatalf("RUT válidos: %v", got)
	}
	if got := values(fs, identity.TypeRUT, StatusInvalidChecksum); !eq(got, []string{"12345678-9"}) {
		t.Fatalf("RUT inválidos: %v", got)
	}
	if got := values(fs, identity.TypeEmail, StatusValidatedFormat); !eq(got, []string{"dos+tag@example.com", "uno.ficticio@example.cl"}) {
		t.Fatalf("emails: %v", got)
	}
}

func TestBoundariesAvoidFalsePositives(t *testing.T) {
	e := NewEngine(DefaultOptions())
	for _, s := range []string{
		"cuenta 9912345678-5",   // parte de número mayor
		"ref 12.345.678-55",     // sigue un dígito
		"monto 1.234.567.890-1", // número con miles
		"serie A12345678-5",
		"telefono 1234 5678",
	} {
		if fs := values(e.Detect(s, Location{}, "digital"), identity.TypeRUT, StatusValidatedFormat); len(fs) > 0 {
			t.Errorf("falso positivo en %q: %v", s, fs)
		}
	}
}

// DOC-002
func TestOCRConfusionIsUncertain(t *testing.T) {
	e := NewEngine(Options{MaxOCREdits: 6})
	fs := e.Detect("RUT 2O.OOO.OO5-6", Location{Page: 1}, "ocr")
	if len(fs) != 1 || fs[0].Status != StatusRequiresReview || fs[0].Normalized != "20000005-6" || fs[0].Confidence >= 0.5 {
		t.Fatalf("hipótesis OCR inesperada: %+v", fs)
	}
	if fs := NewEngine(Options{MaxOCREdits: 2}).Detect("RUT 2O.OOO.OO5-6", Location{}, "ocr"); len(fs) != 0 {
		t.Fatalf("debe respetar umbral: %+v", fs)
	}
	// Guion del DV mal leído (foto de carnet): hipótesis incierta solo en OCR.
	fs = e.Detect("RUN 80.000.004+1 y B0.000.004~1", Location{}, "ocr")
	if len(fs) != 2 || fs[0].Normalized != "80000004-1" || fs[1].Normalized != "80000004-1" ||
		fs[0].Status != StatusRequiresReview || fs[0].Confidence >= 0.5 {
		t.Fatalf("guion OCR: %+v", fs)
	}
	if fs := e.Detect("RUN 80.000.0041", Location{}, "ocr"); len(fs) != 1 || fs[0].Normalized != "80000004-1" {
		t.Fatalf("guion perdido en OCR: %+v", fs)
	}
	if fs := e.Detect("total 80.000.0041 y 12.345.6785", Location{}, "digital"); len(fs) != 0 {
		t.Fatalf("sin guion en texto digital no es RUT: %+v", fs)
	}
	if fs := e.Detect("RUN 80.000.004+1", Location{}, "digital"); len(fs) != 0 {
		t.Fatalf("en texto digital no se reinterpreta: %+v", fs)
	}
	if fs := e.Detect("RUN 80.000.004+4", Location{}, "ocr"); len(fs) != 0 {
		t.Fatalf("DV inválido no debe aceptarse: %+v", fs)
	}
	// Texto OCR con RUT limpio sigue requiriendo revisión.
	fs = e.Detect("12.345.678-5", Location{}, "ocr")
	if len(fs) != 1 || fs[0].Status != StatusRequiresReview {
		t.Fatalf("OCR debe marcar requires_review: %+v", fs)
	}
}

// DOC-006: valores que cruzan límites de ventanas no se pierden ni duplican.
func TestDetectStreamAcrossBoundaries(t *testing.T) {
	e := NewEngine(DefaultOptions())
	window, overlap := 300, 256
	var b strings.Builder
	want := map[string]int{}
	for i := 0; b.Len() < 20000; i++ {
		b.WriteString(strings.Repeat("x ", 7+i%13))
		if i%2 == 0 {
			b.WriteString(" 12.345.678-5 ")
			want["12345678-5"]++
		} else {
			b.WriteString(" persona.ficticia@example.cl ")
			want["persona.ficticia@example.cl"]++
		}
	}
	got := map[string]int{}
	err := e.DetectStream(strings.NewReader(b.String()), window, overlap, Location{}, "digital", func(f Finding) {
		got[f.Normalized]++
	})
	if err != nil {
		t.Fatal(err)
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("%s: esperado %d, obtenido %d", k, n, got[k])
		}
	}
	// Equivale a analizar el texto completo de una vez.
	if all := e.Detect(b.String(), Location{}, "digital"); len(all) != got["12345678-5"]+got["persona.ficticia@example.cl"] {
		t.Errorf("stream (%v) difiere de análisis completo (%d)", got, len(all))
	}
}
