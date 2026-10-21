# Supuestos conservadores (decisiones pendientes de la spec §17)

| ID | Supuesto adoptado | Efecto | Reemplazar cuando |
|---|---|---|---|
| A-01 | 1 tenant por instalación; `tenant_id` existe pero no se afirma multi-tenant seguro | Aislamiento por despliegue | Decisión §17.8 |
| A-02 | Cola en PostgreSQL (ADR-0002) | Menos componentes | Benchmark Fase 3 lo invalide |
| A-03 | `follow_symlinks` no soportado | Archivos tras symlink quedan fuera y se reportan | Cliente lo requiera con análisis |
| A-04 | `max_file_bytes` 500 MiB, `max_pdf_pages` 2000, 60 s OCR/página | Archivos mayores → `skipped` explícito | Benchmark |
| A-05 | Cola liviana `max_extract_workers=4` (reserva 64 MB c/u); cola pesada `max_heavy_workers=2`, `worker_memory_mb=1024`, `memory_budget_mb=3072` | Concurrencia pesada efectiva = min(workers, (presupuesto − livianos×64) / worker_memory) | Benchmark por hardware |
| A-06 | No se almacenan snippets ni valores originales | API devuelve solo ubicaciones | Aprobación de privacidad |
| A-07 | Licencia PyMuPDF (AGPL) **no resuelta**: Fase 1 usa `pypdfium2` (Apache-2.0/BSD-3) detrás del contrato del worker | Sin riesgo AGPL; calidad de extracción a evaluar | Decisión §17.11 |
| A-08 | Autenticación API definitiva pendiente (ADR-0006) | Fase 1 usa token de desarrollo deshabilitado en producción | IdP del cliente §17.7 |
| A-09 | Comparación de emails: dominio en minúsculas/IDNA; parte local intacta; coincidencia por igualdad exacta tras normalizar | Posibles falsos negativos por mayúsculas en parte local | Política explícita del cliente |
| A-10 | Licencia del producto **no elegida**; no se incluye `LICENSE` | — | Decisión de negocio |
| A-11 | Target Linux x86_64, kernel ≥ 5.6 (para `openat2`), fallback documentado | — | §17.3 |
| A-12 | `sslmode=disable` solo con el servicio interno `postgres` | BD externa exige TLS | — |
| A-13 | PostgreSQL ≥ 15 (`UNIQUE NULLS NOT DISTINCT`); referencia 16 | — | — |
| A-14 | Confusiones OCR solo se evalúan sobre texto OCR | Sin falsos positivos por "O/0" en texto digital | — |
| A-15 | Fase 1: extractor Python como subproceso del contenedor `scanner` | Menos contenedores; aislamiento por proceso | Fase 2 (worker separado sin red) |
| A-16 | Ubicaciones: máx. 100 almacenadas y 20 devueltas por hallazgo | Acota exposición y tamaño | Necesidad del cliente |
| A-17 | Lease de tarea = timeout por archivo + 60 s; reintento con backoff 5 s·2^n | — | Benchmark |
| A-18 | `confidence` es heurística no calibrada (1.0 digital validado; ≤0.5 hipótesis OCR) | No usar como probabilidad | Evaluación con corpus (Fase 2) |
| A-19 | Clase de cola por extensión en inventario (sin abrir archivos); el tipo real se verifica al procesar y se reclasifica | Inventario rápido; un PDF disfrazado cuesta una reclasificación | — |
| A-20 | Archivos >= `large_file_bytes` (1 MiB) van al final de su cola; dentro de cada grupo, por tamaño ascendente | Resultados tempranos en archivos pequeños; los grandes pueden tardar en aparecer | Necesidad del cliente |
| A-21 | El worker vuelca el archivo a un temporal anónimo en `temp_dir` (borrado automático aun si muere) | Requiere espacio en disco cifrado ≈ heavy_workers × max_file_bytes | Fase 4 (cifrado de temporales) |
| A-22 | La reserva de memoria es el tope duro del proceso, no una estimación por tamaño | Conservador: puede infrautilizar memoria | Benchmark |
