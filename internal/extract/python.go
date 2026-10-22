package extract

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"
	"time"

	"arsop-scanner/internal/detection"
)

// Python ejecuta el worker de extracción en un proceso aislado. El archivo se
// entrega por stdin: el worker nunca recibe rutas (contrato spec §20). La
// salida es JSON-lines; el texto no se escribe en disco ni en logs.
type Python struct {
	Command  []string      // p. ej. ["python3", "-I", "-B", "/opt/arsop/workers/extractor/extract.py"]
	Timeout  time.Duration // tiempo máximo por archivo
	MaxPages int
	MemoryMB int    // tope duro de memoria del proceso (RLIMIT_AS, Linux)
	TempDir  string // directorio de temporales del worker (vacío = por defecto del sistema)

	// OCR (Fase 2). Sin idiomas no se hace OCR: imágenes y páginas sin texto
	// quedan como cobertura parcial (*_needs_ocr).
	OCRLanguages   []string
	OCRPageTimeout time.Duration // por página o imagen
	OCRDPI         int
}

// workerMsg es el contrato de salida del worker (versión 1).
type workerMsg struct {
	T          string   `json:"t"` // unit | warn | error | done
	Unit       string   `json:"unit,omitempty"`
	Page       int      `json:"page,omitempty"`
	Sheet      string   `json:"sheet,omitempty"`
	Row        int      `json:"row,omitempty"`
	Column     string   `json:"column,omitempty"`
	Text       string   `json:"text,omitempty"`
	Method     string   `json:"method,omitempty"` // "ocr" o vacío (texto digital)
	Code       string   `json:"code,omitempty"`
	Units      int      `json:"units,omitempty"`
	Hidden     bool     `json:"hidden,omitempty"`
	Confidence *float64 `json:"confidence,omitempty"`
}

const maxWorkerLine = 32 << 20

func (p *Python) Extract(ctx context.Context, kind string, file io.Reader, eng *detection.Engine, opt Options) (*Result, error) {
	if len(p.Command) == 0 {
		return nil, &CodedError{Code: ErrUnsupported}
	}
	ctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	args := append(append([]string{}, p.Command[1:]...), "--kind", kind, "--max-pages", itoa(p.MaxPages))
	if p.MemoryMB > 0 {
		args = append(args, "--max-mem-mb", itoa(p.MemoryMB))
	}
	if len(p.OCRLanguages) > 0 {
		args = append(args, "--ocr-langs", strings.Join(p.OCRLanguages, "+"))
		if p.OCRPageTimeout > 0 {
			args = append(args, "--ocr-timeout", itoa(int(p.OCRPageTimeout/time.Second)))
		}
		if p.OCRDPI > 0 {
			args = append(args, "--ocr-dpi", itoa(p.OCRDPI))
		}
	}
	cmd := exec.CommandContext(ctx, p.Command[0], args...)
	// Entorno mínimo: sin proxies, sin variables heredadas.
	cmd.Env = workerEnv(p.TempDir)
	cmd.Stdin = file
	cmd.Stderr = io.Discard // stderr de parsers puede contener fragmentos de texto
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, &CodedError{Code: ErrExtractorFailed, Retryable: true}
	}
	if err := cmd.Start(); err != nil {
		return nil, &CodedError{Code: ErrExtractorFailed, Retryable: true}
	}
	res := newResult()
	ocrSeen := map[string]bool{}
	var codeErr *CodedError
	done := false
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), maxWorkerLine)
	for sc.Scan() {
		var m workerMsg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			codeErr = &CodedError{Code: ErrExtractorFailed}
			break
		}
		switch m.T {
		case "unit":
			loc := detection.Location{Page: m.Page, Sheet: m.Sheet, Row: m.Row, Column: m.Column}
			method := "digital"
			if m.Method == "ocr" {
				method = "ocr" // menor confianza y revisión requerida (detection)
			}
			emit := func(f detection.Finding) {
				if method == "ocr" {
					// Una imagen se lee en varias pasadas: el mismo valor en la misma
					// página cuenta una vez.
					k := string(f.Type) + "\x00" + f.Normalized + "\x00" + itoa(f.Location.Page)
					if ocrSeen[k] {
						return
					}
					ocrSeen[k] = true
				}
				res.Findings = append(res.Findings, f)
			}
			if len(m.Text) > opt.Window {
				_ = eng.DetectStream(stringsReader(m.Text), opt.Window, opt.Overlap, loc, method, emit)
			} else {
				for _, f := range eng.Detect(m.Text, loc, method) {
					emit(f)
				}
			}
			res.Units++
		case "warn":
			partial := m.Code == WarnPageNeedsOCR || m.Code == WarnTruncated || m.Code == WarnImageNeedsOCR ||
				m.Code == WarnPageUnreadable || m.Code == WarnOCRFailed
			res.warn(sanitizeCode(m.Code), partial)
		case "error":
			c := sanitizeCode(m.Code)
			// Falta de espacio temporal es transitoria; memoria/bomba/corrupto no.
			codeErr = &CodedError{Code: c, Retryable: c == ErrTempSpace}
		case "done":
			done = true
		}
	}
	scanErr := sc.Err()
	if scanErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nil, &CodedError{Code: ErrTimeout, Retryable: true}
	case codeErr != nil:
		return nil, codeErr
	case scanErr != nil || waitErr != nil || !done:
		return nil, &CodedError{Code: ErrExtractorFailed, Retryable: true}
	}
	return res, nil
}

var knownCodes = map[string]bool{
	WarnPageNeedsOCR: true, WarnImageNeedsOCR: true, WarnHiddenSheet: true, WarnTruncated: true,
	ErrEncrypted: true, ErrCorrupt: true, ErrUnsupported: true, ErrTimeout: true, ErrExtractorFailed: true,
	ErrOutOfMemory: true, ErrZipBomb: true, ErrTempSpace: true, WarnPageUnreadable: true,
	WarnOCRFailed: true, ErrImageTooLarge: true,
}

// sanitizeCode impide que el worker inyecte texto arbitrario en errores/logs.
func sanitizeCode(c string) string {
	if knownCodes[c] {
		return c
	}
	return "other"
}
