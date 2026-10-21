#!/usr/bin/env sh
# Genera secretos de DESARROLLO en deploy/secrets (0600). En producción usar el
# gestor de secretos del cliente; nunca comitear este directorio.
set -eu
cd "$(dirname "$0")"
umask 077
mkdir -p secrets config
[ -f secrets/db_password ]    || head -c 32 /dev/urandom | base64 | tr -d '\n=/+' > secrets/db_password
[ -f secrets/hmac_key ]       || head -c 32 /dev/urandom > secrets/hmac_key
[ -f secrets/encryption_key ] || head -c 32 /dev/urandom > secrets/encryption_key
if [ ! -f secrets/api_principals.yaml ]; then
  TOKEN=$(head -c 64 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 43)
  HASH=$(printf '%s' "$TOKEN" | sha256sum | cut -d' ' -f1)
  printf 'principals:\n  - {id: arsop-app, token_sha256: %s, scopes: [findings:search, files:read, scans:read, scans:diagnose]}\n' "$HASH" > secrets/api_principals.yaml
  echo "Token de API de desarrollo (se muestra una sola vez): $TOKEN"
fi
[ -f config/scanner.yaml ] || { sed 's/^api:$/api:\n  container_bind_all: true/' ../config/scanner.example.yaml > config/scanner.yaml; }
# Los contenedores corren como 10001 (scanner) y 65532 (api): ajustar propietario
# de los secretos según el usuario del host (Docker Compose los monta tal cual).
echo "Secretos en deploy/secrets. Revise deploy/config/scanner.yaml antes de continuar."
