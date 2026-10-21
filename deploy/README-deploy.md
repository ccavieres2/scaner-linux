# Despliegue (referencia, Fase 1)

1. **Host**: Linux x86_64, kernel ≥ 5.6, Docker + Compose, disco de datos cifrado (LUKS), NTP, firewall.
2. **Fuentes**: el cliente monta SMB/NFS en `/data/input/<fuente>` con una cuenta de **solo lectura**
   (ej. `mount -t cifs -o ro,credentials=/root/.smb-cred,uid=10001 //nas/rrhh /data/input/rrhh`).
   `:ro` en Docker no reemplaza los permisos del NAS.
3. **Secretos**: `./make-dev-secrets.sh` (desarrollo). En producción, archivos del gestor del cliente con
   permisos 0600 y propietario igual al UID del contenedor (scanner 10001, api 65532), o mecanismo equivalente.
4. **Configuración**: revisar `deploy/config/scanner.yaml` (fuentes, límites). `container_bind_all: true` solo en contenedor.
5. **Arranque**:
   ```sh
   docker compose up -d postgres
   docker compose run --rm scanner init --file /etc/arsop/scanner.yaml
   docker compose up -d api
   docker compose run --rm scanner sources check --file /etc/arsop/scanner.yaml
   docker compose run --rm scanner scan start --source rrhh --file /etc/arsop/scanner.yaml
   ```
6. **Exposición**: la API publica solo en `127.0.0.1:8080`; poner un reverse proxy TLS controlado delante.
7. **Verificar** antes de producción: restauración de backup, reinicio en medio de un escaneo (`scan resume`), corte de mount.

Estado: el archivo compose es sintácticamente válido; las imágenes se construyeron y probaron de punta a punta (escaneo, pendientes y API con mTLS).
