# ADR-0002 — Cola inicial en PostgreSQL (`FOR UPDATE SKIP LOCKED`)

- **Estado:** Aceptado (Fase 0), revisable tras benchmark de Fase 3.
- **Contexto:** spec §28 Fase 0 pide preferir menor complejidad verificable y no introducir Redis por costumbre.
- **Decisión:** la tabla `file_tasks` actúa como cola durable: reclamación atómica con
  `SELECT ... FOR UPDATE SKIP LOCKED`, `lease_owner`/`lease_expires_at`, reentrega al vencer lease,
  `attempt_count` con backoff y estado `failed` (DLQ lógica) al superar `retry_max`.
- **Consecuencias:** una sola fuente de verdad para progreso y cola; transacción única entre hallazgos y
  estado de tarea (idempotencia más simple). Riesgo: contención/WAL a decenas de millones de tareas →
  medir; si se satura, migrar a RabbitMQ manteniendo PostgreSQL como fuente de verdad.
- **Semántica:** *at-least-once*; idempotencia por `UNIQUE` y `ON CONFLICT DO NOTHING` en hallazgos.
