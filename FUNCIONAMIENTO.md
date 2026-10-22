# Cómo funciona el escáner y todos sus comandos

Para instalarlo, ver [INSTALACION.md](INSTALACION.md).

## 1. Idea general

```
scanner.yaml (carpetas = "fuentes")
        │
   scan start ──► inventario (recorre la carpeta, solo lectura, sin seguir symlinks)
        │
        ├─ archivo < 1 MB ──► cola light (TXT, CSV) ───┐
        │                 └─► cola heavy (PDF, XLSX) ──┴─► workers ──► RUT / email
        │                                                    (cifrado en PostgreSQL)
        └─ archivo ≥ 1 MB ──► tabla de PENDIENTES ──► scan process-pending (cuando decidas)
        │
   scan report / scan pending ──► resultados
        │
   API (token o mTLS + token) ──► aplicación del cliente consulta "¿dónde aparece este RUT?"
```

- **Solo lectura:** el escáner nunca modifica, mueve ni borra tus archivos.
- **Un archivo por worker:** la base de datos impide que dos workers tomen el mismo archivo. Cada worker extrae el
  texto en un proceso aislado, con límite de memoria y sin red.
- **Datos protegidos:** el RUT o email encontrado se guarda como hash (HMAC, para buscarlo) y cifrado (AES), junto
  con su ubicación (archivo, página, hoja, fila). Nunca aparece en los logs.
- **Archivos problemáticos:** un PDF corrupto, con contraseña o ilegible queda registrado con su motivo (`corrupt`,
  `encrypted`…) y el escaneo continúa.
- **Estado final del escaneo:**
  - `completed`: se procesó todo;
  - `partial`: quedaron archivos pendientes o con error;
  - `failed`: el escaneo falló;
  - `unavailable`: la carpeta dejó de estar accesible.

## 2. Configuración (`/etc/arsop/scanner.yaml`)

Los campos más usados:

```yaml
sources:                         # carpetas a escanear
  - id: rrhh
    root_path: /data/input/rrhh  # ruta absoluta y en solo lectura
scanner:
  max_extract_workers: 4         # workers de la cola light (TXT, CSV)
  max_heavy_workers: 2           # workers de la cola heavy (PDF, XLSX)
  large_files: defer             # defer = archivos ≥ 1 MB a pendientes | last = procesarlos al final
api:
  bind: 127.0.0.1
  port: 8080
```

La plantilla completa, con todos los campos comentados, está en
[config/scanner.example.yaml](config/scanner.example.yaml). Después de cambiar algo, ejecuta siempre
`config validate`.

## 3. Comandos

Todos los comandos aceptan `--file RUTA`; por defecto usan `/etc/arsop/scanner.yaml`. En la VM se ejecutan con el
usuario de servicio:

```bash
sudo -u arsop arsop-scanner <comando> --file /etc/arsop/scanner.yaml
```
Con **Docker**, el mismo comando se ejecuta así, sin `--file`:
```bash
./arsop.sh <comando>
```
Por ejemplo, `./arsop.sh scan start --source ejemplo`.

### Configuración y preparación
| Comando | Qué hace |
|---|---|
| `arsop-scanner version` | Muestra la versión. |
| `arsop-scanner help` | Muestra la ayuda. |
| `arsop-scanner config validate` | Revisa la configuración y rechaza valores inseguros. |
| `arsop-scanner sources list` | Lista las carpetas configuradas. |
| `arsop-scanner sources check` | Verifica que cada carpeta exista y sea de solo lectura. |
| `arsop-scanner init` | Crea o actualiza las tablas y registra las fuentes. Se puede repetir sin problema. |
| `arsop-scanner token new` | Genera un token de API y su hash SHA-256. El hash va en `api_principals.yaml`. |

