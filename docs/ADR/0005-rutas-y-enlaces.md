# ADR-0005 — Resolución segura de rutas; sin seguir enlaces simbólicos en v1

- **Estado:** Aceptado (Fase 0).
- **Decisión:**
  - Las raíces se definen solo en configuración (nunca por API), absolutas, canónicas, sin anidamiento
    y fuera de rutas del sistema (`/`, `/etc`, `/proc`, `/home`, `/var/lib/docker`, …).
  - `follow_symlinks: true` se rechaza en v1. El recorrido usa `openat2(RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_XDEV)`
    en Linux ≥ 5.6, con fallback `O_NOFOLLOW` + `Lstat` + verificación de `st_dev` por raíz.
  - No cruzar puntos de montaje distintos al de la raíz (`mount_fingerprint`).
  - Hardlinks: se indexan por ruta; `file_identity = (st_dev, st_ino)` permite detectar duplicados sin seguirlos fuera.
- **Consecuencias:** archivos accesibles solo vía symlink quedan fuera de cobertura y se reportan como `skipped(symlink)`.
