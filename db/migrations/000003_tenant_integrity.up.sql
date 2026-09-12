-- Enforce that tenant-scoped foreign keys cannot point across tenants.
CREATE UNIQUE INDEX IF NOT EXISTS users_tenant_id_uidx ON users (tenant_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS assets_tenant_id_uidx ON assets (tenant_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS baseline_versions_tenant_id_uidx ON baseline_versions (tenant_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS detection_jobs_tenant_id_uidx ON detection_jobs (tenant_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS incidents_tenant_id_uidx ON incidents (tenant_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS alert_channels_tenant_id_uidx ON alert_channels (tenant_id, id);

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'assets_owner_same_tenant_fk') THEN
        ALTER TABLE assets ADD CONSTRAINT assets_owner_same_tenant_fk
            FOREIGN KEY (tenant_id, owner_id) REFERENCES users (tenant_id, id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'monitor_policies_asset_same_tenant_fk') THEN
        ALTER TABLE monitor_policies ADD CONSTRAINT monitor_policies_asset_same_tenant_fk
            FOREIGN KEY (tenant_id, asset_id) REFERENCES assets (tenant_id, id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'baseline_versions_asset_same_tenant_fk') THEN
        ALTER TABLE baseline_versions ADD CONSTRAINT baseline_versions_asset_same_tenant_fk
            FOREIGN KEY (tenant_id, asset_id) REFERENCES assets (tenant_id, id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'detection_jobs_asset_same_tenant_fk') THEN
        ALTER TABLE detection_jobs ADD CONSTRAINT detection_jobs_asset_same_tenant_fk
            FOREIGN KEY (tenant_id, asset_id) REFERENCES assets (tenant_id, id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'evidence_asset_same_tenant_fk') THEN
        ALTER TABLE evidence ADD CONSTRAINT evidence_asset_same_tenant_fk
            FOREIGN KEY (tenant_id, asset_id) REFERENCES assets (tenant_id, id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'evidence_job_same_tenant_fk') THEN
        ALTER TABLE evidence ADD CONSTRAINT evidence_job_same_tenant_fk
            FOREIGN KEY (tenant_id, job_id) REFERENCES detection_jobs (tenant_id, id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'incidents_asset_same_tenant_fk') THEN
        ALTER TABLE incidents ADD CONSTRAINT incidents_asset_same_tenant_fk
            FOREIGN KEY (tenant_id, asset_id) REFERENCES assets (tenant_id, id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'incidents_baseline_same_tenant_fk') THEN
        ALTER TABLE incidents ADD CONSTRAINT incidents_baseline_same_tenant_fk
            FOREIGN KEY (tenant_id, baseline_id) REFERENCES baseline_versions (tenant_id, id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'alert_deliveries_incident_same_tenant_fk') THEN
        ALTER TABLE alert_deliveries ADD CONSTRAINT alert_deliveries_incident_same_tenant_fk
            FOREIGN KEY (tenant_id, incident_id) REFERENCES incidents (tenant_id, id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'alert_deliveries_channel_same_tenant_fk') THEN
        ALTER TABLE alert_deliveries ADD CONSTRAINT alert_deliveries_channel_same_tenant_fk
            FOREIGN KEY (tenant_id, channel_id) REFERENCES alert_channels (tenant_id, id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'audit_events_actor_same_tenant_fk') THEN
        ALTER TABLE audit_events ADD CONSTRAINT audit_events_actor_same_tenant_fk
            FOREIGN KEY (tenant_id, actor_id) REFERENCES users (tenant_id, id);
    END IF;
END $$;
