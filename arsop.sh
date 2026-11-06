#!/usr/bin/env bash
# Atajo para usar el escáner con Docker. Requiere solo Docker (con Compose).
#
#   ./arsop.sh instalar                 construye, crea claves y levanta todo (muestra el token)
#   ./arsop.sh scan start --source ejemplo
#   ./arsop.sh scan start --all         todas las carpetas del yaml (solo lo nuevo o modificado)
#   ./arsop.sh scan report  --scan ID   (y cualquier otro comando del escáner)
#   ./arsop.sh escaneos                 lista de escaneos (fuente, estado, fecha)
#   ./arsop.sh archivos [FUENTE]        archivos vistos: estado y cantidad de hallazgos
#   ./arsop.sh inspect --source F (--path RUTA | --todos) --caso REF [--completo]
#                                       RUT/correos detectados en un archivo y dónde
#   ./arsop.sh interfaz                 cómo abrir la interfaz web (en este equipo o desde otro PC)
#   ./arsop.sh estado | detener | iniciar | logs
#   ./arsop.sh token-nuevo              reemplaza el token de la API
#   ./arsop.sh api-externa activar IP [NOMBRE_O_IP_PUBLICA]   acceso para un sistema externo (UpShield)
#   ./arsop.sh api-externa estado | desactivar
#   ./arsop.sh actualizar               git pull + reconstruir + reiniciar
#   ./arsop.sh desinstalar              borra contenedores, base de datos y claves
set -euo pipefail
cd "$(dirname "$0")"
command -v docker >/dev/null || { echo "Docker no está instalado" >&2; exit 1; }
dc() { docker compose "$@"; }
mkdir -p entrada local
EXT=secrets/api-externa
if [ -f "$EXT/env" ]; then set -a; . "$EXT/env"; set +a; fi

