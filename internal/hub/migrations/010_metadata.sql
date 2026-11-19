ALTER TABLE customers ADD COLUMN IF NOT EXISTS metadata jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE plans ADD COLUMN IF NOT EXISTS metadata jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE offers ADD COLUMN IF NOT EXISTS metadata jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE checkout_sessions ADD COLUMN IF NOT EXISTS metadata jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS metadata jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE charges ADD COLUMN IF NOT EXISTS metadata jsonb NOT NULL DEFAULT '{}'::jsonb;

-- Rebuild the wildcard view: column positions differ between fresh and upgraded databases.
DROP VIEW IF EXISTS api_subscriptions;
CREATE VIEW api_subscriptions AS SELECT s.*, (SELECT jsonb_build_object('id',c.id,'plan_id',c.plan_id,'charge_id',c.charge_id,'status',c.status,'effective_at',c.effective_at,'next_at',c.next_at) FROM subscription_changes c WHERE c.tenant_id=s.tenant_id AND c.subscription_id=s.id AND c.status IN ('pending','review') ORDER BY c.created_at DESC LIMIT 1) AS pending_change FROM subscriptions s;
CREATE OR REPLACE VIEW api_charge_details AS SELECT c.tenant_id,c.id,c.customer_id,c.provider,c.provider_id,c.amount,c.currency,c.credits,c.subscription_id,c.cycle,c.status,c.client_secret,c.last_error,c.created_at,c.updated_at,c.method,c.installments,c.payment_instructions,c.attempts,c.first_attempt,c.next_at,c.kind,c.billing_credit_used,c.metadata FROM charges c;
INSERT INTO schema_version(version) VALUES(10) ON CONFLICT DO NOTHING;
