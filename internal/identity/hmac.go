package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
)

// Type es la categoría de identificador.
type Type string

const (
	TypeRUT   Type = "rut"
	TypeEmail Type = "email"
)

// MinKeyBytes es el tamaño mínimo de la clave HMAC.
const MinKeyBytes = 32

// Key es una clave HMAC versionada. Su String() nunca revela el material.
type Key struct {
	Version  int
	material []byte
}

func NewKey(version int, material []byte) (*Key, error) {
	if len(material) < MinKeyBytes {
		return nil, fmt.Errorf("clave HMAC demasiado corta: mínimo %d bytes", MinKeyBytes)
	}
	if version < 1 {
		return nil, errors.New("versión de clave inválida")
	}
	return &Key{Version: version, material: append([]byte(nil), material...)}, nil
}

func (k *Key) String() string { return fmt.Sprintf("hmac-key(v%d, redacted)", k.Version) }

// LoadKeyFile lee una clave desde archivo. En Linux exige que no sea legible
// por grupo ni otros (spec §9.4).
func LoadKeyFile(path string, version int) (*Key, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("abrir archivo de clave: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("archivo de clave con permisos laxos (requerido 0400 o 0600)")
	}
	data, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return nil, err
	}
	return NewKey(version, trimNewline(data))
}

func trimNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// LookupToken calcula HMAC-SHA-256(key, type || 0x00 || normalized).
// normalized DEBE estar ya normalizado con NormalizeRUT/NormalizeEmail.
func (k *Key) LookupToken(t Type, normalized string) []byte {
	m := hmac.New(sha256.New, k.material)
	m.Write([]byte(t))
	m.Write([]byte{0})
	m.Write([]byte(normalized))
	return m.Sum(nil)
}

// Normalize despacha a la normalización por tipo. Para RUT exige DV válido:
// la búsqueda por API solo acepta identificadores bien formados.
func Normalize(t Type, value string) (string, error) {
	switch t {
	case TypeRUT:
		c, ok, err := NormalizeRUT(value)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", ErrInvalidRUT
		}
		return c, nil
	case TypeEmail:
		return NormalizeEmail(value)
	default:
		return "", fmt.Errorf("tipo de identificador no soportado")
	}
}
