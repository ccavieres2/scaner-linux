package pipeline_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"arsop-scanner/internal/api"
)

type certKey struct {
	cert *x509.Certificate
	der  []byte
	key  *ecdsa.PrivateKey
}

func newCert(t *testing.T, cn string, parent *certKey, isCA, server bool) *certKey {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, BasicConstraintsValid: true, IsCA: isCA,
	}
	switch {
	case isCA:
		tpl.KeyUsage |= x509.KeyUsageCertSign
	case server:
		tpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	default:
		tpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	signer, signerKey := tpl, key
	if parent != nil {
		signer, signerKey = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return &certKey{cert: c, der: der, key: key}
}

func (c *certKey) tls() tls.Certificate {
	return tls.Certificate{Certificate: [][]byte{c.der}, PrivateKey: c.key}
}

// Modo mtls_token: sin certificado de la CA del cliente no hay conexión; con
// certificado válido pero token de otro cliente, 401; token vencido, 401;
// IP fuera de allowed_cidrs, 403.
func TestMTLSTokenBinding(t *testing.T) {
	e := newEnv(t, 1000)
	ca := newCert(t, "ca-cliente", nil, true, false)
	other := newCert(t, "ca-ajena", nil, true, false)
	srvCert := newCert(t, "api", ca, false, true)
	app := newCert(t, "app-cliente", ca, false, false)
	app2 := newCert(t, "app-cliente-2", ca, false, false)
	rogue := newCert(t, "intruso", other, false, false)

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.der}), 0o600)
	fp := func(c *certKey) string { h := sha256.Sum256(c.der); return hex.EncodeToString(h[:]) }
	th := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	tok, tok2, tokOld := strings.Repeat("m", 43), strings.Repeat("n", 43), strings.Repeat("o", 43)
	pf := fmt.Sprintf(`principals:
  - {id: app, token_sha256: %s, client_cert_sha256: %s, scopes: [findings:search]}
  - {id: app2, token_sha256: %s, client_cert_sha256: %s, scopes: [findings:search]}
  - {id: vencido, token_sha256: %s, client_cert_sha256: %s, scopes: [findings:search], expires_at: "2020-01-01"}
`, th(tok), fp(app), th(tok2), fp(app2), th(tokOld), fp(app))
	if _, err := api.ParsePrincipals(strings.NewReader(
		"principals:\n  - {id: x, token_sha256: "+th(tok)+", scopes: [findings:search]}\n"), true); err == nil {
		t.Fatal("mtls_token debe exigir client_cert_sha256")
	}
	ps, err := api.ParsePrincipals(strings.NewReader(pf), true)
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg, err := api.ServerTLS(caFile)
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg.Certificates = []tls.Certificate{srvCert.tls()}
	opts := api.Options{MaxBodyBytes: 65536, MaxPageSize: 100, SearchRatePerMinute: 1000, SearchBurst: 1000, RequireClientCert: true}
	start := func(o api.Options) *httptest.Server {
		s := httptest.NewUnstartedServer(api.New(e.store, e.pipe.Key, e.pipe.Sealer, e.tenant, ps, o, e.pipe.Log).Handler())
		s.TLS = tlsCfg
		s.StartTLS()
		t.Cleanup(s.Close)
		return s
	}
	srv := start(opts)
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	client := func(c *certKey) *http.Client {
		cfg := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
		if c != nil {
			cfg.Certificates = []tls.Certificate{c.tls()}
		}
		return &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}, Timeout: 10 * time.Second}
	}
	search := func(s *httptest.Server, c *http.Client, token string) (int, error) {
		body := `{"type":"rut","value":"5.126.663-3","case_reference":"C-1"}`
		req, _ := http.NewRequest("POST", s.URL+"/api/v1/identifiers/search", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.Do(req)
		if err != nil {
			return 0, err
		}
		resp.Body.Close()
		return resp.StatusCode, nil
	}

	if code, err := search(srv, client(app), tok); err != nil || code != http.StatusOK {
		t.Fatalf("cliente legítimo: %d %v", code, err)
	}
	if _, err := search(srv, client(nil), tok); err == nil {
		t.Fatal("sin certificado de cliente la conexión TLS debe fallar")
	}
	if _, err := search(srv, client(rogue), tok); err == nil {
		t.Fatal("certificado de otra CA debe rechazarse en el handshake")
	}
	if code, _ := search(srv, client(app2), tok); code != http.StatusUnauthorized {
		t.Fatalf("token robado usado con otro certificado: %d", code)
	}
	if code, _ := search(srv, client(app), tokOld); code != http.StatusUnauthorized {
		t.Fatalf("token vencido: %d", code)
	}
	if code, _ := search(srv, client(app), ""); code != http.StatusUnauthorized {
		t.Fatalf("certificado sin token: %d", code)
	}

	// Lista de redes permitidas: 127.0.0.1 fuera de 10.0.0.0/8 → 403 incluso con credenciales válidas.
	_, n, _ := net.ParseCIDR("10.0.0.0/8")
	o := opts
	o.AllowedNets = []*net.IPNet{n}
	if code, _ := search(start(o), client(app), tok); code != http.StatusForbidden {
		t.Fatalf("IP fuera de allowed_cidrs: %d", code)
	}
	_, lo, _ := net.ParseCIDR("127.0.0.0/8")
	o.AllowedNets = []*net.IPNet{n, lo}
	if code, _ := search(start(o), client(app), tok); code != http.StatusOK {
		t.Fatalf("IP permitida: %d", code)
	}
}
