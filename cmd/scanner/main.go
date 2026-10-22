// Comando scanner: interfaz administrativa primaria (spec §27).
// Los secretos nunca se pasan por flags: se leen de los archivos indicados en
// la configuración.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"arsop-scanner/internal/config"
)

// Asignadas en build con -ldflags.
var (
	version = "0.1.0-fase1"
	commit  = "unknown"
)

const defaultConfigPath = "/etc/arsop/scanner.yaml"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	sub := ""
	if len(args) > 1 {
		sub = args[1]
	}
	switch args[0] + " " + sub {
	case "config validate":
		return cmdConfigValidate(args[2:], stdout, stderr)
	case "sources list":
		return cmdSourcesList(args[2:], stdout, stderr)
	case "sources check":
		return cmdSourcesCheck(args[2:], stdout, stderr)
	case "scan start":
		return cmdScanStart(args[2:], stdout, stderr)
	case "scan status", "scan report":
		return cmdScanStatus(args[2:], stdout, stderr, sub == "report")
	case "scan pause":
		return cmdScanPause(args[2:], stdout, stderr)
	case "scan resume":
		return cmdScanResume(args[2:], stdout, stderr)
	case "inspect " + sub:
		return cmdInspect(args[1:], stdout, stderr)
	case "scan pending":
		return cmdScanPending(args[2:], stdout, stderr)
	case "scan process-pending":
		return cmdScanProcessPending(args[2:], stdout, stderr)
	case "token new":
		return cmdTokenNew(stdout)
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "scanner %s (commit %s, config schema %d)\n", version, commit, config.SchemaVersion)
		return 0
	case "init":
		return cmdInit(args[1:], stdout, stderr)
	case "benchmark", "doctor":
		return notImplemented(stderr, args[0], "Fase 3/4")
	case "-h", "--help", "help":
		usage(stdout)
		return 0
	}
	fmt.Fprintf(stderr, "subcomando desconocido: %q\n", args[0])
	usage(stderr)
	return 2
}

// flags agrega --file a un FlagSet y carga la configuración tras parsear.
type flags struct {
	*flag.FlagSet
	file *string
}

func newFlags(name string, stderr io.Writer) *flags {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return &flags{FlagSet: fs, file: fs.String("file", defaultConfigPath, "ruta del archivo de configuración YAML")}
}

func (f *flags) load(args []string, stderr io.Writer) (*config.Config, int) {
	if err := f.Parse(args); err != nil {
		return nil, 2
	}
	cfg, err := config.LoadFile(*f.file)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, 1
	}
	return cfg, 0
}

func cmdConfigValidate(args []string, stdout, stderr io.Writer) int {
	cfg, code := newFlags("config validate", stderr).load(args, stderr)
	if cfg == nil {
		return code
	}
	fmt.Fprintf(stdout, "configuración válida (schema %d, %d fuentes)\n", cfg.SchemaVersion, len(cfg.Sources))
	return 0
}

func cmdSourcesList(args []string, stdout, stderr io.Writer) int {
	cfg, code := newFlags("sources list", stderr).load(args, stderr)
	if cfg == nil {
		return code
	}
	for _, s := range cfg.Sources {
		state := "enabled"
		if !*s.Enabled {
			state = "disabled"
		}
		// root_path es configuración administrativa, no PII; se muestra al operador.
		fmt.Fprintf(stdout, "%s\t%s\t%s\n", s.ID, state, s.RootPath)
	}
	return 0
}

func notImplemented(stderr io.Writer, cmd, phase string) int {
	fmt.Fprintf(stderr, "%s: no implementado todavía (planificado para %s)\n", cmd, phase)
	return 3
}

func usage(w io.Writer) {
	fmt.Fprint(w, `uso: scanner <subcomando> [opciones]   (todas aceptan --file RUTA)

  version
  config validate
  sources list
  sources check                       verifica accesibilidad de raíces (solo lectura)
  init                                aplica migraciones y registra tenant/fuentes
  scan start  --source ID [--full]    ejecuta un escaneo hasta terminar o pausarse
  scan status --scan ID [--json]
  scan report --scan ID               reporte JSON de cobertura y errores
  scan pause  --scan ID
  scan resume --scan ID               reanuda (también tras un corte del proceso)
  scan pending --scan ID [--json]     lista archivos grandes diferidos (no procesados)
  scan process-pending --scan ID [--max-bytes N]
                                      procesa los diferidos (todos o solo <= N bytes)
  inspect --source ID (--path RUTA | --todos) --caso REF [--completo]
                                      vuelve a leer un archivo y muestra los RUT/emails
                                      detectados y dónde (enmascarados; no guarda nada)
  token new                           genera un token de API y su hash SHA-256

Planificados (código 3): benchmark, doctor
`)
}
