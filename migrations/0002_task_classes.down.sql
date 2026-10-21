DROP INDEX IF EXISTS file_tasks_claim;
ALTER TABLE file_tasks DROP COLUMN IF EXISTS class, DROP COLUMN IF EXISTS size_bytes;
CREATE INDEX file_tasks_claim ON file_tasks (scan_id, state, available_at, priority);