### Escaneo
| Comando | Qué hace |
|---|---|
| `scan start --source ID` | Escanea la fuente. Si no se indica otra cosa, es incremental: solo procesa los archivos nuevos o modificados. |
| `scan start --source ID --full` | Vuelve a procesar todos los archivos. |
| `scan status --scan ID [--json]` | Muestra el estado y el avance. |
| `scan report --scan ID` | Entrega un reporte JSON con archivos vistos, hallazgos, errores y pendientes. |
| `scan pause --scan ID` | Pausa el escaneo. |
| `scan resume --scan ID` | Retoma un escaneo pausado o cortado, por ejemplo después de reiniciar la máquina. |

### Archivos grandes (≥ 1 MB)
| Comando | Qué hace |
|---|---|
| `scan pending --scan ID [--json]` | Muestra la tabla de archivos pendientes, ordenada por tamaño. |
| `scan process-pending --scan ID` | Procesa todos los pendientes. |
| `scan process-pending --scan ID --max-bytes N` | Procesa solo los pendientes de hasta N bytes. |

Ejemplo de `scan pending`:
```
TAMAÑO      COLA    ARCHIVO
1.2 MB      light   nomina_grande.txt
1.9 MB      heavy   informe_grande.pdf
2 archivos pendientes (3.1 MB)
```

### Servicio de la API (VM)
```bash
sudo systemctl status  arsop-api
sudo systemctl restart arsop-api        # después de cambiar scanner.yaml o api_principals.yaml
sudo journalctl -u arsop-api -f         # logs (no contienen RUTs ni emails)
```

## 4. Flujo típico de un escaneo

```bash
F="--file /etc/arsop/scanner.yaml"
sudo -u arsop arsop-scanner config validate $F
sudo -u arsop arsop-scanner sources check $F
sudo -u arsop arsop-scanner scan start --source rrhh $F    # anota el ID que imprime
sudo -u arsop arsop-scanner scan report  --scan <ID> $F
sudo -u arsop arsop-scanner scan pending --scan <ID> $F
sudo -u arsop arsop-scanner scan process-pending --scan <ID> --max-bytes 10000000 $F   # primero los de hasta 10 MB
sudo -u arsop arsop-scanner scan process-pending --scan <ID> $F                        # luego el resto
sudo -u arsop arsop-scanner scan report  --scan <ID> $F
```

## 4b. Ver qué se escaneó (Docker)
```bash
./arsop.sh escaneos           # escaneos realizados
./arsop.sh archivos rrhh      # archivos revisados de la fuente rrhh
```
Ejemplo de `archivos`:
```
 fuente  |    archivo    | bytes |  estado   | motivo | identificadores | apariciones
 ejemplo | clientes.csv  |   385 | completed |        |               9 |           9
 ejemplo | contrato.pdf  |  1358 | partial   |        |               2 |           2
```
- `estado`: `completed` significa **leído entero**, no "encontró datos". Mira la columna `identificadores`: si es 0,
  el archivo se leyó, pero no se reconoció ningún RUT ni correo. En una foto, eso puede deberse a la calidad de la
  imagen. Revísalo con `./arsop.sh inspect`.
- Los demás estados son:
  - `partial`: revisado en parte (por ejemplo, el OCR de una página falló);
  - `failed`: no se pudo leer;
  - `skipped`: omitido (el motivo aparece en `motivo`, por ejemplo `encrypted`).
- `identificadores`: cuántos RUT o emails distintos se encontraron en el archivo.

### Ver qué encontró en un archivo
```bash
./arsop.sh inspect --source ejemplo --path escaneados/foto.jpg --caso REVISION-1
```
```
archivo: escaneados/foto.jpg (image, 45896 bytes)

TIPO  VALOR        UBICACIÓN  MÉTODO  CONFIANZA  ESTADO
rut   7.***.**1-6  página 1   ocr     0.80       requires_review
```
- **Cómo funciona:** vuelve a leer el archivo con los mismos extractores y el mismo OCR del escaneo, y muestra cada
  RUT y correo con su ubicación.
