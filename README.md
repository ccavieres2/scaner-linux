# arsop-scanner

Motor on-premise de descubrimiento de datos personales (RUT, correo) en file servers, como apoyo a
derechos ARSOP (Ley 21.719). Requisitos: `ESPECIFICACION_TECNICA_COMPLETA_SCANNER_ARSOP_21719.md`.

> **Estado: Fase 1** (vertical slice sin OCR): inventario seguro, TXT/CSV/PDF digital/XLSX, RUT/email,
> PostgreSQL, API de búsqueda. **No apto para producción** (ver `docs/security-controls.md`). No certifica cumplimiento legal.

## Uso

```bash
make test        # vet + tests (levanta PostgreSQL 16 embebido; requiere Python con workers/extractor/requirements.txt)
make build       # bin/scanner, bin/api
bin/scanner config validate --file config/scanner.example.yaml
bin/scanner init --file /etc/arsop/scanner.yaml
bin/scanner scan start --source rrhh --file /etc/arsop/scanner.yaml
bin/scanner scan report --scan <ID> --file /etc/arsop/scanner.yaml
bin/api --file /etc/arsop/scanner.yaml
```

**Instalación: [INSTALACION.md](INSTALACION.md). Cómo funciona y todos los comandos: [FUNCIONAMIENTO.md](FUNCIONAMIENTO.md).** Prueba en VM: `docs/INSTALL-VM.md`. Exponer la API a un cliente (mTLS + token): `docs/API-CLIENTE.md`. Despliegue con Docker Compose: `deploy/README-deploy.md`. Contrato de API: `api/openapi.yaml`.

## Documentación
- `docs/architecture.md`, `docs/threat-model.md`, `docs/security-controls.md`
- `docs/requirements-traceability.md`, `docs/assumptions.md`, `docs/limitations.md`
- `docs/ADR/` — decisiones de arquitectura
- `docs/phase1-report.md` — resultado de la Fase 1, pruebas no ejecutadas y riesgos
