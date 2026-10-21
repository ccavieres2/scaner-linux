# ADR-0001 — Go para núcleo/API, Python aislado para extractores y OCR

- **Estado:** Aceptado (Fase 0)
- **Contexto:** spec §4, §20. Se requiere concurrencia, binario compacto y librerías maduras de PDF/Office/OCR.
- **Decisión:** `scanner` (CLI, inventario, scheduling, detección RUT/email, persistencia) y `api` en Go.
  Extracción documental y OCR en procesos Python separados, sin puertos expuestos, con límites de CPU/RAM/tiempo.
- **Consecuencias:** dos toolchains en CI; contrato de extractor versionado (§20). Un fallo/OOM de un parser
  no derriba al scheduler. Las licencias de PyMuPDF (AGPL/comercial) deben resolverse antes de distribuir (ver `assumptions.md` A-07).
