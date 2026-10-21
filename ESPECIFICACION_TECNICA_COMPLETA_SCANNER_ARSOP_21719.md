# Especificación técnica y de seguridad — Motor de descubrimiento de datos personales para soporte ARSOP

**Versión:** 2.0 (documento de diseño, no implementación certificada)  
**Fecha:** 19 de octubre de 2026  
**Modalidad:** On-premise, Linux, sin interfaz gráfica obligatoria, contenedores Docker  
**Marco:** Ley chilena N.º 21.719, que modifica la Ley 19.628 (entrada en vigencia: 1 de diciembre de 2026)

> **Advertencia:** Esta especificación distingue las obligaciones legales de las decisiones de ingeniería. No constituye asesoría jurídica, certificación de cumplimiento ni garantía de detectar todos los datos personales. Debe ser revisada con el equipo jurídico, de seguridad y de protección de datos de cada cliente.

## 1. Resumen ejecutivo

Se desarrollará un motor de descubrimiento e indexación de datos personales en archivos corporativos. Será instalado **en una máquina Linux del cliente**; el cliente administra los **mounts** de sus file servers (SMB/CIFS, NFS, NAS o discos locales). El producto escanea únicamente rutas expresamente autorizadas, procesa archivos digitales y escaneados (OCR), detecta identificadores tales como **RUT y correos**, conserva evidencia mínima de cada hallazgo y expone una **API autenticada** para que otra aplicación busque en qué archivos aparece un identificador.

El escáner será un **componente de apoyo** a los derechos de acceso, rectificación, supresión, oposición y portabilidad, además del bloqueo previsto en la ley. No automatizará cambios en los archivos de origen ni resolverá por sí mismo la identidad del titular o la procedencia jurídica de una solicitud.

**Objetivos principales:** minimización de datos, confidencialidad, seguridad desde el diseño, continuidad ante fallos, trazabilidad, resultados consultables, procesamiento incremental y escalabilidad. **Objetivo aspiracional de negocio:** completar escaneos iniciales de millones de archivos en hasta 14 días, **sujeto a pruebas de capacidad por instalación**; no es una garantía universal.

## 2. Requisitos y restricciones acordados

- Instalación del producto en **una máquina Linux propia del cliente**, dentro de su red.
- El cliente decide cómo montar y autorizar los repositorios de archivos; el producto consume rutas locales visibles en Linux. No necesita instalarse en cada file server.
- Funcionamiento desde CLI/servicios en segundo plano, **sin GUI** para reducir complejidad y consumo; otra aplicación usará la API.
- Despliegue reproducible con Docker Compose; PostgreSQL y componentes auxiliares en contenedores con volúmenes persistentes.
- Fuentes iniciales: PDF con texto, PDF imagen/escaneado, Excel, Word, CSV, TXT, JPG, PNG, TIFF; ampliar por plugins probados.
- Detección de RUT, correo electrónico y futuras categorías de datos personales; ubicación (página, hoja/fila/celda), método y confianza.
- Millones o decenas de millones de archivos; alta concurrencia configurable, suspensión/reanudación, errores aislados y métricas.
- Sin servicios de OCR externos ni salida de contenido documental a Internet por defecto.
- Acceso **solo lectura** a repositorios de origen; la API de consulta no modifica documentos.
- Aislamiento entre clientes: despliegue y base de datos independientes por organización, al menos en primera versión.
- **Decisión pendiente:** si se admitirá procesamiento distribuido en múltiples máquinas en versiones posteriores. Dejar interfaces preparadas sin asumirlo como requisito inicial.

## 3. Alcance funcional

### Dentro del alcance

1. Configurar rutas permitidas y validar accesibilidad, permisos y espacio disponible.
2. Inventariar metadatos: ruta, tamaño, tipo real, tiempos, estado, identificador interno y versión observada.
3. Detectar tipo real por firma/MIME cuando corresponda; no confiar solamente en extensiones.
4. Extraer texto estructurado con referencia a fuente y localización.
5. Ejecutar OCR **solo donde hace falta**, por página o imagen; PDF mixtos tratados página a página.
6. Detectar candidatos, normalizarlos y validarlos mediante reglas versionadas.
7. Registrar evidencia mínima, errores y cobertura con precisión de nivel de archivo y, cuando proceda, página.
8. Buscar por identificador exacto normalizado a través de API con autorización granular.
9. Ofrecer estadísticas de escaneo, cobertura, tasa de errores y estado de actualización.
10. Reanudar y ejecutar escaneos incrementales; gestionar eliminaciones, renombrados y datos obsoletos.

### Fuera de alcance inicial

- Clasificación semántica exhaustiva de todos los datos sensibles con certeza jurídica.
- Verificar la identidad de titulares por sí mismo, tomar decisiones automáticas sobre procedencia de derechos, editar/eliminar documentos originales.
- Acceder a SaaS y correo electrónico no montados como archivos; requeriría conectores separados.
- Garantizar cero falsos negativos y plazo fijo sin conocer hardware, red y corpus.
- Extraer contraseñas de documentos cifrados o eludir controles de acceso.

## 4. Arquitectura propuesta

```text
File servers / NAS / almacenamiento local (SMB, NFS, disco)
              | mounts gestionados por el cliente
              v
Host Linux del cliente
  /mnt/fuente1  /mnt/fuente2  ...  [SOLO LECTURA]
              |
       +------+------------------------------------------+
       | Docker Compose                                  |
       |  scanner-cli / scheduler (Go)                   |
       |      -> inventario -> cola duradera             |
       |             |                                  |
       |             +--> extractores texto/Office      |
       |             +--> workers OCR aislados          |
       |                         |                      |
       |                 detectores PII                  |
       |                         |                      |
       |           PostgreSQL (índices/hallazgos)        |
       |                         |                      |
       |             API REST (Go) [TLS, authz]          |
       +-------------------------+----------------------+
                                 |
                       Software cliente autorizado
```

**Separación de cargas:** (a) inventario e I/O, (b) extracción documental, (c) OCR CPU/GPU opcional, (d) detector y persistencia, (e) API. Escalar pools por límites de red/CPU/RAM, no con un número fijo de threads. En una primera implementación puede haber pocos contenedores y procesos separados; evitar microservicios innecesarios.

### Stack sugerido (a validar mediante benchmark)

| Capa | Elección principal | Consideraciones |
|---|---|---|
| Orquestación, CLI y API | Go; `chi` o Gin | Concurrencia, binario compacto, endpoints y jobs |
| Extractores y OCR | Python aislado para librerías concretas | Separar costo/memoria por proceso |
| PDF digital | PyMuPDF | Revisar licencia AGPL/comercial y obligaciones de distribución antes de adoptarlo |
| Office/formatos complejos | Apache Tika y/o extractores específicos | Tika en proceso aislado; limitar memoria/tiempo |
| Excel | `openpyxl` modo read-only o librería streaming/calamine | Hojas ocultas, filas, fórmulas y valores cacheados |
| OCR | Tesseract como referencia; evaluar PaddleOCR | Licencias, modelos, precisión en español, rendimiento CPU/GPU |
| Preproceso | OpenCV / Pillow | Rotación, deskew, resolución, ruido |
| Cola | RabbitMQ durable o Redis Streams con ACK y recuperación | Definir semántica *at least once* e idempotencia |
| Índices de hallazgos | PostgreSQL | Particionado, índices por tenant/tipo/token, bulk inserts |
| Observabilidad | Prometheus, Grafana, logs JSON | Logs redactados y sin PII |
| Entrega | Docker Compose, imágenes fijadas por digest | Linux con filesystem persistente |

No se usará un motor de búsqueda externo como Elasticsearch inicialmente sin pruebas que justifiquen costo, volumen y controles adicionales.

## 5. Procesamiento por formato

| Formato | Estrategia | Riesgos y medidas |
|---|---|---|
| PDF digital | Extraer texto por página; conservar coordenadas si disponibles | Texto invisible, orden de lectura, fuentes extrañas |
| PDF escaneado | Identificar páginas sin texto útil y rasterizar/OCR por página | DPI configurable, timeout y límite de páginas; evitar OCR de páginas ya legibles |
| PDF mixto | Decisión por página/región | No asumir que PDF con algo de texto no necesita OCR |
| XLSX/XLS | Lectura streaming con posiciones hoja/fila/celda | No evaluar macros ni fórmulas; no confiar en solo valores cacheados |
| CSV/TSV/TXT | Streaming con detección controlada de codificación | Grandes tamaños, separadores, codificación, líneas largas |
| DOCX/Word | Extractor sin ejecutar macros ni contenido activo | Tablas, encabezados, comentarios y contenido incrustado |
| JPG/PNG/TIFF | OCR con límites por imagen/frames | Archivos malformados, explosión de píxeles, rotación |
| ZIP y adjuntos (futuro) | Solo si se autoriza explícitamente | Límite profundidad/tamaño/ratio anti zip-bomb; sin seguir rutas externas |

