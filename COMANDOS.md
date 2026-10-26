# Comandos de ARSOP: referencia rápida

Todos los comandos se ejecutan desde la carpeta del proyecto (por ejemplo `~/scaner-linux-sin-intefaz`).
`<ID>` es el identificador del escaneo: lo muestra `./arsop.sh escaneos` y también aparece al iniciar un escaneo.

## Instalación y mantenimiento

| Comando | Para qué sirve |
|---|---|
| `./arsop.sh instalar` | Instala todo la primera vez. Muestra el token de la API **una sola vez**: guárdelo. |
| `./arsop.sh actualizar` | Descarga la última versión de GitHub y reinstala. Conserva los datos. |
| `./arsop.sh estado` | Muestra si los servicios están funcionando. |
| `./arsop.sh iniciar` / `./arsop.sh detener` | Enciende o apaga los servicios. |
| `./arsop.sh logs` | Muestra los registros de los servicios. |
| `./arsop.sh token-nuevo` | Genera un token nuevo para la API. El anterior deja de funcionar. |
| `./arsop.sh desinstalar` | Borra todo, **incluidos los resultados**. |

## Fuentes (carpetas)

| Comando | Para qué sirve |
|---|---|
| `./arsop.sh sources list` | Lista las carpetas configuradas. |
| `./arsop.sh sources check` | Verifica que el escáner puede leer cada carpeta. |
| `./arsop.sh config validate` | Revisa que `local/scanner.yaml` no tenga errores. |

## Escanear

| Comando | Para qué sirve |
|---|---|
| `./arsop.sh scan start --source pruebas` | Escaneo incremental: lee solo los archivos nuevos o modificados. |
| `./arsop.sh scan start --source pruebas --full` | Vuelve a leer todos los archivos. |
| `./arsop.sh scan pause --scan <ID>` | Pausa un escaneo en curso. |
| `./arsop.sh scan resume --scan <ID>` | Continúa un escaneo pausado o interrumpido. |
| `./arsop.sh scan pending --scan <ID>` | Lista los archivos de 1 MB o más que quedaron pendientes. |
| `./arsop.sh scan process-pending --scan <ID>` | Lee los archivos pendientes. |

Desde una terminal, el escaneo muestra una barra de progreso:

```
[#####################---------]  70%  7/10 archivos  9s  (2 pendientes por tamaño)
```

## Ver resultados

| Comando | Para qué sirve |
|---|---|
| `./arsop.sh escaneos` | Lista los escaneos: ID, fuente, estado y fecha. |
| `./arsop.sh archivos pruebas` | Lista los archivos revisados con su estado, cantidad de hallazgos y método (texto u OCR). |
| `./arsop.sh scan status --scan <ID>` | Resumen de un escaneo: archivos vistos, parciales, fallidos y omitidos. |
| `./arsop.sh scan report --scan <ID>` | Reporte JSON de cobertura y errores. |
| `./arsop.sh inspect --source pruebas --todos --caso REVISION` | RUT y correos de toda la fuente, enmascarados. Al final lista los archivos **sin hallazgos** y el motivo. |
| `./arsop.sh inspect --source pruebas --path carpeta/archivo.pdf --caso REVISION` | Lo mismo, pero de un solo archivo. |
| Agregar `--completo` al final de `inspect` | Muestra los valores completos, sin enmascarar. Cada consulta queda en la auditoría con el texto de `--caso`. |

> Ojo: es `./arsop.sh inspect ...`, **no** `./arsop.sh scan inspect ...`.

## Agregar una carpeta nueva

No hay que copiar nada: el escáner lee la carpeta donde está y solo en modo lectura.

1. Abra la configuración:
   ```
   nano local/scanner.yaml
   ```
2. En `sources:`, agregue una línea con un nombre corto y la **ruta completa**:
   ```yaml
   sources:
     - {id: pruebas, root_path: /home/kali/pruebas}
     - {id: rrhh,    root_path: /home/kali/Documentos/rrhh}   # nueva
   ```
   - `id`: una palabra sin espacios. Es el nombre que se usa en `--source`.
   - `root_path`: ruta absoluta, que empieza con `/`.
3. Verifique la configuración y el acceso a la carpeta:
   ```
   ./arsop.sh config validate
   ./arsop.sh sources check
   ```
4. Escanee la carpeta:
   ```
   ./arsop.sh scan start --source rrhh
   ```

`arsop.sh` monta automáticamente cada `root_path` en modo de solo lectura. No hay que modificar Docker.

## ¿Se vuelven a revisar los archivos ya escaneados?

En el escaneo normal (incremental), no. Para cada archivo, el escáner compara el tamaño, la fecha de modificación
y la identidad en disco con la última lectura correcta:

| Situación | Qué hace |
|---|---|
| Sin cambios | No lo abre y conserva los hallazgos anteriores. |
| Modificado o reemplazado | Lo vuelve a leer y actualiza los hallazgos. |
| Nuevo | Lo lee. |
| Falló la vez anterior | Lo intenta de nuevo. |
| Cambiaron las reglas de detección (tras `actualizar`) | Lo vuelve a leer. |

Por eso, si escanea dos veces seguidas sin cambios, la barra puede mostrar `100%  0/0 archivos`: no había nada
nuevo que leer. Para volver a leer todo, use `--full`.

## Flujo típico

```
./arsop.sh scan start --source pruebas                         # 1. escanear
./arsop.sh escaneos                                            # 2. ver el ID
./arsop.sh scan process-pending --scan <ID>                    # 3. leer los archivos grandes (si hay pendientes)
./arsop.sh inspect --source pruebas --todos --caso REVISION    # 4. ver los RUT y correos encontrados
```
