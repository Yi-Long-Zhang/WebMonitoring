-- Ownership verification methods: FILE (well-known file), DNS (TXT record),
-- and META (HTML meta tag). Existing assets default to FILE, preserving the
-- original well-known file flow. Stored as text (not an enum) so adding a
-- method never requires an ALTER TYPE.
ALTER TABLE assets ADD COLUMN verification_method text NOT NULL DEFAULT 'FILE';