**Principio:** el texto bruto es efímero en memoria o almacenamiento temporal cifrado y limitado; por defecto no se conserva en base de datos. No generar un nuevo PDF OCR salvo requerimiento explícito. Documentos con contraseña: registrar como inaccesibles; nunca intentar fuerza bruta.

## 6. Reglas de detección y calidad

**Pipeline:** fragmento extraído -> candidatos -> normalización -> validación -> score -> hallazgo o revisión.

**RUT chileno:** reconocer variantes con/sin puntos y guion, normalizar a forma canónica, calcular dígito verificador módulo 11, controlar falsos positivos y detectar confusiones OCR `O/0`, `I/1`, `S/5`, `B/8` solo con contexto. No corregir silenciosamente un identificador ni inferir titular por coincidencia aislada. El checksum no demuestra identidad.

**Correo:** sintaxis válida, normalización conservadora del dominio (IDNA cuando aplique), evitar alterar arbitrariamente parte local, manejar OCR incierto. No consultar validadores o servicios externos sin autorización.

**Otras categorías futuras:** teléfonos, nombres, direcciones, cuentas u otras identificaciones, con detectores separados y política por cliente. Datos sensibles requieren contexto, revisión y umbrales específicos.

Guardar: tipo, token consultable protegido, archivo y versión, ubicación, motor/regla versión, método (`text`, `ocr`, `hybrid`), confianza, timestamp y estado de revisión. **Nunca presentar un score de OCR como probabilidad de verdad calibrada sin evaluación previa.**

Evaluación con corpus etiquetado: precisión, recall, falsos positivos, falsos negativos, tasa de páginas no procesadas, métricas por formato y calidad de digitalización; mediciones segmentadas por tipo de dato y cliente piloto. Documentar límites.

## 7. Persistencia y modelo de datos

Tablas orientativas:

- `tenants` (si se admite multiempresa; en v1 preferir una instancia por cliente).
- `repositories` y `scan_roots`: mounts, políticas, rutas permitidas.
- `scan_jobs`, `scan_tasks`: ejecución, intentos, heartbeat, lease, checkpoints, versiones de reglas.
- `files`: identidad del archivo, ruta normalizada, metadatos, `status`, fingerprint/version, `last_seen`, errores.
- `identifiers`: `type`, `lookup_token`, valores originales solo cifrados si necesarios y autorizados.
- `findings`: relación única `(file_version_id, identifier_id, detection_rule_version)` según política de deduplicación.
- `finding_locations`: página, hoja, fila, celda, recuadro o segmento.
- `scan_errors`: categoría, retries, estado, sin contenido personal.
- `access_audit`: quién consultó, acción, finalidad/ticket, resultado, cuándo.
- `privacy_cases` (futuro): solicitudes ARSOP vinculadas a sistemas externos sin automatizar la resolución.

**Índices:** `(tenant_id, identifier_type, lookup_token)`; sobre `file_id`, `scan_job_id` y estados pendientes. Particionar únicamente cuando los perfiles de consulta y volumen justifiquen. Usar lotes e inserciones idempotentes; monitorizar WAL, crecimiento de índices y disco.

### Protección de identificadores

- Para búsquedas exactas: **HMAC-SHA-256 con clave distinta por cliente** sobre valor normalizado, con versión de clave y diseño de rotación/reindexación.
- No usar SHA-256 sin clave como protección de RUT: el espacio puede enumerarse.
- Si se necesita recuperar el valor original, almacenarlo mediante cifrado autenticado con claves externas a BD, exclusivamente según finalidad y retención aprobadas; minimizarlo por defecto.
- Los HMAC son seudonimización, **no anonimización**; rutas, metadatos y vínculos también pueden ser personales.
- Controlar acceso a índices y backups; nunca imprimir tokens, rutas sensibles o valores en logs generales.

## 8. API y permisos

Base sugerida `/api/v1`; la API debe funcionar aunque el escáner esté detenido.

| Método | Ruta | Alcance |
|---|---|---|
| `POST` | `/scans` | Iniciar escaneo solo sobre roots previamente autorizados |
| `GET` | `/scans/{id}` | Estado, porcentajes, cobertura y errores |
| `POST` | `/scans/{id}/pause` | Pausar |
| `POST` | `/scans/{id}/resume` | Reanudar |
| `POST` | `/findings/search` | Buscar por RUT/correo sin dejar PII en query strings |
| `GET` | `/files/{id}/findings` | Resultados autorizados por archivo |
| `GET` | `/stats` | Reportes agregados de cobertura |
| `GET` | `/errors` | Errores redactados |
| `GET` | `/health` y `/ready` | Estado sin secretos ni PII |

**Ejemplo de solicitud:**

```http
POST /api/v1/findings/search
Authorization: Bearer <token-de-acceso>
Content-Type: application/json

{"type":"rut","value":"12.345.678-5","case_reference":"ARSOP-2026-001","page_size":50}
```

**Ejemplo de respuesta:**

```json
{
  "query_type": "rut",
  "total_files": 2,
  "index_freshness": "2026-10-19T13:00:00Z",
  "coverage_status": "partial",
  "results": [
    {"file_id":"f-001","file_name":"contrato.pdf","location":{"page":4},"method":"text","review_status":"unreviewed"},
    {"file_id":"f-002","file_name":"clientes.xlsx","location":{"sheet":"Clientes","row":82,"column":"D"},"method":"ocr","review_status":"needs_review"}
  ],
  "next_cursor": null
}
```

Datos de ejemplo ficticios. No revelar rutas completas, valores o extractos de forma predeterminada; habilitarlos únicamente por permiso y necesidad. Paginación por cursor, topes, cuotas, filtros y auditoría. En búsquedas sensibles no implementar endpoints públicos para verificar indiscriminadamente si un RUT existe: impedir enumeración masiva, establecer motivos autorizados y controles de abuso. La autenticación de una aplicación consumidora **no prueba** por sí sola que el titular solicitante tenga derecho a esos datos.

## 9. Seguridad por diseño: controles obligatorios para salida a producción

### 9.1 Modelo de amenazas

Activos protegidos: documentos de origen, resultados de hallazgos, índices HMAC, claves, mounts, credenciales, auditoría y disponibilidad. Adversarios considerados: usuarios internos no autorizados, consumidor API comprometido, archivo malicioso, administrador con privilegios excesivos, exposición de respaldos, atacante en red, ransomware y errores operacionales.

### 9.2 Host, contenedores y fuentes

- Host Linux endurecido y actualizado; cuentas de servicio dedicadas; SSH con claves, MFA para administración cuando proceda; firewall que bloquee accesos no permitidos.
- Mounts configurados por cliente en el host y bind-mounted como `:ro` en workers; credenciales SMB/NFS fuera de contenedores y con permisos mínimos. Configurar explícitamente comportamiento de symlinks, hardlinks, mount traversal y rutas autorizadas para impedir salirse del alcance.
- Ejecutar contenedores como usuarios no root; `no-new-privileges`, `cap_drop: ALL`, perfil seccomp/AppArmor, sistema de archivos raíz read-only donde aplique, `tmpfs` con cuotas, límites de CPU/memoria/PIDs/ulimits y red restringida.
- **Nunca montar** `/var/run/docker.sock`, directorios privilegiados del host ni credenciales de administrador en los workers.
- No ejecutar scripts, macros, JavaScript incrustado, enlaces de red ni archivos extraídos. Sandboxing reforzado y timeouts para parsers no confiables.
- Archivos de temporales protegidos, preferentemente en tmpfs limitado o disco cifrado, borrado al terminar; evitar swap no cifrado y dumps con información confidencial.
- Sin telemetría saliente, subida de documentos ni descarga automática de modelos durante el análisis; habilitar actualizaciones mediante proceso controlado y firmado.

### 9.3 Red y API

