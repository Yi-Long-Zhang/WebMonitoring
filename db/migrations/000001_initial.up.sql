CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TYPE user_role AS ENUM ('ADMIN', 'OPERATOR', 'AUDITOR');
CREATE TYPE verification_status AS ENUM ('PENDING', 'VERIFIED', 'REJECTED', 'SUSPENDED');
CREATE TYPE baseline_status AS ENUM ('CANDIDATE', 'APPROVED', 'REJECTED', 'RETIRED');
CREATE TYPE job_status AS ENUM ('QUEUED', 'RUNNING', 'SUCCEEDED', 'FAILED', 'TIMED_OUT', 'CANCELLED');
CREATE TYPE incident_status AS ENUM ('OPEN', 'ACKNOWLEDGED', 'INVESTIGATING', 'RESOLVED', 'CLOSED', 'FALSE_POSITIVE');
CREATE TYPE severity AS ENUM ('INFO', 'LOW', 'MEDIUM', 'HIGH', 'CRITICAL');

CREATE TABLE tenants (
    id uuid PRIMARY KEY,
    name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    username text NOT NULL,
    password_hash text NOT NULL,
    role user_role NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, username)
);

CREATE TABLE assets (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    site_id uuid,
    name text NOT NULL,
    url text NOT NULL,
    asset_type text NOT NULL,
    owner_id uuid NOT NULL REFERENCES users(id),
    enabled boolean NOT NULL DEFAULT false,
    verification_status verification_status NOT NULL DEFAULT 'PENDING',
    verification_token text,
    verification_issued_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, url)
);
CREATE INDEX assets_tenant_enabled_idx ON assets (tenant_id, enabled);

CREATE TABLE monitor_policies (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    asset_id uuid NOT NULL REFERENCES assets(id),
    version integer NOT NULL CHECK (version > 0),
    config jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, asset_id)
);

CREATE TABLE baseline_versions (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    asset_id uuid NOT NULL REFERENCES assets(id),
    version integer NOT NULL CHECK (version > 0),
    status baseline_status NOT NULL,
    snapshot jsonb NOT NULL,
    evidence_ids jsonb NOT NULL DEFAULT '[]',
    created_by text NOT NULL,
    approved_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    approved_at timestamptz,
    UNIQUE (tenant_id, asset_id, version)
);
CREATE UNIQUE INDEX one_approved_baseline_per_asset
    ON baseline_versions (tenant_id, asset_id)
    WHERE status = 'APPROVED';

CREATE TABLE detection_jobs (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    asset_id uuid NOT NULL REFERENCES assets(id),
    mode text NOT NULL CHECK (mode IN ('HTTP', 'BROWSER')),
    status job_status NOT NULL,
    policy_version integer NOT NULL,
    baseline_version integer,
    error_code text,
    error_message text,
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz
);
CREATE INDEX detection_jobs_schedule_idx ON detection_jobs (tenant_id, asset_id, mode, created_at DESC);

CREATE TABLE evidence (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    asset_id uuid NOT NULL REFERENCES assets(id),
    job_id uuid NOT NULL REFERENCES detection_jobs(id),
    kind text NOT NULL,
    media_type text NOT NULL,
    size_bytes bigint NOT NULL,
    sha256 char(64) NOT NULL,
    object_key text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    retain_until timestamptz,
    UNIQUE (tenant_id, object_key)
);

CREATE TABLE incidents (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    asset_id uuid NOT NULL REFERENCES assets(id),
    baseline_id uuid NOT NULL REFERENCES baseline_versions(id),
    baseline_version integer NOT NULL,
    fingerprint char(64) NOT NULL,
    severity severity NOT NULL,
    status incident_status NOT NULL,
    title text NOT NULL,
    signals jsonb NOT NULL,
    evidence_ids jsonb NOT NULL,
    occurrence_count integer NOT NULL DEFAULT 1,
    owner_id uuid,
    resolution text,
    created_at timestamptz NOT NULL DEFAULT now(),
    first_seen_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    resolved_at timestamptz
);
CREATE UNIQUE INDEX active_incident_dedup_idx
    ON incidents (tenant_id, asset_id, baseline_version, fingerprint)
    WHERE status NOT IN ('CLOSED', 'FALSE_POSITIVE');

CREATE TABLE alert_channels (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    name text NOT NULL,
    channel_type text NOT NULL CHECK (channel_type IN ('WEBHOOK', 'WECHAT_WORK')),
    url text NOT NULL,
    secret_ciphertext text,
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE audit_events (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    actor_id uuid,
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text NOT NULL,
    result text NOT NULL,
    trace_id text NOT NULL,
    details jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_events_tenant_created_idx ON audit_events (tenant_id, created_at DESC);

INSERT INTO tenants (id, name)
VALUES ('00000000-0000-4000-8000-000000000001', 'Default Tenant')
ON CONFLICT DO NOTHING;
