-- Escalation has to fire once per incident, not once per sweep. The sweep runs
-- on a timer, so without a record of "this one has already been escalated" every
-- pass would send the same notice again until someone claimed the incident --
-- which trains people to ignore it, the exact opposite of the point.
--
-- The marker lives on the row rather than in an in-memory set because the set
-- would be empty after every restart, and a deployment restarts on every
-- upgrade: the first sweep after a deploy would re-notify every open incident.
ALTER TABLE incidents ADD COLUMN escalated_at timestamptz;
CREATE INDEX incidents_escalation_idx
    ON incidents (tenant_id, severity, created_at)
    WHERE escalated_at IS NULL AND owner_id IS NULL
      AND status NOT IN ('CLOSED', 'FALSE_POSITIVE');
