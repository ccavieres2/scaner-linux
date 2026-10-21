package catalog

import (
	"archive/zip"
	"bytes"
	"io"
	"path"
	"strings"
	"unicode/utf8"
)

// Kinds de archivo reconocidos por firma (spec §3.3: no confiar en extensión).
const (
	KindPDF     = "pdf"
	KindXLSX    = "xlsx"
	KindDOCX    = "docx"
	KindCSV     = "csv"
	KindText    = "text"
	KindImage   = "image"
	KindOLE     = "ole" // XLS/DOC antiguos
	KindZip     = "zip"
	KindUnknown = "unknown"
)

// maxZipEntries limita la inspección del directorio central (anti zip-bomb).
const maxZipEntries = 10000

// Sniff determina el tipo real leyendo la cabecera. Para ZIP inspecciona solo
// el directorio central (sin descomprimir contenido).
func Sniff(r io.ReaderAt, size int64, relPath string) string {
	head := make([]byte, 8192)
	n, _ := r.ReadAt(head, 0)
	head = head[:n]
	switch {
	case bytes.HasPrefix(head, []byte("%PDF-")):
		return KindPDF
	case bytes.HasPrefix(head, []byte("PK\x03\x04")):
		return sniffZip(r, size)
	case bytes.HasPrefix(head, []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}):
		return KindOLE
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")),
		bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}),
		bytes.HasPrefix(head, []byte("II*\x00")), bytes.HasPrefix(head, []byte("MM\x00*")):
		return KindImage
	}
	if looksText(head) {
		switch strings.ToLower(path.Ext(relPath)) {
		case ".csv", ".tsv":
			return KindCSV
		}
		return KindText
	}
	return KindUnknown
}

func sniffZip(r io.ReaderAt, size int64) string {
	zr, err := zip.NewReader(r, size)
	if err != nil || len(zr.File) > maxZipEntries {
		return KindZip
	}
	for _, f := range zr.File {
		switch f.Name {
		case "xl/workbook.xml":
			return KindXLSX
		case "word/document.xml":
			return KindDOCX
		}
	}
	return KindZip
}

// looksText: sin NUL y mayoritariamente UTF-8 válido o Latin-1 imprimible.
func looksText(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	if bytes.IndexByte(b, 0) >= 0 {
		// UTF-16 con BOM se trata como texto (decodificado en extract).
		return bytes.HasPrefix(b, []byte{0xFF, 0xFE}) || bytes.HasPrefix(b, []byte{0xFE, 0xFF})
	}
	if utf8.Valid(trimPartialRune(b)) {
		return true
	}
	ctrl := 0
	for _, c := range b {
		if c < 0x09 || (c > 0x0D && c < 0x20) {
			ctrl++
		}
	}
	return ctrl*100/len(b) < 2
}

// trimPartialRune quita una runa UTF-8 cortada al final del buffer de cabecera.
func trimPartialRune(b []byte) []byte {
	for i := 1; i <= 3 && i <= len(b); i++ {
		if utf8.RuneStart(b[len(b)-i]) {
			if !utf8.FullRune(b[len(b)-i:]) {
				return b[:len(b)-i]
			}
			break
		}
	}
	return b
}

func splitRel(rel string) []string { return strings.Split(rel, "/") }
