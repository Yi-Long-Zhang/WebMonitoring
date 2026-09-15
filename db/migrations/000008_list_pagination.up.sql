-- Keyset-cursor pagination indexes for the list endpoints introduced with
-- ListOptions{limit, cursor}. Each list query orders by (tenant_id,
-- <sort_key> DESC, id DESC) and filters with a row-value cursor
-- (sort_key, id) < (cursor_sort_key, cursor_id). These indexes let Postgres
-- satisfy that ordering and filter without an explicit sort. Incidents sort by
-- last_seen_at; monitor_policies by updated_at; every other list by created_at.

CREATE INDEX IF NOT EXISTS tenants_keyset_idx
    ON tenants (created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS assets_keyset_idx
    ON assets (tenant_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS sites_keyset_idx
    ON sites (tenant_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS monitor_policies_keyset_idx
    ON monitor_policies (tenant_id, updated_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS baseline_versions_keyset_idx
    ON baseline_versions (tenant_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS detection_jobs_keyset_idx
    ON detection_jobs (tenant_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS evidence_keyset_idx
    ON evidence (tenant_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS incidents_keyset_idx
    ON incidents (tenant_id, last_seen_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS alert_deliveries_keyset_idx
    ON alert_deliveries (tenant_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS audit_events_keyset_idx
    ON audit_events (tenant_id, created_at DESC, id DESC);
