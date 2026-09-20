-- At most one QUEUED or RUNNING detection job may exist per (tenant, asset, mode).
--
-- The scheduler decides whether a check is due by reading the job table and then
-- enqueueing ("read-then-decide"). That is not atomic, so two control-plane
-- replicas that tick at the same time both observe no in-flight job and both
-- queue one. The queue envelope carries no idempotency key either, so the
-- duplicate is undetectable downstream. This index makes the second insert fail
-- instead, and CreateJob maps that conflict to store.ErrJobAlreadyActive.
--
-- Pre-existing duplicates must be resolved before the index can be created.
-- The newest job is kept because it carries the most recent policy version; the
-- rest are CANCELLED rather than deleted so the history stays queryable. Only
-- QUEUED/RUNNING rows are touched, so no terminal job is rewritten.
WITH ranked AS (
    SELECT id, row_number() OVER (
        PARTITION BY tenant_id, asset_id, mode
        ORDER BY created_at DESC, id DESC
    ) AS rn
    FROM detection_jobs
    WHERE status IN ('QUEUED', 'RUNNING')
)
UPDATE detection_jobs
SET status = 'CANCELLED', finished_at = now(), error_code = 'SUPERSEDED'
WHERE id IN (SELECT id FROM ranked WHERE rn > 1);

CREATE UNIQUE INDEX detection_jobs_active_uidx
    ON detection_jobs (tenant_id, asset_id, mode)
    WHERE status IN ('QUEUED', 'RUNNING');
