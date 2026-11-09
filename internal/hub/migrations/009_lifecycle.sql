ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS paused_at timestamptz;
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS billing_credit bigint NOT NULL DEFAULT 0;
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS billing_epoch int NOT NULL DEFAULT 0;
CREATE TABLE IF NOT EXISTS subscription_changes (
 tenant_id text NOT NULL, id text NOT NULL, subscription_id text NOT NULL, plan_id text NOT NULL,
 charge_id text, status text NOT NULL CHECK(status IN ('pending','applied','failed','review')),
 effective_at timestamptz NOT NULL, next_at timestamptz NOT NULL,
 unused_amount bigint NOT NULL, credit_delta bigint NOT NULL, usage_credits bigint NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(tenant_id,id),
 FOREIGN KEY(tenant_id,subscription_id) REFERENCES subscriptions(tenant_id,id),
 FOREIGN KEY(tenant_id,plan_id) REFERENCES plans(tenant_id,id),
 FOREIGN KEY(tenant_id,charge_id) REFERENCES charges(tenant_id,id), UNIQUE(tenant_id,charge_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS subscription_change_pending ON subscription_changes(tenant_id,subscription_id) WHERE status='pending';
CREATE TABLE IF NOT EXISTS billing_ledger (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, tenant_id text NOT NULL, subscription_id text NOT NULL,
 delta bigint NOT NULL, reference text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(tenant_id,reference), FOREIGN KEY(tenant_id,subscription_id) REFERENCES subscriptions(tenant_id,id)
);
CREATE TRIGGER billing_ledger_append_only BEFORE UPDATE OR DELETE ON billing_ledger FOR EACH ROW EXECUTE FUNCTION deny_ledger_mutation();
INSERT INTO schema_version(version) VALUES(9) ON CONFLICT DO NOTHING;

CREATE OR REPLACE VIEW api_subscriptions AS SELECT s.*, (SELECT jsonb_build_object('id',c.id,'plan_id',c.plan_id,'charge_id',c.charge_id,'status',c.status,'effective_at',c.effective_at,'next_at',c.next_at) FROM subscription_changes c WHERE c.tenant_id=s.tenant_id AND c.subscription_id=s.id AND c.status IN ('pending','review') ORDER BY c.created_at DESC LIMIT 1) AS pending_change FROM subscriptions s;

ALTER TABLE charges DROP CONSTRAINT charges_amount_check;
ALTER TABLE charges ADD CONSTRAINT charges_amount_check CHECK(amount>=0 AND amount<=99999999);
ALTER TABLE charges ADD COLUMN kind text NOT NULL DEFAULT 'payment' CHECK(kind IN ('payment','plan_change','credit_settlement'));
ALTER TABLE charges ADD COLUMN billing_credit_used bigint NOT NULL DEFAULT 0 CHECK(billing_credit_used>=0);
CREATE OR REPLACE VIEW api_charge_details AS SELECT c.tenant_id,c.id,c.customer_id,c.provider,c.provider_id,c.amount,c.currency,c.credits,c.subscription_id,c.cycle,c.status,c.client_secret,c.last_error,c.created_at,c.updated_at,c.method,c.installments,c.payment_instructions,c.attempts,c.first_attempt,c.next_at,c.kind,c.billing_credit_used FROM charges c;
