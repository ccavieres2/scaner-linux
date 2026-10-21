// Package config carga y valida estrictamente la configuración del escáner.
//
// Principios (spec §27, §13): rechazo de claves desconocidas, valores fuera de
// límites, rutas no permitidas y configuraciones inseguras. La configuración
// nunca contiene secretos en línea: solo rutas a archivos de secretos.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// SchemaVersion es la única versión de esquema aceptada por este binario.
const SchemaVersion = 1

// MaxConfigBytes limita el tamaño del archivo de configuración.
const MaxConfigBytes = 1 << 20

type Config struct {
	SchemaVersion int            `yaml:"schema_version"`
	Tenant        TenantConfig   `yaml:"tenant"`
	Sources       []SourceConfig `yaml:"sources"`
	Scanner       ScannerConfig  `yaml:"scanner"`
	API           APIConfig      `yaml:"api"`
	Database      DatabaseConfig `yaml:"database"`
	Secrets       SecretsConfig  `yaml:"secrets"`
	Security      SecurityConfig `yaml:"security"`
}

type TenantConfig struct {
	Name string `yaml:"name"`
}

type SourceConfig struct {
	ID       string `yaml:"id"`
	RootPath string `yaml:"root_path"`
	Enabled  *bool  `yaml:"enabled"`
}

type ScannerConfig struct {
	ScanMode             string   `yaml:"scan_mode"`
	FollowSymlinks       bool     `yaml:"follow_symlinks"`
	MaxFileBytes         int64    `yaml:"max_file_bytes"`
	MaxPDFPages          int      `yaml:"max_pdf_pages"`
	MaxExtractWorkers    int      `yaml:"max_extract_workers"`
	MaxOCRWorkers        int      `yaml:"max_ocr_workers"`
	MaxInflightFiles     int      `yaml:"max_inflight_files"`
	MaxOCRSecondsPerPage int      `yaml:"max_ocr_seconds_per_page"`
	OCRLanguages         []string `yaml:"ocr_languages"`
	RetryMax             int      `yaml:"retry_max"`
	ResumeOnRestart      *bool    `yaml:"resume_on_restart"`
	NetworkAccess        bool     `yaml:"network_access"`
	FileMountsReadOnly   *bool    `yaml:"file_mounts_read_only"`
	ChunkOverlapChars    int      `yaml:"chunk_overlap_chars"`
	// Tiempo máximo de extracción por archivo (texto/PDF/XLSX).
	MaxExtractSecondsPerFile int `yaml:"max_extract_seconds_per_file"`
	// Comando del worker Python de extracción; recibe el archivo por stdin.
	ExtractorCommand []string `yaml:"extractor_command"`
	// MaxHeavyWorkers: procesos Python simultáneos para PDF/XLSX.
	// MaxExtractWorkers aplica a la cola liviana (TXT/CSV, Go streaming).
	MaxHeavyWorkers int `yaml:"max_heavy_workers"`
	// WorkerMemoryMB: tope duro de memoria por proceso Python (RLIMIT_AS).
	WorkerMemoryMB int `yaml:"worker_memory_mb"`
	// MemoryBudgetMB: memoria total que el escáner puede comprometer en workers.
	MemoryBudgetMB int `yaml:"memory_budget_mb"`
	// LargeFileBytes: archivos >= este tamaño se procesan al final.
	LargeFileBytes int64 `yaml:"large_file_bytes"`
	// LargeFiles: qué hacer con archivos >= large_file_bytes.
	//   defer: no se procesan; quedan en la lista de pendientes (scan pending)
	//          hasta ejecutar `scan process-pending`.
	//   last:  se procesan en el mismo escaneo, después de todos los pequeños.
	LargeFiles string `yaml:"large_files"`
	// TempDir: temporales del worker (disco cifrado; se borran solos).
	TempDir string `yaml:"temp_dir"`
}

type APIConfig struct {
	Bind                string     `yaml:"bind"`
	Port                int        `yaml:"port"`
	RequireAuth         *bool      `yaml:"require_auth"`
	TLS                 TLSConfig  `yaml:"tls"`
	MaxBodyBytes        int64      `yaml:"max_body_bytes"`
	MaxPageSize         int        `yaml:"max_page_size"`
	Auth                AuthConfig `yaml:"auth"`
	SearchRatePerMinute int        `yaml:"search_rate_per_minute"`
	SearchBurst         int        `yaml:"search_burst"`
	// ContainerBindAll: dentro de un contenedor la API escucha en 0.0.0.0 del
	// contenedor; la exposición se restringe publicando el puerto solo en
	// api.bind del host (deploy/compose.yaml). Fuera de contenedores: false.
	ContainerBindAll bool `yaml:"container_bind_all"`
	// AllowedCIDRs: si no está vacío, solo se aceptan conexiones desde estas
	// redes (p. ej. la IP del servidor de la aplicación del cliente). Se evalúa
	// sobre la IP de la conexión TCP; no se confía en X-Forwarded-For.
	AllowedCIDRs []string `yaml:"allowed_cidrs"`
}

