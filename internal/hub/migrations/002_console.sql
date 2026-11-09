CREATE TABLE provider_connections (
 tenant_id text NOT NULL REFERENCES tenants(id), provider text NOT NULL CHECK(provider IN ('stripe','pagarme')),
 encrypted_config bytea NOT NULL, updated_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,provider)
);
CREATE TABLE console_sessions (
 token_hash text PRIMARY KEY, tenant_id text REFERENCES tenants(id), is_admin bool NOT NULL DEFAULT false,
 expires_at timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(is_admin OR tenant_id IS NOT NULL)
);
CREATE INDEX console_sessions_expiry ON console_sessions(expires_at);
CREATE TABLE webhook_receipts (
 tenant_id text NOT NULL REFERENCES tenants(id), provider text NOT NULL, event_id text NOT NULL,
 event_type text NOT NULL, order_id text NOT NULL, received_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,provider,event_id)
);
INSERT INTO schema_version(version) VALUES(2);
