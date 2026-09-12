ALTER TABLE assets
    ADD COLUMN IF NOT EXISTS verification_token_hash char(64),
    ADD COLUMN IF NOT EXISTS verification_expires_at timestamptz;

ALTER TABLE incidents
    ADD COLUMN IF NOT EXISTS acknowledged_at timestamptz;

CREATE TABLE IF NOT EXISTS alert_deliveries (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    incident_id uuid NOT NULL REFERENCES incidents(id),
    channel_id uuid NOT NULL REFERENCES alert_channels(id),
    dedup_key text NOT NULL,
    status text NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, dedup_key)
);

CREATE INDEX IF NOT EXISTS alert_deliveries_incident_idx
    ON alert_deliveries (tenant_id, incident_id, created_at DESC);

ALTER TABLE detection_jobs
    ADD COLUMN IF NOT EXISTS retry_count integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS max_attempts integer NOT NULL DEFAULT 3;
