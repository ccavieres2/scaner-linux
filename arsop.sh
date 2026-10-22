#!/usr/bin/env bash
# Atajo para usar el escáner con Docker. Requiere solo Docker (con Compose).
#
#   ./arsop.sh instalar                 construye, crea claves y levanta todo (muestra el token)
#   ./arsop.sh scan start --source ejemplo
#   ./arsop.sh scan report  --scan ID   (y cualquier otro comando del escáner)
#   ./arsop.sh escaneos                 lista de escaneos (fuente, estado, fecha)
#   ./arsop.sh archivos [FUENTE]        archivos vistos: estado y cantidad de hallazgos
#   ./arsop.sh inspect --source F --path RUTA --caso REF [--completo]
#                                       RUT/correos detectados en un archivo y dónde
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
ayuda|-h|--help) sed -n '2,14p' "$0" ;;
*)
  out=$(fuentes) || exit 1
  m=(); [ -z "$out" ] || mapfile -t m <<<"$out"
  MSYS_NO_PATHCONV=1 dc run --rm "${m[@]}" scanner "$@" ;;
esac