- TLS 1.2+ con preferencia por TLS 1.3 y certificados válidos; mTLS para integraciones entre servicios cuando corresponda.
- Por defecto API ligada a `127.0.0.1` o red privada, detrás de reverse proxy controlado; **nunca exposición pública accidental** de BD, cola, métricas o OCR.
- Tokens de corta vida emitidos por proveedor de identidad empresarial/OIDC o autenticación mTLS de máquina; no claves API estáticas compartidas como mecanismo único para integraciones sensibles.
- RBAC + controles por ámbito (`tenant`, repositorio, tipo de dato, acción), deny-by-default y verificación de autorización en cada objeto y respuesta.
- Límite de frecuencia, cuota por usuario/servicio, control de concurrencia, registros de auditoría e investigación de anomalías; impedir enumeración de RUT.
- Validar entradas, SQL parametrizado, tamaños máximos de cuerpo, esquemas de respuesta y mensajes de error sin detalles internos.
- Revisar contra **OWASP API Security Top 10**: BOLA, autenticación, autorización por propiedades, agotamiento de recursos, funciones privilegiadas y configuración insegura.

### 9.4 Cifrado y gestión de secretos

- Cifrado en reposo para volúmenes de BD, backups y disco temporal mediante LUKS u opción corporativa equivalente; PostgreSQL por sí solo no cifra el volumen físico.
- Secretos suministrados mediante secret manager o mecanismos protegidos del host; **no** contraseñas en imagen Docker, Git, logs, argumentos visibles de proceso ni archivo `.env` de permisos laxos.
- Plan de rotación, revocación, recuperación y separación de funciones; proteger claves de HMAC y de cifrado independientemente de sus datos.
- Backups cifrados, pruebas de restauración y acceso restringido; definir RPO/RTO y política de retención aprobada.

### 9.5 Auditoría, incidentes y ciclo de vida

- Audit log de accesos a datos, búsqueda, exportación, cambios de configuración, inicio/fin de escaneos y acciones administrativas, con integridad y retención definida.
- Los logs operacionales deben ser redactados; evitar contenido documental, RUT, correo completo, texto OCR, tokens de sesión y secretos.
- Alertas por consultas masivas, fallos repetidos, accesos fuera de horario, cambios de permisos, inactividad inesperada y agotamiento de disco.
- Procedimiento de respuesta a incidentes, aislamiento, preservación de evidencias, clasificación y comunicación conforme a obligaciones aplicables.
- CI/CD con SAST, SCA, escaneo de imágenes, SBOM, pinning de versiones/digests, revisión de licencias, parches y firmas de artefactos.
- Pentest y revisión de arquitectura antes de producción, y periódicamente según criticidad; pruebas de recuperación y seguridad de dependencias.

### 9.6 Privacidad y conservación

- Determinar documentalmente roles de responsable/encargado según contrato; finalidad, base de licitud, instrucciones, acceso, plazo de conservación y eliminación al término del servicio.
- No exportar resultados a nube ni a terceros por defecto.
- Solo almacenar evidencia mínima y necesaria; aprobar si se guardan snippets OCR o valores de datos personales. Preferir ubicaciones y metadatos suficientes sin conservar texto.
- Borrado seguro lógico/criptográfico según medios y política, incluida retirada de índices y expiración de respaldos; evitar prometer borrado físico inmediato en medios modernos.
- Cuando hay contradicción entre solicitud ARSOP y obligaciones legales de conservación, someterla a decisión de personal autorizado; mantener justificación.

## 10. Resiliencia, tolerancia a errores y consistencia

Estados del archivo/tarea: `PENDING`, `PROCESSING`, `COMPLETED`, `PARTIAL`, `RETRY`, `FAILED`, `SKIPPED`, `DELETED/STALE` según ciclo de vida. Cada transición registra motivo, versión y timestamp.

- La cola tendrá persistencia, ACK explícito, visibility timeout/leases, reintentos con backoff y DLQ; usar **idempotencia** porque *at-least-once* produce duplicados posibles.
- Checkpoints por archivo y, cuando sea rentable, por página/hoja; reanudación sin volver a procesar todo.
- Timeouts, límites de memoria y página para OCR, parser y extracción; proceso fallido aislado del scheduler.
- Diferenciar archivo corrupto, permisos denegados, archivo cifrado, mount desconectado, archivo cambiado mientras se lee y error interno.
- Ante cortes SMB/NFS: pausar o reintentar; **no** interpretar mount vacío o desconectado como eliminación masiva. Validar salud y raíz antes de reconciliar borrados.
- Comprobar estabilidad de archivo (metadata antes/después, y hash cuando corresponda); evitar hallazgos de versiones mezcladas.
- Las inserciones deben ser transaccionales: actualizar versión y hallazgos consistentemente para evitar resultados parciales presentados como completos.
- Distinguir `scan_coverage` de `match_found`: no encontrar un dato no significa que no exista si hay archivos no cubiertos o fallas OCR.

## 11. Rendimiento y capacidad para 14 días

El plazo de 14 días es un **SLO objetivo a validar**, no garantía sin pruebas. Para 5 millones de archivos en 14 días, 24 h/día, se requiere una tasa sostenida de **~4,13 archivos/s**; para 20 millones, **~16,53 archivos/s**. Estas tasas agregadas no representan velocidad de OCR.

Variables críticas:

- Total de archivos, bytes, páginas e imágenes; distribución de tamaños.
- Proporción digital/escaneada, resolución, idiomas y complejidad OCR.
- Throughput y latencia del almacenamiento remoto SMB/NFS, red y NAS.
- Cores, RAM, disco temporal, IOPS, GPU opcional y tasa de inserciones PostgreSQL.
- Archivo pesado/dañado, tiempos de espera y cantidad de archivos pequeños.

Optimizaciones priorizadas:

1. Inventariar primero con metadatos; no hashear previamente todos los archivos (lectura adicional). Calcular hashes selectivamente o durante la extracción.
2. Procesar texto digital sin OCR; OCR selectivo por página.
3. Agrupar operaciones y usar backpressure entre etapas. Colas y pools independientes para I/O, OCR, BD y API.
4. Control adaptativo de concurrencia según latencia del mount, RAM y CPU; evitar saturación de NAS o BD.
5. Para OCR, perfiles rápidos y precisos con reintento selectivo de casos dudosos.
6. Escaneo incremental por versión observada y reconciliación segura de eliminados.
7. Reducir trabajo duplicado, conservar cachés únicamente donde el riesgo y costo lo permitan.

**Benchmark obligatorio previo a comprometer plazos:** corpus autorizado y representativo, 10k–100k archivos de prueba o muestra estadística, diversidad de formatos y páginas, mediciones p50/p95, archivos/s, páginas OCR/s, MB/s, CPU/RAM, falsos negativos y tamaño DB/hallazgo. Extrapolar por clases de archivo y con margen de contingencia; ejecutar piloto en la infraestructura real del cliente.

**Criterios de éxito propuestos (por acordar):** 100 % de archivos inventariados dentro del alcance que estuvo accesible; >99 % de tareas procesadas o clasificadas con error explícito; cero pérdidas silenciosas de trabajo; tiempo de recuperación documentado; controles críticos sin vulnerabilidades abiertas. Cobertura de datos detectados solo se evaluará contra corpus etiquetado; no prometer 100 % de recall.

## 12. Uso para ejercicio de derechos ARSOP

La Ley 21.719 reconoce derechos de **acceso, rectificación, supresión, oposición, portabilidad y bloqueo**. El escáner ayuda a localizar potenciales datos tratados, pero una respuesta legal exige también evaluar finalidad, bases jurídicas, origen, destinatarios, plazos y excepciones, información que no necesariamente se desprende de un archivo encontrado.

**Flujo recomendado:**

1. El sistema de gestión de solicitudes recibe el caso y verifica identidad/representación por un canal seguro (fuera del escáner).
2. Personal autorizado valida alcance y legitimidad de la consulta; asigna número de caso.
3. Software ARSOP consulta la API por RUT/correo u otros datos corroborados; aplicar auditoría y permisos.
4. El escáner devuelve ubicaciones, estado de cobertura y nivel de confianza; resultados dudosos se revisan.
5. El responsable correlaciona con otros sistemas (bases operacionales, SaaS, respaldos, papel) y determina tratamiento y excepciones.
6. Las acciones de rectificar, bloquear, suprimir, oponerse o portar se ejecutan **en sistemas de origen**, bajo aprobación y con evidencia; el escáner no las efectúa automáticamente.
7. Reescaneo posterior valida, cuando corresponde, si el hallazgo desapareció o se modificó; **no equivale por sí solo** a probar el cumplimiento material del derecho.