# Monta (solo lectura y en la misma ruta) cada carpeta de sources: del scanner.yaml,
# para escanearla donde está, sin copiarla. /data/input (./entrada) y la carpeta de
# ejemplo de la imagen no necesitan montaje.
fuentes() {
  [ -f local/scanner.yaml ] || return 0
  grep -v '^[[:space:]]*#' local/scanner.yaml \
    | grep -oE "root_path:[[:space:]]*(\"[^\"]*\"|'[^']*'|[^,}#\"']*[^,}#\"'[:space:]])" \
    | sed -E "s/root_path:[[:space:]]*//; s/^[\"']//; s/[\"']\$//" | sort -u \
    | while read -r p; do
        case "$p" in /data/input|/data/input/*|/opt/arsop/ejemplo) continue ;; esac
        [ -d "$p" ] || { echo "ERROR: la carpeta '$p' de scanner.yaml no existe en este equipo" >&2; exit 1; }
        printf '%s\n' "-v" "$p:$p:ro"
      done
}

case "${1:-ayuda}" in
instalar)
  dc build
  dc run --rm setup          # el token aparece aquí, una sola vez
  dc up -d
  cat <<'EOF'

Listo. Pruebe con los datos de ejemplo:
  ./arsop.sh scan start --source ejemplo
  ./arsop.sh scan report --scan <ID>

Sus carpetas: agregue su ruta real en sources: de ./local/scanner.yaml (no hay que copiarlas)
API: http://127.0.0.1:8080  (Authorization: Bearer <TOKEN>)
EOF
  ;;
api-externa)
  case "${2:-}" in
  activar)
    ip="${3:?uso: ./arsop.sh api-externa activar IP_DEL_SERVIDOR [NOMBRE_O_IP_PUBLICA]}"
    [ ! -d "$EXT" ] || { echo "Ya está activada. Para rehacerla: ./arsop.sh api-externa desactivar" >&2; exit 1; }
    command -v openssl >/dev/null || { echo "Se requiere openssl" >&2; exit 1; }
    bash scripts/make-api-certs.sh ca "$EXT/certs" >/dev/null
    bash scripts/make-api-certs.sh server "$EXT/certs" "$ip" ${4:+"$4"} >/dev/null
    out=$(bash scripts/make-api-certs.sh client "$EXT/certs" upshield 365)
    token=$(printf '%s\n' "$out" | awk '/2\) token:/ {print $3}')
    linea=$(printf '%s\n' "$out" | grep '^  - {id:' | sed 's/scopes: \[[^]]*\]/scopes: [findings:search, files:read, files:names, scans:read]/')
    mkdir -p "$EXT/servicio"
    cp "$EXT/certs/api.crt" "$EXT/certs/api.key" "$EXT/certs/client-ca.crt" "$EXT/servicio/"
    printf 'principals:\n%s\n' "$linea" > "$EXT/servicio/api_principals.yaml"
    cat > "$EXT/servicio/api.yaml" <<EOF2
api:
  bind: $ip
  container_bind_all: true
  port: 8443
  auth: {mode: mtls_token, principals_file: /etc/arsop-externa/api_principals.yaml}
  tls:
    cert_file: /etc/arsop-externa/api.crt
    key_file: /etc/arsop-externa/api.key
    client_ca_file: /etc/arsop-externa/client-ca.crt
EOF2
    printf 'ARSOP_EXT_BIND=%s\nCOMPOSE_PROFILES=externa\n' "$ip" > "$EXT/env"
    set -a; . "$EXT/env"; set +a
    # La API corre como 65532: solo ese usuario lee esta carpeta.
    MSYS_NO_PATHCONV=1 dc run --rm --no-deps -v "$PWD/$EXT/servicio:/x" --entrypoint sh setup \
      -c 'chown -R 65532:65532 /x && chmod 0700 /x && chmod 0600 /x/*'
    dc up -d
    cat <<EOF2

API externa activada en https://$ip:8443 (solo con certificado de cliente + token).
La API local y la interfaz web siguen igual.

Entregar a UpShield POR CANALES SEPARADOS:
  1) $EXT/certs/upshield.crt y upshield.key  (archivo cifrado)
  2) token (se muestra UNA sola vez):  $token
  3) $EXT/certs/ca.crt

Firewall: Docker se salta ufw. Deje entrar solo a UpShield con:
  sudo iptables -I DOCKER-USER -p tcp --dport 8443 ! -s IP_DE_UPSHIELD -j DROP
Comprobar: ./arsop.sh api-externa estado   (guía: docs/API-CLIENTE.md)
EOF2
    ;;
  estado) dc ps -a api-externa; dc logs --tail 5 api-externa ;;
  desactivar)
    read -r -p "Se borrarán el certificado y el token de UpShield (no los datos). Escriba BORRAR: " ok
    [ "$ok" = BORRAR ] || exit 1
    dc rm -sf api-externa
    MSYS_NO_PATHCONV=1 dc run --rm --no-deps -v "$PWD/$EXT:/x" --entrypoint sh setup -c 'rm -rf /x/servicio'
    rm -rf "$EXT" ;;
  *) echo "uso: ./arsop.sh api-externa activar IP [NOMBRE_O_IP_PUBLICA] | estado | desactivar" >&2; exit 2 ;;
  esac ;;
token-nuevo) dc run --rm setup nuevo-token && dc restart api ;;
estado)      dc ps -a ;;
detener)     dc stop ;;
iniciar)     dc up -d ;;
logs)        dc logs -f --tail 100 api ;;
actualizar)  git pull --ff-only && dc build && dc up -d ;;
desinstalar)
  read -r -p "Se borrarán la base de datos, los hallazgos y las claves. Escriba BORRAR: " ok
  [ "$ok" = BORRAR ] && dc --profile tools down -v ;;
interfaz)
  ip=$(hostname -I 2>/dev/null | awk '{print $1}' || true); ip=${ip:-IP_DEL_SERVIDOR}
  cat <<FIN
Interfaz web (inicie sesión con el token de la API):

  En este equipo:   http://127.0.0.1:8080/ui/

  Desde otro PC, abra un túnel SSH cifrado y deje esa ventana abierta:
      ssh -L 8080:127.0.0.1:8080 $(id -un)@$ip
  y luego, en el navegador de ese PC:  http://localhost:8080/ui/

Por seguridad la interfaz no se publica en la red sin cifrado: el túnel SSH
usa el usuario y la contraseña (o llave) de este servidor.
FIN
  ;;
escaneos)
  dc exec -T postgres psql -U arsop_scanner -d arsop -c "SELECT s.logical_name AS fuente, sc.id AS escaneo,
    sc.status AS estado, sc.started_at::timestamp(0) AS inicio FROM scans sc JOIN sources s ON s.id = sc.source_id
    ORDER BY sc.started_at DESC LIMIT 50;" ;;
archivos)
  # Solo metadatos (ruta, tamaño, estado, conteos): los RUT/emails están cifrados y no se muestran.
  f="${2:-}"; [[ "$f" =~ ^[A-Za-z0-9_.-]*$ ]] || { echo "fuente inválida" >&2; exit 2; }
  dc exec -T postgres psql -U arsop_scanner -d arsop -c "SELECT s.logical_name AS fuente, f.relative_path AS archivo,
    f.size_bytes AS bytes, f.state AS estado, f.skip_reason AS motivo, count(fi.id) AS identificadores,
    coalesce(sum(fi.occurrences), 0) AS apariciones, string_agg(DISTINCT fi.detection_method, ',') AS metodo
    FROM files f JOIN sources s ON s.id = f.source_id
    LEFT JOIN file_versions v ON v.id = f.last_success_version
    LEFT JOIN findings fi ON fi.file_version_id = v.id
    WHERE f.active AND ('$f' = '' OR s.logical_name = '$f')
    GROUP BY 1, 2, 3, 4, 5 ORDER BY 1, 2;" ;;
ayuda|-h|--help) sed -n '2,17p' "$0" ;;
*)
  out=$(fuentes) || exit 1
  m=(); [ -z "$out" ] || mapfile -t m <<<"$out"
  MSYS_NO_PATHCONV=1 dc run --rm "${m[@]}" scanner "$@" ;;
esac
