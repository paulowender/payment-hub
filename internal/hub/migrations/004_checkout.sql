CREATE TABLE checkout_branding (
 tenant_id text PRIMARY KEY REFERENCES tenants(id), name text NOT NULL DEFAULT '', description text NOT NULL DEFAULT '',
 logo_url text NOT NULL DEFAULT '', primary_color text NOT NULL DEFAULT '#6e91ef', background_color text NOT NULL DEFAULT '#101720'
);
CREATE TABLE offers (
 tenant_id text NOT NULL REFERENCES tenants(id), id text NOT NULL, name text NOT NULL, description text NOT NULL,
 amount bigint NOT NULL CHECK(amount>0 AND amount<=99999999), currency text NOT NULL CHECK(currency='brl'),
 credits bigint NOT NULL CHECK(credits>=0 AND credits<=1000000000), plan_id text,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,id),
 FOREIGN KEY(tenant_id,plan_id) REFERENCES plans(tenant_id,id)
);
CREATE TABLE checkout_sessions (
 tenant_id text NOT NULL, id text NOT NULL, offer_id text NOT NULL, customer_id text NOT NULL,
 attempts int NOT NULL DEFAULT 0, reference text NOT NULL, snapshot jsonb NOT NULL, account_fingerprint text NOT NULL,
 charge_id text, subscription_id text, created_at timestamptz NOT NULL DEFAULT now(), expires_at timestamptz NOT NULL DEFAULT now()+interval '24 hours',
 PRIMARY KEY(tenant_id,id), UNIQUE(id), UNIQUE(tenant_id,reference),
 FOREIGN KEY(tenant_id,offer_id) REFERENCES offers(tenant_id,id), FOREIGN KEY(tenant_id,customer_id) REFERENCES customers(tenant_id,id),
 FOREIGN KEY(tenant_id,charge_id) REFERENCES charges(tenant_id,id), FOREIGN KEY(tenant_id,subscription_id) REFERENCES subscriptions(tenant_id,id)
);
INSERT INTO schema_version(version) VALUES(4);
