# ADR-0004 — Despliegue en un host con Docker Compose y redes internas

- **Estado:** Aceptado (Fase 0).
- **Decisión:** una máquina Linux, Docker Compose, sin Kubernetes. Redes Docker `internal: true` para
  workers y BD; solo `api` publica un puerto ligado a `127.0.0.1` (o IP privada con TLS).
  Contenedores no-root, `read_only`, `cap_drop: [ALL]`, `no-new-privileges`, límites `pids/mem/cpu`, `tmpfs` acotado.
  Solo `extractor`/`ocr` montan fuentes, con `:ro`. Nunca `docker.sock`, `privileged` ni `network_mode: host`.
- **TLS a PostgreSQL:** `sslmode=disable` se permite **solo** para el servicio interno `postgres` o loopback
  (red Docker interna sin egress); cualquier BD externa exige `require`/`verify-*` (validado en config).
- **Consecuencias:** `:ro` no es frontera suficiente (spec §14): el cliente debe montar SMB/NFS con cuenta de solo lectura.
