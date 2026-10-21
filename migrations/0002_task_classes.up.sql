-- 0002: colas por clase de archivo y orden por tamaño.
-- class: light (TXT/CSV, Go streaming) | heavy (PDF/XLSX, worker Python) | ocr (Fase 2)
ALTER TABLE file_tasks
    ADD COLUMN class      text   NOT NULL DEFAULT 'light' CHECK (class IN ('light', 'heavy', 'ocr')),
    ADD COLUMN size_bytes bigint NOT NULL DEFAULT 0;

DROP INDEX IF EXISTS file_tasks_claim;
CREATE INDEX file_tasks_claim ON file_tasks (scan_id, class, state, priority, size_bytes);
