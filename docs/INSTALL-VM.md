# Instalación de prueba en una VM Linux desde GitHub

Para probar en una máquina virtual. **No es una instalación de producción.**

## 1. Crear la VM
- VirtualBox o Hyper-V, con **Ubuntu Server 24.04 LTS**: 2 CPU, 4 GB RAM, 30 GB de disco.
- Durante la instalación, marcar **"Install OpenSSH server"**.
- Red en modo puente para obtener una IP de la red local (`ip a` dentro de la VM).

## 2. Instalar (dentro de la VM, por SSH)

Repositorio **público**:
```bash
curl -fsSLO https://raw.githubusercontent.com/ccavieres2/scaner-linux-sin-intefaz/main/scripts/install-vm.sh
bash install-vm.sh https://github.com/ccavieres2/scaner-linux-sin-intefaz.git
```

Repositorio **privado**: primero dé acceso a la VM y luego clone.
```bash
ssh-keygen -t ed25519 -C "vm-arsop"        # agregar ~/.ssh/id_ed25519.pub como Deploy key (solo lectura) en GitHub
git clone git@github.com:ccavieres2/scaner-linux-sin-intefaz.git ~/arsop-src
bash ~/arsop-src/scripts/install-vm.sh
```

El script:
- instala Go 1.26.10, PostgreSQL y el entorno Python;
- clona el repositorio en `/opt/arsop/src` y compila;
- genera secretos aleatorios con permisos 0600 en `/etc/arsop/secrets` y crea la configuración `/etc/arsop/scanner.yaml`;
- copia los datos ficticios de ejemplo en `/data/input/ejemplo`;
- inicializa la base de datos y deja la API como servicio `systemd` (solo `127.0.0.1:8080`);
- muestra **una vez** el token de la API.

## 3. Probar
```bash
sudo -u arsop arsop-scanner scan start --source ejemplo --file /etc/arsop/scanner.yaml
sudo -u arsop arsop-scanner scan report --scan <ID> --file /etc/arsop/scanner.yaml
curl -s -X POST http://127.0.0.1:8080/api/v1/identifiers/search \
  -H "Authorization: Bearer <TOKEN>" -H "Content-Type: application/json" \
  -d '{"type":"rut","value":"12.345.678-5","case_reference":"PRUEBA-1"}'
```
Que el escaneo termine en `partial` es lo esperado: una página del PDF de ejemplo requiere OCR (Fase 2).

Para consultar la API desde Windows, abrir un túnel: `ssh -L 8080:127.0.0.1:8080 usuario@IP_VM`.

## 4. Actualizar a la última versión de GitHub
```bash
bash /opt/arsop/src/scripts/install-vm.sh
```
El script hace `git pull` y recompila sin tocar los secretos ni la configuración.

## 5. Escanear una carpeta compartida de Windows
```bash
sudo mkdir -p /data/input/compartida
sudo mount -t cifs -o ro,username=USUARIO_WINDOWS,uid=$(id -u arsop),gid=$(id -g arsop) //IP_WINDOWS/Compartida /data/input/compartida
sudo nano /etc/arsop/scanner.yaml   # agregar en sources:  - {id: compartida, root_path: /data/input/compartida}
sudo -u arsop arsop-scanner init --file /etc/arsop/scanner.yaml
sudo -u arsop arsop-scanner scan start --source compartida --file /etc/arsop/scanner.yaml
```

## Qué nunca debe subirse a GitHub
Secretos (`/etc/arsop/secrets`, `deploy/secrets/`), configuraciones reales (`deploy/config/`), tokens, datos reales
de personas. El `.gitignore` los excluye; revisar `git status` antes de cada commit.
