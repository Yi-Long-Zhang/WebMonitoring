ALTER TABLE alert_deliveries
    ADD COLUMN IF NOT EXISTS next_attempt_at timestamptz;

CREATE INDEX IF NOT EXISTS alert_deliveries_retry_idx
    ON alert_deliveries (tenant_id, next_attempt_at)
    WHERE status IN ('QUEUED', 'PENDING');
