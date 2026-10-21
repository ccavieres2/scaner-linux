package api

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
)

// ServerTLS devuelve la configuración TLS del servidor: TLS 1.2+ con suites
// AEAD y, si clientCAFile no está vacío, certificado de cliente obligatorio y
// verificado contra esa CA (modo mtls_token).
func ServerTLS(clientCAFile string) (*tls.Config, error) {
	c := &tls.Config{
		MinVersion: tls.VersionTLS12,
		CipherSuites: []uint16{ // solo aplican a TLS 1.2; TLS 1.3 ya usa suites seguras
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256, tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305, tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
		},
	}
	if clientCAFile == "" {
		return c, nil
	}
	pem, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("client_ca_file no contiene certificados PEM válidos")
	}
	c.ClientCAs = pool
	c.ClientAuth = tls.RequireAndVerifyClientCert
	return c, nil
}
