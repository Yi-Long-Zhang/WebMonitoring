-- The incident timeline: what happened to an incident, in order.
--
-- Until now the upsert in UpsertIncident overwrote `signals` on every recurrence,
-- so an incident kept only occurrence_count and last_seen_at. The second, third
-- and later occurrences were recorded as a number; what was actually seen during
-- them was destroyed at write time and cannot be reconstructed afterwards.
--
-- Entries are append-only. docs/security-model.md classifies customer changes and
-- disposition actions as audit data that must not be silently modified, so this
-- table is deliberately not wired into the audit retention sweep: an incident's
-- history lives as long as the incident does.

CREATE TYPE incident_event_type AS ENUM (
    'CREATED',
    'RECURRED',
    'STATUS_CHANGED',
    'OWNER_ASSIGNED',
    'COMMENTED'
);

CREATE TABLE incident_events (
    id           uuid PRIMARY KEY,
    tenant_id    uuid NOT NULL,
    incident_id  uuid NOT NULL,
    event_type   incident_event_type NOT NULL,
    -- Empty for entries the detection worker writes on its own; there is no
    -- human actor behind an automated recurrence.
    actor_id     uuid,
    from_status  incident_status,
    to_status    incident_status,
    severity     severity,
    title        text,
    signals      jsonb,
    evidence_ids jsonb,
    -- The detection job that produced a CREATED or RECURRED entry, so an
    -- occurrence can be traced back to the run that observed it. Null for
    -- entries a human wrote.
    job_id       uuid,
    details      jsonb,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- incidents_tenant_id_uidx comes from 000003; the composite key below is what
-- makes it structurally impossible for a timeline entry to point at another
-- tenant's incident.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'incident_events_incident_same_tenant_fk') THEN
        ALTER TABLE incident_events ADD CONSTRAINT incident_events_incident_same_tenant_fk
            FOREIGN KEY (tenant_id, incident_id) REFERENCES incidents (tenant_id, id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'incident_events_actor_same_tenant_fk') THEN
        ALTER TABLE incident_events ADD CONSTRAINT incident_events_actor_same_tenant_fk
            FOREIGN KEY (tenant_id, actor_id) REFERENCES users (tenant_id, id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'incident_events_job_same_tenant_fk') THEN
        ALTER TABLE incident_events ADD CONSTRAINT incident_events_job_same_tenant_fk
            FOREIGN KEY (tenant_id, job_id) REFERENCES detection_jobs (tenant_id, id);
    END IF;
END $$;

CREATE INDEX incident_events_keyset_idx
    ON incident_events (tenant_id, incident_id, created_at DESC, id DESC);

-- Backfill one anchor entry per existing incident, so an incident that has been
-- open for weeks does not render as "no history at all".
--
-- The anchor deliberately carries NO signals. The only signal set still in the
-- database is the most recent occurrence, and writing that into the CREATED slot
-- would fabricate a first occurrence that never happened -- and it would be
-- indistinguishable from a real record afterwards. `unrecordedOccurrences` states
-- how much is missing instead, so the UI can say "N earlier occurrences were not
-- recorded" rather than show a plausible lie.
--
-- created_at is first_seen_at rather than now(), so the anchor sorts where the
-- incident actually began; that this row was written later is what the
-- `backfilled` marker records.
INSERT INTO incident_events (id, tenant_id, incident_id, event_type, severity, title, signals, evidence_ids, details, created_at)
SELECT gen_random_uuid(), i.tenant_id, i.id, 'CREATED', i.severity, i.title, '[]'::jsonb, '[]'::jsonb,
       jsonb_build_object(
           'backfilled', true,
           'unrecordedOccurrences', GREATEST(i.occurrence_count - 1, 0),
           'note', 'reconstructed by migration 000013; per-occurrence signals were overwritten before this migration and are not recoverable'
       ),
       i.first_seen_at
FROM incidents i
WHERE NOT EXISTS (SELECT 1 FROM incident_events e WHERE e.incident_id = i.id);
