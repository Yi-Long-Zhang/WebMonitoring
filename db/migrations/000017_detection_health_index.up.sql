-- The self-health read asks two platform-wide questions every scrape: when a
-- check last succeeded, and how the checks that finished since a cutoff went.
-- Neither predicate carries a tenant, so none of the existing indexes can serve
-- them: detection_jobs_schedule_idx leads with tenant_id and
-- detection_jobs_reap_idx with (tenant_id, status). Both questions were
-- sequential scans over the whole retention window, on every metrics scrape.
--
-- They need separate indexes because they need opposite leading columns, and
-- the difference is not academic.
--
-- The window count filters on finished_at alone, so finished_at has to lead for
-- the scan to cover only the window. With status first, the planner can only
-- walk the whole index and filter -- O(history) instead of O(window).
--
-- The maximum wants the opposite, and pairing it with a status-first index is
-- what makes it safe: a scan of the SUCCEEDED range stops at its first entry.
-- Under a finished_at-first index it would instead walk backwards from now,
-- skipping every row, until it found a success -- and the worse the outage, the
-- longer that scan. The case the metric exists to report is the case that would
-- have been slowest.
--
-- Both are partial: only terminal rows can match either query, so QUEUED and
-- RUNNING rows stay out of both indexes.
CREATE INDEX detection_jobs_health_success_idx
    ON detection_jobs (finished_at DESC)
    WHERE status = 'SUCCEEDED';

CREATE INDEX detection_jobs_health_finished_idx
    ON detection_jobs (finished_at DESC)
    WHERE finished_at IS NOT NULL;
