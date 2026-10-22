# Despliegue con Docker

La instalación con Docker está en [INSTALACION.md](../INSTALACION.md), sección B. En resumen: `./arsop.sh instalar`.

| Archivo | Para qué sirve |
|---|---|
| `../compose.yaml` | Define los servicios: `setup`, `postgres`, `migrate`, `api` y `scanner`. |
| `docker-setup.sh` | Corre en el servicio `setup`. Genera claves, token y configuración si faltan; nunca sobrescribe. |
| `dockerfiles/` | Las imágenes del escáner (Go + Python) y de la API (distroless). |

## Seguridad
- **Contenedores endurecidos:** sistema de archivos de solo lectura, `cap_drop: ALL`, `no-new-privileges` y sin core
  dumps.
- **Sin root:** el escáner corre con el UID 10001 y la API con el 65532. Solo `setup` corre como root, y solo un
  momento, para fijar el dueño de las claves.
- **Claves:** viven en el volumen `arsop_secrets`, con una carpeta 0700 por servicio y archivos 0600. Cada contenedor
  monta solo su carpeta.
- **Red:** PostgreSQL y el escáner están en una red interna, sin salida a Internet. La API publica solo en
  `127.0.0.1:8080`.
- **Fuentes:** se montan en solo lectura (`./entrada` o `ARSOP_INPUT`). En un NAS, usa además una cuenta de solo
  lectura, porque `:ro` en Docker no reemplaza los permisos del NAS.

## Antes de producción
- Pon los volúmenes en un disco cifrado (LUKS) y configura respaldos de `arsop_pgdata` y de `arsop_secrets`.
- Para exponer la API a un cliente, configura mTLS + token (`../docs/API-CLIENTE.md`) y aplica la lista de IPs
  permitidas en el firewall del host.
