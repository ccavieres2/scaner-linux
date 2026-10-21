# ADR-0003 — Protección de identificadores: HMAC-SHA-256 por cliente + AES-GCM opcional

- **Estado:** Aceptado (Fase 0). Debe aprobarse antes de persistir hallazgos reales (spec §28).
- **Decisión:**
  - `lookup_hmac = HMAC-SHA-256(K_hmac[v], type || 0x00 || normalized_value)`; se guarda `key_version`.
  - Nunca SHA-256 sin clave (espacio de RUT enumerable, spec §7).
  - Valor original: **no se almacena por defecto**. Si una política aprobada lo exige, AES-256-GCM con
    `K_enc` distinta de `K_hmac`, nonce aleatorio de 96 bits y AAD = `tenant_id|identifier_id`.
  - Claves leídas desde archivos con ACL estrictas (`/run/secrets/*`), nunca env, flags, imagen ni Git.
  - Rotación: nueva versión de clave → reindexación de `lookup_hmac` en segundo plano; búsquedas consultan
    versiones activas durante la transición.
- **Consecuencias:** HMAC es seudonimización, no anonimización. La pérdida de `K_hmac` obliga a reescanear;
  la pérdida de `K_enc` hace irrecuperables los valores cifrados (documentar en runbook de claves).
