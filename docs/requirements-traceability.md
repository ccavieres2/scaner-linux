# Matriz de trazabilidad REQ-ID → código → prueba

| REQ-ID | Requisito (spec) | Código | Prueba | Estado |
|---|---|---|---|---|
| REQ-CFG-01..04 | Config estricta, rutas, secretos, defaults (§27) | `internal/config` | `config_test.go` | ✅ |
| REQ-CLI-01 | version, config validate, sources list/check, init (§27) | `cmd/scanner` | `main_test.go`, `integration_test.go` (Linux) | ✅ / CI |
| REQ-CLI-02 | scan start/status/report/pause/resume (§27) | `cmd/scanner/ops.go` | `integration_test.go` (Linux) | 🟡 solo CI |
| REQ-INV-01 | Inventario de raíces autorizadas, solo lectura (§3) | `internal/catalog` | `catalog_test.go`, E2E | ✅ |
| REQ-INV-02 | Tipo real por firma (§3.3) | `catalog/sniff.go` | `TestSniff` | ✅ |
| REQ-SEC-PATH | Traversal/symlink/montajes (§9.2, SEC-004) | `catalog/open_*.go` | `TestOpenBeneathRejectsEscapes` | ✅ parcial / CI |
| REQ-EXT-01 | TXT/CSV streaming, codificaciones (§5) | `internal/extract` | `extract_test.go` | ✅ |
| REQ-EXT-02 | PDF digital por página; páginas sin texto → parcial (§5, §20) | `workers/extractor` | `TestPythonPDFAndXLSX` | ✅ |
| REQ-EXT-03 | XLSX streaming hoja/fila/celda, hojas ocultas (§5) | `workers/extractor` | `TestPythonPDFAndXLSX` | ✅ |
| REQ-DET-01 | RUT módulo 11, normalización, sin corrección silenciosa (§23) | `identity/rut.go`, `detection` | DOC-001 | ✅ |
| REQ-DET-02 | Email conservador + IDNA, sin DNS (§23) | `identity/email.go` | DOC-003 | ✅ |
| REQ-DET-03 | Valores que cruzan fragmentos (§20) | `detection.DetectStream` | DOC-006 | ✅ |
| REQ-DET-04 | Confusiones OCR solo como hipótesis (§23.4) | `identity.OCRHypothesis` | DOC-002 | ✅ (se activa con OCR en Fase 2) |
| REQ-ID-01 | HMAC por cliente; nunca SHA-256 sin clave (§7) | `identity/hmac.go` | `TestLookupToken` | ✅ |
| REQ-DB-01 | Esquema versionado, migraciones (§22) | `migrations/` | store tests | ✅ |
| REQ-DB-02 | Idempotencia, lease, reintentos, DLQ lógica (§10, §22) | `persistence/tasks.go` | REL-001, DATA-001 | ✅ |
| REQ-DB-03 | Documentos ≠ ocurrencias (§22) | `findings.occurrences`, `result_count` | E2E | ✅ |
| REQ-API-01 | POST search, paginación, auditoría, no-store (§24) | `internal/api` | E2E, API-001 | ✅ |
| REQ-API-02 | Authn/authz deny-by-default, BOLA, rate limit (§9.3) | `internal/api` | SEC-001/002/003/007 | ✅ (auth de desarrollo) |
| REQ-PRIV-01 | Sin PII en logs (§9.5) | `app.NewLogger`, pipeline | SEC-006 | ✅ |
| REQ-RES-01 | Mount caído ≠ borrado (§10) | `pipeline.Run` | REL-002 | ✅ |
| REQ-RES-02 | Archivo cambiado durante lectura (§10) | `pipeline.processFile` | REL-003 | 🟡 implementado, test Fase 3 |
| REQ-DEP-01 | Compose endurecido (§14) | `deploy/` | `docker compose config` | 🟡 no ejecutado |
| REQ-OCR-01 | OCR selectivo por página (§20) | — | DOC-004 | ⏳ Fase 2 |
| REQ-DOCX-01 | Word (§5) | — | — | ⏳ Fase 2 (hoy: `skipped` explícito) |
