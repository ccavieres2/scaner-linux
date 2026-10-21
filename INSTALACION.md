# Instalación del escáner

Hay dos formas de instalarlo en Linux:

- **A. Máquina virtual o servidor Ubuntu**, con un script automático. Es la forma recomendada para probar.
- **B. Docker Compose.**

> Esto es una instalación de **prueba / Fase 1**. Antes de usarlo con datos reales de clientes, revisa
> [Pendiente para producción](#pendiente-para-producción).

---

## A. Máquina virtual o servidor Ubuntu (recomendado)

### 1. Requisitos
- **Ubuntu Server 24.04 LTS** con 2 CPU, 4 GB de RAM y 30 GB de disco.
- Al instalar Ubuntu, marca **"Install OpenSSH server"**.
- Red en modo **puente**, para que la VM tenga una IP de tu red. Para verla, ejecuta `ip a` dentro de la VM.
- Un usuario normal con `sudo`. No uses root.

### 2. Instalar
Conéctate a la VM por SSH (`ssh usuario@IP_VM`) y ejecuta:

```bash
sudo apt-get update && sudo apt-get install -y git curl
curl -fsSLO https://raw.githubusercontent.com/ccavieres2/scaner-linux-sin-intefaz/main/scripts/install-vm.sh
bash install-vm.sh https://github.com/ccavieres2/scaner-linux-sin-intefaz.git
```

Si el repositorio es **privado**, crea una clave SSH en la VM y agrégala en GitHub como *Deploy key* de solo lectura.
Luego instala así:
```bash
ssh-keygen -t ed25519 -C "vm-arsop"
cat ~/.ssh/id_ed25519.pub          # copiar en GitHub → Settings → Deploy keys
git clone git@github.com:ccavieres2/scaner-linux-sin-intefaz.git ~/arsop-src
bash ~/arsop-src/scripts/install-vm.sh
```

El script hace lo siguiente:

| Paso | Resultado |
|---|---|
| Instala Go 1.26.10, PostgreSQL 16, Python y sus librerías | dependencias listas |
| Clona el repositorio en `/opt/arsop/src` y compila | `/usr/local/bin/arsop-scanner` y `arsop-api` |
| Crea el usuario de servicio `arsop` | el escáner no corre como root |
| Genera claves aleatorias con permisos 0600 | `/etc/arsop/secrets/` |
| Crea la configuración | `/etc/arsop/scanner.yaml` |
| Copia datos **ficticios** de ejemplo | `/data/input/ejemplo` |
| Inicializa la base de datos y crea el servicio de la API | `systemctl status arsop-api` |
| Muestra **una sola vez** el token de la API | guárdalo en un gestor de contraseñas |

### 3. Comprobar que funciona
```bash
sudo -u arsop arsop-scanner config validate --file /etc/arsop/scanner.yaml
sudo -u arsop arsop-scanner sources check   --file /etc/arsop/scanner.yaml
sudo -u arsop arsop-scanner scan start --source ejemplo --file /etc/arsop/scanner.yaml
```
El último comando imprime `escaneo <ID> iniciado … finalizado: partial`.

El estado `partial` es normal con los datos de ejemplo. Puede deberse a archivos grandes que quedaron pendientes o a
una página que necesita OCR (Fase 2). Ver [FUNCIONAMIENTO.md](FUNCIONAMIENTO.md).

### 4. Agregar tu propia carpeta
1. Deja la carpeta disponible en la VM **en solo lectura**.

   **Opción 1: carpeta local.**
   ```bash
   sudo mkdir -p /data/input/rrhh
   sudo cp -r /ruta/origen/* /data/input/rrhh/
   sudo chown -R root:arsop /data/input/rrhh
   sudo chmod -R a-w,g+rX /data/input/rrhh   # solo lectura para arsop
   ```

   **Opción 2: carpeta compartida de Windows (SMB).**
   ```bash
   sudo apt-get install -y cifs-utils
   sudo mkdir -p /data/input/rrhh
   sudo mount -t cifs -o ro,username=USUARIO_WINDOWS,uid=$(id -u arsop),gid=$(id -g arsop) \
        //IP_WINDOWS/Compartida /data/input/rrhh
   ```

2. Regístrala en `/etc/arsop/scanner.yaml`, dentro de `sources:`:
   ```bash
   sudo nano /etc/arsop/scanner.yaml
   ```
   ```yaml
   sources:
     - id: rrhh
       root_path: /data/input/rrhh
   ```

3. Aplica el cambio:
   ```bash
   sudo -u arsop arsop-scanner config validate --file /etc/arsop/scanner.yaml
   sudo -u arsop arsop-scanner init            --file /etc/arsop/scanner.yaml
   sudo -u arsop arsop-scanner sources check   --file /etc/arsop/scanner.yaml
   ```

### 5. Actualizar a la última versión de GitHub
```bash
bash /opt/arsop/src/scripts/install-vm.sh
```
El script hace `git pull` y recompila. No toca las claves ni la configuración.

### 6. Usar la API desde tu PC
La API escucha solo en `127.0.0.1:8080` de la VM. Para usarla desde Windows, abre un túnel SSH:
```powershell
ssh -L 8080:127.0.0.1:8080 usuario@IP_VM
```
Luego, en otra terminal, usa `http://127.0.0.1:8080`. Los ejemplos están en [FUNCIONAMIENTO.md](FUNCIONAMIENTO.md#7-api).

Para exponer la API a la aplicación de un cliente, con certificado, token e IP permitida, sigue
[docs/API-CLIENTE.md](docs/API-CLIENTE.md).

---

## B. Docker Compose

Requisitos: Linux con Docker y el plugin Compose.

```bash
git clone https://github.com/ccavieres2/scaner-linux-sin-intefaz.git
cd scaner-linux-sin-intefaz/deploy
sh make-dev-secrets.sh            # claves de desarrollo en deploy/secrets (no se suben a GitHub)
nano config/scanner.yaml          # revisar fuentes y límites
```

Monta la carpeta que vas a escanear en el host, en `/data/input/<fuente>` y en solo lectura.

Los contenedores corren como el UID 10001 (escáner) y el 65532 (API). Los archivos de `deploy/secrets` deben pertenecer
a esos usuarios y tener permisos 0600:
```bash
sudo chown 10001 secrets/db_password secrets/hmac_key secrets/encryption_key
sudo chown 65532 secrets/api_principals.yaml
```

Arranque:
```bash
docker compose build
docker compose up -d postgres
docker compose run --rm scanner init          --file /etc/arsop/scanner.yaml
docker compose up -d api
docker compose run --rm scanner sources check --file /etc/arsop/scanner.yaml
docker compose run --rm scanner scan start --source rrhh --file /etc/arsop/scanner.yaml
```

Con Docker, todos los comandos de [FUNCIONAMIENTO.md](FUNCIONAMIENTO.md) se ejecutan como
`docker compose run --rm scanner <comando> --file /etc/arsop/scanner.yaml`.

---

## Qué nunca se sube a GitHub
- Las claves y tokens: `/etc/arsop/secrets`, `deploy/secrets/`, `*.key`, `*.pem`.
- Las configuraciones reales: `deploy/config/`.
- Datos reales de personas.

El `.gitignore` ya los excluye. Igual, revisa `git status` antes de cada commit.

## Pendiente para producción
- Cifrado del disco (LUKS), respaldos de PostgreSQL y rotación de las claves HMAC/AES.
- Un pentest externo antes de exponer la API en la red del cliente.
- OCR de PDFs escaneados (Fase 2).
