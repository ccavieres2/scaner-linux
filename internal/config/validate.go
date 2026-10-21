package config

import (
	"fmt"
	"net"
	"path"
	"regexp"
	"strings"
)

// LightWorkerReserveMB es la memoria reservada por cada worker liviano (Go,
// lectura por partes) dentro del presupuesto global.
const LightWorkerReserveMB = 64

// ValidationError agrupa todos los problemas encontrados para que el operador
// pueda corregirlos de una vez.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	return "configuración inválida:\n  - " + strings.Join(e.Problems, "\n  - ")
}

type validator struct{ problems []string }

func (v *validator) addf(format string, args ...any) {
	v.problems = append(v.problems, fmt.Sprintf(format, args...))
}

func (v *validator) intRange(field string, val, min, max int) {
	if val < min || val > max {
		v.addf("%s: valor %d fuera de rango [%d, %d]", field, val, min, max)
	}
}

var (
	sourceIDRe   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
	ocrLangRe    = regexp.MustCompile(`^[a-z]{3}(_[a-z]+)?$`)
	identifierRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
	hostnameRe   = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
)

// forbiddenRoots son rutas del host que nunca deben escanearse como raíz ni
// contener una raíz de escaneo (spec §27: no montar /, docker.sock, etc.).
var forbiddenRoots = []string{
	"/", "/etc", "/proc", "/sys", "/dev", "/boot", "/run", "/var/run",
	"/root", "/home", "/var/lib/docker", "/usr", "/bin", "/sbin", "/lib",
}

// forbiddenPrefixes no pueden contener raíces de escaneo en ningún nivel.
var forbiddenPrefixes = []string{
	"/etc", "/proc", "/sys", "/dev", "/boot", "/run", "/var/run", "/root",
	"/var/lib/docker", "/usr", "/bin", "/sbin", "/lib",
}

// Validate comprueba la configuración ya decodificada y con valores por defecto.
func (c *Config) Validate() error {
	v := &validator{}

	if c.SchemaVersion != SchemaVersion {
		v.addf("schema_version: se requiere %d (recibido %d)", SchemaVersion, c.SchemaVersion)
	}
	if n := strings.TrimSpace(c.Tenant.Name); n == "" || len(n) > 128 {
		v.addf("tenant.name: requerido, máximo 128 caracteres")
	}

	c.validateSources(v)
	c.validateScanner(v)
	c.validateAPI(v)
	c.validateDatabase(v)
	c.validateSecrets(v)
	c.validateSecurity(v)

	if len(v.problems) > 0 {
		return &ValidationError{Problems: v.problems}
	}
	return nil
}

