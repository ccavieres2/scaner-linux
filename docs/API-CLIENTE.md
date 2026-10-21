# Exponer la API a la aplicación de un cliente de forma segura

La API entrega **dónde** aparece un RUT o email (archivo, página, hoja, fila). Eso ya es información sensible, así
que se protege con varias capas. Cada capa se aplica aunque las demás fallen.

| Capa | Qué evita | Cómo se configura |
|---|---|---|
| 1. Red | que alguien fuera de la red del cliente llegue al puerto | `api.bind` en IP privada + firewall + `api.allowed_cidrs` |
| 2. TLS | que se lea o altere el tráfico | `api.tls.cert_file` / `key_file` (TLS 1.2+, solo suites AEAD) |
| 3. Certificado de cliente (mTLS) | conexiones de cualquier equipo sin el certificado emitido por usted | `api.auth.mode: mtls_token` + `api.tls.client_ca_file` |
| 4. Token ligado al certificado | uso de un token robado desde otro equipo | `client_cert_sha256` en `api_principals.yaml` |
| 5. Vencimiento | credenciales olvidadas que siguen vigentes | `expires_at` (la API avisa en el log 14 días antes) |
| 6. Permisos mínimos (scopes) | que la app vea más de lo necesario | `scopes` por principal; sin `files:names` no ve nombres de archivo |
| 7. Límite de consultas | enumerar RUTs por fuerza bruta | `api.search_rate_per_minute`, `api.search_burst` |
| 8. Auditoría | consultas sin justificación | `case_reference` obligatorio; tabla `audit_events` append-only |

La configuración **rechaza** escuchar fuera de loopback sin TLS ni `mtls_token`, escuchar en `0.0.0.0`, usar IPs
públicas y abrir `allowed_cidrs` a `0.0.0.0/0`.

## Paso a paso (VM o servidor Linux)

Supongamos que la API escucha en `10.20.0.10` y que la aplicación del cliente corre en `10.20.0.15`.

```bash
# 1) CA, certificado del servidor y certificado del cliente (en el servidor de la API)
sudo bash scripts/make-api-certs.sh ca     /etc/arsop/tls
sudo bash scripts/make-api-certs.sh server /etc/arsop/tls 10.20.0.10
sudo bash scripts/make-api-certs.sh client /etc/arsop/tls app-cliente 365
sudo chown -R root:arsop /etc/arsop/tls && sudo chmod 0750 /etc/arsop/tls && sudo chmod 0640 /etc/arsop/tls/*
sudo chmod 0600 /etc/arsop/tls/ca.key   # la clave de la CA no la necesita la API
```

El último comando imprime la línea para `/etc/arsop/secrets/api_principals.yaml`. Por ejemplo:

```yaml
principals:
  - {id: app-cliente, token_sha256: <hash>, client_cert_sha256: <huella>, expires_at: "2027-10-21",
     scopes: [findings:search, files:read, scans:read]}
```

```yaml
# 2) /etc/arsop/scanner.yaml
api:
  bind: 10.20.0.10
  port: 8443
  allowed_cidrs: [10.20.0.15/32]
  auth:
    mode: mtls_token
    principals_file: /etc/arsop/secrets/api_principals.yaml
  tls:
    cert_file: /etc/arsop/tls/api.crt
    key_file: /etc/arsop/tls/api.key
    client_ca_file: /etc/arsop/tls/client-ca.crt
```

```bash
# 3) Firewall: solo la app del cliente llega al puerto
sudo ufw default deny incoming
sudo ufw allow from 10.20.0.15 to 10.20.0.10 port 8443 proto tcp
sudo ufw allow OpenSSH && sudo ufw enable

# 4) Validar y reiniciar
sudo -u arsop arsop-scanner config validate --file /etc/arsop/scanner.yaml
sudo systemctl restart arsop-api
```

## Qué recibe el cliente (por canales separados)

1. `app-cliente.crt` + `app-cliente.key`: en un archivo cifrado o un medio físico.
2. El token: por un gestor de contraseñas o una llamada. **Nunca en el mismo correo que el certificado.**
3. `ca.crt`: para que su aplicación verifique que habla con su API y no con un impostor.

Llamada desde la aplicación del cliente:

```bash
curl --cacert ca.crt --cert app-cliente.crt --key app-cliente.key \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"type":"rut","value":"12.345.678-5","case_reference":"CASO-2026-001"}' \
  https://10.20.0.10:8443/api/v1/identifiers/search
```

Endpoints: `POST /api/v1/identifiers/search`, `GET /api/v1/files/{id}`, `GET /api/v1/scans/{id}`,
`GET /api/v1/scans/{id}/pending` (archivos grandes pendientes) y `GET /api/v1/scans/{id}/errors`.

## Rotación y revocación

- **Rotar** (antes de `expires_at`): emita un certificado nuevo con otro id (`app-cliente-2027`). Agregue su línea al
  archivo de principals junto a la anterior y reinicie la API. Cuando el cliente confirme el cambio, borre la línea
  vieja y reinicie de nuevo.
- **Revocar ya** (equipo o token comprometido): borre la línea del principal y reinicie la API. El certificado deja de
  servir aunque siga siendo válido para la CA, porque cada token exige la huella exacta de su certificado.
- **Si se compromete la CA:** genere una CA nueva y vuelva a emitir todos los certificados.

## Docker

Con Docker Compose, la API publica su puerto en `api.bind` del host. `allowed_cidrs` evalúa la IP que ve el
contenedor, y con el proxy de Docker esa IP puede ser la del gateway en vez de la del cliente. Por eso, en Docker,
la restricción por IP se hace en el **firewall del host** (paso 3); mTLS y el token funcionan igual. Monte
`/etc/arsop/tls` en solo lectura en el servicio `api`.

## Lo que todavía falta para producción

- OIDC o un IdP corporativo, si el cliente lo exige (ADR-0006). mTLS + token es adecuado para integrar una sola
  aplicación servidor a servidor.
- Cifrado del disco (LUKS), respaldos de PostgreSQL y rotación de las claves HMAC/AES (Fase 4).
- Pentest externo antes de exponerla a la red del cliente.
