-- Restore the partial index. The duplicates this migration merged are NOT
-- restored: the merge was lossy by design (the folded rows' identities are
-- gone), and recreating them would invent incidents that no longer correspond
-- to anything an operator saw. Rolling back therefore returns the schema to its
-- previous behaviour while keeping the consolidated data.
--
-- The REOPENED label cannot be removed from an enum in PostgreSQL without
-- rewriting the type and every dependent column, and dropping it would break
-- any incident_events row written since. It is left in place; a rolled-back
-- database simply never produces it.
DROP INDEX IF EXISTS active_incident_fingerprint_uidx;
CREATE UNIQUE INDEX active_incident_dedup_idx
    ON incidents (tenant_id, asset_id, baseline_version, fingerprint)
    WHERE status NOT IN ('CLOSED', 'FALSE_POSITIVE');
