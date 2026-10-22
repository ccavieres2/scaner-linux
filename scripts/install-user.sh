#!/usr/bin/env bash
# Instalación SIN permisos de administrador (sin sudo, sin Docker).
# Todo queda en ~/arsop y corre con el usuario actual (ver INSTALACION.md, sección C).
#
# Uso:
#   curl -fsSLO https://raw.githubusercontent.com/ccavieres2/scaner-linux-sin-intefaz/main/scripts/install-user.sh
#   bash install-user.sh [URL_REPO] [rama]
# o, desde un clon existente:
#   bash scripts/install-user.sh
#
# Requiere solo lo que trae casi cualquier Linux: curl, tar, xz, python3.
# PostgreSQL: por defecto se instala una copia propia en ~/arsop/pg (puerto 55432,
# solo 127.0.0.1). Para usar una base que entregue el cliente, definir antes:
#   ARSOP_DB_HOST ARSOP_DB_PORT ARSOP_DB_NAME ARSOP_DB_USER ARSOP_DB_PASSWORD [ARSOP_DB_SSLMODE]
#
# Idempotente: ejecutarlo de nuevo actualiza el código y recompila sin tocar
# secretos, configuración ni datos.
set -euo pipefail

REPO_URL="${1:-https://github.com/ccavieres2/scaner-linux-sin-intefaz.git}"
BRANCH="${2:-main}"
GO_VERSION="1.26.10"
PG_VERSION="16.4.0"
A="${ARSOP_HOME:-$HOME/arsop}"
PG_PORT="${ARSOP_PG_PORT:-55432}"
API_PORT="${ARSOP_API_PORT:-8080}"

log() { printf '\n==> %s\n' "$*"; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -ne 0 ] || die "este instalador es para un usuario normal; con root use scripts/install-vm.sh"
[ "$(uname -s)-$(uname -m)" = "Linux-x86_64" ] || die "solo Linux x86_64"
for c in curl tar xz python3 sha256sum; do
  command -v "$c" >/dev/null || die "falta '$c' y no se puede instalar sin administrador; pídalo al cliente"
done

umask 077
mkdir -p "$A"/{bin,secrets,tmp,logs,run}
chmod 0700 "$A" "$A/secrets" "$A/tmp"

# --- Código fuente -------------------------------------------------------
log "Código fuente en $A/src"
here="$(cd "$(dirname "$0")/.." 2>/dev/null && pwd || true)"
if [ -d "$A/src/.git" ] && command -v git >/dev/null; then
  git -C "$A/src" pull --quiet --ff-only origin "$BRANCH"
elif [ -f "$here/go.mod" ] && [ "$here" != "$A/src" ] && [ ! -d "$A/src" ]; then
  cp -r "$here" "$A/src"
elif command -v git >/dev/null; then
  [ -d "$A/src" ] || git clone --quiet --branch "$BRANCH" "$REPO_URL" "$A/src"
else
  # Sin git: descargar el .tar.gz de GitHub (repositorio público).
  slug=$(printf '%s' "$REPO_URL" | sed -E 's#^https://github.com/##; s#\.git$##')
  rm -rf "$A/src.new" && mkdir -p "$A/src.new"
  curl -fsSL "https://codeload.github.com/$slug/tar.gz/refs/heads/$BRANCH" | tar -xz -C "$A/src.new" --strip-components=1
  rm -rf "$A/src" && mv "$A/src.new" "$A/src"
fi
cd "$A/src"

