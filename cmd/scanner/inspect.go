package main

import (
	"context"
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

// maxInspectAll limita --todos (cada archivo se vuelve a leer, incluido el OCR).
const maxInspectAll = 5000

// cmdInspect muestra qué detecta el escáner en un archivo (--path) o en todos los
// archivos de la fuente con hallazgos (--todos), para revisarlo a mano.
// La base guarda los identificadores solo como HMAC (no se pueden listar), así que
// los archivos se vuelven a leer. No se guarda nada; la consulta queda en auditoría.
func cmdInspect(args []string, stdout, stderr io.Writer) int {
	f := newFlags("inspect", stderr)
	source := f.String("source", "", "id de la fuente")
	rel := f.String("path", "", "ruta del archivo relativa a la fuente (como en `archivos`)")
	all := f.Bool("todos", false, "revisar todos los archivos de la fuente en los que el escaneo encontró algo")
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
	switch {
	case *all && *rel != "":
		fmt.Fprintln(stderr, "use --path o --todos, no ambos")
		return 2
	case !*all && (*rel == "" || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../")):
		fmt.Fprintln(stderr, "--path debe ser una ruta relativa dentro de la fuente (o use --todos)")
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
	tenant, sources, err := rt.EnsureCatalog(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "catálogo (¿ejecutó scanner init?):", err)
		return 1
	}
	paths := []string{clean}
	if *all {
		if paths, err = rt.Store.FilesWithFindings(ctx, tenant, sources[src.ID], maxInspectAll+1); err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		if len(paths) > maxInspectAll {
			fmt.Fprintf(stderr, "más de %d archivos con hallazgos; revise por archivo con --path\n", maxInspectAll)
			return 1
		}
	}
	var rest []persistence.FileWithoutFindings
	if *all {
		if rest, err = rt.Store.FilesWithoutFindings(ctx, tenant, sources[src.ID], maxInspectAll); err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		if len(paths) == 0 && len(rest) == 0 {
			fmt.Fprintln(stdout, "Esta fuente aún no tiene archivos escaneados: ejecute scan start.")
			return 0
		}
	}

	// Auditoría ANTES de mostrar nada (append-only): quién, qué caso y alcance; nunca los valores.
	principal := "cli"
	if u, e := user.Current(); e == nil {
		principal = "cli:" + u.Username
	}
	files := len(paths)
	if aerr := rt.Store.Audit(ctx, persistence.AuditEvent{Tenant: &tenant, Principal: principal, Action: "cli_inspect",
		Outcome: "ok", CaseReference: *caso, ResultCount: &files,
		Details: map[string]any{"source": src.ID, "all_files": *all, "full_values": *full}}); aerr != nil {
		fmt.Fprintln(stderr, "no se pudo registrar la auditoría; no se muestran resultados")
		return 1
	}

	p := newPipeline(rt, tenant)
	failed, total := 0, 0
	for i, rp := range paths {
		if ctx.Err() != nil {
			break
		}
		if i > 0 {
			fmt.Fprintln(stdout, strings.Repeat("-", 60))
		}
		n, ok := inspectOne(ctx, p, src.RootPath, rp, *full, stdout)
		total += n
		if !ok {
			failed++
		}
	}
	if *all {
		printWithoutFindings(stdout, rest, cfg.Scanner.LargeFileBytes)
		fmt.Fprintf(stdout, "\n%d archivos con hallazgos revisados, %d hallazgos", len(paths), total)
		if failed > 0 {
			fmt.Fprintf(stdout, ", %d no se pudieron leer", failed)
		}
		fmt.Fprintln(stdout, ".")
	}
	fmt.Fprintln(stdout, "\nNada de esto se guardó. La consulta quedó registrada en auditoría.")
	if !*full && total > 0 {
		fmt.Fprintln(stdout, "Valores enmascarados; use --completo para verlos enteros.")
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// inspectOne relee un archivo e imprime sus hallazgos. Devuelve cuántos hubo y si se pudo leer.
func inspectOne(ctx context.Context, p *pipeline.Pipeline, root, rel string, full bool, w io.Writer) (int, bool) {
	kind, size, res, err := p.Inspect(ctx, root, rel)
	fmt.Fprintf(w, "archivo: %s (%s, %d bytes)\n", rel, kind, size)
	var ce *extract.CodedError
	switch {
	case errors.Is(err, os.ErrNotExist):
		fmt.Fprintln(w, "  ya no existe en la fuente")
		return 0, false
	case errors.Is(err, catalog.ErrOutsideRoot):
		fmt.Fprintln(w, "  ruta fuera de la fuente o a través de un enlace simbólico")
		return 0, false
	case errors.As(err, &ce):
		fmt.Fprintf(w, "  no se pudo leer: %s\n", ce.Code)
		return 0, false
	case errors.Is(err, pipeline.ErrUnsupportedKind):
		fmt.Fprintln(w, "  formato no soportado")
		return 0, false
	case err != nil:
		fmt.Fprintln(w, "  no se pudo leer")
		return 0, false
	}
	fmt.Fprintln(w)
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
		fmt.Fprintln(w, "No se detectaron RUT ni correos.")
	} else {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "TIPO\tVALOR\tUBICACIÓN\tMÉTODO\tCONFIANZA\tESTADO")
		for _, fd := range valid {
			v := fd.Normalized
			if !full {
				v = mask(string(fd.Type), v)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%.2f\t%s\n", fd.Type, v, locString(fd.Location), fd.Method, fd.Confidence, fd.Status)
		}
		tw.Flush()
	}
	if invalid > 0 {
		fmt.Fprintf(w, "%d posibles RUT con dígito verificador inválido (no se indexan).\n", invalid)
	}
	if len(res.Warnings) > 0 {
		keys := make([]string, 0, len(res.Warnings))
		for k := range res.Warnings {
			keys = append(keys, fmt.Sprintf("%s=%d", k, res.Warnings[k]))
		}
		sort.Strings(keys)
		fmt.Fprintf(w, "avisos: %s\n", strings.Join(keys, ", "))
	}
	if res.Partial {
		fmt.Fprintln(w, "cobertura: PARCIAL (parte del archivo no se pudo leer)")
	}
	return len(valid), true
}

// printWithoutFindings explica por qué cada archivo restante no aparece arriba.
func printWithoutFindings(w io.Writer, rest []persistence.FileWithoutFindings, largeBytes int64) {
	if len(rest) == 0 {
		return
	}
	fmt.Fprintf(w, "%s\nArchivos SIN hallazgos que mostrar (%d):\n", strings.Repeat("=", 60), len(rest))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, f := range rest {
		why := ""
		switch {
		case f.Deferred:
			why = fmt.Sprintf("PENDIENTE por tamaño (>= %d bytes): ejecute scan process-pending --scan <ID>", largeBytes)
		case f.State == "completed":
			why = "leído entero, sin RUT ni correos reconocidos (si es una foto, revise su calidad)"
		case f.State == "partial":
			why = "leído en parte, sin RUT ni correos reconocidos"
		case f.State == "skipped" && strings.HasPrefix(f.SkipReason, "unsupported_"):
			why = "OMITIDO: formato no soportado (" + strings.TrimPrefix(f.SkipReason, "unsupported_") + ")"
		case f.State == "skipped" || f.State == "failed":
			reason := f.SkipReason
			if reason == "" {
				reason = "ver detalle con scan report --scan <ID>"
			}
			why = strings.ToUpper(map[string]string{"skipped": "omitido", "failed": "falló"}[f.State]) + ": " + reason
		case f.State == "queued" || f.State == "discovered":
			why = "aún no procesado (escaneo en curso o interrumpido: scan resume)"
		default:
			why = f.State
		}
		fmt.Fprintf(tw, "  %s\t%s\n", f.RelPath, why)
	}
	tw.Flush()
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
