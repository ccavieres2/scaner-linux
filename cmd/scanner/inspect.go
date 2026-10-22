package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"

	"arsop-scanner/internal/catalog"
	"arsop-scanner/internal/detection"
	"arsop-scanner/internal/extract"
	"arsop-scanner/internal/persistence"
	"arsop-scanner/internal/pipeline"
)

var caseRefRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// cmdInspect muestra qué detecta el escáner en UN archivo, para revisarlo a mano.
// La base guarda los identificadores solo como HMAC (no se pueden listar), así que
// el archivo se vuelve a leer. No se guarda nada; la consulta queda en auditoría.
func cmdInspect(args []string, stdout, stderr io.Writer) int {
	f := newFlags("inspect", stderr)
	source := f.String("source", "", "id de la fuente")
	rel := f.String("path", "", "ruta del archivo relativa a la fuente (como en `archivos`)")
	caso := f.String("caso", "", "referencia del caso que justifica la consulta (auditoría)")
	full := f.Bool("completo", false, "mostrar los valores completos (por defecto enmascarados)")
	cfg, code := f.load(args, stderr)
	if cfg == nil {
		return code
	}
	src := findSource(cfg, *source)
	if src == nil {
		fmt.Fprintln(stderr, "--source debe ser una fuente configurada")
		return 2
	}
	clean := path.Clean(strings.ReplaceAll(*rel, "\\", "/"))
	if *rel == "" || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		fmt.Fprintln(stderr, "--path debe ser una ruta relativa dentro de la fuente")
		return 2
	}
	if !caseRefRe.MatchString(*caso) {
		fmt.Fprintln(stderr, "--caso es obligatorio (letras, números, . _ -), p. ej. --caso REVISION-1")
		return 2
	}
	ctx, cancel := signalContext()
	defer cancel()
	rt, code := openRuntime(ctx, cfg, stderr)
	if rt == nil {
		return code
	}
	defer rt.Close()
	tenant, _, err := rt.EnsureCatalog(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "catálogo (¿ejecutó scanner init?):", err)
		return 1
	}
	kind, size, res, err := newPipeline(rt, tenant).Inspect(ctx, src.RootPath, clean)

	// Auditoría (append-only): quién, qué caso y cuántos resultados; nunca los valores.
	principal := "cli"
	if u, e := user.Current(); e == nil {
		principal = "cli:" + u.Username
	}
	n, outcome := 0, "ok"
	if res != nil {
		n = len(res.Findings)
	}
	if err != nil {
		outcome = "error"
	}
	if aerr := rt.Store.Audit(ctx, persistence.AuditEvent{Tenant: &tenant, Principal: principal, Action: "cli_inspect",
		Outcome: outcome, CaseReference: *caso, ResultCount: &n,
		Details: map[string]any{"source": src.ID, "kind": kind, "full_values": *full}}); aerr != nil {
		fmt.Fprintln(stderr, "no se pudo registrar la auditoría; no se muestran resultados")
		return 1
	}

	var ce *extract.CodedError
	switch {
	case errors.Is(err, os.ErrNotExist):
		fmt.Fprintln(stderr, "el archivo no existe en la fuente")
		return 1
	case errors.Is(err, catalog.ErrOutsideRoot):
		fmt.Fprintln(stderr, "ruta fuera de la fuente o a través de un enlace simbólico")
		return 1
	case errors.As(err, &ce):
		fmt.Fprintf(stdout, "archivo: %s (%s, %d bytes)\nno se pudo leer: %s\n", clean, kind, size, ce.Code)
		return 1
	case errors.Is(err, pipeline.ErrUnsupportedKind):
		fmt.Fprintf(stdout, "archivo: %s (%s, %d bytes)\nformato no soportado\n", clean, kind, size)
		return 1
	case err != nil:
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}

	fmt.Fprintf(stdout, "archivo: %s (%s, %d bytes)\n\n", clean, kind, size)
	var valid []detection.Finding
	invalid := 0
	for _, fd := range res.Findings {
		if fd.Status == detection.StatusInvalidChecksum {
			invalid++
			continue
		}
		valid = append(valid, fd)
	}
	sort.SliceStable(valid, func(i, j int) bool { return locString(valid[i].Location) < locString(valid[j].Location) })
	if len(valid) == 0 {
		fmt.Fprintln(stdout, "No se detectaron RUT ni correos.")
	} else {
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "TIPO\tVALOR\tUBICACIÓN\tMÉTODO\tCONFIANZA\tESTADO")
		for _, fd := range valid {
			v := fd.Normalized
			if !*full {
				v = mask(string(fd.Type), v)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%.2f\t%s\n", fd.Type, v, locString(fd.Location), fd.Method, fd.Confidence, fd.Status)
		}
		tw.Flush()
	}
	if invalid > 0 {
		fmt.Fprintf(stdout, "\n%d posibles RUT con dígito verificador inválido (no se indexan).\n", invalid)
	}
	if len(res.Warnings) > 0 {
		keys := make([]string, 0, len(res.Warnings))
		for k := range res.Warnings {
			keys = append(keys, fmt.Sprintf("%s=%d", k, res.Warnings[k]))
		}
		sort.Strings(keys)
		fmt.Fprintf(stdout, "\navisos: %s\n", strings.Join(keys, ", "))
	}
	if res.Partial {
		fmt.Fprintln(stdout, "cobertura: PARCIAL (parte del archivo no se pudo leer)")
	}
	fmt.Fprintln(stdout, "\nNada de esto se guardó. La consulta quedó registrada en auditoría.")
	if !*full && len(valid) > 0 {
		fmt.Fprintln(stdout, "Valores enmascarados; use --completo para verlos enteros.")
	}
	return 0
}

func locString(l detection.Location) string {
	var p []string
	if l.Page > 0 {
		p = append(p, fmt.Sprintf("página %d", l.Page))
	}
	if l.Sheet != "" {
		p = append(p, "hoja "+l.Sheet)
	}
	if l.Row > 0 {
		p = append(p, fmt.Sprintf("fila %d", l.Row))
	}
	if l.Column != "" {
		p = append(p, "col "+l.Column)
	}
	if l.Line > 0 {
		p = append(p, fmt.Sprintf("línea %d", l.Line))
	}
	if len(p) == 0 {
		return "-"
	}
	return strings.Join(p, ", ")
}

// mask deja ver lo justo para reconocer el valor: RUT 12345678-5 -> 12.***.**8-5,
// correo nueve.ficticia@example.cl -> n***@example.cl.
func mask(typ, v string) string {
	if typ == "email" {
		at := strings.LastIndexByte(v, '@')
		if at <= 0 {
			return "***"
		}
		return v[:1] + "***" + v[at:]
	}
	num, dv, ok := strings.Cut(v, "-")
	if !ok || len(num) < 7 {
		return "***"
	}
	return num[:len(num)-6] + ".***.**" + num[len(num)-1:] + "-" + dv
}
