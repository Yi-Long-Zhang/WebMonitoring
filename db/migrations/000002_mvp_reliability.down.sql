ALTER TABLE detection_jobs
    DROP COLUMN IF EXISTS max_attempts,
    DROP COLUMN IF EXISTS retry_count;

DROP INDEX IF EXISTS alert_deliveries_incident_idx;
DROP TABLE IF EXISTS alert_deliveries;
ALTER TABLE incidents DROP COLUMN IF EXISTS acknowledged_at;
ALTER TABLE assets
    DROP COLUMN IF EXISTS verification_expires_at,
    DROP COLUMN IF EXISTS verification_token_hash;
