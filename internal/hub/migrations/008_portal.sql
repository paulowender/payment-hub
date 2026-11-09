CREATE TABLE IF NOT EXISTS portal_sessions (
 id text PRIMARY KEY CHECK(id ~ '^[a-f0-9]{64}$'),
 tenant_id text NOT NULL, subscription_id text NOT NULL,
 expires_at timestamptz NOT NULL DEFAULT now()+interval '24 hours',
 revoked_at timestamptz, attempts int NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 10),
 consent_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,subscription_id) REFERENCES subscriptions(tenant_id,id)
);
CREATE INDEX IF NOT EXISTS portal_subscription ON portal_sessions(tenant_id,subscription_id);
INSERT INTO schema_version(version) VALUES(8) ON CONFLICT DO NOTHING;
