CREATE TABLE webhook_diagnostics (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant_id text NOT NULL REFERENCES tenants(id), capture_until timestamptz NOT NULL,
 authenticated bool NOT NULL, headers jsonb NOT NULL, payload jsonb NOT NULL,
 body_bytes int NOT NULL, received_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webhook_diagnostics_tenant ON webhook_diagnostics(tenant_id,id DESC);
INSERT INTO schema_version(version) VALUES(3);
