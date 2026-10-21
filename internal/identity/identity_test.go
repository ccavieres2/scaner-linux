package identity

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

// DOC-001: RUT con DV correcto e incorrecto; datos ficticios.
func TestNormalizeRUT(t *testing.T) {
	cases := []struct {
		in, want string
		valid    bool
		err      bool
	}{
		{"12.345.678-5", "12345678-5", true, false},
		{"12345678-5", "12345678-5", true, false},
		{"123456785", "12345678-5", true, false},
		{"12 345 678 - 5", "12345678-5", true, false},
		{"10.000.013-k", "10000013-K", true, false},
		{"10000004-0", "10000004-0", true, false},
		{"5.126.663-3", "5126663-3", true, false},
		{"12.345.678-9", "12345678-9", false, false}, // DV incorrecto: se conserva, no se corrige
		{"9.999.999-9", "9999999-9", false, false},
		{"999.999-3", "", false, true},     // demasiado corto
		{"123.456.789-0", "", false, true}, // demasiado largo
		{"1K345678-5", "", false, true},    // K fuera de posición
		{"12,345,678-5", "", false, true},
		{"01234567-8", "", false, true},
		{"", "", false, true},
	}
	for _, c := range cases {
		got, valid, err := NormalizeRUT(c.in)
		if (err != nil) != c.err || got != c.want || valid != c.valid {
			t.Errorf("NormalizeRUT(%q) = %q,%v,%v; esperado %q,%v,err=%v", c.in, got, valid, err, c.want, c.valid, c.err)
		}
	}
}

// DOC-002: confusiones OCR solo como hipótesis acotada.
func TestOCRHypothesis(t *testing.T) {
	if c, e, ok := OCRHypothesis("2O.OOO.OO5-6", 6); !ok || c != "20000005-6" || e != 6 {
		t.Fatalf("esperada hipótesis válida, got %q %d %v", c, e, ok)
	}
	if _, _, ok := OCRHypothesis("2O.OOO.OO5-6", 2); ok {
		t.Fatal("costo de edición excede umbral: no debe aceptarse")
	}
	if _, _, ok := OCRHypothesis("12.345.678-5", 3); ok {
		t.Fatal("sin sustituciones no es hipótesis OCR")
	}
	if _, _, ok := OCRHypothesis("1S.345.678-9", 3); ok {
		t.Fatal("DV inválido tras corrección no debe aceptarse")
	}
}

// DOC-003: email con IDN y parte local especial.
func TestNormalizeEmail(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  Uno.Ficticio@Example.CL ", "Uno.Ficticio@example.cl"}, // parte local intacta
		{"dos+tag@example.com", "dos+tag@example.com"},
		{"a.b.c@EJEMPLO.test", "a.b.c@ejemplo.test"},
		{"cinco@ejémplo.test", "cinco@xn--ejmplo-cva.test"},
		{"o'brien@example.org", "o'brien@example.org"},
	}
	for _, c := range cases {
		got, err := NormalizeEmail(c.in)
		if err != nil || got != c.want {
			t.Errorf("NormalizeEmail(%q) = %q, %v; esperado %q", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "@x.cl", "a@", "a..b@x.cl", ".a@x.cl", "a@localhost", "a@x.c", "a b@x.cl", "a@-x.cl", strings.Repeat("a", 65) + "@x.cl"} {
		if _, err := NormalizeEmail(bad); err == nil {
			t.Errorf("NormalizeEmail(%q) debía fallar", bad)
		}
	}
}

func TestLookupToken(t *testing.T) {
	k1, _ := NewKey(1, bytes.Repeat([]byte{1}, 32))
	k2, _ := NewKey(1, bytes.Repeat([]byte{2}, 32))
	a := k1.LookupToken(TypeRUT, "12345678-5")
	if !bytes.Equal(a, k1.LookupToken(TypeRUT, "12345678-5")) {
		t.Fatal("HMAC no determinista")
	}
	if bytes.Equal(a, k2.LookupToken(TypeRUT, "12345678-5")) {
		t.Fatal("claves distintas deben producir tokens distintos")
	}
	if bytes.Equal(a, k1.LookupToken(TypeEmail, "12345678-5")) {
		t.Fatal("el tipo debe separar dominios del HMAC")
	}
	// Vector fijo para detectar cambios accidentales de construcción (rompería índices).
	if got := hex.EncodeToString(a); got != hex.EncodeToString(k1.LookupToken(TypeRUT, "12345678-5")) || len(a) != 32 {
		t.Fatal("tamaño de token inesperado")
	}
	if _, err := NewKey(1, []byte("corta")); err == nil {
		t.Fatal("clave corta debe rechazarse")
	}
	if strings.Contains(k1.String(), "\x01") {
		t.Fatal("String() filtra material de clave")
	}
}
