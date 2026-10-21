# Propuesta concreta — Fase 1 (vertical slice sin OCR)

**Objetivo / puerta (spec §28):** E2E sobre `tests/fixtures/synthetic/tree`, resultados exactos y deduplicados
(contra `expected.json`), reinicio sin pérdida, API deniega por defecto.

## Archivos a crear

| Archivo | Contenido | Pruebas |
|---|---|---|
| `internal/identity/rut.go` | Normalización `NNNNNNNN-D`, DV módulo 11, `K` mayúscula | DOC-001: válidos/ inválidos/ K/ 0/ separadores raros |
| `internal/identity/email.go` | Sintaxis, dominio minúsculas + IDNA (`golang.org/x/net/idna`), parte local intacta | DOC-003 |
| `internal/identity/hmac.go` | `LookupHMAC(key, ver, type, value)`; carga de clave desde archivo con chequeo de permisos `0400/0600` | vectores fijos, clave distinta → token distinto |
| `internal/detection/detector.go` | Interfaz `Detector{Name, Version, Detect(chunks)}`; regex + validación | DOC-006 (valor cruzando fragmentos) |
| `internal/catalog/walk_linux.go` | Recorrido con `openat2(RESOLVE_BENEATH|NO_SYMLINKS|NO_XDEV)`, detección MIME por firma | SEC-004 (traversal, symlink fuera) |
| `internal/extract/text.go` | CSV/TXT streaming con detección de codificación acotada (Go nativo) | archivos grandes, líneas largas |
| `workers/extractor/` | Python: PDF digital por página, XLSX `openpyxl read_only` (hoja/fila/celda); stdin/stdout JSON-lines | fixtures sintéticos |
| `migrations/0001_init.sql` (+down) | `tenants, sources, scans, files, file_versions, identifiers, findings, finding_locations, file_tasks, scan_errors, audit_events, key_versions` + índices §22 | migración up/down en contenedor |
| `internal/persistence/` | `pgx/v5`, solo SQL parametrizado, `ON CONFLICT DO NOTHING`, cola `SKIP LOCKED` | DATA-001, REL-001 (kill tras escribir) |
| `internal/logging/` | `slog` JSON con tipo `Redacted`; test que escanea la salida buscando RUT/email del fixture | SEC-006 |
| `cmd/api` + `internal/api/` | chi, `POST /api/v1/identifiers/search`, `Cache-Control: no-store`, límite de cuerpo, cursor opaco, auth deny-by-default | SEC-001, SEC-002, API-001 |
| `api/openapi.yaml` | Contrato v1 | validación en CI |
| `deploy/compose.yaml` + Dockerfiles | Hardening ADR-0004 | `docker compose config` + inspección |

## Comandos CLI que se activan
`scanner init`, `scanner sources check`, `scanner scan start|status|report`.

## Riesgos
- Licencia PyMuPDF (A-07): se empezará con `pypdfium2` (Apache/BSD) detrás de la interfaz si no hay decisión.
- `openat2` no existe en Windows/macOS de desarrollo: tests de recorrido solo en Linux (CI/contenedor).
- Dependencias nuevas: `pgx/v5`, `chi/v5`, `golang.org/x/net` — fijadas en `go.sum`, revisadas con `govulncheck`.
