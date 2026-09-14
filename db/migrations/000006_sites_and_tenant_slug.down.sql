-- Reverse 000006. Note: the SUPER_ADMIN user_role enum value cannot be dropped
-- (PostgreSQL does not support removing enum values), so it is intentionally
-- left in place.
ALTER TABLE assets DROP CONSTRAINT IF EXISTS assets_site_same_tenant_fk;
ALTER TABLE assets DROP CONSTRAINT IF EXISTS assets_site_fk;
ALTER TABLE sites DROP CONSTRAINT IF EXISTS sites_owner_same_tenant_fk;
DROP TABLE IF EXISTS sites;
DROP INDEX IF EXISTS tenants_slug_uidx;
ALTER TABLE tenants DROP COLUMN IF EXISTS slug;
