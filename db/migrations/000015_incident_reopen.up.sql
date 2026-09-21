-- Recurrences must reopen a closed incident instead of opening a second one.
--
-- active_incident_dedup_idx (000001) was a PARTIAL unique index that excluded
-- CLOSED and FALSE_POSITIVE rows, so a fingerprint that came back after an
-- operator closed it did not match anything: UpsertIncident inserted a brand
-- new incident with the same fingerprint. The operator ended up with two rows
-- that were obviously the same problem and no way to see they were related.
-- Nothing depended on that behaviour -- no code path looked an incident up by
-- fingerprint ignoring status, no test asserted it, and the reader-facing docs
-- already described RECURRED as "the same fingerprint matched again" with no
-- carve-out for closed incidents. It was a gap, not a decision.
--
-- The index becomes unconditional so there is exactly one incident per
-- fingerprint at any time, which is what makes reopening expressible: the
-- recurrence has a single row to land on.
--
-- Existing databases do have the duplicates -- every close-then-recur left one
-- behind -- so they are merged here before the index can be built. The oldest
-- row survives because it carries the true first_seen_at and is the one an
-- operator has been looking at; the later ones fold into it.
--
-- NOTE: ALTER TYPE ... ADD VALUE may not be *used* in the transaction that adds
-- it, so this migration only adds the label. Nothing here writes a REOPENED
-- entry -- the rows folded below are old recurrences and are left as RECURRED,
-- which is what they were. Only the running upsert emits REOPENED, long after
-- this transaction has committed.
ALTER TYPE incident_event_type ADD VALUE IF NOT EXISTS 'REOPENED';

-- Every row that must fold into another, and which one it folds into. Rows of a
-- key with no duplicate are absent, so a lone incident is never touched.
--
-- The grouping key is exactly the (tenant_id, asset_id, baseline_version,
-- fingerprint) the new index enforces. It deliberately spans every status: a
-- CLOSED row and an OPEN one sharing a fingerprint are the duplicate this
-- migration exists to collapse. The survivor is the earliest first_seen_at --
-- it carries the true first sighting and is the row an operator has been
-- looking at -- with id as a tiebreak so the choice is deterministic when two
-- rows were created in the same instant.
CREATE TEMP TABLE incident_reopen_victims ON COMMIT DROP AS
WITH ranked AS (
    SELECT id, tenant_id, asset_id, baseline_version, fingerprint,
           row_number() OVER (
               PARTITION BY tenant_id, asset_id, baseline_version, fingerprint
               ORDER BY first_seen_at, id
           ) AS rn
    FROM incidents
),
survivor AS (
    SELECT id, tenant_id, asset_id, baseline_version, fingerprint
    FROM ranked WHERE rn = 1
)
SELECT i.id AS victim_id, s.id AS survivor_id
FROM incidents i
JOIN ranked r
  ON r.id = i.id AND r.rn > 1
JOIN survivor s
  ON s.tenant_id = i.tenant_id AND s.asset_id = i.asset_id
 AND s.baseline_version = i.baseline_version AND s.fingerprint = i.fingerprint;

-- Repoint timeline entries onto the survivor before their incident disappears.
-- incident_events has a foreign key to incidents with no ON DELETE, so the
-- delete below would otherwise fail. Entries are never deleted: this is the same
-- history an open incident's would be, and losing it is precisely what
-- docs/domain-model.md says must not happen. mergedFrom keeps the record
-- explicable instead of letting it look like it always belonged to the survivor.
UPDATE incident_events e
SET incident_id = v.survivor_id,
    details = COALESCE(e.details, '{}'::jsonb) || jsonb_build_object('mergedFrom', e.incident_id)
FROM incident_reopen_victims v
WHERE e.incident_id = v.victim_id;

-- Fold occurrence counts and evidence sets in.
--
-- evidence_ids is flattened to scalars BEFORE the union: every array is expanded
-- with jsonb_array_elements and the elements are aggregated, then the survivor's
-- and the victims' element lists are concatenated and deduplicated. Aggregating
-- the arrays first (jsonb_agg) and expanding afterwards nests them -- an array
-- is itself valid jsonb, so jsonb_array_elements yields it whole instead of
-- descending, and the stored evidence_ids ends up as ["[\"ev-2\",\"ev-3\"]",...]
-- rather than one flat list. That is what the store's own merge would never
-- produce, and it is what this statement is checked against.
UPDATE incidents t
SET occurrence_count = t.occurrence_count + folded.added_occurrences,
    evidence_ids = folded.merged_evidence,
    last_seen_at = GREATEST(t.last_seen_at, folded.last_seen_at)
FROM (
    SELECT v.survivor_id,
           sum(i.occurrence_count) AS added_occurrences,
           max(i.last_seen_at)     AS last_seen_at,
           (SELECT COALESCE(jsonb_agg(DISTINCT value ORDER BY value), '[]'::jsonb)
            FROM jsonb_array_elements(
                (SELECT COALESCE(jsonb_agg(elem), '[]'::jsonb)
                 FROM incidents survivor, jsonb_array_elements(survivor.evidence_ids) elem
                 WHERE survivor.id = v.survivor_id)
                || (SELECT COALESCE(jsonb_agg(elem), '[]'::jsonb)
                    FROM incidents victim, jsonb_array_elements(victim.evidence_ids) elem
                    WHERE victim.id IN (
                        SELECT victim_id FROM incident_reopen_victims WHERE survivor_id = v.survivor_id
                    ))
            ) AS merged(value)) AS merged_evidence
    FROM incident_reopen_victims v
    JOIN incidents i ON i.id = v.victim_id
    GROUP BY v.survivor_id
) AS folded
WHERE t.id = folded.survivor_id;

DELETE FROM incidents i
USING incident_reopen_victims v
WHERE i.id = v.victim_id;

-- One incident per fingerprint, unconditionally. A recurrence now has exactly
-- one row to land on, which is what lets it reopen rather than duplicate.
DROP INDEX IF EXISTS active_incident_dedup_idx;
CREATE UNIQUE INDEX active_incident_fingerprint_uidx
    ON incidents (tenant_id, asset_id, baseline_version, fingerprint);
