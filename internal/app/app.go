// Package app construye las dependencias de ejecución a partir de la
// configuración validada (secretos solo desde archivos, spec §9.4).
package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strconv"

	"github.com/google/uuid"

	"arsop-scanner/internal/config"
	"arsop-scanner/internal/identity"
	"arsop-scanner/internal/persistence"
	"arsop-scanner/internal/security"
)

// KeyVersion activa en v1. La rotación (Fase 4) agregará versiones.
const KeyVersion = 1

type Runtime struct {
	Cfg    *config.Config
	Store  *persistence.Store
	Key    *identity.Key
	Sealer *security.Sealer
	Log    *slog.Logger
}

// NewLogger crea un logger JSON. Por diseño ningún llamador pasa PII; además
// se descartan atributos con nombres que suelen contener contenido.
func NewLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			switch a.Key {
			case "path", "relative_path", "text", "value", "rut", "email", "token", "password", "dsn":
				return slog.String(a.Key, "[redacted]")
			}
			return a
		},
	}))
}

// DSN arma la cadena de conexión con la contraseña leída de archivo.
func DSN(c config.DatabaseConfig) (string, error) {
	pw, err := security.ReadSecretFile(c.PasswordFile)
	if err != nil {
		return "", fmt.Errorf("contraseña de base de datos: %w", err)
	}
	u := url.URL{Scheme: "postgres", User: url.UserPassword(c.User, string(pw)),
		Host: c.Host + ":" + strconv.Itoa(c.Port), Path: "/" + c.Name}
	q := url.Values{}
	q.Set("sslmode", c.SSLMode)
	q.Set("application_name", "arsop-scanner")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func Open(ctx context.Context, cfg *config.Config, log *slog.Logger) (*Runtime, error) {
	keyMat, err := security.ReadSecretFile(cfg.Secrets.HMACKeyFile)
	if err != nil {
		return nil, fmt.Errorf("clave HMAC: %w", err)
	}
	key, err := identity.NewKey(KeyVersion, keyMat)
	if err != nil {
		return nil, err
	}
	encMat, err := security.ReadSecretFile(cfg.Secrets.EncryptionKeyFile)
	if err != nil {
		return nil, fmt.Errorf("clave de cifrado: %w", err)
	}
	sealer, err := security.NewSealer(KeyVersion, encMat)
	if err != nil {
		return nil, err
	}
	dsn, err := DSN(cfg.Database)
	if err != nil {
		return nil, err
	}
	st, err := persistence.Open(ctx, dsn, int32(cfg.Scanner.MaxExtractWorkers+4))
	if err != nil {
		return nil, err
	}
	return &Runtime{Cfg: cfg, Store: st, Key: key, Sealer: sealer, Log: log}, nil
}

func (r *Runtime) Close() { r.Store.Close() }

// EnsureCatalog registra tenant, fuentes y versiones de clave (idempotente).
func (r *Runtime) EnsureCatalog(ctx context.Context) (tenant uuid.UUID, sources map[string]uuid.UUID, err error) {
	t, err := r.Store.EnsureTenant(ctx, r.Cfg.Tenant.Name)
	if err != nil {
		return tenant, nil, err
	}
	sources = map[string]uuid.UUID{}
	for _, s := range r.Cfg.Sources {
		id, err := r.Store.EnsureSource(ctx, t, s.ID, s.RootPath, *s.Enabled)
		if err != nil {
			return tenant, nil, err
		}
		sources[s.ID] = id
	}
	for _, p := range []string{"hmac", "encryption"} {
		if err := r.Store.EnsureKeyVersion(ctx, p, KeyVersion); err != nil {
			return tenant, nil, err
		}
	}
	return t, sources, nil
}