// AuthConfig (ADR-0006):
//
//	static_token_dev: token Bearer (hash SHA-256 en principals_file). Solo
//	                  pruebas o API en loopback.
//	mtls_token:       TLS obligatorio con certificado de cliente firmado por
//	                  tls.client_ca_file Y token Bearer; cada principal queda
//	                  ligado a la huella de su certificado. Un token robado sin
//	                  la clave privada del certificado no sirve.
type AuthConfig struct {
	Mode           string `yaml:"mode"`
	PrincipalsFile string `yaml:"principals_file"`
}

type TLSConfig struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	// ClientCAFile: CA que firma los certificados de cliente (modo mtls_token).
	ClientCAFile string `yaml:"client_ca_file"`
}

type DatabaseConfig struct {
	Host         string `yaml:"host"`
	Port         int    `yaml:"port"`
	Name         string `yaml:"name"`
	User         string `yaml:"user"`
	PasswordFile string `yaml:"password_file"`
	SSLMode      string `yaml:"sslmode"`
}

type SecretsConfig struct {
	HMACKeyFile       string `yaml:"hmac_key_file"`
	EncryptionKeyFile string `yaml:"encryption_key_file"`
}

type SecurityConfig struct {
	StoreRawText  bool  `yaml:"store_raw_text"`
	StoreSnippets bool  `yaml:"store_snippets"`
	AuditEnabled  *bool `yaml:"audit_enabled"`
}

// LoadFile lee, decodifica y valida un archivo de configuración.
func LoadFile(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("no se pudo abrir la configuración: %w", err)
	}
	defer f.Close()
	return Load(f)
}

// Load decodifica de forma estricta y valida. Los errores no incluyen valores
// de secretos (la configuración no admite secretos en línea).
func Load(r io.Reader) (*Config, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("lectura de configuración: %w", err)
	}
	if len(data) > MaxConfigBytes {
		return nil, errors.New("configuración excede el tamaño máximo permitido")
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("configuración vacía")
		}
		return nil, fmt.Errorf("YAML inválido o con claves desconocidas: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("la configuración debe contener un único documento YAML")
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func boolPtr(b bool) *bool { return &b }

// applyDefaults fija valores conservadores para campos omitidos.
func (c *Config) applyDefaults() {
	s := &c.Scanner
	if s.ScanMode == "" {
		s.ScanMode = "incremental"
	}
	if s.MaxFileBytes == 0 {
		s.MaxFileBytes = 500 << 20
	}
	if s.MaxPDFPages == 0 {
		s.MaxPDFPages = 2000
	}
	if s.MaxExtractWorkers == 0 {
		s.MaxExtractWorkers = 4
	}
	if s.MaxOCRWorkers == 0 {
		s.MaxOCRWorkers = 2
	}
	if s.MaxInflightFiles == 0 {
		s.MaxInflightFiles = 64
	}
	if s.MaxOCRSecondsPerPage == 0 {
		s.MaxOCRSecondsPerPage = 60
	}
	if len(s.OCRLanguages) == 0 {
		s.OCRLanguages = []string{"spa", "eng"}
	}
	if s.RetryMax == 0 {
		s.RetryMax = 3
	}
	if s.ResumeOnRestart == nil {
		s.ResumeOnRestart = boolPtr(true)
	}
	if s.FileMountsReadOnly == nil {
		s.FileMountsReadOnly = boolPtr(true)
	}
	if s.ChunkOverlapChars == 0 {
		s.ChunkOverlapChars = 256
	}
	if s.MaxExtractSecondsPerFile == 0 {
		s.MaxExtractSecondsPerFile = 300
	}
	if s.MaxHeavyWorkers == 0 {
		s.MaxHeavyWorkers = 2
	}
	if s.WorkerMemoryMB == 0 {
		s.WorkerMemoryMB = 1024
	}
	if s.MemoryBudgetMB == 0 {
		s.MemoryBudgetMB = 3072
	}
	if s.LargeFileBytes == 0 {
		s.LargeFileBytes = 1 << 20
	}
	if s.LargeFiles == "" {
		s.LargeFiles = "defer"
	}
	if len(s.ExtractorCommand) == 0 {
		s.ExtractorCommand = []string{"python3", "-I", "-B", "/opt/arsop/workers/extractor/extract.py"}
	}
	for i := range c.Sources {
		if c.Sources[i].Enabled == nil {
			c.Sources[i].Enabled = boolPtr(true)
		}
	}
	a := &c.API
	if a.Bind == "" {
		a.Bind = "127.0.0.1"
	}
	if a.Port == 0 {
		a.Port = 8080
	}
	if a.RequireAuth == nil {
		a.RequireAuth = boolPtr(true)
	}
	if a.MaxBodyBytes == 0 {
		a.MaxBodyBytes = 64 << 10
	}
	if a.MaxPageSize == 0 {
		a.MaxPageSize = 100
	}
	if a.Auth.Mode == "" {
		a.Auth.Mode = "static_token_dev"
	}
	if a.SearchRatePerMinute == 0 {
		a.SearchRatePerMinute = 30
	}
	if a.SearchBurst == 0 {
		a.SearchBurst = 5
	}
	d := &c.Database
	if d.Port == 0 {
		d.Port = 5432
	}
	if d.SSLMode == "" {
		d.SSLMode = "verify-full"
	}
	if c.Security.AuditEnabled == nil {
		c.Security.AuditEnabled = boolPtr(true)
	}
}
