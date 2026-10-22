#!/usr/bin/env bash
# Instalación de PRUEBA en una VM Ubuntu Server 24.04 desde GitHub.
# NO es una instalación de producción (ver deploy/README-deploy.md).
#
# Uso:
#   curl -fsSLO https://raw.githubusercontent.com/ccavieres2/scaner-linux-sin-intefaz/main/scripts/install-vm.sh
#   bash install-vm.sh https://github.com/ccavieres2/scaner-linux-sin-intefaz.git [rama]
# o, si ya clonó el repositorio:
#   bash scripts/install-vm.sh
#
# Idempotente: puede ejecutarse de nuevo para actualizar (git pull + build).
# Nunca sobrescribe secretos ni configuración existentes.
set -euo pipefail

REPO_URL="${1:-}"
BRANCH="${2:-main}"
GO_VERSION="1.26.10"
SRC_DIR="${ARSOP_SRC_DIR:-/opt/arsop/src}"
VENV=/opt/arsop/venv
ETC=/etc/arsop
SECRETS=$ETC/secrets
DATA=/data/input
SVC_USER=arsop

log() { printf '\n==> %s\n' "$*"; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -ne 0 ] || die "ejecute como usuario normal con sudo, no como root"
command -v sudo >/dev/null || die "se requiere sudo"
. /etc/os-release
[ "${ID:-}" = "ubuntu" ] || echo "Aviso: probado solo para Ubuntu 24.04 (detectado: ${PRETTY_NAME:-desconocido})"

log "Paquetes del sistema"
sudo apt-get update -qq
sudo apt-get install -y -qq git make curl postgresql python3-venv cifs-utils ca-certificates tesseract-ocr tesseract-ocr-spa tesseract-ocr-eng >/dev/null

log "Go ${GO_VERSION}"
if ! /usr/local/go/bin/go version 2>/dev/null | grep -q "go${GO_VERSION} "; then
  tmp=$(mktemp -d)
  curl -fsSL -o "$tmp/go.tgz" "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"
  sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf "$tmp/go.tgz"
  rm -rf "$tmp"
fi
export PATH="$PATH:/usr/local/go/bin"
grep -q /usr/local/go/bin ~/.bashrc || echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc

log "Usuario de servicio '${SVC_USER}' (sin login)"
id "$SVC_USER" >/dev/null 2>&1 || sudo useradd --system --no-create-home --shell /usr/sbin/nologin "$SVC_USER"

log "Código fuente en ${SRC_DIR}"
sudo mkdir -p "$(dirname "$SRC_DIR")"
sudo chown "$USER" "$(dirname "$SRC_DIR")"
if [ -d "$SRC_DIR/.git" ]; then
  git -C "$SRC_DIR" fetch --quiet origin "$BRANCH"
  git -C "$SRC_DIR" checkout --quiet "$BRANCH"
  git -C "$SRC_DIR" pull --quiet --ff-only origin "$BRANCH"
elif [ -n "$REPO_URL" ]; then
  git clone --quiet --branch "$BRANCH" "$REPO_URL" "$SRC_DIR"
elif [ -f "$(dirname "$0")/../go.mod" ]; then
  SRC_DIR="$(cd "$(dirname "$0")/.." && pwd)"   # ejecutado desde un clon existente
else
  die "indique la URL del repositorio: bash install-vm.sh https://github.com/ccavieres2/scaner-linux-sin-intefaz.git"
fi
cd "$SRC_DIR"
echo "Versión: $(git rev-parse --short HEAD 2>/dev/null || echo local)"

log "Entorno Python del extractor"
sudo mkdir -p /opt/arsop
[ -x "$VENV/bin/python" ] || sudo python3 -m venv "$VENV"
sudo "$VENV/bin/pip" install --quiet -r workers/extractor/requirements.txt
sudo install -D -m 0644 workers/extractor/extract.py /opt/arsop/workers/extractor/extract.py

log "Compilación"
make build >/dev/null
sudo install -m 0755 bin/scanner /usr/local/bin/arsop-scanner
sudo install -m 0755 bin/api /usr/local/bin/arsop-api

log "PostgreSQL"
sudo systemctl enable --now postgresql >/dev/null
sudo mkdir -p "$SECRETS"
sudo chmod 0750 "$ETC" && sudo chmod 0700 "$SECRETS"
if ! sudo test -f "$SECRETS/db_password"; then
  PW=$(head -c 32 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 32)
  printf '%s' "$PW" | sudo tee "$SECRETS/db_password" >/dev/null
  sudo -u postgres psql -qc "DO \$\$BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='arsop_scanner') THEN CREATE ROLE arsop_scanner LOGIN; END IF; END\$\$;"
  sudo -u postgres psql -qc "ALTER ROLE arsop_scanner PASSWORD '$PW';"
  unset PW
fi
sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname='arsop'" | grep -q 1 \
  || sudo -u postgres createdb -O arsop_scanner arsop

