-- Audit filtering narrows the (tenant_id, created_at DESC, id DESC) keyset from
-- audit_events_keyset_idx by actor, action or resource. Only the three
-- dimensions an investigator actually asks for are indexed:
--
--   * actor + time  -- "what did this account do"
--   * action + time -- "every tenant disable"
--   * resource      -- "this asset's or incident's whole history"
--
-- `result` and a bare `resource_id` are deliberately left unindexed. The tenant
-- plus time keyset already bounds the scan, and the filter is applied inside
-- that bound, which is how any keyset-paginated admin view behaves. audit_events
-- is append-only and swept at 365 days by DeleteAuditsBefore, so every added
-- index is write cost and bloat on every insert and every sweep. Do not add
-- them back without a query plan that shows the keyset bound is not enough.
--
-- `action` is an EXACT match, not a prefix search. A prefix such as `baseline.`
-- cannot use audit_events_action_idx and is not a supported query.

CREATE INDEX IF NOT EXISTS audit_events_actor_idx
    ON audit_events (tenant_id, actor_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS audit_events_action_idx
    ON audit_events (tenant_id, action, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS audit_events_resource_idx
    ON audit_events (tenant_id, resource_type, resource_id);
