# Instalación del escáner

Hay tres formas de instalarlo en Linux:

- **A. Máquina virtual o servidor Ubuntu**, con un script automático. Requiere `sudo`. Es la forma recomendada para probar.
- **B. Docker**, la más simple: clonar y ejecutar `./arsop.sh instalar`. Requiere Docker, que en la práctica equivale a permisos de administrador.
- **C. Sin permisos de administrador**, ni `sudo` ni Docker. Todo queda en la carpeta del usuario.

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
un archivo que no se pudo leer entero. Ver [FUNCIONAMIENTO.md](FUNCIONAMIENTO.md).

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

## B. Docker (la más simple)

Solo necesitas **Docker** con el plugin Compose, en versión 26 o superior. No hace falta instalar Go, Python ni
PostgreSQL: todo va dentro de los contenedores.

```bash
git clone https://github.com/ccavieres2/scaner-linux-sin-intefaz.git
cd scaner-linux-sin-intefaz
./arsop.sh instalar
```

`./arsop.sh instalar` hace lo siguiente:
- construye las imágenes;
- genera las claves y el token, y los guarda en un volumen de Docker con permisos 0600, uno por servicio;
- crea `./local/scanner.yaml`;
- levanta PostgreSQL y la API, que queda en `127.0.0.1:8080`;
- muestra **el token de la API una sola vez**.

Pruébalo con los datos ficticios incluidos:
```bash
./arsop.sh scan start --source ejemplo
./arsop.sh scan report --scan <ID>
```

### Escanear tus carpetas
No hay que copiar nada: la carpeta se escanea donde está.

1. Agrega la ruta real de la carpeta en `sources:` de `./local/scanner.yaml`:
   ```yaml
     - {id: rrhh, root_path: /srv/archivos/rrhh}
   ```
2. Escanea:
   ```bash
   ./arsop.sh sources check
   ./arsop.sh scan start --source rrhh
   ./arsop.sh scan pending --scan <ID>
   ```

`./arsop.sh` monta automáticamente cada carpeta de `sources:` en **solo lectura** y con la misma ruta dentro del
contenedor. Si la ruta no existe, avisa antes de escanear.

- **Recursos compartidos (SMB/NFS):** también funcionan. Basta con que estén montados en el equipo, por ejemplo en
  `/mnt/rrhh`.
- **Rutas con espacios:** no se admiten. Usa un punto de montaje o un enlace sin espacios.
- **Carpeta `./entrada`:** sigue disponible si prefieres dejar ahí los archivos (`root_path: /data/input/<nombre>`),
  pero no es necesaria.

### Comandos de administración
| Comando | Qué hace |
|---|---|
| `./arsop.sh <comando del escáner>` | Ejecuta cualquier comando de [FUNCIONAMIENTO.md](FUNCIONAMIENTO.md), por ejemplo `scan report --scan ID`. |
| `./arsop.sh escaneos` | Lista los escaneos: fuente, ID, estado y fecha. |
| `./arsop.sh archivos [FUENTE]` | Lista cada archivo revisado, con su estado y cuántos RUT o emails se encontraron. |
| `./arsop.sh inspect --source F --path RUTA --caso REF` | Muestra los RUT y correos que encontró en un archivo, con su ubicación (enmascarados; `--completo` para verlos enteros). Con `--todos` en lugar de `--path`, revisa toda la fuente. |
| `./arsop.sh estado` | Muestra el estado de los contenedores. |
| `./arsop.sh detener` / `iniciar` | Detiene o vuelve a levantar todo. Los datos se conservan. |
| `./arsop.sh logs` | Muestra los logs de la API, que no contienen RUTs ni emails. |
| `./arsop.sh token-nuevo` | Reemplaza el token de la API, por ejemplo si se perdió. |
| `./arsop.sh actualizar` | Hace `git pull`, reconstruye y reinicia. |
| `./arsop.sh desinstalar` | Borra contenedores, base de datos y claves. Pide confirmación. |

Si no quieres usar `arsop.sh`, `docker compose up -d --build` hace lo mismo que `instalar`. En ese caso, el token se
ve con `docker compose logs setup`.

**Windows:** ejecuta `./arsop.sh` desde **Git Bash**, con Docker Desktop abierto.

---

## C. Sin permisos de administrador

Sirve cuando el cliente te da un usuario normal de Linux, sin `sudo` y sin Docker.

