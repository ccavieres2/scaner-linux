// Package testpg provee una base PostgreSQL efímera para pruebas de
// integración: usa ARSOP_TEST_PG_DSN si existe; si no, levanta PostgreSQL 16
// embebido (binarios descargados una vez a la caché del usuario).
// Solo para pruebas: nunca se importa desde código de producción.
package testpg

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
)

// Main se usa desde TestMain: inicia la base, ejecuta las pruebas y la detiene.
// Devuelve el DSN a través de dsnOut.
func Main(m *testing.M, dsnOut *string) int {
	if dsn := os.Getenv("ARSOP_TEST_PG_DSN"); dsn != "" {
		*dsnOut = dsn
		return m.Run()
	}
	if os.Getenv("ARSOP_SKIP_PG") != "" {
		return m.Run()
	}
	port, err := freePort()
	if err != nil {
		fmt.Fprintln(os.Stderr, "testpg:", err)
		return 1
	}
	tmp, err := os.MkdirTemp("", "arsop-pg-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "testpg:", err)
		return 1
	}
	defer os.RemoveAll(tmp)
	cache, _ := os.UserCacheDir()
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).
		Port(uint32(port)).
		Username("arsop").Password("arsop-test").Database("arsop").
		RuntimePath(filepath.Join(tmp, "rt")).
		DataPath(filepath.Join(tmp, "data")).
		BinariesPath(filepath.Join(tmp, "bin")).
		CachePath(filepath.Join(cache, "arsop-embedded-pg")).
		Logger(nil))
	if err := pg.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "testpg: no se pudo iniciar PostgreSQL embebido:", err)
		return 1
	}
	*dsnOut = fmt.Sprintf("postgres://arsop:arsop-test@127.0.0.1:%d/arsop?sslmode=disable", port)
	code := m.Run()
	if err := pg.Stop(); err != nil {
		fmt.Fprintln(os.Stderr, "testpg: stop:", err)
	}
	return code
}

// FreshDB crea una base vacía y devuelve su DSN (aislamiento por test).
func FreshDB(t *testing.T, baseDSN string) string {
	t.Helper()
	if baseDSN == "" {
		t.Skip("PostgreSQL de pruebas no disponible (ARSOP_SKIP_PG)")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, baseDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	name := fmt.Sprintf("t_%d_%s", os.Getpid(), sanitize(t.Name()))
	if len(name) > 60 {
		name = name[:60]
	}
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	cfg, _ := pgx.ParseConfig(baseDSN)
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", cfg.User, cfg.Password, cfg.Host, cfg.Port, name)
}

func sanitize(s string) string {
	b := []byte(s)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			if c >= 'A' && c <= 'Z' {
				b[i] = c + 32
			} else {
				b[i] = '_'
			}
		}
	}
	return string(b)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
