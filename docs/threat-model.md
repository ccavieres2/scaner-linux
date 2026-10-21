# Modelo de amenazas (STRIDE resumido) — v0.1

**Activos:** documentos de origen, hallazgos, índices HMAC, claves HMAC/cifrado, credenciales de mounts y BD,
auditoría, disponibilidad del servicio.

**Adversarios:** usuario interno no autorizado, consumidor API comprometido, archivo malicioso, administrador con
exceso de privilegios, robo de backup, atacante en red interna, ransomware, error operacional.

| ID | Amenaza (STRIDE) | Vector | Control previsto | Fase | Prueba |
|---|---|---|---|---|---|
| T-01 | Divulgación | OCR/parser exfiltra por red | Redes `internal`, sin egress, modelos empaquetados; `network_access=false` validado | 0 (config) / 2 | SEC-005 |
| T-02 | DoS / EoP | Zip bomb, PDF hostil, imagen gigante | Límites de tamaño/páginas/píxeles/tiempo; proceso aislado no-root | 2 | Parsers |
| T-03 | Divulgación | Path traversal / symlink fuera de raíz | Raíces solo por config, canónicas, sin anidar, sin rutas de sistema; `openat2 RESOLVE_BENEATH` | 0 (config) / 1 | SEC-004 |
| T-04 | Manipulación | Inyección SQL | Solo consultas parametrizadas | 1 | Seguridad |
| T-05 | Divulgación | BOLA/IDOR | Autorización por objeto; IDs opacos; tenant derivado del principal | 1 | SEC-003 |
| T-06 | Divulgación | Enumeración masiva de RUT | Scope dedicado, rate limit, cuotas, alertas, `case_reference` obligatorio | 1 / 4 | SEC-007 |
| T-07 | Divulgación | Robo de BD/backup | HMAC con clave externa a BD; sin valores en claro; LUKS; backups cifrados | 1 / 4 | SEC-008 |
| T-08 | Divulgación | PII/secretos en logs | Logger con tipos redactados; test automático de logs; config sin secretos en línea | 0 (config) / 1 | SEC-006 |
| T-09 | EoP | Escape de contenedor | no-root, `cap_drop ALL`, `read_only`, seccomp, sin `docker.sock` | 1 | Inspección |
| T-10 | DoS | Tormenta de reintentos / cola | Backoff, `retry_max`, DLQ lógica, backpressure | 3 | Soak |
| T-11 | Integridad | Mount caído interpretado como borrado | `mount_fingerprint` + salud de raíz antes de reconciliar | 3 | REL-002 |
| T-12 | Repudio | Acciones sin trazabilidad | `audit_events` append-only; auditoría no desactivable (validado) | 0 (config) / 1 | Auditoría |
| T-13 | Suplantación | Token robado/estático | OIDC de vida corta o mTLS; token estático solo en desarrollo | 1 / 4 | SEC-001 |
| T-14 | Manipulación | Dependencia comprometida | Versiones fijadas, `go.sum`, imágenes por digest, SBOM, escaneo CVE | 0 (CI) / 4 | CI |
| T-15 | Divulgación | API expuesta públicamente | `bind` solo loopback/privado; TLS obligatorio fuera de loopback (validado) | 0 | config tests |

**Riesgos residuales:** el host y el NAS están fuera del control del producto (responsabilidad contractual del
cliente); un root del host puede leer claves; HMAC no impide inferencias a través de la API autorizada.