> Docker no es una alternativa en este caso: usar Docker exige ser administrador o pertenecer al grupo `docker`, que
> equivale a ser root. Docker o Podman "rootless" también necesitan que un administrador los prepare.

### Qué necesita la máquina
Linux x86_64 con `curl`, `tar`, `xz` y `python3`. Casi todas las distribuciones los traen. Si falta alguno, el
instalador lo indica y hay que pedírselo al cliente.

No necesita `git`, `make`, `python3-venv` ni PostgreSQL instalado.

**OCR:** para leer imágenes y PDF escaneados hace falta Tesseract (`tesseract-ocr` y `tesseract-ocr-spa`), y un
usuario sin administrador no puede instalarlo. Si el cliente ya lo tiene instalado, se usa automáticamente. Si no,
las imágenes quedan como `partial` (`image_needs_ocr`). La instalación con Docker (B) ya trae Tesseract incluido.

### Instalar
```bash
curl -fsSLO https://raw.githubusercontent.com/ccavieres2/scaner-linux-sin-intefaz/main/scripts/install-user.sh
bash install-user.sh
```
Al final muestra el token de la API una sola vez. Luego abre una terminal nueva, para que tome el `PATH`.

El instalador:
- descarga Go y PostgreSQL 16 dentro de `~/arsop`, y verifica su checksum;
- compila el escáner;
- crea el entorno Python;
- inicializa una base de datos propia, que escucha solo en `127.0.0.1:55432`;
- genera las claves en `~/arsop/secrets` con permisos 0600;
- crea `~/arsop/scanner.yaml`;
- deja la API corriendo en `127.0.0.1:8080`;
- registra el arranque automático con `crontab @reboot`.

Si el cliente entrega su **propia base PostgreSQL**, define estas variables antes de instalar:
```bash
export ARSOP_DB_HOST=10.0.0.20 ARSOP_DB_NAME=arsop ARSOP_DB_USER=arsop_scanner ARSOP_DB_PASSWORD='...'
bash install-user.sh
```

### Usar
Todos los comandos de [FUNCIONAMIENTO.md](FUNCIONAMIENTO.md) funcionan igual, sin `sudo -u arsop` y con
`--file ~/arsop/scanner.yaml`:
```bash
arsop-scanner sources check --file ~/arsop/scanner.yaml
arsop-scanner scan start --source ejemplo --file ~/arsop/scanner.yaml
arsopctl status        # estado de PostgreSQL y la API
arsopctl start | stop | restart
```

**Para escanear una carpeta del cliente,** agrégala en `sources:` de `~/arsop/scanner.yaml`. Tu usuario necesita
**permiso de lectura** sobre ella. Sin administrador no se puede montar un recurso compartido (SMB) por tu cuenta: el
cliente debe dejarlo montado, por ejemplo en `/mnt/rrhh`. Lo ideal es que tu usuario tenga **solo lectura**; si tiene
escritura, `sources check` lo advierte con `WARNING_writable`. Aun así, el escáner abre siempre los archivos en solo
lectura.

**Para actualizar,** vuelve a ejecutar `bash install-user.sh`. No toca las claves, la configuración ni los datos.

### Diferencias con la instalación con administrador
| | Con administrador (A) | Sin administrador (C) |
|---|---|---|
| Usuario que ejecuta | servicio dedicado `arsop`, sin login | tu propio usuario |
| Claves | `/etc/arsop/secrets` | `~/arsop/secrets`, legibles por tu usuario |
| API | servicio `systemd` endurecido | proceso en segundo plano (`arsopctl`) |
| Arranque automático | `systemd` | `crontab @reboot`, si el sistema lo permite |
| Montar SMB | sí | no, lo hace el cliente |

Para una prueba, o cuando el cliente no da más acceso, la opción C es suficiente. Para producción conviene la A, con
un usuario de servicio separado.

---

## Qué nunca se sube a GitHub
- Las claves y tokens: `/etc/arsop/secrets`, `deploy/secrets/`, `*.key`, `*.pem`.
- Las configuraciones reales: `deploy/config/`.
- Datos reales de personas.

El `.gitignore` ya los excluye. Igual, revisa `git status` antes de cada commit.

## Pendiente para producción
- Cifrado del disco (LUKS), respaldos de PostgreSQL y rotación de las claves HMAC/AES.
- Un pentest externo antes de exponer la API en la red del cliente.
- Medir la calidad del OCR con documentos reales del cliente (benchmark de la Fase 2).
