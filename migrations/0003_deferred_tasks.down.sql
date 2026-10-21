DROP INDEX IF EXISTS file_tasks_deferred;
UPDATE file_tasks SET state = 'pending' WHERE state = 'deferred';
ALTER TABLE file_tasks DROP COLUMN deferred_at;
ALTER TABLE file_tasks DROP CONSTRAINT file_tasks_state_check;
ALTER TABLE file_tasks ADD CONSTRAINT file_tasks_state_check
    CHECK (state IN ('pending', 'processing', 'completed', 'retry_wait', 'failed', 'skipped'));
