# Matriz de controles — estado real (Fase 1)

Leyenda: **Probado** (test automatizado que pasa localmente), **Probado solo en CI Linux** (escrito, no ejecutable en
Windows), **Implementado sin prueba**, **Diseñado**, **Pendiente**.

| Control | Estado | Evidencia |
|---|---|---|
| Config estricta: claves desconocidas, rutas de sistema, raíces anidadas, opciones inseguras | Probado | `internal/config/config_test.go` |
| Sin secretos en línea; archivos de secreto 0600/0400 (Unix) | Probado (config) / Implementado (permisos) | `security.ReadSecretFile` |
| Recorrido sin seguir symlinks ni cruzar montajes | Probado (parcial en Windows) / CI Linux (symlinks, `st_dev`) | `internal/catalog` |
| Apertura `openat2(RESOLVE_BENEATH\|NO_SYMLINKS\|NO_XDEV)` + fallback | Probado solo en CI Linux | `open_linux.go`, SEC-004 |
| Traversal (`../`, absolutas, `\`) rechazado | Probado | `TestOpenBeneathRejectsEscapes` |
| Tipo real por firma (no extensión); ZIP solo directorio central | Probado | `TestSniff` |
| Worker de extracción aislado: sin rutas (stdin), entorno mínimo, stderr descartado, timeout, `RLIMIT_AS`, sin core dumps | Implementado; timeout/errores probados | `extract/python.go`, `extract.py` |
| Códigos de error del worker saneados (sin texto arbitrario) | Probado | `TestPythonCorrupt` |
| Anti zip-bomb en XLSX (entradas, tamaño descomprimido, ratio) | Probado | `TestXLSXZipBomb` |
| PDF con contraseña → `encrypted`, omitido sin intentar abrir | Probado | `TestPDFEncrypted` + escaneo real |
| PDF truncado/corrupto → `corrupt`, el escaneo continúa | Probado | `TestPDFTruncated`, `TestMemoryBudgetAndHostileFilesInScan` |
| Parser colgado → timeout, proceso terminado, reintentable | Probado | `TestWorkerTimeout` |
| Tope de memoria por proceso Python (RLIMIT_AS) → `out_of_memory` | Probado solo en CI Linux | `TestWorkerMemoryLimit` |
| Presupuesto global de memoria limita workers pesados simultáneos | Probado | `TestMemoryBudgetAndHostileFilesInScan` (4 configurados → 2 efectivos) |
| Colas separadas liviana/pesada; PDF disfrazado se reclasifica | Probado | `TestClassesAndSizeOrder`, escaneo real |
| Archivos pequeños primero, grandes (>= `large_file_bytes`) al final | Probado | `TestSmallFilesFirst`, `TestClassesAndSizeOrder` |
| Archivos grandes diferidos (`large_files: defer`): no se procesan, se listan (CLI/API) y se procesan a pedido, también por tamaño | Probado | `TestDeferLargeFiles` |
| PDF/XLSX grandes sin cargarlos completos en RAM (temporal anónimo) | Probado (3 MB) | `TestPDFLarge` |
| HMAC-SHA-256 por cliente con separación de tipo | Probado | `identity_test.go` |
| AES-256-GCM para nombres de hoja (AAD = versión de archivo) | Probado | `sealer_test.go` + E2E |
| Sin valores de RUT/email en BD en claro | Implementado (esquema) | `migrations/0001_init.up.sql` |
| SQL solo parametrizado | Implementado (revisión de código) | `internal/persistence` |
| Idempotencia tras crash / lease vencido; publicación atómica | Probado | `TestIdempotentPipeline` (REL-001, DATA-001) |
| Raíz caída ≠ borrado; resultados previos conservados | Probado | `TestRootUnavailable` (REL-002) |
| Archivo cambiado → no publicado (stale) | Implementado; prueba dirigida pendiente | REL-003 → Fase 3 |
| API deny-by-default (401/403), scopes por endpoint | Probado | `TestAPISecurity` (SEC-001/002) |
| BOLA: tenant del principal, 404 idéntico | Probado | SEC-003 |
| Rate limit por principal (anti-enumeración) | Probado | SEC-007 |
| `case_reference` obligatorio y auditado; auditoría append-only (trigger) | Probado | E2E + `TestSearchAndRetryAndAudit` |
| Nombres de archivo/hoja enmascarados sin `files:names` | Probado | E2E |
| Cursores firmados, ligados a la consulta, expirables | Implementado; paginación probada en store | `api/server.go` |
| Sin PII en logs (RUT, email, nombres de archivo, case_reference) | Probado | E2E (SEC-006) |
| Cabeceras `no-store`, `nosniff`, límite de cuerpo, `DisallowUnknownFields` | Probado | E2E |
| Contenedores no-root, read-only, `cap_drop ALL`, red interna, fuentes `:ro`, puertos solo loopback | Implementado y probado de punta a punta (`./arsop.sh instalar`, escaneo, pendientes, API) | `compose.yaml` |
| mTLS: certificado de cliente obligatorio y verificado contra CA propia | Probado | `TestMTLSTokenBinding` |
| Token ligado a la huella del certificado (token robado no sirve en otro equipo) | Probado | `TestMTLSTokenBinding` |
| Vencimiento de credenciales (`expires_at`) y aviso de rotación | Probado (vencido → 401) | `TestMTLSTokenBinding` |
| Lista de redes permitidas (`allowed_cidrs`), IP de la conexión TCP | Probado | `TestMTLSTokenBinding` |
| Fuera de loopback: se exige TLS + `mtls_token`; sin `0.0.0.0/0` | Probado | `config_test.go` |
| Autenticación con IdP corporativo (OIDC) | Pendiente (opcional según cliente) | ADR-0006 |
| TLS en API | Implementado (config); no probado | `cmd/api` |
| Cifrado en reposo (LUKS), backups, rotación de claves | **Pendiente P0** | Fase 4 |
| Imágenes por digest, hashes pip, SBOM | **Pendiente** | Fase 4 |

**No apto para producción**: quedan pendientes controles P0 (rotación de claves HMAC/AES, cifrado en reposo/backups,
pentest).