Para **acceso** debe poder informarse dónde se mantienen datos, sin exponer otros titulares. Para **rectificación/supresión** localizar copias y revisar conservación legal. Para **oposición/bloqueo** la ejecución depende de los sistemas de tratamiento, no de borrar el índice. Para **portabilidad** el motor identifica fuentes potenciales, pero la exportación estructurada de los datos pertinentes necesita integración y controles adicionales.

**Importante:** no usar coincidencias aproximadas OCR para afirmar identidad ni eliminar información; requerir confirmación humana, especialmente cuando hay información sensible o múltiples titulares por documento.

## 13. Administración por consola

Comandos conceptuales a implementar (no afirmar que ya existen):

```bash
scanner init
scanner config validate
scanner mounts check
scanner scan start --profile default
scanner scan status --job <id>
scanner scan pause --job <id>
scanner scan resume --job <id>
scanner scan report --job <id> --format json
scanner db backup --destination <ruta-protegida>
scanner diagnostics --redacted
```

Ejemplo de configuración **ilustrativa**, sin secretos:

```yaml
scanner:
  allowed_roots:
    - /data/input/rrhh
    - /data/input/finanzas
  follow_symlinks: false
  scan_mode: incremental
  max_file_size_mb: 500
  extraction_workers: 8
  ocr_workers: 2
  ocr_languages: [spa, eng]
  ocr_timeout_seconds_per_page: 60
  allow_network_egress: false
api:
  bind: 127.0.0.1
  port: 8080
  require_auth: true
security:
  store_raw_text: false
  store_snippets: false
  audit_enabled: true
```

La configuración definitiva debe incluir validación de esquema, políticas por repositorio, límites por formato, y gestión externa de secretos. La configuración anterior no sustituye un control de firewall ni el aislamiento de redes Docker.

## 14. Despliegue seguro en Linux

- Requisitos previos: Linux soportado, kernel y Docker/Compose actualizados, espacio de base y backups, mounts probados bajo cuenta de solo lectura.
- Preparación: usuario de servicio, directorios protegidos, volúmenes cifrados, firewall, gestión de certificados y políticas de actualización.
- Docker Compose con cinco servicios conceptuales: `scanner-core`, `scanner-extract`, `scanner-ocr`, `postgres`, `scanner-api`, y cola durable si se usa. La implementación puede agrupar core/extraer según perfil.
- Solo `scanner-extract`/`scanner-ocr` deben montar rutas fuente, y solo lectura; API no requiere ver archivos originales salvo funcionalidad de evidencia aprobada.
- Redes Docker privadas; DB y cola sin puertos publicados; API solo escucha en interfaz explícitamente autorizada.
- Comprobar que restauración de backup, reinicio y recuperación de cola funcionen antes de producción.
- Instrumentar healthchecks, métricas de saturación y alarmas por espacio en disco.

**Precaución:** los montajes con `:ro` reducen el riesgo de escritura desde el contenedor, pero **no son una frontera de seguridad suficiente por sí solos**. El host y los controles de acceso al NAS deben aplicar permisos mínimos también.

## 15. Plan de implementación

### Fase 0 — Seguridad y alcance

Definir contrato de tratamiento, roles, amenazas, política de retención, modelo de permisos, corpus de prueba sintético/anonimizado o autorizado, baseline de seguridad, dependencias y licencias.

### Fase 1 — MVP funcional

CLI Go, inventario, PDFs digitales/CSV/TXT/XLSX, detección y validación RUT/email, PostgreSQL y consulta API con autenticación/autorización desde el inicio. Escaneo pequeño con pruebas de recuperación.

### Fase 2 — OCR y fiabilidad

OCR de imagen/PDF mixto, sandboxing, checkpoints, DLQ, reporte de `PARTIAL`, revisión de casos dudosos, benchmark con corpus etiquetado.

### Fase 3 — Escala y operación

Backpressure, colas, batch inserts, incremento por cambios, resiliencia SMB/NFS, monitoreo, tuning por hardware y pilotos de millones de archivos.

### Fase 4 — Preparación empresarial

Hardening automatizado, rotación de secretos, gestión de versiones, recuperación comprobada, pentest, documentación de APIs, auditoría y procedimientos de soporte ARSOP.

## 16. Matriz mínima de pruebas y criterios de salida

| Área | Prueba obligatoria |
|---|---|
| Calidad | RUT válidos/inválidos, OCR confuso, emails complejos, múltiples titulares |
| Cobertura | PDF digital/escaneado/mixto, Excel grande, hojas ocultas, Word, TIFF multipágina |
| Resiliencia | Kill de worker/host, corte de mount, recuperación sin perder/duplicar hallazgos |
| Consistencia | Archivo cambia durante lectura; versiones y ubicaciones correctas |
| Seguridad | Autorización por objeto y propiedad, enumeración RUT, inyección, DoS, bypass de rutas |
| Parsers | Archivo corrupto, zip bomb, PDF malicioso, imágenes enormes, macros no ejecutadas |
| Privacidad | No PII en logs, temporales, métricas ni respuestas no autorizadas |
| Criptografía | Rotación HMAC/cifrado y recuperación de backups |
| Rendimiento | Corpus realista, control de NAS saturado, plazos y costo por millón |
| Operación | Instalación limpia, upgrade, rollback, restauración y reporte de auditoría |

**Bloqueantes para producción:** vulnerabilidades críticas/altas sin mitigación aprobada, API sin autorización objeto a objeto, exposición de secretos o PII en logs, ausencia de recuperación transaccional, ausencia de cifrado/gestión de claves según riesgo, o incapacidad de identificar cobertura incompleta.

## 17. Decisiones abiertas por validar

1. Número esperado de archivos, bytes, páginas y porcentaje real de documentos escaneados.
2. Recursos Linux típicos (CPU/RAM/disco/GPU) y limitaciones de red/NAS.
3. Sistema operativo y requisitos de soporte del cliente.
4. Formatos imprescindibles para MVP; política para archivos comprimidos y cifrados.
5. Necesidad de localizar solo identificadores o también categorías sensibles, y en qué etapa.
6. Política de conservación/anonimización de hallazgos, auditoría y backups.
7. IdP disponible (OIDC, AD), segmentación de redes y mecanismos de certificados.
8. Si se ofrecerá solo una instancia por empresa o múltiples dominios aislados dentro de una misma instalación.
9. Requerimientos de alta disponibilidad, RPO/RTO y futuros workers remotos.
10. Integración con gestor de solicitudes ARSOP, reglas de permisos y responsables de validación.
11. Licencias definitivas de extractores, OCR y componentes de terceros.
12. Indicadores de aceptación exigidos contractualmente y proceso jurídico de aprobación.

## 18. Referencias verificables

- **Biblioteca del Congreso Nacional de Chile, Ley 21.719**, texto oficial: https://www.bcn.cl/leychile/navegar?i=1209272
- **BCN, versión vigente desde 1 de diciembre de 2026**, definiciones y derechos: https://www.bcn.cl/leychile/Navegar?idNorma=1209272&idParte=10527471&idVersion=2026-12-01
- **OWASP API Security Top 10 (2023)**: https://api-security.owasp.org/editions/2023/en/0x11-t10/
- **OWASP API Security Project**: https://owasp.org/www-project-api-security/

---

**Estado:** documento base de requisitos y arquitectura para discusión técnica, seguridad y revisión jurídica. **No certifica conformidad legal ni representa una solución implementada.**


---

# PARTE II — CONTRATO DE IMPLEMENTACIÓN PARA UNA IA DE DESARROLLO

> **Propósito:** esta segunda parte convierte la especificación funcional y de seguridad en instrucciones implementables, criterios verificables y un orden de trabajo. La IA que reciba este archivo debe tratar **ambas partes como requisitos del producto**. Ante ambigüedad, elegir la opción más conservadora en seguridad, dejar una decisión documentada (ADR) y no inventar capacidades.

## 19. Instrucción maestra para la IA programadora

Actúa como un equipo senior de ingeniería de software, arquitectura, SRE, ingeniería de seguridad, bases de datos y QA. Implementa un motor **on-premise de descubrimiento de datos personales**, ejecutable en una máquina Linux de un cliente, que analiza directorios autorizados y montados previamente por el cliente, incluidos file servers conectados por SMB/NFS. Debe detectar RUT chileno, correos electrónicos y categorías extensibles; procesar PDF digital, PDF escaneado, PDF mixto, Excel, Word, CSV, TXT e imágenes; guardar ubicaciones y estados en PostgreSQL; y ofrecer una API autenticada para un sistema externo que apoye trámites ARSOP. La CLI es la interfaz administrativa primaria. Usa Docker Compose, pero nunca supongas acceso privilegiado a Docker para el proceso de escaneo.