- **Por qué relee el archivo:** la base de datos guarda los RUT y correos solo como huella cifrada (HMAC), así que no
  se pueden listar desde ahí. Releer el archivo es lo que permite mostrarlos.
- **Valores enmascarados:** por defecto se ven así: `12.***.**8-5`, `n***@example.cl`. Para verlos enteros, agrega
  `--completo`.
- **`--caso` es obligatorio:** cada consulta queda en la auditoría (`audit_events`), con quién la hizo, el caso y
  cuántos resultados hubo. Nunca se registran los valores.
- **Qué se guarda:** nada. No se guarda el texto ni los valores.
- **`--path`:** es la ruta que aparece en `./arsop.sh archivos`.

**Para ver todo lo que encontró en una fuente completa,** usa `--todos` en vez de `--path`. Al final, además,
lista los archivos **sin hallazgos** con el motivo, para que ninguno quede oculto:

```
Archivos SIN hallazgos que mostrar (2):
  descargada.jpg    leído entero, sin RUT ni correos reconocidos (si es una foto, revise su calidad)
  foto_celular.jpg  PENDIENTE por tamaño (>= 1048576 bytes): ejecute scan process-pending --scan <ID>
```

Las fotos de celular suelen pesar más de 1 MB, así que quedan **pendientes** hasta que ejecutes
`./arsop.sh scan process-pending --scan <ID>`. Si quieres que se procesen en el mismo escaneo, configura
`large_files: last` en `local/scanner.yaml`.

Comando:
```bash
./arsop.sh inspect --source pruebas --todos --caso REVISION-1
```
Revisa uno por uno los archivos en los que el escaneo encontró algo y al final muestra un resumen, por ejemplo
`8 archivos revisados, 24 hallazgos`. Como relee cada archivo, incluido el OCR, puede tardar en fuentes grandes. El
máximo es de 5000 archivos por consulta. Para guardar el resultado en un archivo:
`./arsop.sh inspect --source pruebas --todos --caso REVISION-1 > revision.txt` (guárdalo en un lugar protegido,
porque contiene datos personales).

Para saber en qué archivos aparece un RUT o correo en particular, usa la API (sección 7).

## 5. Si algo se corta
- **Se reinició la máquina o se cortó el proceso:** ejecuta `scan resume --scan <ID>`. El escaneo sigue desde donde
  quedó, sin repetir los archivos ya procesados.
- **Se cayó el montaje de la carpeta:** el escaneo queda como `unavailable` con error `root_unavailable`. Vuelve a
  montar la carpeta y ejecuta `scan resume`.
- **Hay un PDF corrupto o con contraseña:** aparece en `scan report` con su motivo. No detiene el escaneo.

## 6. Tipos de archivo soportados
- TXT, CSV y XLSX.
- PDF con texto digital.

- **Con OCR (Fase 2):**
  - imágenes JPG, PNG, TIFF, WEBP, BMP y GIF, incluidos los TIFF de varias páginas y las fotos giradas de celular.
    El tipo se reconoce por el contenido, no por la extensión: una imagen WEBP llamada `.jpg` (algo común en
    imágenes descargadas) se lee igual.
  - **No soportado aún: HEIC/HEIF**, el formato de las fotos de iPhone, aunque el archivo se llame `.jpg`. Aparece
    como "formato no soportado (heic)". Para leerlo, conviértelo a JPG;
  - PDF escaneados;
  - PDF mixtos: una página con texto digital que además trae un escaneo pegado (una imagen que cubre al menos el 20 %
    de la página) también pasa por OCR.

### Cómo funciona el OCR
- **Motor:** usa **Tesseract** local, en español e inglés (`ocr_languages` en `scanner.yaml`). No se envía nada a
  Internet. La imagen va al OCR en memoria y no se guarda en disco.
