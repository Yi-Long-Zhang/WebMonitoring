DROP INDEX IF EXISTS incidents_escalation_idx;
ALTER TABLE incidents DROP COLUMN IF EXISTS escalated_at;