Reglas de ejecución de la IA:

1. **Implementar por fases y de manera ejecutable**. No generar de una sola vez un repositorio gigante sin pruebas. Entregar una fase, ejecutarla, corregir fallos, actualizar documentación y solo entonces continuar.
2. Antes de codificar cada fase, describir decisiones, riesgos y pruebas. Si falta un dato no bloqueante, elegir un valor **conservador y configurable** y dejarlo en `docs/assumptions.md`.
3. No declarar que algo funciona sin haber corrido las pruebas. Si el entorno no permite Docker, OCR, red o pruebas de integración, indicar explícitamente lo pendiente y proporcionar comandos reproducibles.
4. Prohibido enviar documentos o texto extraído a modelos externos, herramientas de telemetría, endpoints cloud o servicios de terceros por defecto.
5. Prohibido registrar texto completo, RUT, correo, tokens, contraseñas o rutas confidenciales en logs comunes. Los errores deben registrar identificadores opacos y códigos sanitizados.
6. No implementar edición, eliminación o alteración de documentos de origen; **fuentes solo lectura**. Las acciones ARSOP se gestionarán por sistemas y responsables autorizados.
7. No agregar componentes innecesarios: preferir arquitectura modular desplegada en una sola máquina; dejar la posibilidad de múltiples workers/nodos como ampliación futura.
8. No inventar garantías de rendimiento, seguridad o cumplimiento legal. Mantener una matriz de controles implementados, probados y pendientes.
9. Versionar API, esquema SQL, contratos de extractores, reglas de detección y configuración. Aplicar migraciones controladas y compatibles.
10. Todo cambio debe incorporar código, pruebas, documentación mínima, migraciones (si aplican) y notas sobre amenazas o consecuencias operacionales.

**Definición de terminado:** código compila, tests automatizados pasan, configuración validada, API segura por defecto, despliegue reproducible, migraciones reversibles cuando sea seguro, métricas funcionales, errores observables, documentación actualizada y ningún control P0 pendiente.

## 20. Arquitectura implementable: separación y contratos

**Despliegue inicial:** una sola máquina Linux, Docker Compose, sin Kubernetes, sin dependencia SaaS.

| Proceso lógico | Implementación | Responsabilidades | No debe hacer |
|---|---|---|---|
| `scanner` | Go | CLI, inventario de árboles, scheduling, coordinación, checkpoints, carga, detección RUT/email, registro de progreso | Ejecutar OCR de todas las páginas indiscriminadamente |
| `extractor` | Python 3, servicio/worker local | Extraer texto y ubicaciones de documentos complejos; PyMuPDF, lectura Excel streaming, Word, formatos soportados | Exponer puerto al exterior ni registrar texto |
| `ocr-worker` | Python 3, proceso aislado | Preprocesar imágenes, OCR local y reportar texto estructurado/confianza | Descargar modelos en runtime sin aprobación |
| `postgres` | PostgreSQL | Catálogo, hallazgos, trabajos, auditoría y estado | Exponer puerto público |
| `queue` | Redis Streams o RabbitMQ (ADR) | Desacoplar trabajos con entrega recuperable | Ser única fuente de verdad de progreso |
| `api` | Go, chi | Autenticación, autorización, consultas, paginación, auditoría y límites | Leer directamente rutas arbitrarias pedidas por HTTP |
| `metrics` | Endpoint interno Prometheus | Métricas de salud, avance y recursos | Incorporar PII como etiquetas |

**Contrato de trabajo:** `job_id`, `tenant_id`, `scan_id`, `file_id`, `file_version`, `task_kind`, `attempt`, `deadline`, `trace_id` (opaco), `rule_set_version`. El contenido documental nunca se transmite en la cola. Los workers reciben identificadores y resuelven rutas únicamente desde una tabla de mounts autorizados.

**Contrato de extractor:** solicitud interna `{file_id, version, path_handle, parser_options}`; respuesta **en streaming o por fragmentos** `{unit_type, unit_index, text_chunk, coordinates?, extraction_method, confidence?, warnings[]}`. El texto existe solamente en memoria/acoplamiento local temporal de corta vida; no persiste completo. El motor de detección consume los fragmentos y escribe hallazgos por lotes. Los límites de fragmentos deben preservar una superposición configurable para evitar perder RUT/email separados por límites de bloque. Usar IPC seguro o red Docker interna con autenticación si el límite de confianza lo exige.

**Ciclo de archivo:** `discovered → fingerprinted → queued → processing → completed | partial | retry_wait | failed | skipped`; los estados deben sobrevivir reinicios. Los archivos cambiados durante el procesamiento deben marcarse `stale/retry` y no publicarse como resultado definitivo de esa versión.

**Regla de selección OCR por página:** texto digital confiable suficiente → no OCR; imagen/no texto → OCR; mixto/insuficiente → analizar regiones o página; dudas → clasificar como cobertura parcial y registrar motivo. No confundir OCR exitoso con certeza de que todos los datos fueron descubiertos.

## 21. Estructura del repositorio propuesta

```text
arsop-scanner/
├── README.md
├── LICENSE                         # elegir explícitamente
├── Makefile
├── .env.example                    # NUNCA secretos reales
├── go.mod / go.sum
├── pyproject.toml / uv.lock        # dependencias Python fijadas
├── cmd/
│   ├── scanner/                    # CLI (init, config, scan, status, resume, report)
│   └── api/                        # servidor API
├── internal/
│   ├── config/                     # validación estricta y carga segura
│   ├── catalog/                    # inventario y rutas autorizadas
│   ├── scheduler/                  # concurrencia, cuotas, prioridades
│   ├── jobs/                       # estados, lease, retry, idempotencia
│   ├── identity/                   # normalización RUT/email + HMAC
│   ├── detection/                  # reglas versionadas + puntuación
│   ├── persistence/                # repositorios SQL, transacciones
│   ├── api/                        # HTTP, middleware, RBAC/ABAC
│   ├── audit/                      # eventos sin PII
│   ├── metrics/                    # métricas sin etiquetas sensibles
│   └── security/                   # secretos, autorización, protección de rutas
├── workers/
│   ├── extractor/                  # Python, PDF / Excel / DOCX / CSV
│   └── ocr/                        # Python, Tesseract y OpenCV
├── migrations/                     # SQL versionado
├── api/openapi.yaml                # contrato OpenAPI 3.x
├── config/scanner.example.yaml
├── deploy/
│   ├── compose.yaml
│   ├── dockerfiles/
│   ├── seccomp/                    # si se requiere perfil específico
│   └── README-deploy.md
├── tests/
│   ├── fixtures/synthetic/         # SOLO datos ficticios
│   ├── unit/
│   ├── integration/
│   ├── security/
│   ├── resilience/
│   ├── ocr-quality/
│   └── performance/
├── benchmarks/
│   ├── methodology.md
│   └── results-template.md
└── docs/
    ├── architecture.md
    ├── threat-model.md
    ├── security-controls.md
    ├── data-model.md
    ├── ADR/
    ├── operations-runbook.md
    ├── incident-response.md
    ├── backup-restore.md
    ├── privacy-arsop.md
    ├── assumptions.md
    └── limitations.md
```

## 22. Especificación de modelos SQL y garantías transaccionales

Requerir **migraciones SQL versionadas**. Claves primarias UUID o ULID sin significado personal. Tiempos `timestamptz` en UTC; presentación local configurable.

- `tenants(id, name, created_at, status)`: por defecto 1 tenant por instalación; el `tenant_id` se mantiene para preparar evolución, pero **no** afirmar multi-tenant seguro sin pruebas.
- `sources(id, tenant_id, logical_name, root_path, enabled, mount_fingerprint, created_at)`: rutas raíz aprobadas, nunca una ruta introducida sin control desde API.
- `scans(id, tenant_id, source_id, type, status, started_at, completed_at, rules_version, scanner_version, coverage_summary)`: escaneo total/incremental.
- `files(id, tenant_id, source_id, relative_path, size_bytes, mtime, file_identity, mime_type, active, last_observed_at, last_success_version, state)`: unívoco por tenant+fuente+ruta normalizada. Tratar renombrados/hardlinks con política explícita.
- `file_versions(id, file_id, size_bytes, mtime, content_digest_nullable, observed_at, parser_version, state)`: resultados ligados a versión inequívoca.
- `identifiers(id, tenant_id, type, lookup_hmac, encrypted_value_nullable, created_at)`: `UNIQUE(tenant_id,type,lookup_hmac)`; no columna de RUT/email indexada en claro.
- `findings(id, tenant_id, identifier_id, file_version_id, detection_method, confidence, status, detector_version, created_at)`: deduplicación por clave de idempotencia apropiada.
- `finding_locations(id, finding_id, page_number_nullable, sheet_name_encrypted_nullable, row_number_nullable, column_ref_nullable, region_nullable)`: ubicaciones paginadas con límites y control de exposición.
- `file_tasks(id, file_version_id, kind, state, priority, attempt_count, available_at, lease_owner, lease_expires_at, last_error_code)`: reclamación atómica, reentrega después de lease vencido, DLQ si aplica.
- `scan_errors(id, scan_id, file_version_id_nullable, stage, error_code, sanitized_detail, first_seen_at, last_seen_at, attempts)`: sin texto extraído.
- `api_principals`, `roles`, `grants`, `audit_events`, `key_versions`, `schema_migrations`: permisos, auditoría y rotación.

