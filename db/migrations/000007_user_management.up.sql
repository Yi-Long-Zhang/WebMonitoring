-- Soft-disable for users so an account can be deactivated without being
-- hard-deleted (users are referenced by assets.owner_id, sites.owner_id, and
-- audit_events.actor_id foreign keys). Disabled users are rejected at login
-- and their existing sessions are refused on the next authenticated request.
ALTER TABLE users ADD COLUMN disabled boolean NOT NULL DEFAULT false;
