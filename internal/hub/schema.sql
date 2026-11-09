CREATE TABLE IF NOT EXISTS schema_version (version int PRIMARY KEY);
CREATE TABLE IF NOT EXISTS tenants (
 id text PRIMARY KEY, name text NOT NULL, api_hash text NOT NULL UNIQUE, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS customers (
 metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
 tenant_id text NOT NULL REFERENCES tenants(id), id text NOT NULL, external_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,id), UNIQUE(tenant_id,external_id)
);
CREATE TABLE IF NOT EXISTS plans (
 metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
 tenant_id text NOT NULL REFERENCES tenants(id), id text NOT NULL, name text NOT NULL,
 amount bigint NOT NULL CHECK(amount>0 AND amount<=99999999), currency text NOT NULL CHECK(currency IN ('brl','usd')),
 credits bigint NOT NULL CHECK(credits>=0 AND credits<=1000000000), interval_unit text NOT NULL CHECK(interval_unit IN ('day','month')),
 interval_count int NOT NULL CHECK(interval_count>0 AND ((interval_unit='day' AND interval_count<=365) OR (interval_unit='month' AND interval_count<=120))),
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,id)
);
CREATE TABLE IF NOT EXISTS subscriptions (
 metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
 tenant_id text NOT NULL, id text NOT NULL, customer_id text NOT NULL, plan_id text NOT NULL,
 provider text NOT NULL CHECK(provider IN ('stripe','pagarme')), provider_customer text NOT NULL, payment_method text NOT NULL, account_fingerprint text NOT NULL DEFAULT '',
 status text NOT NULL DEFAULT 'active' CHECK(status IN ('active','canceled','past_due')),
 anchor timestamptz NOT NULL, cycle int NOT NULL DEFAULT 0, next_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,id),
 FOREIGN KEY(tenant_id,customer_id) REFERENCES customers(tenant_id,id), FOREIGN KEY(tenant_id,plan_id) REFERENCES plans(tenant_id,id)
);
CREATE TABLE IF NOT EXISTS charges (
 metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
 tenant_id text NOT NULL, id text NOT NULL, customer_id text NOT NULL,
 provider text NOT NULL CHECK(provider IN ('stripe','pagarme')), provider_customer text NOT NULL, payment_method text NOT NULL, account_fingerprint text NOT NULL DEFAULT '',
 amount bigint NOT NULL CHECK(amount>0 AND amount<=99999999), currency text NOT NULL CHECK(currency IN ('brl','usd')),
 credits bigint NOT NULL CHECK(credits>=0 AND credits<=1000000000),
 subscription_id text, cycle int, status text NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','pending','requires_action','paid','failed','reversed','manual_review')),
 provider_id text, provider_charge_id text NOT NULL DEFAULT '', client_secret text NOT NULL DEFAULT '', credited bool NOT NULL DEFAULT false, reversed bool NOT NULL DEFAULT false,
 first_attempt timestamptz, next_at timestamptz NOT NULL DEFAULT now(), lease_until timestamptz, lease_token text, attempts int NOT NULL DEFAULT 0,
 last_error text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,id), UNIQUE(tenant_id,subscription_id,cycle), UNIQUE(tenant_id,provider,provider_id),
 FOREIGN KEY(tenant_id,customer_id) REFERENCES customers(tenant_id,id),
 FOREIGN KEY(tenant_id,subscription_id) REFERENCES subscriptions(tenant_id,id)
);
CREATE TABLE IF NOT EXISTS wallets (
 tenant_id text NOT NULL, customer_id text NOT NULL, balance bigint NOT NULL DEFAULT 0,
 PRIMARY KEY(tenant_id,customer_id), FOREIGN KEY(tenant_id,customer_id) REFERENCES customers(tenant_id,id)
);
CREATE TABLE IF NOT EXISTS ledger (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, tenant_id text NOT NULL, customer_id text NOT NULL,
 delta bigint NOT NULL CHECK(delta<>0), reason text NOT NULL CHECK(reason IN ('payment','reversal','consume','grant')), reference text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(tenant_id,reason,reference),
 FOREIGN KEY(tenant_id,customer_id) REFERENCES customers(tenant_id,id)
);
CREATE OR REPLACE FUNCTION deny_ledger_mutation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'ledger is append-only'; END $$;
DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='ledger_append_only' AND tgrelid='ledger'::regclass) THEN
 CREATE TRIGGER ledger_append_only BEFORE UPDATE OR DELETE ON ledger FOR EACH ROW EXECUTE FUNCTION deny_ledger_mutation();
 END IF;
END $$;
CREATE TABLE IF NOT EXISTS idempotency (
 tenant_id text NOT NULL REFERENCES tenants(id), key text NOT NULL, fingerprint text NOT NULL, response jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,key)
);
CREATE TABLE IF NOT EXISTS events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, tenant_id text NOT NULL REFERENCES tenants(id),
 type text NOT NULL, data jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS charges_due ON charges(next_at) WHERE status NOT IN ('failed','reversed','manual_review');
CREATE INDEX IF NOT EXISTS subscriptions_due ON subscriptions(next_at) WHERE status='active';
CREATE INDEX IF NOT EXISTS events_tenant_cursor ON events(tenant_id,id);
CREATE INDEX IF NOT EXISTS ledger_customer ON ledger(tenant_id,customer_id,id);
INSERT INTO schema_version(version) VALUES(1) ON CONFLICT DO NOTHING;
CREATE OR REPLACE VIEW api_charges AS SELECT tenant_id,id,customer_id,provider,provider_id,amount,currency,credits,subscription_id,cycle,status,client_secret,last_error,created_at,updated_at FROM charges;
