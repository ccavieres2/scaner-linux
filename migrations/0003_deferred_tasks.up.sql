-- 0003: archivos grandes diferidos (scanner.large_files = defer).
-- Una tarea 'deferred' no la reclama ningún worker: queda listada como
-- pendiente hasta que el operador ejecute `scanner scan process-pending`.
ALTER TABLE file_tasks DROP CONSTRAINT file_tasks_state_check;
ALTER TABLE file_tasks ADD CONSTRAINT file_tasks_state_check
    CHECK (state IN ('pending', 'processing', 'completed', 'retry_wait', 'failed', 'skipped', 'deferred'));

ALTER TABLE file_tasks ADD COLUMN deferred_at timestamptz;
CREATE INDEX file_tasks_deferred ON file_tasks (scan_id, size_bytes) WHERE state = 'deferred';
