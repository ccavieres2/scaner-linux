-- Reversión de 0001_init. DESTRUYE todos los hallazgos y la auditoría:
-- ejecutar solo en desarrollo o con aprobación y respaldo verificado.
DROP TABLE IF EXISTS audit_events, key_versions, scan_errors, file_tasks, finding_locations,
    findings, identifiers CASCADE;
ALTER TABLE IF EXISTS files DROP CONSTRAINT IF EXISTS files_last_success_fk;
DROP TABLE IF EXISTS file_versions, files, scans, sources, tenants CASCADE;
DROP FUNCTION IF EXISTS audit_events_immutable();