- **Solo donde hace falta:** las páginas con texto digital no pasan por OCR, salvo que traigan un escaneo pegado.
- **Fotos e imágenes sueltas (por ejemplo, fotos de carnet):** si la imagen es chica, se amplía (×2 a ×4) y se le
  sube el contraste y la nitidez. Además se lee en **3 pasadas** con distinto preprocesamiento, porque cada pasada
  falla en caracteres distintos. Los hallazgos se combinan sin contarlos dos veces. Si se ve un número de RUT pero
  ninguna pasada lo leyó completo con un dígito verificador válido (lo típico con la compresión JPG/WEBP), se prueban
  2 variantes más. Las páginas de PDF usan una sola pasada.
- **Guion del dígito verificador:** en fotos, el guion suele leerse como `+`, `~` o una raya, o directamente se pierde
  (`80.000.0041`). En texto OCR, un RUT con puntos de miles y esos errores se acepta **solo si el dígito verificador es
  válido**, y siempre queda para revisión. En texto digital no se hace nada de esto.
- **Revisión humana:** los hallazgos por OCR quedan con `metodo = ocr`, menor confianza y estado `requires_review`.
  El OCR puede confundir caracteres (por ejemplo `5` por `S`), así que un hallazgo por OCR debe revisarlo una
  persona.
- **Límites:** cada página o imagen tiene un tiempo máximo (`max_ocr_seconds_per_page`, 60 s por defecto). Si se
  excede o el OCR falla, la página queda `partial` con el aviso `ocr_failed`. Las imágenes gigantes (más de 100
  megapíxeles) se rechazan como `image_too_large`.
- **Sin Tesseract instalado:** por ejemplo, en la instalación sin administrador. Las imágenes y páginas escaneadas
  quedan `partial` con el aviso `image_needs_ocr` o `page_needs_ocr`, igual que antes.
- **Tiempo por archivo:** un PDF escaneado de muchas páginas puede tardar. El tope por archivo es
  `max_extract_seconds_per_file` (300 s por defecto); súbelo si tienes escaneos de más de unas 80 páginas.

Los datos de ejemplo de Docker incluyen la carpeta `escaneados/`, con una imagen, una foto, un PDF escaneado y uno
mixto (todos ficticios), para comprobar el OCR.

## 7. API

La API solo **consulta** lo que ya se encontró; no escanea.

Endpoints:

| Endpoint | Permiso (scope) | Uso |
|---|---|---|
| `POST /api/v1/identifiers/search` | `findings:search` | ¿En qué archivos aparece este RUT o email? |
| `GET /api/v1/files/{id}` | `files:read` | Metadatos de un archivo. La ruta solo aparece con `files:names`. |
| `GET /api/v1/scans/{id}` | `scans:read` | Estado y avance de un escaneo. |
| `GET /api/v1/scans/{id}/pending` | `scans:read` | Archivos grandes pendientes. |
| `GET /api/v1/scans/{id}/errors` | `scans:diagnose` | Errores del escaneo, sin datos personales. |

Ejemplo, en la VM o a través del túnel SSH:
```bash
curl -s -X POST http://127.0.0.1:8080/api/v1/identifiers/search \
  -H "Authorization: Bearer <TOKEN>" -H "Content-Type: application/json" \
  -d '{"type":"rut","value":"12.345.678-5","case_reference":"CASO-2026-001"}'

curl -s http://127.0.0.1:8080/api/v1/scans/<ID>/pending -H "Authorization: Bearer <TOKEN>"
```

- `case_reference` es obligatorio. Toda consulta queda registrada en la tabla de auditoría.
- La búsqueda tiene un límite de consultas por minuto, para impedir que alguien pruebe RUTs uno por uno.
- Para dar acceso a la aplicación de un cliente, con certificado mTLS, token ligado al certificado, vencimiento e IPs
  permitidas, sigue [docs/API-CLIENTE.md](docs/API-CLIENTE.md). El contrato completo de la API está en
  [api/openapi.yaml](api/openapi.yaml).

## 8. Para desarrolladores
```bash
make build      # bin/scanner y bin/api
make test       # vet + tests (PostgreSQL embebido; necesita Python con workers/extractor/requirements.txt)
```
