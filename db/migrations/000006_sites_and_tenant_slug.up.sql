-- Activate the sites table and add a tenant slug for login resolution.
-- Also introduce the SUPER_ADMIN role for platform-level tenant management.

-- 1. Tenant slug (used to resolve tenant at login without exposing the UUID).
ALTER TABLE tenants ADD COLUMN slug text;
UPDATE tenants SET slug = 'default' WHERE id = '00000000-0000-4000-8000-000000000001';
ALTER TABLE tenants ALTER COLUMN slug SET NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS tenants_slug_uidx ON tenants (slug);

-- 2. Platform-level role. Enum values cannot be dropped by a down migration
--    (PostgreSQL limitation); acceptable and noted in the down file.
ALTER TYPE user_role ADD VALUE 'SUPER_ADMIN';

-- 3. Sites: tenant-scoped organizational grouping for assets.
CREATE TABLE sites (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    name text NOT NULL,
    domain text NOT NULL,
    owner_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);
CREATE UNIQUE INDEX sites_tenant_id_uidx ON sites (tenant_id, id);

-- 4. Fail-closed owner scoping: a site's owner must belong to the same tenant.
ALTER TABLE sites ADD CONSTRAINT sites_owner_same_tenant_fk
    FOREIGN KEY (tenant_id, owner_id) REFERENCES users (tenant_id, id);

-- 5. Asset -> site linkage. site_id was seeded nullable with no FK in 000001;
--    existing rows all have NULL site_id, so the constraint is safe.
ALTER TABLE assets ADD CONSTRAINT assets_site_fk
    FOREIGN KEY (site_id) REFERENCES sites (id);
ALTER TABLE assets ADD CONSTRAINT assets_site_same_tenant_fk
    FOREIGN KEY (tenant_id, site_id) REFERENCES sites (tenant_id, id);
