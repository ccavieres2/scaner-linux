-- 0001_init: esquema inicial (spec §22). Requiere PostgreSQL >= 15.
-- Ninguna columna almacena RUT/email en claro.

CREATE TABLE tenants (
    id         uuid PRIMARY KEY,
    name       text NOT NULL UNIQUE,
    status     text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sources (
    id                uuid PRIMARY KEY,
    tenant_id         uuid NOT NULL REFERENCES tenants(id),
    logical_name      text NOT NULL,
    root_path         text NOT NULL,
    enabled           boolean NOT NULL DEFAULT true,
    mount_fingerprint text,
    created_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, logical_name)
);

CREATE TABLE scans (
    id                     uuid PRIMARY KEY,
    tenant_id              uuid NOT NULL REFERENCES tenants(id),
    source_id              uuid NOT NULL REFERENCES sources(id),
    type                   text NOT NULL CHECK (type IN ('full', 'incremental')),
    status                 text NOT NULL CHECK (status IN ('running', 'paused', 'completed', 'partial', 'failed', 'unavailable')),
    started_at             timestamptz NOT NULL DEFAULT now(),
    inventory_completed_at timestamptz,
    completed_at           timestamptz,
    rules_version          text NOT NULL,
    scanner_version        text NOT NULL,
    coverage_summary       jsonb
);
-- Un único escaneo activo por fuente.
CREATE UNIQUE INDEX scans_one_active_per_source ON scans (source_id) WHERE status IN ('running', 'paused');

CREATE TABLE files (
    id                   uuid PRIMARY KEY,
    tenant_id            uuid NOT NULL REFERENCES tenants(id),
    source_id            uuid NOT NULL REFERENCES sources(id),
    relative_path        text NOT NULL,
    size_bytes           bigint NOT NULL,
    mtime                timestamptz NOT NULL,
    file_identity        text NOT NULL,
    mime_type            text,
    active               boolean NOT NULL DEFAULT true,
    state                text NOT NULL CHECK (state IN ('discovered', 'queued', 'completed', 'partial', 'failed', 'skipped', 'stale')),
    skip_reason          text,
    last_observed_at     timestamptz NOT NULL,
    last_scan_id         uuid REFERENCES scans(id),
    last_success_version uuid,
    UNIQUE (tenant_id, source_id, relative_path)
);
CREATE INDEX files_tenant_source_active ON files (tenant_id, source_id, active);

CREATE TABLE file_versions (
    id             uuid PRIMARY KEY,
    file_id        uuid NOT NULL REFERENCES files(id),
    scan_id        uuid NOT NULL REFERENCES scans(id),
    size_bytes     bigint NOT NULL,
    mtime          timestamptz NOT NULL,
    file_identity  text NOT NULL,
    rules_version  text NOT NULL,
    content_digest bytea,
    observed_at    timestamptz NOT NULL DEFAULT now(),
    processed_at   timestamptz,
    state          text NOT NULL CHECK (state IN ('queued', 'processing', 'completed', 'partial', 'failed', 'stale', 'skipped')),
    UNIQUE (file_id, size_bytes, mtime, file_identity, rules_version)
);
ALTER TABLE files ADD CONSTRAINT files_last_success_fk FOREIGN KEY (last_success_version) REFERENCES file_versions(id);

CREATE TABLE identifiers (
    id              uuid PRIMARY KEY,
    tenant_id       uuid NOT NULL REFERENCES tenants(id),
    type            text NOT NULL CHECK (type IN ('rut', 'email')),
    key_version     integer NOT NULL,
    lookup_hmac     bytea NOT NULL,
    encrypted_value bytea, -- NULL por defecto (ADR-0003)
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, type, key_version, lookup_hmac)
);

CREATE TABLE findings (
    id               uuid PRIMARY KEY,
    tenant_id        uuid NOT NULL REFERENCES tenants(id),
    identifier_id    uuid NOT NULL REFERENCES identifiers(id),
    file_version_id  uuid NOT NULL REFERENCES file_versions(id),
    detection_method text NOT NULL CHECK (detection_method IN ('digital', 'ocr', 'hybrid')),
    confidence       real NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
    status           text NOT NULL CHECK (status IN ('validated_format', 'requires_review', 'confirmed_by_authorized_reviewer', 'rejected_by_reviewer')),
    detector_version text NOT NULL,
    occurrences      integer NOT NULL CHECK (occurrences > 0),
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (file_version_id, identifier_id, detector_version)
);
CREATE INDEX findings_tenant_identifier ON findings (tenant_id, identifier_id);
CREATE INDEX findings_file_version ON findings (file_version_id);

CREATE TABLE finding_locations (
    id                   bigserial PRIMARY KEY,
    finding_id           uuid NOT NULL REFERENCES findings(id) ON DELETE CASCADE,
    location_key         bytea NOT NULL,
    page_number          integer,
    sheet_name_encrypted bytea,
    row_number           integer,
    column_ref           text,
    line_number          integer,
    UNIQUE (finding_id, location_key)
);

CREATE TABLE file_tasks (
    id               uuid PRIMARY KEY,
    scan_id          uuid NOT NULL REFERENCES scans(id),
    file_version_id  uuid NOT NULL REFERENCES file_versions(id),
    kind             text NOT NULL CHECK (kind IN ('extract', 'ocr')),
    state            text NOT NULL CHECK (state IN ('pending', 'processing', 'completed', 'retry_wait', 'failed', 'skipped')),
    priority         integer NOT NULL DEFAULT 100,
    attempt_count    integer NOT NULL DEFAULT 0,
    available_at     timestamptz NOT NULL DEFAULT now(),
    lease_owner      text,
    lease_expires_at timestamptz,
    last_error_code  text,
    UNIQUE (file_version_id, kind)
);
CREATE INDEX file_tasks_claim ON file_tasks (scan_id, state, available_at, priority);

CREATE TABLE scan_errors (
    id            bigserial PRIMARY KEY,
    scan_id       uuid NOT NULL REFERENCES scans(id),
    file_id       uuid REFERENCES files(id),
    stage         text NOT NULL,
    error_code    text NOT NULL,
    attempts      integer NOT NULL DEFAULT 1,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE NULLS NOT DISTINCT (scan_id, file_id, stage, error_code)
);
CREATE INDEX scan_errors_scan_code ON scan_errors (scan_id, error_code);

CREATE TABLE key_versions (
    purpose    text NOT NULL CHECK (purpose IN ('hmac', 'encryption')),
    version    integer NOT NULL,
    status     text NOT NULL CHECK (status IN ('active', 'retiring', 'retired')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (purpose, version)
);

-- Auditoría append-only: sin valores de identificadores (spec §9.5).
CREATE TABLE audit_events (
    id             bigserial PRIMARY KEY,
    at             timestamptz NOT NULL DEFAULT now(),
    tenant_id      uuid REFERENCES tenants(id),
    principal_id   text NOT NULL,
    action         text NOT NULL,
    outcome        text NOT NULL,
    case_reference text,
    request_id     text,
    result_count   integer,
    details        jsonb
);
CREATE INDEX audit_events_principal_at ON audit_events (principal_id, at);

CREATE FUNCTION audit_events_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_events es append-only';
END $$;
CREATE TRIGGER audit_events_no_update BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_immutable();
