ALTER TABLE offers ADD COLUMN active boolean NOT NULL DEFAULT true;
CREATE TABLE checkout_settings (
 tenant_id text PRIMARY KEY REFERENCES tenants(id), config jsonb NOT NULL
);
ALTER TABLE charges ADD COLUMN method text NOT NULL DEFAULT 'card' CHECK(method IN ('card','pix','boleto'));
ALTER TABLE charges ADD COLUMN installments int NOT NULL DEFAULT 1 CHECK(installments BETWEEN 1 AND 12);
ALTER TABLE charges ADD COLUMN pix_expires int NOT NULL DEFAULT 1800;
ALTER TABLE charges ADD COLUMN boleto_due_days int NOT NULL DEFAULT 3;
ALTER TABLE charges ADD COLUMN payment_instructions jsonb NOT NULL DEFAULT '{}';
CREATE TABLE charge_history (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,tenant_id text NOT NULL,charge_id text NOT NULL,
 lease text NOT NULL,kind text NOT NULL,status text NOT NULL,detail text NOT NULL DEFAULT '',created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(tenant_id,charge_id) REFERENCES charges(tenant_id,id), UNIQUE(tenant_id,charge_id,lease,kind)
);
CREATE INDEX charge_history_charge ON charge_history(tenant_id,charge_id,id);
CREATE VIEW api_charge_details AS SELECT c.tenant_id,c.id,c.customer_id,c.provider,c.provider_id,c.amount,c.currency,c.credits,c.subscription_id,c.cycle,c.status,c.client_secret,c.last_error,c.created_at,c.updated_at,c.method,c.installments,c.payment_instructions,c.attempts,c.first_attempt,c.next_at FROM charges c;
INSERT INTO schema_version(version) VALUES(6);
