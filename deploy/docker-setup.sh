#!/bin/sh
# Preparación automática para Docker Compose (servicio "setup", corre como root una vez
# por arranque). Idempotente: solo crea lo que falta; nunca sobrescribe.
#
#   /vol            volumen "secrets": una carpeta por servicio, 0700 y del UID de ese
#                   servicio (postgres 999, scanner 10001, api 65532); archivos 0600.
#   /etc/arsop      carpeta ./local del host: scanner.yaml editable por el usuario.
#
# Argumento opcional "nuevo-token": reemplaza el token de la API e imprime el nuevo.
set -eu
umask 077
V=/vol
M=$V/.master

rand() { head -c 64 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c "$1"; }

mkdir -p "$M" && chmod 0700 "$V" "$M"
[ -f "$M/db_password" ]    || rand 32 > "$M/db_password"
[ -f "$M/hmac_key" ]       || head -c 32 /dev/urandom > "$M/hmac_key"
[ -f "$M/encryption_key" ] || head -c 32 /dev/urandom > "$M/encryption_key"

TOKEN=""
if [ ! -f "$M/api_principals.yaml" ] || [ "${1:-}" = "nuevo-token" ]; then
  TOKEN=$(rand 43)
  HASH=$(printf '%s' "$TOKEN" | sha256sum | cut -d' ' -f1)
  printf 'principals:\n  - {id: arsop-app, token_sha256: %s, scopes: [findings:search, files:read, files:names, scans:read, scans:diagnose]}\n' \
    "$HASH" > "$M/api_principals.yaml"
fi

# Copia por servicio, cada una legible solo por el usuario de ese contenedor.
give() { # give SERVICIO UID ARCHIVO...
  d=$V/$1; u=$2; shift 2
  mkdir -p "$d"
  for f in "$@"; do cp "$M/$f" "$d/$f"; done
  chown -R "$u:$u" "$d" && chmod 0700 "$d" && chmod 0600 "$d"/*
}
give postgres 999   db_password
give scanner  10001 db_password hmac_key encryption_key
give api      65532 db_password hmac_key encryption_key api_principals.yaml

# Configuración inicial (el usuario la edita en ./local/scanner.yaml).
if [ ! -f /etc/arsop/scanner.yaml ]; then
  cat > /etc/arsop/scanner.yaml <<'EOF'
# Configuración del escáner (Docker). Después de editarla:  ./arsop.sh config validate
schema_version: 1
tenant: {name: "Mi organización"}

# Carpetas a escanear: ponga la RUTA REAL de cada carpeta en este equipo (no hay que copiarla;
# ./arsop.sh la monta en solo lectura). Ejemplo:
#   - {id: rrhh, root_path: /srv/archivos/rrhh}
sources:
  - {id: ejemplo, root_path: /opt/arsop/ejemplo}   # datos ficticios incluidos en la imagen

scanner:
  max_extract_workers: 4        # cola liviana (TXT/CSV)
  max_heavy_workers: 2          # cola pesada (PDF/XLSX)
  worker_memory_mb: 768         # tope por proceso Python
  memory_budget_mb: 1792        # 2 x 768 + 4 x 64 (el contenedor tiene 3 GB)
  large_file_bytes: 1048576     # >= 1 MiB = archivo grande
  large_files: defer            # grandes: se saltan y quedan en 'scan pending'
  temp_dir: /var/tmp/arsop

api:
  bind: 127.0.0.1               # en el host la API queda solo en 127.0.0.1:8080
  container_bind_all: true
  port: 8080
  auth: {principals_file: /run/secrets/api_principals.yaml}

database: {host: postgres, port: 5432, name: arsop, user: arsop_scanner, password_file: /run/secrets/db_password, sslmode: disable}
secrets:
  hmac_key_file: /run/secrets/hmac_key
  encryption_key_file: /run/secrets/encryption_key
EOF
  # Que el dueño de ./local en el host pueda editarla sin sudo.
  chown "$(stat -c %u:%g /etc/arsop)" /etc/arsop/scanner.yaml
  chmod 0644 /etc/arsop/scanner.yaml
  echo "Configuración creada: ./local/scanner.yaml"
fi

if [ -n "$TOKEN" ]; then
  cat <<EOF

=====================================================================
 TOKEN DE LA API (se muestra UNA sola vez; guárdelo en un gestor de
 contraseñas. Si lo pierde:  ./arsop.sh token-nuevo)

   $TOKEN
=====================================================================
EOF
fi
echo "setup: listo"
