package main

import (
	"testing"

	"arsop-scanner/internal/detection"
)

func TestMask(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"rut", "12345678-5"}:                  "12.***.**8-5",
		{"rut", "7654321-6"}:                   "7.***.**1-6",
		{"rut", "123-4"}:                       "***",
		{"email", "nueve.ficticia@example.cl"}: "n***@example.cl",
		{"email", "@x"}:                        "***",
	} {
		if got := mask(in[0], in[1]); got != want {
			t.Errorf("mask(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestLocString(t *testing.T) {
	if got := locString(detection.Location{Sheet: "Clientes", Row: 3, Column: "B"}); got != "hoja Clientes, fila 3, col B" {
		t.Errorf("got %q", got)
	}
	if got := locString(detection.Location{}); got != "-" {
		t.Errorf("got %q", got)
	}
}
