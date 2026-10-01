-- ReapTimedOut scans (tenant_id, status='RUNNING', started_at < cutoff) every
-- scheduler tick. The schedule index from migration 000001 leads with
-- (tenant_id, asset_id, mode), so the reap predicate could not use it and the
-- scan widened with the table. This index matches the predicate exactly; with
-- job retention (terminal rows older than JOB_RETENTION_DAYS are swept daily)
-- the RUNNING partition stays tiny regardless.
CREATE INDEX detection_jobs_reap_idx
    ON detection_jobs (tenant_id, status, started_at);
