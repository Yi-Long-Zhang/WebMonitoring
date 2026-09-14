UPDATE evidence
SET retain_until = created_at + INTERVAL '90 days'
WHERE retain_until IS NULL;

CREATE INDEX IF NOT EXISTS evidence_retention_idx
    ON evidence (tenant_id, retain_until)
    WHERE retain_until IS NOT NULL;