log "Secretos (solo si no existen)"
# Claves binarias de 32 bytes. Se repite el sorteo si el último byte es \n o \r,
# porque el lector de secretos los quita (la clave quedaría de 31 bytes).
newkey() { sudo sh -c 'while :; do head -c 32 /dev/urandom > "$0"; tail -c 1 "$0" | od -An -tx1 | grep -qE "0a|0d" || break; done' "$1"; }
sudo test -f "$SECRETS/hmac_key"       || newkey "$SECRETS/hmac_key"
sudo test -f "$SECRETS/encryption_key" || newkey "$SECRETS/encryption_key"
TOKEN=""
if ! sudo test -f "$SECRETS/api_principals.yaml"; then
  TOKEN=$(head -c 64 /dev/urandom | base64 -w0 | tr -dc 'A-Za-z0-9' | head -c 43)
  HASH=$(printf '%s' "$TOKEN" | sha256sum | cut -d' ' -f1)
  printf 'principals:\n  - {id: arsop-app, token_sha256: %s, scopes: [findings:search, files:read, files:names, scans:read, scans:diagnose]}\n' "$HASH" \
    | sudo tee "$SECRETS/api_principals.yaml" >/dev/null
fi
sudo chown -R "$SVC_USER:$SVC_USER" "$SECRETS"
sudo chmod 0600 "$SECRETS"/*

log "Datos de ejemplo y configuración"
# Temporales del worker: solo el servicio puede leerlos (se borran solos al terminar).
sudo install -d -m 0700 -o "$SVC_USER" -g "$SVC_USER" /var/tmp/arsop
sudo mkdir -p "$DATA"
if [ ! -d "$DATA/ejemplo" ]; then
  sudo cp -r tests/fixtures/synthetic/tree "$DATA/ejemplo"
  sudo chown -R root:"$SVC_USER" "$DATA/ejemplo"
  sudo chmod -R u=rwX,g=rX,o= "$DATA/ejemplo"   # el servicio solo puede leer
fi
if ! sudo test -f "$ETC/scanner.yaml"; then
  sudo tee "$ETC/scanner.yaml" >/dev/null <<EOF
schema_version: 1
tenant: {name: "Prueba VM"}
sources:
  - {id: ejemplo, root_path: $DATA/ejemplo}
scanner:
  extractor_command: [$VENV/bin/python, -I, -B, /opt/arsop/workers/extractor/extract.py]
  temp_dir: /var/tmp/arsop
  # Valores para una VM de ~4 GB de RAM. Ajustar según el hardware:
  max_extract_workers: 4        # cola liviana (TXT/CSV)
  max_heavy_workers: 2          # cola pesada (PDF/XLSX)
  worker_memory_mb: 768         # tope por proceso Python
  memory_budget_mb: 1792        # 2 x 768 + 4 x 64
  large_file_bytes: 1048576     # >= 1 MiB = archivo grande
  large_files: defer            # grandes: se saltan y quedan en 'scan pending'
api:
  bind: 127.0.0.1
  port: 8080
  auth: {principals_file: $SECRETS/api_principals.yaml}
database: {host: 127.0.0.1, port: 5432, name: arsop, user: arsop_scanner, password_file: $SECRETS/db_password, sslmode: disable}
secrets:
  hmac_key_file: $SECRETS/hmac_key
  encryption_key_file: $SECRETS/encryption_key
EOF
  sudo chown root:"$SVC_USER" "$ETC/scanner.yaml" && sudo chmod 0640 "$ETC/scanner.yaml"
fi
sudo chown root:"$SVC_USER" "$ETC"

log "Validación e inicialización"
sudo -u "$SVC_USER" arsop-scanner config validate --file "$ETC/scanner.yaml"
sudo -u "$SVC_USER" arsop-scanner init --file "$ETC/scanner.yaml"

log "Servicio systemd de la API"
sudo tee /etc/systemd/system/arsop-api.service >/dev/null <<EOF
[Unit]
Description=ARSOP Scanner API
After=network.target postgresql.service
Requires=postgresql.service

[Service]
User=$SVC_USER
Group=$SVC_USER
ExecStart=/usr/local/bin/arsop-api --file $ETC/scanner.yaml
Restart=on-failure
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
CapabilityBoundingSet=
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
LimitCORE=0

[Install]
WantedBy=multi-user.target
EOF
sudo systemctl daemon-reload
sudo systemctl enable --now arsop-api >/dev/null
sudo systemctl restart arsop-api

cat <<EOF

Instalación completada.

Escanear los datos de ejemplo:
  sudo -u $SVC_USER arsop-scanner scan start --source ejemplo --file $ETC/scanner.yaml

Archivos grandes (>= 1 MiB) que el escaneo dejó pendientes:
  sudo -u $SVC_USER arsop-scanner scan pending --scan <ID> --file $ETC/scanner.yaml
  sudo -u $SVC_USER arsop-scanner scan process-pending --scan <ID> --file $ETC/scanner.yaml

Exponer la API a la aplicación de un cliente: docs/API-CLIENTE.md (mTLS + token).

API (solo en la VM, 127.0.0.1:8080):  sudo systemctl status arsop-api
EOF
if [ -n "$TOKEN" ]; then
  cat <<EOF

TOKEN DE API (se muestra UNA sola vez, guárdelo):
  $TOKEN

Prueba:
  curl -s -X POST http://127.0.0.1:8080/api/v1/identifiers/search \\
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \\
    -d '{"type":"rut","value":"12.345.678-5","case_reference":"PRUEBA-1"}'
EOF
fi