# --- Go (en ~/arsop/go) -----------------------------------------------------
log "Go $GO_VERSION"
if ! "$A/go/bin/go" version 2>/dev/null | grep -q "go$GO_VERSION "; then
  f="go$GO_VERSION.linux-amd64.tar.gz"
  curl -fsSL -o "$A/tmp/$f" "https://go.dev/dl/$f"
  want=$(curl -fsSL "https://go.dev/dl/?mode=json&include=all" | python3 -I -c "
import json,sys
for r in json.load(sys.stdin):
    for x in r['files']:
        if x['filename']=='$f': print(x['sha256'])")
  [ -n "$want" ] && echo "$want  $A/tmp/$f" | sha256sum -c --quiet || die "checksum de Go inválido"
  rm -rf "$A/go" && tar -C "$A" -xzf "$A/tmp/$f" && rm -f "$A/tmp/$f"
fi
export PATH="$A/go/bin:$PATH" GOPATH="$A/gopath" GOCACHE="$A/gopath/cache" GOTOOLCHAIN=local

log "Compilación"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$A/bin/arsop-scanner" ./cmd/scanner
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$A/bin/arsop-api" ./cmd/api

# --- Python del extractor ---------------------------------------------------------
log "Entorno Python del extractor"
if [ ! -x "$A/venv/bin/pip" ]; then
  rm -rf "$A/venv"
  if ! python3 -m venv "$A/venv" >/dev/null 2>&1; then
    # Ubuntu sin python3-venv: venv sin pip + get-pip.py (no requiere root).
    python3 -m venv --without-pip "$A/venv"
    curl -fsSL -o "$A/tmp/get-pip.py" https://bootstrap.pypa.io/get-pip.py
    "$A/venv/bin/python" -I "$A/tmp/get-pip.py" --quiet && rm -f "$A/tmp/get-pip.py"
  fi
fi
"$A/venv/bin/python" -I -m pip install --quiet -r workers/extractor/requirements.txt
install -D -m 0644 workers/extractor/extract.py "$A/workers/extract.py"

# --- PostgreSQL ---------------------------------------------------------------------
if [ -n "${ARSOP_DB_HOST:-}" ]; then
  log "PostgreSQL del cliente ($ARSOP_DB_HOST)"
  DB_HOST=$ARSOP_DB_HOST DB_PORT=${ARSOP_DB_PORT:-5432} DB_NAME=${ARSOP_DB_NAME:-arsop}
  DB_USER=${ARSOP_DB_USER:?falta ARSOP_DB_USER} DB_SSL=${ARSOP_DB_SSLMODE:-verify-full}
  [ -f "$A/secrets/db_password" ] || printf '%s' "${ARSOP_DB_PASSWORD:?falta ARSOP_DB_PASSWORD}" > "$A/secrets/db_password"
  LOCAL_PG=0
else
  log "PostgreSQL $PG_VERSION propio en $A/pg (127.0.0.1:$PG_PORT)"
  DB_HOST=127.0.0.1 DB_PORT=$PG_PORT DB_NAME=arsop DB_USER=arsop_scanner DB_SSL=disable LOCAL_PG=1
  if [ ! -x "$A/pg/bin/pg_ctl" ]; then
    jar="embedded-postgres-binaries-linux-amd64-$PG_VERSION.jar"
    url="https://repo1.maven.org/maven2/io/zonky/test/postgres/embedded-postgres-binaries-linux-amd64/$PG_VERSION/$jar"
    curl -fsSL -o "$A/tmp/$jar" "$url"
    echo "$(curl -fsSL "$url.sha256")  $A/tmp/$jar" | sha256sum -c --quiet || die "checksum de PostgreSQL inválido"
    mkdir -p "$A/tmp/pgjar" "$A/pg"
    python3 -I -m zipfile -e "$A/tmp/$jar" "$A/tmp/pgjar"
    tar -xJf "$A/tmp/pgjar"/postgres-linux-*.txz -C "$A/pg"
    rm -rf "$A/tmp/$jar" "$A/tmp/pgjar"
  fi
  if [ ! -f "$A/pg/data/PG_VERSION" ]; then
    head -c 32 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 32 > "$A/secrets/db_password"
    "$A/pg/bin/initdb" -D "$A/pg/data" -U arsop_scanner --pwfile="$A/secrets/db_password" \
      --auth=scram-sha-256 -E UTF8 --locale=C >/dev/null
    # Los binarios incluidos no traen psql/createdb: se crea la base en modo monousuario.
    echo "CREATE DATABASE arsop OWNER arsop_scanner;" | "$A/pg/bin/postgres" --single -D "$A/pg/data" postgres >/dev/null
    cat >> "$A/pg/data/postgresql.conf" <<EOF
listen_addresses = '127.0.0.1'
port = $PG_PORT
unix_socket_directories = '$A/run'
EOF
  fi
fi

# --- Secretos y configuración ---------------------------------------------------------
log "Secretos (solo si no existen)"
[ -f "$A/secrets/hmac_key" ]       || head -c 32 /dev/urandom > "$A/secrets/hmac_key"
[ -f "$A/secrets/encryption_key" ] || head -c 32 /dev/urandom > "$A/secrets/encryption_key"
TOKEN=""
if [ ! -f "$A/secrets/api_principals.yaml" ]; then
  TOKEN=$(head -c 64 /dev/urandom | base64 -w0 | tr -dc 'A-Za-z0-9' | head -c 43)
  HASH=$(printf '%s' "$TOKEN" | sha256sum | cut -d' ' -f1)
  printf 'principals:\n  - {id: arsop-app, token_sha256: %s, scopes: [findings:search, files:read, files:names, scans:read, scans:diagnose]}\n' "$HASH" \
    > "$A/secrets/api_principals.yaml"
fi
chmod 0600 "$A"/secrets/*

if [ ! -d "$A/data/ejemplo" ]; then
  mkdir -p "$A/data" && cp -r tests/fixtures/synthetic/tree "$A/data/ejemplo"
  chmod -R a-w "$A/data/ejemplo"   # fuente en solo lectura
fi
if [ ! -f "$A/scanner.yaml" ]; then
  cat > "$A/scanner.yaml" <<EOF
schema_version: 1
tenant: {name: "Instalación sin administrador"}
sources:
  - {id: ejemplo, root_path: $A/data/ejemplo}
scanner:
  extractor_command: [$A/venv/bin/python, -I, -B, $A/workers/extract.py]
  temp_dir: $A/tmp
  # Valores para ~4 GB de RAM. Ajustar según el equipo:
  max_extract_workers: 4        # cola liviana (TXT/CSV)
  max_heavy_workers: 2          # cola pesada (PDF/XLSX)
  worker_memory_mb: 768         # tope por proceso Python
  memory_budget_mb: 1792        # 2 x 768 + 4 x 64
  large_file_bytes: 1048576     # >= 1 MiB = archivo grande
  large_files: defer            # grandes: se saltan y quedan en 'scan pending'
api:
  bind: 127.0.0.1
  port: $API_PORT
  auth: {principals_file: $A/secrets/api_principals.yaml}
database: {host: $DB_HOST, port: $DB_PORT, name: $DB_NAME, user: $DB_USER, password_file: $A/secrets/db_password, sslmode: $DB_SSL}
secrets:
  hmac_key_file: $A/secrets/hmac_key
  encryption_key_file: $A/secrets/encryption_key
EOF
  chmod 0600 "$A/scanner.yaml"
fi

# --- arsopctl: arrancar / detener sin systemd de sistema ------------------------------
cat > "$A/bin/arsopctl" <<EOF
#!/usr/bin/env bash
# Control de PostgreSQL local y de la API (sin administrador).
set -euo pipefail
A="$A"; LOCAL_PG=$LOCAL_PG
pg()  { [ "\$LOCAL_PG" = 1 ] || return 0; "\$A/pg/bin/pg_ctl" -D "\$A/pg/data" -l "\$A/logs/postgres.log" -w "\$@" >/dev/null; }
api_pid() { cat "\$A/run/api.pid" 2>/dev/null || true; }
api_up()  { p=\$(api_pid); [ -n "\$p" ] && kill -0 "\$p" 2>/dev/null; }
case "\${1:-}" in
start)
  [ "\$LOCAL_PG" = 1 ] && { "\$A/pg/bin/pg_ctl" -D "\$A/pg/data" status >/dev/null 2>&1 || pg start; }
  api_up || { nohup "\$A/bin/arsop-api" --file "\$A/scanner.yaml" >>"\$A/logs/api.log" 2>&1 & echo \$! > "\$A/run/api.pid"; }
  sleep 1; api_up && echo "API en marcha (log: \$A/logs/api.log)" || { echo "la API no arrancó; ver \$A/logs/api.log" >&2; exit 1; } ;;
stop)
  api_up && kill "\$(api_pid)"; rm -f "\$A/run/api.pid"; pg stop -m fast || true; echo detenido ;;
restart) "\$0" stop; "\$0" start ;;
status)
  [ "\$LOCAL_PG" = 1 ] && { "\$A/pg/bin/pg_ctl" -D "\$A/pg/data" status >/dev/null 2>&1 && echo "postgres: en marcha" || echo "postgres: detenido"; }
  api_up && echo "api: en marcha (pid \$(api_pid))" || echo "api: detenida" ;;
*) echo "uso: arsopctl start|stop|restart|status"; exit 2 ;;
esac
EOF
chmod 0700 "$A/bin/arsopctl"

log "Validación, inicio e inicialización"
"$A/bin/arsopctl" start >/dev/null 2>&1 || true   # la API puede fallar antes de init; se reintenta abajo
"$A/bin/arsop-scanner" config validate --file "$A/scanner.yaml"
"$A/bin/arsop-scanner" init --file "$A/scanner.yaml"
"$A/bin/arsopctl" restart

# Arranque automático tras reiniciar el equipo: crontab del usuario (no requiere admin).
if command -v crontab >/dev/null; then
  ( crontab -l 2>/dev/null | grep -v 'arsopctl start'; echo "@reboot $A/bin/arsopctl start" ) | crontab - \
    && echo "Arranque automático: crontab @reboot" || echo "Aviso: no se pudo registrar el arranque automático; use 'arsopctl start'"
fi
grep -q "$A/bin" ~/.bashrc 2>/dev/null || echo "export PATH=\"$A/bin:\$PATH\"" >> ~/.bashrc

cat <<EOF

Instalación completada en $A (sin administrador).

Abra una terminal nueva (o ejecute: export PATH="$A/bin:\$PATH") y pruebe:
  arsop-scanner scan start --source ejemplo --file $A/scanner.yaml
  arsop-scanner scan report  --scan <ID> --file $A/scanner.yaml
  arsop-scanner scan pending --scan <ID> --file $A/scanner.yaml
  arsopctl status | start | stop
EOF
if [ -n "$TOKEN" ]; then
  cat <<EOF

Token de la API (se muestra UNA sola vez; guárdelo en un gestor de contraseñas):
  $TOKEN
EOF
fi
