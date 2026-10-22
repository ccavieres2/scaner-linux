// Package identity normaliza y valida identificadores personales y calcula
// los tokens de búsqueda protegidos (spec §7, §23, ADR-0003).
package identity

import (
	"errors"
	"strings"
)

// Límites de cuerpo del RUT (sin DV). Se aceptan RUT de 1.000.000 a 99.999.999.
const (
	rutMinBody = 1_000_000
	rutMaxBody = 99_999_999
)

var ErrInvalidRUT = errors.New("rut inválido")

// RUTCheckDigit calcula el dígito verificador módulo 11.
func RUTCheckDigit(body int) byte {
	sum, mul := 0, 2
	for n := body; n > 0; n /= 10 {
		sum += (n % 10) * mul
		mul++
		if mul > 7 {
			mul = 2
		}
	}
	switch r := 11 - sum%11; r {
	case 11:
		return '0'
	case 10:
		return 'K'
	default:
		return byte('0' + r)
	}
}

// NormalizeRUT convierte variantes ("12.345.678-5", "12345678-5", "123456785",
// "12 345 678 - k") a la forma canónica "12345678-5". Devuelve además si el
// DV es correcto. No corrige dígitos: un DV incorrecto se conserva tal cual.
func NormalizeRUT(s string) (canonical string, validDV bool, err error) {
	var digits []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			digits = append(digits, c)
		case c == 'k' || c == 'K':
			if i != lastSignificant(s) {
				return "", false, ErrInvalidRUT
			}
			digits = append(digits, 'K')
		case c == '.' || c == '-' || c == ' ' || c == ' ':
		default:
			return "", false, ErrInvalidRUT
		}
	}
	if len(digits) < 8 || len(digits) > 9 {
		return "", false, ErrInvalidRUT
	}
	bodyDigits, dv := digits[:len(digits)-1], digits[len(digits)-1]
	body := 0
	for _, d := range bodyDigits {
		if d == 'K' {
			return "", false, ErrInvalidRUT
		}
		body = body*10 + int(d-'0')
	}
	if body < rutMinBody || body > rutMaxBody || bodyDigits[0] == '0' {
		return "", false, ErrInvalidRUT
	}
	var b strings.Builder
	b.Write(bodyDigits)
	b.WriteByte('-')
	b.WriteByte(dv)
	return b.String(), RUTCheckDigit(body) == dv, nil
}

func lastSignificant(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if c := s[i]; c != ' ' && c != '.' && c != '-' {
			return i
		}
	}
	return -1
}

// ocrConfusions son sustituciones admitidas solo como hipótesis (spec §23.4).
var ocrConfusions = map[byte]byte{'O': '0', 'o': '0', 'I': '1', 'l': '1', 'S': '5', 's': '5', 'B': '8'}

// ocrDashes son lecturas OCR erróneas del guion del DV (cada una cuenta como edición).
var ocrDashes = []string{"–", "—", "+", "~"}

// OCRHypothesis intenta reinterpretar un candidato con confusiones típicas de
// OCR. Devuelve la forma canónica, el número de sustituciones (costo de
// edición) y ok solo si el resultado tiene DV válido y costo <= maxEdits.
// El resultado SIEMPRE debe tratarse como hallazgo incierto.
func OCRHypothesis(s string, maxEdits int) (canonical string, edits int, ok bool) {
	// El guion antes del DV es lo que más falla en fotos (se lee '+', '~' o raya).
	for _, d := range ocrDashes {
		if n := strings.Count(s, d); n > 0 {
			s = strings.ReplaceAll(s, d, "-")
			edits += n
		}
	}
	// Guion perdido ("80.000.0041"): con puntos de miles, el DV es el último carácter.
	if strings.Count(s, ".") == 2 && !strings.Contains(s, "-") {
		s = strings.TrimSpace(s[:len(s)-1]) + "-" + s[len(s)-1:]
		edits++
	}
	b := []byte(s)
	for i := range b {
		if r, found := ocrConfusions[b[i]]; found {
			// La 'K' final no se toca; solo caracteres confundibles con dígitos.
			b[i] = r
			edits++
		}
	}
	if edits == 0 || edits > maxEdits {
		return "", edits, false
	}
	c, valid, err := NormalizeRUT(string(b))
	if err != nil || !valid {
		return "", edits, false
	}
	return c, edits, true
}
