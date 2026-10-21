package security

import (
	"bytes"
	"testing"
)

func TestSealer(t *testing.T) {
	s, err := NewSealer(1, bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	c1, _ := s.Seal([]byte("Hoja Ficticia"), []byte("finding-1"))
	c2, _ := s.Seal([]byte("Hoja Ficticia"), []byte("finding-1"))
	if bytes.Equal(c1, c2) {
		t.Fatal("nonce debe ser aleatorio")
	}
	if p, err := s.Open(c1, []byte("finding-1")); err != nil || string(p) != "Hoja Ficticia" {
		t.Fatalf("open: %q %v", p, err)
	}
	if _, err := s.Open(c1, []byte("finding-2")); err == nil {
		t.Fatal("AAD distinto debe fallar")
	}
	c1[len(c1)-1] ^= 1
	if _, err := s.Open(c1, []byte("finding-1")); err == nil {
		t.Fatal("manipulación debe detectarse")
	}
	if _, err := NewSealer(1, []byte("corta")); err == nil {
		t.Fatal("clave corta debe fallar")
	}
}
