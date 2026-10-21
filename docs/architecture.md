# Arquitectura (Fase 0)

```text
Host Linux del cliente — mounts SMB/NFS gestionados por el cliente en /data/input/* (solo lectura)
 └─ Docker Compose
     ├─ scanner (Go)    CLI + scheduler + inventario + detección RUT/email + escritura por lotes
     ├─ extractor (Py)  PDF digital / XLSX streaming / DOCX / CSV / TXT → fragmentos de texto    [monta fuentes :ro]
     ├─ ocr (Py)        Tesseract local, por página/imagen, sin red                             [monta fuentes :ro]
     ├─ postgres        catálogo, file_tasks (cola, ADR-0002), hallazgos HMAC, auditoría       [red interna]
     └─ api (Go, chi)   /api/v1, authn/authz, paginación, rate limit, auditoría                [127.0.0.1:8080]
```

## Flujo de datos

1. `scanner` recorre raíces autorizadas (ADR-0005) → `files` / `file_versions` (`size+mtime+inode`).
2. Crea `file_tasks` (`extract` u `ocr`) → los workers reclaman con lease. **La cola nunca transporta contenido.**
3. Extractor/OCR devuelve fragmentos `{unit_type, unit_index, text_chunk, coordinates?, method, confidence?}` con
   superposición `chunk_overlap_chars` (DOC-006). El texto vive solo en memoria.
4. Los detectores normalizan y validan; se calcula `lookup_hmac` (ADR-0003); una transacción escribe hallazgos
   + ubicaciones + estado de versión (idempotente con `ON CONFLICT`).
5. Al completar, se publica atómicamente `files.last_success_version`.
6. La API responde por HMAC del valor consultado, solo con objetos autorizados, y registra auditoría sin PII.

## Límites de confianza

| Límite | Control |
|---|---|
| Archivo de origen → parser | Parser no confiable: proceso aislado, no-root, sin red, límites de tiempo/memoria/páginas |
| Worker → PostgreSQL | Red Docker interna; usuario de BD con mínimo privilegio por servicio |
| Cliente API → API | TLS, OIDC/mTLS (ADR-0006), autorización por objeto, rate limit |
| Operador → CLI | Usuario de servicio Linux; secretos solo desde archivos, nunca flags |

## Estado

Fase 1 implementada (ver `docs/phase1-report.md`). En Fase 1 el extractor Python corre como subproceso del contenedor `scanner`; OCR pendiente (Fase 2).
