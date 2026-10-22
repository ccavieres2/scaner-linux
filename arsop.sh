#!/usr/bin/env bash
# Atajo para usar el escáner con Docker. Requiere solo Docker (con Compose).
#
#   ./arsop.sh instalar                 construye, crea claves y levanta todo (muestra el token)
#   ./arsop.sh scan start --source ejemplo
#   ./arsop.sh scan report  --scan ID   (y cualquier otro comando del escáner)
#   ./arsop.sh estado | detener | iniciar | logs
#   ./arsop.sh token-nuevo              reemplaza el token de la API
#   ./arsop.sh actualizar               git pull + reconstruir + reiniciar
#   ./arsop.sh desinstalar              borra contenedores, base de datos y claves
set -euo pipefail
cd "$(dirname "$0")"
command -v docker >/dev/null || { echo "Docker no está instalado" >&2; exit 1; }
dc() { docker compose "$@"; }
mkdir -p entrada local

# Monta (solo lectura y en la misma ruta) cada carpeta de sources: del scanner.yaml,
# para escanearla donde está, sin copiarla. /data/input (./entrada) y la carpeta de
# ejemplo de la imagen no necesitan montaje.
fuentes() {
  [ -f local/scanner.yaml ] || return 0
  grep -v '^[[:space:]]*#' local/scanner.yaml \
    | grep -oE 'root_path:[[:space:]]*[^,}#[:space:]]+' \
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
token-nuevo) dc run --rm setup nuevo-token && dc restart api ;;
estado)      dc ps -a ;;
detener)     dc stop ;;
iniciar)     dc up -d ;;
logs)        dc logs -f --tail 100 api ;;
actualizar)  git pull --ff-only && dc build && dc up -d ;;
desinstalar)
  read -r -p "Se borrarán la base de datos, los hallazgos y las claves. Escriba BORRAR: " ok
  [ "$ok" = BORRAR ] && dc --profile tools down -v ;;
ayuda|-h|--help) sed -n '2,11p' "$0" ;;
*)
  out=$(fuentes) || exit 1
  m=(); [ -z "$out" ] || mapfile -t m <<<"$out"
  MSYS_NO_PATHCONV=1 dc run --rm "${m[@]}" scanner "$@" ;;
esac
