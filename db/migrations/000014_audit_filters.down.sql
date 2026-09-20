-- Manual rollback only. internal/migrate implements up|status|baseline, and
-- db/migrations/embed.go embeds *.up.sql only, so nothing runs this file
-- automatically.
--
-- Dropping indexes is safe and loses no data, but the runner also checksums
-- applied migrations and records them in schema_migrations. Leaving that row in
-- place means a later `migrate up` skips 000014 forever. A complete rollback is:
--
--   1. psql ... -f db/migrations/000014_audit_filters.down.sql
--   2. DELETE FROM schema_migrations WHERE version = 14;
--   3. docker compose run --rm migrate up

DROP INDEX IF EXISTS audit_events_resource_idx;
DROP INDEX IF EXISTS audit_events_action_idx;
DROP INDEX IF EXISTS audit_events_actor_idx;
