package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLI(t *testing.T) {
	example := filepath.Join("..", "..", "config", "scanner.example.yaml")
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte("schema_version: 1\napi: {require_auth: false}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args     []string
		code     int
		contains string
	}{
		{[]string{"version"}, 0, "scanner"},
		{[]string{"config", "validate", "--file", example}, 0, "configuración válida"},
		{[]string{"config", "validate", "--file", bad}, 1, "require_auth"},
		{[]string{"config", "validate", "--file", "/no/existe.yaml"}, 1, "no se pudo abrir"},
		{[]string{"sources", "list", "--file", example}, 0, "rrhh\tenabled\t/data/input/rrhh"},
		{[]string{"benchmark"}, 3, "no implementado"},
		{[]string{"scan", "start", "--file", example, "--source", "noexiste"}, 2, "--source"},
		{[]string{"scan", "status", "--file", example, "--scan", "x"}, 2, "UUID"},
		{[]string{"token", "new"}, 0, "token_sha256"},
		{[]string{"desconocido"}, 2, "desconocido"},
		{nil, 2, "uso:"},
	}
	for _, tc := range cases {
		var out, errb bytes.Buffer
		code := run(tc.args, &out, &errb)
		all := out.String() + errb.String()
		if code != tc.code || !strings.Contains(all, tc.contains) {
			t.Errorf("args %v: code=%d salida=%q; esperado code=%d con %q", tc.args, code, all, tc.code, tc.contains)
		}
	}
}
