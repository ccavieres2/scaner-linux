#!/usr/bin/env bash
# Certificados para la API en modo mtls_token (ver docs/API-CLIENTE.md).
#
#   bash scripts/make-api-certs.sh ca      DIR
#       Crea la CA privada que firma los certificados de cliente (una vez).
#   bash scripts/make-api-certs.sh server  DIR IP_DE_LA_API
#       Certificado TLS del servidor de la API (SAN = esa IP).
#   bash scripts/make-api-certs.sh client  DIR ID_CLIENTE [DIAS]
#       Certificado de un cliente + token. Imprime la línea para
#       api_principals.yaml (hash del token + huella del certificado + vencimiento).
#
# DIR debe estar en un disco cifrado y ser legible solo por root/arsop.
# La clave de la CA (ca.key) no debe salir de esa máquina; idealmente se
# guarda fuera de línea tras emitir los certificados.
set -euo pipefail
umask 077

cmd="${1:-}"; dir="${2:-}"
[ -n "$cmd" ] && [ -n "$dir" ] || { sed -n '2,13p' "$0"; exit 2; }
command -v openssl >/dev/null || { echo "se requiere openssl" >&2; exit 1; }
mkdir -p "$dir"
cd "$dir"

case "$cmd" in
ca)
  [ -f ca.key ] && { echo "ya existe $dir/ca.key; no se sobrescribe" >&2; exit 1; }
  openssl ecparam -name prime256v1 -genkey -noout -out ca.key
  openssl req -new -x509 -key ca.key -sha256 -days 1825 -subj "/CN=ARSOP API client CA" \
    -addext "basicConstraints=critical,CA:TRUE,pathlen:0" -addext "keyUsage=critical,keyCertSign,cRLSign" -out ca.crt
  cp ca.crt client-ca.crt
  echo "CA creada: $dir/ca.crt (configurar api.tls.client_ca_file = .../client-ca.crt)"
  ;;
server)
  ip="${3:?indique la IP en la que escucha la API}"
  [ -f ca.key ] || { echo "primero: $0 ca $dir" >&2; exit 1; }
  openssl ecparam -name prime256v1 -genkey -noout -out api.key
  openssl req -new -key api.key -subj "/CN=arsop-api" -out api.csr
  printf 'subjectAltName=IP:%s\nextendedKeyUsage=serverAuth\nkeyUsage=critical,digitalSignature\nbasicConstraints=critical,CA:FALSE\n' "$ip" > api.ext
  openssl x509 -req -in api.csr -CA ca.crt -CAkey ca.key -CAcreateserial -sha256 -days 397 -out api.crt -extfile api.ext
  rm -f api.csr api.ext
  echo "Servidor: $dir/api.crt y $dir/api.key (api.tls.cert_file / key_file)"
  ;;
client)
  id="${3:?indique el id del cliente (minúsculas, p. ej. app-cliente)}"
  days="${4:-365}"
  [[ "$id" =~ ^[a-z][a-z0-9_.-]{0,62}$ ]] || { echo "id inválido" >&2; exit 1; }
  [ -f ca.key ] || { echo "primero: $0 ca $dir" >&2; exit 1; }
  [ -f "$id.key" ] && { echo "ya existe $id.key; para rotar use otro id (p. ej. $id-2026)" >&2; exit 1; }
  openssl ecparam -name prime256v1 -genkey -noout -out "$id.key"
  openssl req -new -key "$id.key" -subj "/CN=$id" -out "$id.csr"
  printf 'extendedKeyUsage=clientAuth\nkeyUsage=critical,digitalSignature\nbasicConstraints=critical,CA:FALSE\n' > "$id.ext"
  openssl x509 -req -in "$id.csr" -CA ca.crt -CAkey ca.key -CAcreateserial -sha256 -days "$days" -out "$id.crt" -extfile "$id.ext"
  rm -f "$id.csr" "$id.ext"
  fp=$(openssl x509 -in "$id.crt" -outform DER | sha256sum | cut -d' ' -f1)
  exp=$(date -u -d "$(openssl x509 -in "$id.crt" -noout -enddate | cut -d= -f2)" +%Y-%m-%d)
  token=$(head -c 64 /dev/urandom | base64 -w0 | tr -dc 'A-Za-z0-9' | head -c 43)
  th=$(printf '%s' "$token" | sha256sum | cut -d' ' -f1)
  cat <<EOF

Entregar al cliente POR CANALES SEPARADOS (nunca juntos en el mismo correo):
  1) $dir/$id.crt + $dir/$id.key   (archivo cifrado / medio físico)
  2) token: $token                 (gestor de contraseñas / llamada)
  3) $dir/ca.crt                   (para validar el servidor)

Agregar a api_principals.yaml (y reiniciar la API):
  - {id: $id, token_sha256: $th, client_cert_sha256: $fp, expires_at: "$exp", scopes: [findings:search, files:read, scans:read]}

El token y el certificado vencen el $exp. Este token no se vuelve a mostrar.
EOF
  ;;
*) sed -n '2,13p' "$0"; exit 2 ;;
esac
