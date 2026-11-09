ALTER TABLE checkout_sessions ADD COLUMN consent_at timestamptz;
ALTER TABLE checkout_branding ADD COLUMN checkout_base_url text NOT NULL DEFAULT '';
INSERT INTO schema_version(version) VALUES(5);
