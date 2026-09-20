-- Manual rollback only. internal/migrate implements up|status|baseline, and
-- db/migrations/embed.go embeds *.up.sql only, so nothing runs this file
-- automatically.
--
-- Running it by hand is not sufficient on its own: the runner also checksums
-- applied migrations and records them in schema_migrations. Leaving that row in
-- place means the next `migrate up` skips 000013 forever and the table is never
-- recreated. A complete rollback is:
--
--   1. psql ... -f db/migrations/000013_incident_timeline.down.sql
--   2. DELETE FROM schema_migrations WHERE version = 13;
--   3. docker compose run --rm migrate up
--
-- DROP TABLE discards every incident's timeline. That history is append-only and
-- not reconstructible, so treat this as a last resort rather than a routine step.

DROP INDEX IF EXISTS incident_events_keyset_idx;
DROP TABLE IF EXISTS incident_events;
DROP TYPE IF EXISTS incident_event_type;
