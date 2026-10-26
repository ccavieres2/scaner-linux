package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"arsop-scanner/internal/persistence"
)

func TestRenderProgress(t *testing.T) {
	pr := &persistence.Progress{Tasks: map[string]int{"completed": 3, "failed": 1, "pending": 4, "deferred": 2}}
	got := renderProgress(pr, true, 12*time.Second)
	for _, want := range []string{" 50%", "4/8 archivos", "12s", "(2 pendientes por tamaño)", "[###############---------------]"} {
		if !strings.Contains(got, want) {
			t.Errorf("falta %q en %q", want, got)
		}
	}
	if got := renderProgress(pr, false, 0); !strings.Contains(got, "contando archivos... 10 encontrados") {
		t.Errorf("inventario: %q", got)
	}
	empty := &persistence.Progress{Tasks: map[string]int{}}
	if got := renderProgress(empty, true, 0); !strings.Contains(got, "100%  0/0") {
		t.Errorf("sin archivos: %q", got)
	}
}

func TestProgressWriteRedrawsBar(t *testing.T) {
	var out bytes.Buffer
	p := &progress{out: &out}
	p.draw("BARRA")
	p.Write([]byte("aviso\n"))
	if got := out.String(); !strings.HasSuffix(got, "\r\033[Kaviso\nBARRA") {
		t.Fatalf("salida: %q", got)
	}
	if isTerminal(&out) {
		t.Fatal("un buffer no es terminal")
	}
}
