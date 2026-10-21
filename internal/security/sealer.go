// Package security agrupa primitivas criptográficas y de secretos.
package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
)

// Sealer cifra valores con AES-256-GCM (ADR-0003). El AAD liga el texto
// cifrado a su contexto (p. ej. id de hallazgo) para impedir trasplantes.
type Sealer struct {
	Version int
	aead    cipher.AEAD
}

func NewSealer(version int, key []byte) (*Sealer, error) {
	if len(key) != 32 {
		return nil, errors.New("clave de cifrado: se requieren exactamente 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{Version: version, aead: aead}, nil
}

func (s *Sealer) String() string { return fmt.Sprintf("sealer(v%d, redacted)", s.Version) }

// Seal devuelve version(1 byte) || nonce || ciphertext.
func (s *Sealer) Seal(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := append([]byte{byte(s.Version)}, nonce...)
	return s.aead.Seal(out, nonce, plaintext, aad), nil
}

func (s *Sealer) Open(sealed, aad []byte) ([]byte, error) {
	ns := s.aead.NonceSize()
	if len(sealed) < 1+ns || int(sealed[0]) != s.Version {
		return nil, errors.New("texto cifrado inválido o versión de clave distinta")
	}
	return s.aead.Open(nil, sealed[1:1+ns], sealed[1+ns:], aad)
}

// ReadSecretFile lee un secreto pequeño exigiendo permisos estrictos en Unix.
func ReadSecretFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("abrir secreto: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("archivo de secreto con permisos laxos (requerido 0400 o 0600)")
	}
	b, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return nil, err
	}
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b, nil
}
