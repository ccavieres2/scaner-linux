// Package detection encuentra candidatos de identificadores en fragmentos de
// texto, los normaliza y valida (spec §6, §23). El texto recibido nunca se
// conserva ni se registra.
package detection

import (
	"io"
	"regexp"
	"unicode/utf8"

	"arsop-scanner/internal/identity"
)

// RulesVersion versiona el conjunto de reglas; cambia cuando cambia cualquier
// regex, umbral o normalización (se persiste en scans.rules_version).
const RulesVersion = "rules-1.0.0"

const (
	DetectorRUT   = "rut@1.0.0"
	DetectorEmail = "email@1.0.0"
)

// Estados de un hallazgo (spec §31).
const (
	StatusValidatedFormat = "validated_format"
	StatusRequiresReview  = "requires_review"
	StatusInvalidChecksum = "invalid_checksum" // se cuenta, no se indexa
)

// Location identifica dónde está el hallazgo dentro del archivo.
type Location struct {
	Page   int    `json:"page,omitempty"`
	Sheet  string `json:"sheet,omitempty"`
	Row    int    `json:"row,omitempty"`
	Column string `json:"column,omitempty"`
	Line   int    `json:"line,omitempty"`
}

// Finding es un identificador detectado. Normalized es PII: solo se usa para
// calcular el HMAC y nunca debe registrarse.
type Finding struct {
	Type          identity.Type
	Normalized    string
	Status        string
	Method        string // digital | ocr
	Confidence    float64
	Detector      string
	ValidChecksum bool
	Location      Location
}

// Options configura umbrales.
type Options struct {
	MaxOCREdits int // costo máximo de sustituciones OCR para RUT
}

func DefaultOptions() Options { return Options{MaxOCREdits: 3} }

var (
	rutStrict = regexp.MustCompile(`\d{1,2}\.\d{3}\.\d{3}\s?-\s?[\dkK]|\d{7,8}\s?-\s?[\dkK]`)
	// En OCR el guion del DV puede leerse como otro signo o perderse: con puntos de
	// miles el formato sigue siendo inequívoco (siempre hipótesis con revisión).
	rutOCR  = regexp.MustCompile(`[\dOoIlSsB]{1,2}\.[\dOoIlSsB]{3}\.[\dOoIlSsB]{3}(?:\s?[-–—+~]\s?)?[\dkK]`)
	emailRe = regexp.MustCompile(`[A-Za-z0-9!#$%&'*+/=?^_` + "`" + `{|}~.-]+@[\p{L}\p{N}](?:[\p{L}\p{N}-]*[\p{L}\p{N}])?(?:\.[\p{L}\p{N}](?:[\p{L}\p{N}-]*[\p{L}\p{N}])?)+`)
)

// Engine aplica todos los detectores.
type Engine struct{ opt Options }

func NewEngine(opt Options) *Engine { return &Engine{opt: opt} }

type match struct {
	start, end int
	f          Finding
}

// Detect analiza un fragmento completo (línea, celda o página).
func (e *Engine) Detect(text string, loc Location, method string) []Finding {
	ms := e.detect(text, loc, method)
	out := make([]Finding, len(ms))
	for i, m := range ms {
		out[i] = m.f
	}
	return out
}

func isWordByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// bounded verifica que el candidato no sea parte de un número/palabra mayor.
func bounded(text string, s, e int) bool {
	if s > 0 {
		c := text[s-1]
		if isWordByte(c) || c == '.' && s > 1 && text[s-2] >= '0' && text[s-2] <= '9' || c == '-' {
			return false
		}
	}
	if e < len(text) {
		c := text[e]
		if isWordByte(c) || c == '-' || c == '.' && e+1 < len(text) && text[e+1] >= '0' && text[e+1] <= '9' {
			return false
		}
	}
	return true
}

