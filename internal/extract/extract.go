// Package extract convierte archivos en fragmentos de texto localizados y los
// pasa al motor de detección. El texto solo existe en memoria (spec §5).
package extract

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"

	"arsop-scanner/internal/detection"
)

// Códigos de advertencia/error sanitizados (nunca contienen contenido).
const (
	WarnPageNeedsOCR   = "page_needs_ocr"
	WarnImageNeedsOCR  = "image_needs_ocr"
	WarnHiddenSheet    = "hidden_sheet"
	WarnTruncated      = "truncated_by_limit"
	ErrEncrypted       = "encrypted"
	ErrCorrupt         = "corrupt"
	ErrUnsupported     = "unsupported_format"
	ErrTimeout         = "timeout"
	ErrExtractorFailed = "extractor_failed"
	ErrOutOfMemory     = "out_of_memory"
	ErrZipBomb         = "zip_bomb_suspected"
	ErrTempSpace       = "temp_space_exhausted"
	WarnPageUnreadable = "page_unreadable"
)

// Result resume la extracción de un archivo.
type Result struct {
	Findings []detection.Finding
	Warnings map[string]int
	Units    int
	// Partial indica que parte del contenido no fue cubierto (p. ej. páginas
	// que requieren OCR en Fase 1).
	Partial bool
}

func newResult() *Result { return &Result{Warnings: map[string]int{}} }

func (r *Result) warn(code string, partial bool) {
	r.Warnings[code]++
	if partial {
		r.Partial = true
	}
}

// CodedError es un error de extracción con código sanitizado.
type CodedError struct {
	Code      string
	Retryable bool
}

func (e *CodedError) Error() string { return "extracción: " + e.Code }

// Options de extracción.
type Options struct {
	Overlap int
	Window  int
}

// decodeReader normaliza la codificación a UTF-8: BOM UTF-16/UTF-8 o, si la
// cabecera no es UTF-8 válido, Windows-1252 (habitual en archivos chilenos).
func decodeReader(r io.Reader) io.Reader {
	br := bufio.NewReaderSize(r, 64<<10)
	head, _ := br.Peek(64 << 10)
	switch {
	case bytes.HasPrefix(head, []byte{0xFF, 0xFE}), bytes.HasPrefix(head, []byte{0xFE, 0xFF}):
		return transform.NewReader(br, unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder())
	case bytes.HasPrefix(head, []byte{0xEF, 0xBB, 0xBF}):
		br.Discard(3)
		return br
	}
	h := head
	for i := 1; i <= 3 && i <= len(h); i++ { // runa cortada al final de la ventana
		if utf8.RuneStart(h[len(h)-i]) {
			if !utf8.FullRune(h[len(h)-i:]) {
				h = h[:len(h)-i]
			}
			break
		}
	}
	if !utf8.Valid(h) {
		return transform.NewReader(br, charmap.Windows1252.NewDecoder())
	}
	return br
}

// lineReader entrega una línea como io.Reader (sin cargarla completa), de modo
// que líneas enormes se analizan por ventanas con superposición.
type lineReader struct {
	br   *bufio.Reader
	done bool
}

func (l *lineReader) Read(p []byte) (int, error) {
	if l.done {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) {
		c, err := l.br.ReadByte()
		if err != nil {
			l.done = true
			if n == 0 {
				return 0, err
			}
			return n, nil
		}
		if c == '\n' {
			l.done = true
			break
		}
		p[n] = c
		n++
	}
	if n == 0 && l.done {
		return 0, io.EOF
	}
	return n, nil
}

// Text analiza TXT línea a línea.
func Text(r io.Reader, eng *detection.Engine, opt Options) (*Result, error) {
	res := newResult()
	br := bufio.NewReaderSize(decodeReader(r), 64<<10)
	for line := 1; ; line++ {
		if _, err := br.Peek(1); err != nil {
			if errors.Is(err, io.EOF) {
				return res, nil
			}
			return nil, &CodedError{Code: ErrCorrupt, Retryable: true}
		}
		lr := &lineReader{br: br}
		err := eng.DetectStream(lr, opt.Window, opt.Overlap, detection.Location{Line: line}, "digital",
			func(f detection.Finding) { res.Findings = append(res.Findings, f) })
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, &CodedError{Code: ErrCorrupt, Retryable: true}
		}
		res.Units++
	}
}

// ColumnRef convierte un índice 0-based a referencia estilo Excel (A, B, …, AA).
func ColumnRef(i int) string {
	s := ""
	for i++; i > 0; i = (i - 1) / 26 {
		s = string(rune('A'+(i-1)%26)) + s
	}
	return s
}

func sniffDelimiter(head []byte) rune {
	line := head
	if i := bytes.IndexByte(head, '\n'); i >= 0 {
		line = head[:i]
	}
	best, bestN := ',', -1
	for _, d := range []rune{',', ';', '\t', '|'} {
		if n := strings.Count(string(line), string(d)); n > bestN {
			best, bestN = d, n
		}
	}
	return best
}

// CSV analiza celda por celda con ubicación fila/columna.
func CSV(r io.Reader, eng *detection.Engine, opt Options) (*Result, error) {
	res := newResult()
	br := bufio.NewReaderSize(decodeReader(r), 64<<10)
	head, _ := br.Peek(8 << 10)
	cr := csv.NewReader(br)
	cr.Comma = sniffDelimiter(head)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.ReuseRecord = true
	for row := 1; ; row++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return res, nil
		}
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				// Fila malformada: se registra y se continúa con la siguiente.
				res.warn("csv_malformed_row", true)
				continue
			}
			return nil, &CodedError{Code: ErrCorrupt, Retryable: true}
		}
		for col, cell := range rec {
			if cell == "" {
				continue
			}
			loc := detection.Location{Row: row, Column: ColumnRef(col)}
			if len(cell) > opt.Window {
				_ = eng.DetectStream(strings.NewReader(cell), opt.Window, opt.Overlap, loc, "digital",
					func(f detection.Finding) { res.Findings = append(res.Findings, f) })
			} else {
				res.Findings = append(res.Findings, eng.Detect(cell, loc, "digital")...)
			}
		}
		res.Units++
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
