package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadExample(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "config", "scanner.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestExampleConfigIsValid(t *testing.T) {
	cfg, err := Load(strings.NewReader(loadExample(t)))
	if err != nil {
		t.Fatalf("la configuración de ejemplo debe ser válida: %v", err)
	}
	if len(cfg.Sources) != 2 || !*cfg.API.RequireAuth || cfg.Scanner.FollowSymlinks {
		t.Fatalf("valores inesperados: %+v", cfg)
	}
}

func TestMinimalConfigGetsSecureDefaults(t *testing.T) {
	min := `
schema_version: 1
tenant: {name: t}
sources: [{id: a, root_path: /data/a}]
api: {auth: {principals_file: /run/secrets/p.yaml}}
database: {host: postgres, name: arsop, user: u, password_file: /run/secrets/db, sslmode: disable}
secrets: {hmac_key_file: /run/secrets/h, encryption_key_file: /run/secrets/e}
`
	cfg, err := Load(strings.NewReader(min))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.API.Bind != "127.0.0.1" || !*cfg.API.RequireAuth || !*cfg.Security.AuditEnabled ||
		!*cfg.Scanner.FileMountsReadOnly || cfg.Scanner.NetworkAccess || cfg.Security.StoreRawText {
		t.Fatalf("defaults inseguros: %+v", cfg)
	}
}

// replace sustituye una línea exacta del ejemplo para construir variantes.
func replace(t *testing.T, base, old, new string) string {
	t.Helper()
	if !strings.Contains(base, old) {
		t.Fatalf("fragmento no encontrado en ejemplo: %q", old)
	}
	return strings.Replace(base, old, new, 1)
}

func TestRejectsInsecureOrInvalid(t *testing.T) {
	base := loadExample(t)
	cases := []struct {
		name, old, new, want string
	}{
		{"clave desconocida", "  audit_enabled: true", "  audit_enabled: true\n  debug_dump: true", "claves desconocidas"},
		{"schema", "schema_version: 1", "schema_version: 2", "schema_version"},
		{"traversal", "/data/input/rrhh", "/data/input/../../etc", "canónica"},
		{"relativa", "/data/input/rrhh", "data/input/rrhh", "absoluta"},
		{"raíz sistema", "/data/input/rrhh", "/", "ruta del sistema"},
		{"bajo /etc", "/data/input/rrhh", "/etc/arsop", "ruta del sistema"},
		{"home completo", "/data/input/rrhh", "/home", "ruta del sistema"},
		{"docker.sock", "/data/input/rrhh", "/srv/docker.sock", "Docker"},
		{"anidadas", "/data/input/finanzas", "/data/input/rrhh/sub", "solapa"},
		{"id duplicado", "id: finanzas", "id: rrhh", "duplicado"},
		{"symlinks", "follow_symlinks: false", "follow_symlinks: true", "follow_symlinks"},
		{"red", "network_access: false", "network_access: true", "network_access"},
		{"mounts rw", "file_mounts_read_only: true", "file_mounts_read_only: false", "file_mounts_read_only"},
		{"sin auth", "require_auth: true", "require_auth: false", "require_auth"},
		{"bind público", "bind: 127.0.0.1", "bind: 0.0.0.0", "0.0.0.0"},
		{"bind ip pública", "bind: 127.0.0.1", "bind: 8.8.8.8", "privadas"},
		{"bind privado sin tls", "bind: 127.0.0.1", "bind: 10.0.0.5", "api.tls"},
		{"texto bruto", "store_raw_text: false", "store_raw_text: true", "store_raw_text"},
		{"snippets", "store_snippets: false", "store_snippets: true", "store_snippets"},
		{"auditoría off", "audit_enabled: true", "audit_enabled: false", "audit_enabled"},
		{"contraseña en línea", "password_file: /run/secrets/db_password", "password: hunter2", "claves desconocidas"},
		{"misma clave", "encryption_key_file: /run/secrets/encryption_key", "encryption_key_file: /run/secrets/hmac_key", "distintas"},
		{"secreto en fuente", "hmac_key_file: /run/secrets/hmac_key", "hmac_key_file: /data/input/rrhh/k", "dentro de la fuente"},
		{"sslmode disable externo", "host: postgres", "host: db.example.com", "sslmode"},
		{"workers fuera de rango", "max_ocr_workers: 2", "max_ocr_workers: 1000", "max_ocr_workers"},
		{"idioma inválido", "ocr_languages: [spa, eng]", "ocr_languages: [\"spa;rm\"]", "ocr_languages"},
		{"modo inválido", "scan_mode: incremental", "scan_mode: turbo", "scan_mode"},
		{"presupuesto insuficiente", "memory_budget_mb: 3072", "memory_budget_mb: 1024", "memory_budget_mb"},
		{"memoria worker mínima", "worker_memory_mb: 1024", "worker_memory_mb: 64", "worker_memory_mb"},
		{"temporal dentro de fuente", "temp_dir: /var/tmp/arsop", "temp_dir: /data/input/rrhh/tmp", "temp_dir"},
		{"temporal relativo", "temp_dir: /var/tmp/arsop", "temp_dir: tmp", "temp_dir"},
		{"heavy fuera de rango", "max_heavy_workers: 2", "max_heavy_workers: 100", "max_heavy_workers"},
		{"large_files inválido", "large_files: defer", "large_files: skip", "large_files"},
		{"auth desconocida", "mode: static_token_dev", "mode: none", "api.auth.mode"},
		{"mtls sin tls", "mode: static_token_dev", "mode: mtls_token", "client_ca_file"},
		{"red sin mtls", "bind: 127.0.0.1", "bind: 10.0.0.5\n  tls: {cert_file: /etc/arsop/tls/a.crt, key_file: /etc/arsop/tls/a.key}", "mtls_token"},
		{"cidr abierto", "search_burst: 5", "search_burst: 5\n  allowed_cidrs: [0.0.0.0/0]", "allowed_cidrs"},
		{"cidr inválido", "search_burst: 5", "search_burst: 5\n  allowed_cidrs: [10.0.0.300/8]", "allowed_cidrs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(strings.NewReader(replace(t, base, tc.old, tc.new)))
			if err == nil {
				t.Fatalf("se esperaba rechazo")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q no menciona %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Fatalf("el error filtra un valor secreto")
			}
		})
	}
}

func TestCollectsAllProblems(t *testing.T) {
	bad := `
schema_version: 1
tenant: {name: ""}
sources: []
scanner: {network_access: true}
api: {require_auth: false}
`
	_, err := Load(strings.NewReader(bad))
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("se esperaba ValidationError, recibido %v", err)
	}
	if len(ve.Problems) < 5 {
		t.Fatalf("se esperaban múltiples problemas, recibidos %d: %v", len(ve.Problems), ve.Problems)
	}
}

func TestRejectsMultiDocumentAndEmptyAndOversize(t *testing.T) {
	if _, err := Load(strings.NewReader("")); err == nil {
		t.Fatal("vacío debe fallar")
	}
	if _, err := Load(strings.NewReader(loadExample(t) + "\n---\nschema_version: 1\n")); err == nil {
		t.Fatal("multi-documento debe fallar")
	}
	big := strings.Repeat("#", MaxConfigBytes+10)
	if _, err := Load(strings.NewReader(big)); err == nil || !strings.Contains(err.Error(), "tamaño") {
		t.Fatalf("archivo excesivo debe fallar: %v", err)
	}
}