**Índices mínimos:** `identifiers(tenant_id,type,lookup_hmac)`, `findings(tenant_id,identifier_id)`, `findings(file_version_id)`, `files(tenant_id,source_id,active)`, `file_tasks(state,available_at,priority)`, `scan_errors(scan_id,error_code)`; validar planes `EXPLAIN (ANALYZE, BUFFERS)` con datasets sintéticos realistas.

**Consistencia:**

- Un worker no debe crear hallazgos permanentes duplicados tras un crash; usar transacciones, `ON CONFLICT`, claves únicas e idempotencia.
- Escribir hallazgos por versión; al completar publicación, cambiar una referencia a la **última versión exitosa** de forma atómica. Versiones fallidas no reemplazan resultados válidos previos silenciosamente; exponer `stale`/antigüedad.
- Procesar borrados: cuando una exploración completa y válida confirma ausencia, desactivar o purgar relaciones conforme a política; un mount caído **nunca** debe interpretarse como borrado masivo.
- El conteo de documentos hallados no equivale al número de ocurrencias y ambos deben diferenciarse en la API.
- Retención y purga deben configurarse según políticas del responsable, incluida limpieza verificable de temporales, backups y logs conforme al plan autorizado.

## 23. Especificación concreta de detectores

### RUT/RUN chileno

1. Capturar candidatos con y sin separadores, longitud controlada, contexto opcional y límites de palabra.
2. Normalizar quitando puntos/espacios y unificando DV (`K` mayúscula), preservando la variante original únicamente de manera efímera o cifrada cuando sea indispensable.
3. Validar DV módulo 11; añadir casos de prueba conocidos positivos, negativos, `K`, ceros, formatos extraños y caracteres OCR confundibles.
4. Correcciones OCR (`S↔5`, `O↔0`, etc.) solo dentro de un conjunto limitado de hipótesis, con costo de edición, procedencia y umbral configurables; **nunca equiparar automáticamente una corrección incierta a coincidencia confirmada**.
5. Salida: tipo, versión del detector, confianza calibrada, indicador `validated_checksum`, origen `digital|ocr`, ubicación y justificación.

### Emails

Normalización conservadora: recortar espacios ajenos, normalizar dominio según reglas de dominio/IDNA, **no** eliminar puntos de la parte local ni aplicar reglas específicas de proveedores de correo, no cambiar la parte local a minúsculas ciegamente si puede alterar semántica; hacer coincidir bajo política explícita, documentada y testeada. Validar sintaxis sin efectuar consultas DNS ni tráfico saliente por defecto. Evaluar errores OCR como candidatos inciertos, no convertirlos silenciosamente en identidades definitivas.

### Extensibilidad

Contrato `Detector` con `name`, `version`, `supported_types`, `detect(chunks, context)`, `normalize()`, `validate()` y métricas. Nuevas categorías (teléfono, dirección, cuenta bancaria, nombres u otros datos sensibles) requieren análisis independiente de falsos positivos, permisos de búsqueda y evaluación de privacidad. No asumir que una regex aislada identifica de forma fiable a una persona.

## 24. API: contrato mínimo verificable

Base: `/api/v1`; JSON UTF-8; TLS en punto de entrada; autenticación obligatoria; prohibido `GET` con identificadores personales en query string en producción porque pueden quedar en logs y sistemas intermedios. **Preferir búsqueda POST** con cuerpo JSON y cabeceras `Cache-Control: no-store`.

| Método | Ruta | Propósito | Permiso |
|---|---|---|---|
| `POST` | `/identifiers/search` | Buscar por RUT/email normalizado; pagina resultados | `findings:search` |
| `GET` | `/files/{opaque_id}` | Metadatos autorizados por archivo | `files:read` |
| `GET` | `/scans/{opaque_id}` | Estado, avance y cobertura | `scans:read` |
| `POST` | `/scans` | Crear un escaneo sobre una `source_id` previamente autorizada | `scans:start` |
| `POST` | `/scans/{id}/pause` | Pausar nuevas tareas | `scans:manage` |
| `POST` | `/scans/{id}/resume` | Reanudar | `scans:manage` |
| `GET` | `/scans/{id}/errors` | Errores sanitizados y paginados | `scans:diagnose` |
| `GET` | `/health/live` | Vitalidad mínima sin detalles | configuración interna |
| `GET` | `/health/ready` | Dependencias y migraciones | configuración interna |

Ejemplo ilustrativo de búsqueda (el dato es ficticio):

```http
POST /api/v1/identifiers/search
Authorization: Bearer <TOKEN_REDACTED>
Content-Type: application/json
Cache-Control: no-store

{"type":"rut","value":"12.345.678-5","page_size":25}
```

```json
{
  "query_id": "opaque-query-id",
  "type": "rut",
  "result_count": 2,
  "coverage": {"scan_status": "completed", "last_completed_at": "2026-10-18T23:00:00Z"},
  "results": [
    {"file_id":"opaque-file-id-1","display_name":"contrato.pdf","location":{"page":4},"method":"digital","confidence":1.0},
    {"file_id":"opaque-file-id-2","display_name":"planilla.xlsx","location":{"sheet":"Hoja1","row":82,"column":"D"},"method":"digital","confidence":1.0}
  ],
  "next_cursor": null
}
```

Los nombres/rutas reales de archivos también pueden ser sensibles: aplicar enmascaramiento según permiso, y mantener rutas completas exclusivamente en endpoints privilegiados si están justificadas. Agregar `request_id`, `schema_version`, `as_of` y estados parciales en contrato real.

**Seguridad de la API:** OIDC/OAuth2 mediante identidad del cliente o mTLS/credenciales de máquina administradas; scopes y RBAC/ABAC, revisión del contexto de pertenencia a la organización por **cada objeto**; rate limits por principal y clase de endpoint, tamaño máximo de cuerpo, cursores opacos y expirables, límites máximos de página, timeouts, audit log sin datos personales, revocación de credenciales, rotación y protección contra enumeración masiva. No aceptar como autoridad el `tenant_id` enviado por el cliente. Usar un reverse proxy TLS y restringir interfaces de escucha; definir formalmente el modelo de autenticación en ADR antes de integrar un sistema externo.

## 25. Modelo de amenazas y controles P0

| Amenaza | Medida obligatoria | Prueba requerida |
|---|---|---|
| Exfiltración desde OCR/parser | Red externa bloqueada; workers aislados; modelos empaquetados | Ejecutar sin Internet y probar conexiones denegadas |
| Documento malicioso / zip bomb / PDF hostil | Límite de memoria, tiempo, páginas, descompresión, tamaño y profundidad; sandbox no-root | Fixtures hostiles y prueba de supervivencia |
| Escape de directorio / symlink | Raíces allowlist, resolución segura por descriptor, no seguir enlaces por defecto, verificar montaje | Prueba traversal y symlink fuera de raíz |
| Inyección SQL | Parámetros tipados, sin SQL concatenado | Pruebas de entradas maliciosas |
| BOLA/IDOR | Verificación de permisos por objeto, nunca confiar en IDs recibidos | Intentos cruzados denegados |
| Enumeración masiva de RUT | Permisos específicos, rate limit, auditoría, detección de anomalías | Pruebas de abuso y bloqueo |
| Robo de base de datos | Cifrado de valores almacenados y claves separadas, HMAC indexado, backups cifrados | Restauración con/sin claves y validación de no exposición |
| Secretos en logs/diagnósticos | Redacción y pruebas automáticas de logs | Scanner de logs y artefactos |
| Escalada de privilegios de contenedor | Usuario no-root, read-only FS, drop capabilities, no privileged, no docker.sock, seccomp, no-new-privileges | Inspección de contenedores y pruebas de escritura |
| DoS por OCR / cola | Límites por tarea, backpressure, cuotas CPU/RAM, DLQ | Soak test / retry storm |
| Pérdida de datos por caída NAS | Mount health + estados `unavailable`, reconciliación segura | Cortar montaje durante un scan |
| Cambios no autorizados de reglas | Config versionada y protegida, control administrativo y trazabilidad | Prueba de cambios y rollback |
| Compromiso de dependencias | Dependencias pinneadas, imágenes con digest, SBOM, escaneo CVEs, firmas y proceso de parcheo | Validación en CI |
| Filtración por backups/export | Cifrado, ACL, retención, borrado seguro, recuperación probada | Restore drill y revisión ACL |

