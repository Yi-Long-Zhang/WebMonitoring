DROP INDEX IF EXISTS alert_deliveries_retry_idx;
ALTER TABLE alert_deliveries DROP COLUMN IF EXISTS next_attempt_at;
