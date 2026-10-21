package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Scopes (spec §24).
const (
	ScopeSearch   = "findings:search"
	ScopeFiles    = "files:read"
	ScopeNames    = "files:names" // ver nombres de archivo/hoja; sin él se enmascaran
	ScopeScans    = "scans:read"
	ScopeDiagnose = "scans:diagnose"
)

var knownScopes = map[string]bool{ScopeSearch: true, ScopeFiles: true, ScopeNames: true, ScopeScans: true, ScopeDiagnose: true}

// Principal autenticado. El tenant nunca proviene del cliente.
type Principal struct {
	ID       string
	scopes   map[string]bool
	hash     []byte
	certHash []byte    // huella SHA-256 del certificado de cliente (mtls_token)
	expires  time.Time // cero = sin vencimiento
}

func (p *Principal) Has(scope string) bool { return p.scopes[scope] }

// Principals es el registro de credenciales (ADR-0006): solo se guardan
// hashes SHA-256 de tokens aleatorios de >= 256 bits y, en mtls_token, la
// huella del certificado de cliente al que queda ligado cada token.
type Principals struct {
	list []*Principal
	now  func() time.Time
}

type principalsFile struct {
	Principals []struct {
		ID          string   `yaml:"id"`
		TokenSHA256 string   `yaml:"token_sha256"`
		Scopes      []string `yaml:"scopes"`
		// ClientCertSHA256: huella SHA-256 (hex) del certificado DER del cliente.
		ClientCertSHA256 string `yaml:"client_cert_sha256"`
		// ExpiresAt: fecha (AAAA-MM-DD) o RFC 3339 desde la que el token deja de valer.
		ExpiresAt string `yaml:"expires_at"`
	} `yaml:"principals"`
}

var principalIDRe = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,62}$`)

// ParsePrincipals lee el archivo de principals. Con requireCert (modo
// mtls_token) todo principal debe declarar client_cert_sha256.
func ParsePrincipals(r io.Reader, requireCert bool) (*Principals, error) {
	data, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var pf principalsFile
	if err := dec.Decode(&pf); err != nil {
		return nil, errors.New("archivo de principals inválido")
	}
	ps := &Principals{now: time.Now}
	seen := map[string]bool{}
	for i, p := range pf.Principals {
		if !principalIDRe.MatchString(p.ID) || seen[p.ID] {
			return nil, fmt.Errorf("principals[%d]: id inválido o duplicado", i)
		}
		seen[p.ID] = true
		h, err := hex.DecodeString(p.TokenSHA256)
		if err != nil || len(h) != sha256.Size {
			return nil, fmt.Errorf("principals[%d]: token_sha256 debe ser hex de 64 caracteres", i)
		}
		sc := map[string]bool{}
		for _, s := range p.Scopes {
			if !knownScopes[s] {
				return nil, fmt.Errorf("principals[%d]: scope desconocido", i)
			}
			sc[s] = true
		}
		pr := &Principal{ID: p.ID, scopes: sc, hash: h}
		if p.ClientCertSHA256 != "" {
			ch, err := hex.DecodeString(strings.ReplaceAll(strings.ToLower(p.ClientCertSHA256), ":", ""))
			if err != nil || len(ch) != sha256.Size {
				return nil, fmt.Errorf("principals[%d]: client_cert_sha256 debe ser hex de 64 caracteres", i)
			}
			pr.certHash = ch
		} else if requireCert {
			return nil, fmt.Errorf("principals[%d]: client_cert_sha256 es obligatorio en mtls_token", i)
		}
		if p.ExpiresAt != "" {
			exp, err := time.Parse(time.RFC3339, p.ExpiresAt)
			if err != nil {
				exp, err = time.Parse(time.DateOnly, p.ExpiresAt)
			}
			if err != nil {
				return nil, fmt.Errorf("principals[%d]: expires_at inválido (AAAA-MM-DD o RFC 3339)", i)
			}
			pr.expires = exp
		}
		ps.list = append(ps.list, pr)
	}
	if len(ps.list) == 0 {
		return nil, errors.New("no hay principals configurados")
	}
	return ps, nil
}

// Authenticate compara en tiempo constante contra todos los hashes. Si se
// entrega certDER (conexión mTLS ya verificada contra la CA), el principal
// debe estar ligado a ese certificado exacto. Tokens vencidos no autentican.
func (ps *Principals) Authenticate(token string, certDER []byte) *Principal {
	if len(token) < 32 || len(token) > 512 {
		return nil
	}
	h := sha256.Sum256([]byte(token))
	var found *Principal
	for _, p := range ps.list {
		if subtle.ConstantTimeCompare(h[:], p.hash) == 1 {
			found = p
		}
	}
	if found == nil {
		return nil
	}
	if !found.expires.IsZero() && !ps.now().Before(found.expires) {
		return nil
	}
	if found.certHash != nil || certDER != nil {
		ch := sha256.Sum256(certDER)
		if certDER == nil || subtle.ConstantTimeCompare(ch[:], found.certHash) != 1 {
			return nil
		}
	}
	return found
}

// Expiring devuelve los principals que vencen antes de d (para avisar en logs).
func (ps *Principals) Expiring(d time.Duration) []string {
	var out []string
	for _, p := range ps.list {
		if !p.expires.IsZero() && p.expires.Before(ps.now().Add(d)) {
			out = append(out, p.ID)
		}
	}
	return out
}

type ctxKey int

const principalKey ctxKey = 1

func principalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey).(*Principal)
	return p
}

// allowNetwork rechaza conexiones fuera de api.allowed_cidrs antes de
// cualquier otra lógica (incluido /health). Usa la IP de la conexión TCP.
func (s *Server) allowNetwork(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.opt.AllowedNets) > 0 {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			ip := net.ParseIP(host)
			ok := false
			for _, n := range s.opt.AllowedNets {
				if err == nil && ip != nil && n.Contains(ip) {
					ok = true
					break
				}
			}
			if !ok {
				s.audit(r, nil, "network", "forbidden_network", "", nil)
				writeError(w, http.StatusForbidden, "forbidden")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(h, "Bearer ")
		var cert []byte
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			cert = r.TLS.PeerCertificates[0].Raw
		}
		var p *Principal
		if ok && (cert != nil || !s.opt.RequireClientCert) {
			p = s.principals.Authenticate(strings.TrimSpace(token), cert)
		}
		if p == nil {
			// SEC-001: 401 sin información de existencia de recursos.
			s.audit(r, nil, "authenticate", "unauthenticated", "", nil)
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey, p)))
	})
}

func (s *Server) require(scope string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := principalFrom(r.Context())
		if p == nil || !p.Has(scope) {
			s.audit(r, p, "authorize:"+scope, "forbidden", "", nil)
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		next(w, r)
	}
}
