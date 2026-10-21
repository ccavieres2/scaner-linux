# Informe de Fase 1 — vertical slice sin OCR

## Puerta (spec §28) y resultado

| Criterio | Resultado |
|---|---|
| E2E sobre árbol sintético con resultados exactos | ✅ `internal/pipeline/e2e_test.go` contra `tests/fixtures/synthetic/expected.json` (15 identificadores, CSV/TXT/PDF/XLSX) |
| Deduplicado | ✅ reproceso `full` e incremental dan resultados idénticos; `TestIdempotentPipeline` |
| Reinicio sin pérdida | ✅ lease vencido → reentrega; el worker viejo no puede publicar (REL-001) |
| API deniega por defecto | ✅ SEC-001/002/003/007 |

## Flujo implementado
`scanner init` → `scanner scan start --source X` → inventario (`files`, `file_versions`, `file_tasks`) → workers
reclaman con `SKIP LOCKED` → extracción (Go: TXT/CSV; Python aislado: PDF/XLSX) → detección → HMAC → transacción
única que publica hallazgos y `last_success_version` → `FinishScan` (`completed`/`partial`) → `api` responde.

## Decisiones tomadas en la fase
- `mtime` truncado a microsegundos (precisión de PostgreSQL); sin esto todo archivo parecía modificado.
- Hipótesis de confusión OCR solo sobre texto de origen OCR.
- RUT con DV inválido: se cuentan (`warn_invalid_checksum_candidates`) pero no se indexan; la API rechaza consultarlos.
- Imágenes y páginas PDF sin texto → archivo `partial` con código `image_needs_ocr`/`page_needs_ocr` (Fase 2).
- DOCX/XLS/DOC/ZIP → `skipped` con código `unsupported_*` (visibles en cobertura, nunca silenciosos).
- En Fase 1 el extractor Python se ejecuta como subproceso del contenedor `scanner` (no como servicio separado).
- Cola en PostgreSQL (ADR-0002) validada a pequeña escala; sin benchmark aún.

## Pruebas no ejecutadas en este entorno (Windows, sin Docker) — ver «Ampliación 2»: ya ejecutadas
- Symlinks y verificación de montaje (`st_dev`) y `openat2`: requieren Linux → CI.
- `cmd/scanner/integration_test.go` (CLI completa con BD): requiere rutas Linux → CI.
- `go test -race`: requiere cgo en Windows → CI.
- Construcción y ejecución de imágenes Docker: daemon no disponible; solo `docker compose config`.

## Riesgos abiertos
- Autenticación solo de desarrollo (token estático hasheado). P0 antes de producción.
- Licencia de componentes: se usa `pypdfium2` (Apache-2.0/BSD) en vez de PyMuPDF (AGPL) — A-07.
- Permisos de secretos en Docker Compose: el usuario del contenedor debe poder leer archivos 0600 (ver `deploy/README-deploy.md`).
- Sin rendimiento medido: no hay afirmaciones de throughput.

## Ampliación: planificación por tipo, memoria y archivos problemáticos
- Dos colas con workers propios: **liviana** (TXT/CSV, Go) y **pesada** (PDF/XLSX, Python aislado). Migración `0002`.
- Presupuesto de memoria compartido (`memory_budget_mb`): cada worker pesado reserva `worker_memory_mb` (igual a su
  tope duro RLIMIT_AS) antes de reclamar una tarea; nunca retiene un lease esperando memoria.
- Orden: pequeños primero; `>= large_file_bytes` al final; por tamaño ascendente dentro de cada grupo.
- El worker ya no carga el archivo completo: temporal anónimo + lectura bajo demanda (pdfium/openpyxl).
- Casos problemáticos probados: corrupto, truncado, con contraseña, zip bomb, cuelgue (timeout), límite de páginas,
  PDF de 3 MB, PDF con extensión `.txt`, página ilegible (`page_unreadable` → parcial). OOM real: solo CI Linux.
- No medido aún: memoria real con PDFs de cientos de MB, ni throughput con miles de archivos (benchmark, Fase 3).

## Ampliación 2: archivos grandes diferidos, API para clientes y Docker
- `scanner.large_files: defer` (por defecto): los archivos >= `large_file_bytes` no se procesan en el escaneo; quedan
  en estado `deferred` (migración `0003`), el escaneo termina `partial` y se listan con `scan pending` o
  `GET /scans/{id}/pending`. `scan process-pending [--max-bytes N]` los procesa con los mismos workers y presupuesto.
  `large_files: last` mantiene el comportamiento anterior (procesarlos al final).
- API: modo `mtls_token` (certificado de cliente de CA propia + token ligado a la huella del certificado),
  `expires_at` por credencial, `allowed_cidrs`; fuera de loopback se exige `mtls_token`. Guía: `docs/API-CLIENTE.md`;
  certificados con `scripts/make-api-certs.sh`.
- Probado en Docker (Windows + Docker Desktop): build de imágenes, `init` (esquema v3), escaneo con PDF corrupto y
  cifrado, lista y proceso de pendientes, API en modo desarrollo y en `mtls_token` con 7 casos de ataque.
- Probado en Linux (contenedor `golang:1.26.10-bookworm`): suite completa con `-race`, límite real de memoria
  (`TestWorkerMemoryLimit`), symlinks/`openat2` y CLI completa (`TestCLIIntegration`).
- Toolchain Go 1.26.10 (GO-2026-6809 en crypto/x509).

## Próximo paso (Fase 2)
OCR Tesseract local por página/imagen, DOCX, fixtures hostiles (zip bomb, PDF malformado, imagen gigante), worker en
contenedor separado sin red, corpus etiquetado con precisión/recall por formato.
