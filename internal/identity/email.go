package identity

import (
	"errors"
	"strings"

	"golang.org/x/net/idna"
)

var ErrInvalidEmail = errors.New("email inválido")

var idnaProfile = idna.New(idna.MapForLookup(), idna.Transitional(false), idna.StrictDomainName(true))

// NormalizeEmail aplica normalización conservadora (spec §23, assumptions A-09):
// recorta espacios, convierte el dominio a A-label en minúsculas mediante IDNA
// y deja la parte local intacta (sin quitar puntos, etiquetas '+', ni minúsculas).
// No realiza consultas DNS.
func NormalizeEmail(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) > 254 {
		return "", ErrInvalidEmail
	}
	at := strings.LastIndexByte(s, '@')
	if at <= 0 || at == len(s)-1 {
		return "", ErrInvalidEmail
	}
	local, domain := s[:at], s[at+1:]
	if !validLocal(local) {
		return "", ErrInvalidEmail
	}
	ascii, err := idnaProfile.ToASCII(strings.TrimSuffix(domain, "."))
	if err != nil || !strings.Contains(ascii, ".") || len(ascii) > 253 {
		return "", ErrInvalidEmail
	}
	tld := ascii[strings.LastIndexByte(ascii, '.')+1:]
	if len(tld) < 2 {
		return "", ErrInvalidEmail
	}
	return local + "@" + strings.ToLower(ascii), nil
}

// validLocal acepta dot-atom (RFC 5322) sin comillas, que es lo que el
// detector extrae de texto libre.
func validLocal(l string) bool {
	if len(l) == 0 || len(l) > 64 || l[0] == '.' || l[len(l)-1] == '.' || strings.Contains(l, "..") {
		return false
	}
	for i := 0; i < len(l); i++ {
		if !isLocalChar(l[i]) {
			return false
		}
	}
	return true
}

func isLocalChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		strings.IndexByte("!#$%&'*+/=?^_`{|}~.-", c) >= 0
}