Criterios de aislamiento: binds `:ro` de fuentes, sin `privileged`, sin `network_mode: host` salvo necesidad documentada y aprobada, DB/red interna no publicadas, seccomp/AppArmor/SELinux aplicados según host, límites `pids`, `memory`, CPU, ulimits, temporales acotados, sistema de archivos raíz de contenedores `read_only` donde sea viable, `tmpfs` con tamaño y opciones seguros para temporales. La seguridad de mounts SMB/NFS y del sistema operativo anfitrión también forma parte del modelo de amenazas y queda bajo responsabilidades contractualmente definidas.

**HMAC y cifrado:** usar claves independientes por cliente, no reutilizar claves para HMAC y cifrado; almacenar material secreto en gestor del cliente o archivos secretos con ACL estrictas, nunca `.env` comiteado ni imagen; documentar versiones de claves, proceso de rotación, disponibilidad, backup seguro y pérdida irrecuperable cuando corresponda. Usar criptografía de librerías reconocidas (AEAD como AES-GCM), sin algoritmos propios. Los índices HMAC permiten coincidencia exacta pero no impiden inferencias a través de la propia API; los controles de autorización siguen siendo obligatorios.

**Mínima exposición:** no almacenar texto completo, miniaturas ni copias de documentos de forma persistente por defecto. El buffer temporal del OCR debe borrarse al terminar, incluso tras excepción, y nunca incluirse en core dumps, informes de errores ni trazas. Configurar políticas de desactivación de core dumps y límites a volcados diagnósticos.

## 26. Rendimiento, benchmarks y capacidad

Meta comercial deseada: **hasta 14 días para el escaneo inicial** de ciertos clientes con millones de archivos. Es un objetivo condicionado; antes de comprometerlo medir `bytes/s`, `files/s`, `pages/s`, `ocr_pages/s`, consultas/s, `p50/p95/p99`, CPU, RAM, IOPS, red, tasa de retry y tamaño real de hallazgos.

Cálculo orientativo para 5 millones de archivos en 14 días continuos: `5_000_000 / (14 * 24 * 3600) ≈ 4.13 archivos/s`; con margen de capacidad del 30 %: `≈5.37 archivos/s`. **No basta** medir archivos/s: separar categorías por páginas, tamaño y OCR.

Método obligatorio:

1. Muestreo estratificado por formato, tamaño, número de páginas, origen NAS y digital/OCR, sin extraer información fuera del ambiente autorizado.
2. Construir corpus sintético para CI y corpus real controlado para benchmark local; publicar únicamente resultados agregados sin PII.
3. Medir modo secuencial y diversos niveles de concurrencia para hallar saturación de CPU, RAM, red, NAS y disco temporal; evitar asumir que más workers siempre mejora.
4. Implementar colas independientes de I/O, extracción y OCR, con backpressure y fairness para evitar starvation.
5. Usar caché de detección de cambios (`size + mtime + identidad`), con política de revalidación y hash cuando se amerite; no hacer hash global de todo el corpus como prerequisito universal.
6. Simular caída de red, archivo corrupto, suspensión forzada, reinicio host, falta de espacio y saturación prolongada.
7. Medir memoria máxima del mayor Excel/PDF soportado: procesamiento streaming, límites configurables y rutas de fallo claras.
8. Elaborar informe reproducible con hardware, sistema operativo, almacenamiento/red, versiones, configuración, matriz de tamaños y distribución de tipos.

**Dimensionamiento por instalación:** no fijar requisitos universales como 16 vCPU o 64 GB sin pruebas. Entregar sizing inicial por perfiles **a validar** (pequeño, medio, grande) y herramienta CLI `scanner benchmark` que estime capacidad sobre archivos autorizados, sin almacenar PII extraída.

## 27. CLI y configuración obligatorias

```bash
scanner version
scanner init
scanner config validate --file /etc/arsop/scanner.yaml
scanner sources list
scanner sources check
scanner scan start --source SOURCE_ID
scanner scan status --scan SCAN_ID --json
scanner scan pause --scan SCAN_ID
scanner scan resume --scan SCAN_ID
scanner scan report --scan SCAN_ID --format json
scanner benchmark --source SOURCE_ID --sample 1000
scanner doctor
```

No exponer secretos en flags de línea de comandos (pueden aparecer en historia de shell y lista de procesos). Validar esquema estricto: rechazo a claves desconocidas, rutas no permitidas, valores fuera de límites, UUID malformados y configuración insegura. Parámetros iniciales ejemplares (ajustables tras benchmark): `max_extract_workers=4`, `max_ocr_workers=2`, `max_inflight_files=64`, `max_file_bytes`, `max_pdf_pages`, `max_ocr_seconds_per_page`, `retry_max=3`, `resume_on_restart=true`, `follow_symlinks=false`, `network_access=false`, `file_mounts_read_only=true`.

**Instalación:** documentar usuario dedicado Linux, directorios de datos y claves con permisos mínimos, verificación previa de mounts, espacio libre, backup, firewall y sincronización NTP. No montar `/`, `/var/run/docker.sock`, credenciales SMB o directorios personales completos por defecto.

## 28. Plan de entrega por fases con puertas de calidad

### Fase 0 — Diseño y amenazas (sin producción)

- Crear repo, ADRs iniciales, diagrama de componentes, modelo de amenazas, matriz de requisitos `REQ-ID → código → prueba`, documentación de responsabilidades del cliente.
- Definir si queue inicial será PostgreSQL (`FOR UPDATE SKIP LOCKED`) o RabbitMQ/Redis; preferir menor complejidad verificable. No introducir Redis únicamente por costumbre.
- Aprobar el esquema de identidad y claves antes de persistir hallazgos reales.
- **Puerta:** arquitectura y superficie de ataque revisadas, `config validate` funcionando, CI inicial y fixtures sintéticos.

### Fase 1 — Vertical slice sin OCR

- Inventario seguro de una ruta, PDF digital, CSV/TXT, extractor Excel streaming, RUT/email, validación, PostgreSQL, búsqueda POST, estados durables, tests.
- **Puerta:** prueba end-to-end sobre árbol sintético, resultados exactos y deduplicados, reinicio sin pérdida, API deniega por defecto.

### Fase 2 — OCR y formatos ampliados

- PDF mixtos, imagen, DOCX, OCR Tesseract local, preprocesamiento limitado, metadatos de páginas/hojas, confianza y errores parciales.
- **Puerta:** conjunto etiquetado con recall/precision reportados por formato, test sin Internet, parser aislado, memoria acotada.

### Fase 3 — Resiliencia e incremental

- Cambios de versión de archivo, directorios temporalmente indisponibles, reintentos con backoff, dead-letter, cambio de reglas, borrados confirmados, auditoría.
- **Puerta:** chaos tests con kill/restart/mount outage, sin duplicados ni borrados masivos incorrectos.

### Fase 4 — Hardening y rendimiento

- Cifrado/rotación/backup restore, TLS y permisos por rol, límites API, escaneo de dependencias, SBOM, fuzzing, pruebas de abuso, tuning y benchmark representativo.
- **Puerta:** cero vulnerabilidades críticas o altas sin tratamiento aprobado, controles P0 probados, restore comprobado y reporte de capacidad con límites conocidos.

### Fase 5 — Piloto controlado

- Implementación en ambiente de cliente con fuentes de prueba o autorizado, aprobación de permisos, responsables identificados, observabilidad, runbooks y plan de respuesta a incidentes.
- **Puerta:** aprobación formal de seguridad, privacidad, legal y operaciones del cliente; informe de cobertura y errores; decisión de go/no-go.

## 29. Casos de prueba imprescindibles

