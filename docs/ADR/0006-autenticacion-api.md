# ADR-0006 — Autenticación de la API

- **Estado:** Propuesto — **decisión pendiente con el cliente** (spec §17.7, §24).
- **Opción conservadora por defecto:** OIDC (JWT de vida corta, validación de `iss`, `aud`, `exp`, firma con JWKS
  cargado localmente o por endpoint interno del IdP) **o** mTLS de máquina. Scopes: `findings:search`, `files:read`,
  `scans:read`, `scans:start`, `scans:manage`, `scans:diagnose`. Deny-by-default.
- **Para desarrollo/Fase 1:** principal estático con token aleatorio ≥ 256 bits leído de archivo, almacenado como
  hash, **deshabilitado** en builds de producción. Nunca es el único mecanismo en producción.
- `tenant_id` siempre se deriva del principal autenticado, jamás del cuerpo de la solicitud.