// isCleanAbsLinuxPath verifica que p sea absoluta, canónica y sin '..'.
// El producto se despliega en Linux, por lo que se usa el paquete path (POSIX).
func isCleanAbsLinuxPath(p string) bool {
	if p == "" || !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\x00\\") {
		return false
	}
	if path.Clean(p) != p {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

// within reporta si child es igual a parent o está contenido en él.
func within(child, parent string) bool {
	if parent == "/" {
		return true
	}
	return child == parent || strings.HasPrefix(child, parent+"/")
}

func (c *Config) validateSources(v *validator) {
	if len(c.Sources) == 0 {
		v.addf("sources: se requiere al menos una fuente autorizada")
	}
	if len(c.Sources) > 256 {
		v.addf("sources: máximo 256 fuentes")
	}
	seenID := map[string]bool{}
	var roots []string
	for i, s := range c.Sources {
		f := fmt.Sprintf("sources[%d]", i)
		if !sourceIDRe.MatchString(s.ID) {
			v.addf("%s.id: debe cumplir %s", f, sourceIDRe.String())
		} else if seenID[s.ID] {
			v.addf("%s.id: duplicado %q", f, s.ID)
		}
		seenID[s.ID] = true

		if !isCleanAbsLinuxPath(s.RootPath) {
			v.addf("%s.root_path: debe ser una ruta Linux absoluta y canónica, sin '..'", f)
			continue
		}
		bad := false
		for _, fr := range forbiddenRoots {
			if s.RootPath == fr {
				v.addf("%s.root_path: la ruta del sistema %q no puede ser raíz de escaneo", f, fr)
				bad = true
			}
		}
		for _, fp := range forbiddenPrefixes {
			if !bad && within(s.RootPath, fp) {
				v.addf("%s.root_path: no puede estar bajo la ruta del sistema %q", f, fp)
				bad = true
			}
		}
		if strings.HasSuffix(s.RootPath, "docker.sock") {
			v.addf("%s.root_path: el socket de Docker nunca es una fuente válida", f)
		}
		for j, r := range roots {
			if within(s.RootPath, r) || within(r, s.RootPath) {
				v.addf("%s.root_path: se solapa con sources[%d]; las raíces no pueden anidarse", f, j)
			}
		}
		roots = append(roots, s.RootPath)
	}
}

func (c *Config) validateScanner(v *validator) {
	s := c.Scanner
	if s.ScanMode != "incremental" && s.ScanMode != "full" {
		v.addf("scanner.scan_mode: debe ser 'incremental' o 'full'")
	}
	if s.FollowSymlinks {
		v.addf("scanner.follow_symlinks: seguir enlaces simbólicos no está soportado en v1 (ver ADR-0005)")
	}
	if s.NetworkAccess {
		v.addf("scanner.network_access: el acceso de red saliente está prohibido por defecto (spec §2, §9.2)")
	}
	if s.FileMountsReadOnly != nil && !*s.FileMountsReadOnly {
		v.addf("scanner.file_mounts_read_only: debe ser true; las fuentes son solo lectura")
	}
	if s.MaxFileBytes < 1 || s.MaxFileBytes > 20<<30 {
		v.addf("scanner.max_file_bytes: fuera de rango [1, 21474836480]")
	}
	v.intRange("scanner.max_pdf_pages", s.MaxPDFPages, 1, 100000)
	v.intRange("scanner.max_extract_workers", s.MaxExtractWorkers, 1, 256)
	v.intRange("scanner.max_ocr_workers", s.MaxOCRWorkers, 1, 256)
	v.intRange("scanner.max_inflight_files", s.MaxInflightFiles, 1, 100000)
	v.intRange("scanner.max_ocr_seconds_per_page", s.MaxOCRSecondsPerPage, 1, 3600)
	v.intRange("scanner.retry_max", s.RetryMax, 0, 20)
	// Debe superar el largo máximo de un email (254) para no perder valores en límites.
	v.intRange("scanner.chunk_overlap_chars", s.ChunkOverlapChars, 256, 65536)
	v.intRange("scanner.max_heavy_workers", s.MaxHeavyWorkers, 1, 64)
	v.intRange("scanner.worker_memory_mb", s.WorkerMemoryMB, 256, 65536)
	v.intRange("scanner.memory_budget_mb", s.MemoryBudgetMB, 512, 1<<20)
	if s.MemoryBudgetMB < s.WorkerMemoryMB+LightWorkerReserveMB*s.MaxExtractWorkers {
		v.addf("scanner.memory_budget_mb: debe alcanzar al menos para un worker pesado (worker_memory_mb) más %d MB por worker liviano", LightWorkerReserveMB)
	}
	if s.LargeFileBytes < 1 {
		v.addf("scanner.large_file_bytes: debe ser positivo")
	}
	if s.LargeFiles != "defer" && s.LargeFiles != "last" {
		v.addf("scanner.large_files: debe ser 'defer' o 'last'")
	}
	if s.TempDir != "" && !isCleanAbsLinuxPath(s.TempDir) {
		v.addf("scanner.temp_dir: ruta absoluta canónica")
	}
	for _, src := range c.Sources {
		if s.TempDir != "" && isCleanAbsLinuxPath(src.RootPath) && (within(s.TempDir, src.RootPath) || within(src.RootPath, s.TempDir)) {
			v.addf("scanner.temp_dir: no puede estar dentro de una fuente ni contenerla")
		}
	}
	if s.MaxInflightFiles < s.MaxExtractWorkers+s.MaxHeavyWorkers+s.MaxOCRWorkers {
		v.addf("scanner.max_inflight_files: debe ser >= suma de workers")
	}
	v.intRange("scanner.max_extract_seconds_per_file", s.MaxExtractSecondsPerFile, 1, 86400)
	if len(s.ExtractorCommand) > 16 {
		v.addf("scanner.extractor_command: máximo 16 elementos")
	}
	for i, a := range s.ExtractorCommand {
		if a == "" || len(a) > 1024 || strings.ContainsRune(a, 0) {
			v.addf("scanner.extractor_command[%d]: argumento inválido", i)
		}
	}
	if len(s.OCRLanguages) > 16 {
		v.addf("scanner.ocr_languages: máximo 16 idiomas")
	}
	for i, l := range s.OCRLanguages {
		if !ocrLangRe.MatchString(l) {
			v.addf("scanner.ocr_languages[%d]: código de idioma Tesseract inválido", i)
		}
	}
}

func (c *Config) validateAPI(v *validator) {
	a := c.API
	if a.RequireAuth != nil && !*a.RequireAuth {
		v.addf("api.require_auth: no puede desactivarse (deny-by-default, spec §9.3)")
	}
	ip := net.ParseIP(a.Bind)
	switch {
	case ip == nil:
		v.addf("api.bind: debe ser una dirección IP explícita (no nombre de host)")
	case ip.IsUnspecified():
		v.addf("api.bind: 0.0.0.0/:: no permitido; ligar a loopback o a una IP privada concreta")
	case !ip.IsLoopback() && !ip.IsPrivate():
		v.addf("api.bind: solo se permiten direcciones loopback o privadas (sin exposición pública)")
	}
	v.intRange("api.port", a.Port, 1024, 65535)
	tlsSet := a.TLS.CertFile != "" || a.TLS.KeyFile != ""
	if tlsSet {
		if !isCleanAbsLinuxPath(a.TLS.CertFile) || !isCleanAbsLinuxPath(a.TLS.KeyFile) {
			v.addf("api.tls: cert_file y key_file deben ser rutas absolutas canónicas")
		}
	}
	if ip != nil && !ip.IsLoopback() && !tlsSet {
		v.addf("api.tls: requerido cuando api.bind no es loopback")
	}
	if a.MaxBodyBytes < 1024 || a.MaxBodyBytes > 1<<20 {
		v.addf("api.max_body_bytes: fuera de rango [1024, 1048576]")
	}
	v.intRange("api.max_page_size", a.MaxPageSize, 1, 500)
	v.intRange("api.search_rate_per_minute", a.SearchRatePerMinute, 1, 6000)
	v.intRange("api.search_burst", a.SearchBurst, 1, 1000)
	switch a.Auth.Mode {
	case "static_token_dev":
		if a.TLS.ClientCAFile != "" {
			v.addf("api.tls.client_ca_file: solo se usa con api.auth.mode: mtls_token")
		}
	case "mtls_token":
		if !tlsSet {
			v.addf("api.auth.mode mtls_token: requiere api.tls.cert_file y key_file")
		}
		if !isCleanAbsLinuxPath(a.TLS.ClientCAFile) {
			v.addf("api.tls.client_ca_file: requerido en mtls_token, ruta absoluta canónica")
		}
	default:
		v.addf("api.auth.mode: debe ser 'static_token_dev' o 'mtls_token'")
	}
	if ip != nil && !ip.IsLoopback() && a.Auth.Mode != "mtls_token" {
		v.addf("api.auth.mode: fuera de loopback se exige mtls_token (certificado de cliente + token)")
	}
	if len(a.AllowedCIDRs) > 64 {
		v.addf("api.allowed_cidrs: máximo 64 redes")
	}
	for i, c := range a.AllowedCIDRs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			v.addf("api.allowed_cidrs[%d]: CIDR inválido", i)
			continue
		}
		if ones, _ := n.Mask.Size(); ones == 0 {
			v.addf("api.allowed_cidrs[%d]: no se permite 0.0.0.0/0 ni ::/0", i)
		}
	}
	if !isCleanAbsLinuxPath(a.Auth.PrincipalsFile) {
		v.addf("api.auth.principals_file: requerido, ruta absoluta canónica")
	}
}