func (e *Engine) detect(text string, loc Location, method string) []match {
	var out []match
	baseConf := 1.0
	if method == "ocr" {
		baseConf = 0.8
	}
	strictSpans := map[int]bool{}
	for _, ix := range rutStrict.FindAllStringIndex(text, -1) {
		if !bounded(text, ix[0], ix[1]) {
			continue
		}
		canon, valid, err := identity.NormalizeRUT(text[ix[0]:ix[1]])
		if err != nil {
			continue
		}
		strictSpans[ix[0]] = true
		f := Finding{Type: identity.TypeRUT, Normalized: canon, Method: method, Detector: DetectorRUT,
			ValidChecksum: valid, Location: loc, Confidence: baseConf, Status: StatusValidatedFormat}
		if !valid {
			f.Status, f.Confidence = StatusInvalidChecksum, 0
		} else if method == "ocr" {
			f.Status = StatusRequiresReview
		}
		out = append(out, match{ix[0], ix[1], f})
	}
	// Las hipótesis de confusión de caracteres solo tienen sentido sobre texto OCR.
	for _, ix := range rutOCR.FindAllStringIndex(text, -1) {
		if method != "ocr" || strictSpans[ix[0]] || !bounded(text, ix[0], ix[1]) {
			continue
		}
		canon, edits, ok := identity.OCRHypothesis(text[ix[0]:ix[1]], e.opt.MaxOCREdits)
		if !ok {
			continue
		}
		conf := 0.5 - 0.05*float64(edits)
		out = append(out, match{ix[0], ix[1], Finding{Type: identity.TypeRUT, Normalized: canon, Method: method,
			Detector: DetectorRUT, ValidChecksum: true, Location: loc, Confidence: conf, Status: StatusRequiresReview}})
	}
	for _, ix := range emailRe.FindAllStringIndex(text, -1) {
		s := ix[0]
		for s < ix[1] && text[s] == '.' {
			s++
		}
		end := ix[1]
		for end > s && text[end-1] == '.' {
			end--
		}
		norm, err := identity.NormalizeEmail(text[s:end])
		if err != nil {
			continue
		}
		f := Finding{Type: identity.TypeEmail, Normalized: norm, Method: method, Detector: DetectorEmail,
			ValidChecksum: true, Location: loc, Confidence: baseConf, Status: StatusValidatedFormat}
		if method == "ocr" {
			f.Status = StatusRequiresReview
		}
		out = append(out, match{s, end, f})
	}
	return out
}

// DetectStream analiza un texto largo leído por ventanas de tamaño window con
// superposición overlap, de modo que un valor que cruza el límite entre
// ventanas no se pierda ni se duplique (DOC-006). loc se aplica a todos.
func (e *Engine) DetectStream(r io.Reader, window, overlap int, loc Location, method string, emit func(Finding)) error {
	if overlap >= window {
		overlap = window / 2
	}
	buf := make([]byte, 0, window+2*overlap)
	chunk := make([]byte, window)
	reported := 0 // offset (dentro de buf) hasta el que ya se emitieron coincidencias
	first := true
	for {
		n, err := io.ReadFull(r, chunk)
		buf = append(buf, chunk[:n]...)
		eof := err == io.EOF || err == io.ErrUnexpectedEOF
		if err != nil && !eof {
			return err
		}
		// Coincidencias que terminan en la zona final podrían estar cortadas;
		// se difieren a la siguiente ventana salvo en EOF.
		limit := len(buf)
		if !eof {
			limit = len(buf) - overlap
		}
		// Una coincidencia ya emitida termina en o antes de 'reported'. Las que
		// empiezan en 0 (salvo la primera ventana) pueden estar truncadas por la
		// izquierda; una coincidencia real diferida siempre empieza después.
		for _, m := range e.detect(string(buf), loc, method) {
			if m.end > reported && m.end <= limit && (m.start > 0 || first) {
				emit(m.f)
			}
		}
		if eof {
			return nil
		}
		first = false
		// Conservar overlap bytes antes de 'limit' (contexto) más la zona diferida,
		// alineado a límite de runa UTF-8.
		cut := max(limit-overlap, 0)
		for cut < len(buf) && !utf8.RuneStart(buf[cut]) {
			cut++
		}
		reported = max(limit-cut, 0)
		buf = append(buf[:0], buf[cut:]...)
	}
}
