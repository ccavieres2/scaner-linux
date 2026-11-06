# CLAUDE.md

Escáner ARSOP on-premise (Ley 21.719): busca RUT y correos en file servers y responde dónde aparecen, para
solicitudes de acceso, rectificación, supresión, oposición y portabilidad (ARSOP). El usuario y la documentación hablan español:
responder y escribir mensajes, comentarios y docs en español.

## Arquitectura

- `cmd/scanner`: CLI en Go (init, `scan start/resume/pause/pending/process-pending`, inspect, token).
- `cmd/api`: API HTTP `/api/v1` y la interfaz web `/ui` (embebida con `go:embed` desde `internal/api/ui`).
- `internal/`: `pipeline` (inventario, colas, detección), `detection` (RUT/email), `identity` (normalización +
  HMAC), `persistence` (PostgreSQL, pgx), `api`, `config` (validación estricta), `security` (sellado AES).
- `workers/extractor`, `workers/ocr`: Python (Pillow, pypdfium2, openpyxl, Tesseract spa+eng). OCR no usa Tika.
- `migrations/`: SQL embebido; `scanner init` las aplica.
- Despliegue: `compose.yaml` + `arsop.sh` (setup, postgres, migrate, api, scanner; `api-externa` con perfil
  `externa`). Spec completa: `ESPECIFICACION_TECNICA_COMPLETA_SCANNER_ARSOP_21719.md`; ADR en `docs/ADR`.

## Comandos

```bash
go vet ./... && go test -count=1 ./...        # integración: PostgreSQL embebido o ARSOP_TEST_PG_DSN
go test ./internal/pipeline/ -run 'UI|EndToEnd|APISecurity'   # e2e principales (necesita Python + libs)
gofmt -l .                                    # debe salir vacío
node --check internal/api/ui/app.js           # tras cambiar la interfaz
bash -n arsop.sh
```

Docs de usuario: `COMANDOS.md` (referencia de comandos: actualizarla al agregar uno), `INSTALACION.md`,
`FUNCIONAMIENTO.md`, `docs/API-CLIENTE.md` (acceso externo con mTLS).

## Reglas que no se rompen

- **Privacidad:** nunca se guardan valores de RUT/correo, solo HMAC (`lookup_hmac`); nada de PII en logs,
  errores ni respuestas de error. La API no repite el valor buscado.
- **Fuentes en solo lectura:** el escáner nunca modifica archivos escaneados; `arsop.sh` monta cada
  `root_path` con `:ro`.
- **No subir secretos ni datos reales:** `local/`, `secrets/`, `entrada/`, `*.key` están en `.gitignore`.
  No commitear imágenes ni archivos reales del usuario (p. ej. cédulas de identidad). Tokens de prueba solo en
  el scratchpad y borrarlos al terminar.
- **API:** deny-by-default, scopes (`findings:search`, `files:read`, `files:names`, `scans:read`,
  `scans:diagnose`), `case_reference` obligatorio, auditoría de cada llamada, límite de consultas.
  Fuera de loopback se exige TLS + `mtls_token`; la config rechaza `0.0.0.0` e IP públicas. No relajar esto sin
  que el usuario lo pida explícitamente.
- **Interfaz web:** CSP `script-src 'self'`, sin scripts en línea; todo texto al DOM con `textContent`.
- **No romper datos existentes:** cambios compatibles con la base ya escaneada de los clientes; esquema solo
  vía nueva migración. `./arsop.sh actualizar` no debe perder hallazgos, claves ni token.
- **Honestidad en pruebas:** decir qué se probó y qué no (p. ej. "no probado en el navegador" o "solo en Docker
  Desktop"); nunca inventar resultados.

## Convenciones

- IDs de fuente: `^[a-z][a-z0-9_-]{0,62}$`. `root_path` absoluto, sin `\`; con espacios va entre comillas.
- Escaneo incremental: se salta un archivo si coinciden tamaño + mtime + identidad + versión de reglas.
  Los archivos grandes quedan `deferred` hasta `scan process-pending`.
- `scan start` acepta `--source a,b,c` (en orden) o `--all` (todas las habilitadas).
- La barra de progreso solo se dibuja si stderr es TTY; sin TTY (cron) salen logs normales.
- Commits en español; push a `main` tras cada cambio (remoto: github.com/ccavieres2/scaner-linux).

## Entorno de desarrollo (Windows)

- Shell Git Bash: los literales `\n` dentro de heredocs/scripts Python inline se han corrompido; preferir
  escribir scripts en archivos o usar la herramienta Edit.
- Docker Desktop: anteponer `MSYS_NO_PATHCONV=1` a comandos `docker` con rutas; `openssl` de Git Bash
  reescribe `-subj "/CN=..."` sin esa variable.
- Probar contenedores mTLS desde la red Docker (`curlimages/curl`), porque el curl de Windows (schannel) no
  sirve para mTLS con PEM.
- Al terminar pruebas: `docker compose --profile tools --profile externa down -v` y borrar `local/` y
  `secrets/`.