func (c *Config) validateDatabase(v *validator) {
	d := c.Database
	if !hostnameRe.MatchString(d.Host) {
		v.addf("database.host: requerido, nombre de host o IP válido")
	}
	v.intRange("database.port", d.Port, 1, 65535)
	if !identifierRe.MatchString(d.Name) {
		v.addf("database.name: identificador inválido")
	}
	if !identifierRe.MatchString(d.User) {
		v.addf("database.user: identificador inválido")
	}
	if !isCleanAbsLinuxPath(d.PasswordFile) {
		v.addf("database.password_file: requerido, ruta absoluta canónica (nunca contraseña en línea)")
	}
	switch d.SSLMode {
	case "verify-full", "verify-ca":
	case "require":
	case "disable":
		// Permitido solo dentro de la red Docker interna; documentado en ADR-0004.
		if !strings.HasPrefix(d.Host, "postgres") && d.Host != "localhost" && d.Host != "127.0.0.1" {
			v.addf("database.sslmode: 'disable' solo se permite para el servicio interno 'postgres' o loopback")
		}
	default:
		v.addf("database.sslmode: valor no soportado")
	}
}

func (c *Config) validateSecrets(v *validator) {
	s := c.Secrets
	if !isCleanAbsLinuxPath(s.HMACKeyFile) {
		v.addf("secrets.hmac_key_file: requerido, ruta absoluta canónica")
	}
	if !isCleanAbsLinuxPath(s.EncryptionKeyFile) {
		v.addf("secrets.encryption_key_file: requerido, ruta absoluta canónica")
	}
	if s.HMACKeyFile != "" && s.HMACKeyFile == s.EncryptionKeyFile {
		v.addf("secrets: las claves HMAC y de cifrado deben ser distintas (spec §25)")
	}
	for _, src := range c.Sources {
		for _, p := range []string{s.HMACKeyFile, s.EncryptionKeyFile, c.Database.PasswordFile} {
			if p != "" && isCleanAbsLinuxPath(src.RootPath) && within(p, src.RootPath) {
				v.addf("secrets: un archivo de secretos no puede estar dentro de la fuente %q", src.ID)
			}
		}
	}
}

func (c *Config) validateSecurity(v *validator) {
	s := c.Security
	if s.StoreRawText {
		v.addf("security.store_raw_text: no soportado; el texto extraído es efímero (spec §5)")
	}
	if s.StoreSnippets {
		v.addf("security.store_snippets: requiere aprobación de privacidad; no habilitable en v1")
	}
	if s.AuditEnabled != nil && !*s.AuditEnabled {
		v.addf("security.audit_enabled: la auditoría no puede desactivarse")
	}
}
