CREATE TABLE notification_configs (
 tenant_id text PRIMARY KEY REFERENCES tenants(id), url text NOT NULL,
 enabled boolean NOT NULL DEFAULT false, encrypted_secret bytea NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE notification_deliveries (
 tenant_id text NOT NULL REFERENCES tenants(id), event_id bigint NOT NULL REFERENCES events(id),
 endpoint text NOT NULL, encrypted_secret bytea NOT NULL, payload bytea NOT NULL,
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','delivered','failed','dead')),
 attempts integer NOT NULL DEFAULT 0, status_code integer NOT NULL DEFAULT 0,
 last_error text NOT NULL DEFAULT '', next_at timestamptz NOT NULL DEFAULT now(),
 lease_until timestamptz, lease_token text, created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,event_id)
);
CREATE INDEX notification_due ON notification_deliveries(next_at) WHERE status IN ('pending','failed');
INSERT INTO schema_version(version) VALUES(7) ON CONFLICT DO NOTHING;