| ID | Escenario | Resultado aceptable |
|---|---|---|
| SEC-001 | Consultar sin token | 401 y ninguna información de existencia |
| SEC-002 | Principal sin permiso `findings:search` | 403, auditoría sin valor de RUT |
| SEC-003 | Solicitar archivo de otro tenant o fuente prohibida | Denegación sin fuga de metadatos |
| SEC-004 | Path traversal `../../` o symlink fuera de raíz | Rechazado; no lectura externa |
| SEC-005 | Archivo con código hostil incrustado | Sin ejecución ni salida a Internet |
| SEC-006 | Contraseñas/PII en errores y traces | Ninguna fuga |
| SEC-007 | 1000 búsquedas de RUT secuenciales | Rate limits y alerta sin exfiltración |
| SEC-008 | Volumen de DB perdido/cifrado | Backup y restore con RPO/RTO documentados |
| REL-001 | `SIGKILL` al worker tras escribir resultados | Reanuda sin hallazgos duplicados |
| REL-002 | NAS desaparece durante escaneo | Estado `unavailable`; no borrado de catálogo |
| REL-003 | Archivo cambia durante OCR | Versión inconsistente no publicada como definitiva |
| REL-004 | PDF corrupto o cifrado sin clave | Error clasificado; resto avanza |
| REL-005 | Fallo OCR de una página | `partial`, localizaciones válidas conservadas según política |
| DOC-001 | RUT con DV correcto e incorrecto | Clasificación diferenciada; sin falsificación de valor |
| DOC-002 | RUT OCR `S/5` | Hipótesis incierta; umbral configurable |
| DOC-003 | Email con dominio IDN y local-part especial | Comportamiento documentado en tests |
| DOC-004 | PDF mixto páginas texto e imagen | OCR solo en páginas o regiones necesarias |
| DOC-005 | Excel de gran tamaño con hojas ocultas | Cobertura y memoria medidos |
| DOC-006 | Valores cruzando límites de chunks | Detector no los pierde |
| DATA-001 | Reprocesar misma versión | Sin duplicados |
| DATA-002 | Borrado confirmado y cambio de ruta | Datos obsoletos tratados según política |
| API-001 | Miles de resultados | Paginación acotada estable, sin respuestas ilimitadas |
| PERF-001 | Corpus representativo 24h+ | Sin fugas progresivas de memoria; tasa reportada |

Para cada test crear `fixture`, configuración, comando, aserciones y archivo de resultados. **No poner datos reales de personas en fixtures, capturas o reportes compartidos.**

## 30. Métricas de calidad, no funcionales y SLO internos

Proponer objetivos iniciales y calibrarlos en el piloto:

- Cobertura: `% archivos inventariados`, `% procesados`, `% parciales`, `% fallidos`, `% omitidos justificados`, `% páginas con OCR necesario y realizado`, separados por fuente/formato.
- Detección: `precision`, `recall` y `F1` por formato, calidad de imagen y tipo de dato; reportar intervalo de confianza y tamaño de muestra. Ningún resultado debe afirmarse como 100 % completo.
- Confiabilidad: cero pérdidas silenciosas ante reinicio; `retry exhaustion` visible; backlog medible.
- Seguridad: cero accesos no autenticados a PII; ningún dato personal bruto en logs; mínimo privilegio verificado.
- Escalabilidad: throughput medido con 1, 2, 4, 8... workers hasta saturación; porcentaje dedicado a OCR vs I/O.
- API: latencia p95/p99 por tamaño de resultado con índices reales; paginación y rate limits verificados.
- Recuperación: definir RPO, RTO, frecuencia de backup y política de restauración con el cliente antes de producción.

**Indicadores sugeridos de operaciones:** `scanner_files_processed_total`, `scanner_file_errors_total{stage,code}`, `scanner_queue_depth{kind}`, `scanner_ocr_pages_total`, `scanner_bytes_read_total{source_id}`, `scanner_task_duration_seconds{kind}`, `scanner_db_write_seconds`, `scanner_mount_available{source_id}`, `scanner_scan_coverage_ratio{scan_id}`. No usar `filename`, `full_path`, `rut` o `email` como etiquetas de métricas.

## 31. Límites del producto respecto de los derechos ARSOP

El inventario detecta **posibles ubicaciones de datos**; por sí solo no acredita que un RUT pertenezca al solicitante, no confirma legitimidad de una solicitud y no decide causales o excepciones legales. Diseñar interfaz para que el sistema gestor de derechos consuma los hallazgos, adjunte un `case_id` opaco y documente la revisión humana/autorización, pero no implementar operaciones destructivas sobre repositorios.

Distinguir claramente los derechos de **acceso, rectificación, supresión, oposición, portabilidad y bloqueo** tal como establece el marco legal aplicable; no asumir que todo dato personal localizado puede suprimirse o exportarse automáticamente. Cada institución debe validar procedencia, alcance, plazos, posibles excepciones, identidad y comunicaciones con responsables y asesores jurídicos. La ley entra en vigencia el 1 de diciembre de 2026; usar texto legal vigente a la fecha de despliegue y verificar nuevas normas e instrucciones aplicables.

El sistema debe etiquetar resultados como `potential_match`, `validated_format`, `requires_review` y, cuando corresponda, `confirmed_by_authorized_reviewer`; una coincidencia OCR no se transforma en vínculo identitario sin la verificación apropiada.

## 32. Entregables finales exigidos a la IA

1. Código Go y Python funcional con versiones de dependencias fijadas.
2. Dockerfiles multi-stage o minimalistas y `compose.yaml` seguro sin privilegios innecesarios.
3. Esquema SQL, migraciones, índices y estrategia de recuperación.
4. `api/openapi.yaml` y ejemplos seguros de consumo.
5. CLI implementada y ejemplos operacionales.
6. Configuración YAML validada por esquema y documentación de opciones.
7. Fixtures sintéticos, unit/integration/e2e/security/resilience/performance tests.
8. Manual de instalación Linux y mounts (SMB/NFS) administrados por el cliente.
9. Runbooks de escaneo, monitoreo, errores, renovación de claves, backups, restore, actualización y rollback.
10. Modelo de amenazas, matriz de controles y hallazgos de seguridad pendientes.
11. Informe de benchmark (condiciones, medidas, límites y estimación de 14 días por perfil).
12. Registro de decisiones ADR, limitaciones conocidas y backlog priorizado.

## 33. Primer mensaje recomendado para iniciar el desarrollo con la IA

Copiar **este archivo completo** al contexto del agente de programación y dar la siguiente instrucción:

> Toma el archivo `ESPECIFICACION_TECNICA_COMPLETA_SCANNER_ARSOP_21719.md` como fuente de requisitos. No comiences implementando todas las fases a la vez. Inicia por **Fase 0**, creando estructura del repositorio, ADRs, modelo de amenazas, matriz de trazabilidad y una configuración estrictamente validada. Luego entrega una propuesta concreta para la **Fase 1** con archivos y tests. Prioriza seguridad por defecto, lectura solo en mounts autorizados y la separación entre inventario, extracción, OCR, persistencia y API. No hagas llamadas a servicios externos con documentos. No inventes resultados de pruebas ni afirmes cumplimiento legal. Al terminar cada fase, resume código creado, comandos ejecutados, pruebas aprobadas, pruebas no ejecutadas, riesgos y próximo paso. Si una decisión está pendiente, documenta la opción conservadora y su efecto antes de avanzar.

## 34. Referencias técnicas y jurídicas a consultar durante la implementación

- Ley N.º 21.719 — Biblioteca del Congreso Nacional, texto con vigencia 01-12-2026: https://www.bcn.cl/leychile/navegar?idNorma=1209272&idVersion=2026-12-01
- OWASP API Security Top 10 (2023): https://api-security.owasp.org/editions/2023/en/0x11-t10/
- PostgreSQL, políticas de Row-Level Security: https://www.postgresql.org/docs/current/ddl-rowsecurity.html
- PostgreSQL, transacciones y concurrencia: https://www.postgresql.org/docs/current/mvcc.html
- Docker Compose / hardening: https://docs.docker.com/compose/ y https://docs.docker.com/engine/security/
- Tesseract OCR: https://tesseract-ocr.github.io/
- PyMuPDF: https://pymupdf.readthedocs.io/
- NIST SP 800-53 y guías de gestión de claves según el contexto aplicable: https://csrc.nist.gov/publications

**Cierre:** la definición de cumplimiento exige validación independiente legal, privacidad y ciberseguridad. El diseño persigue minimizar exposición, maximizar confiabilidad y ofrecer evidencia trazable, pero no constituye una garantía automática de conformidad ni de encontrar todos los datos personales existentes.
