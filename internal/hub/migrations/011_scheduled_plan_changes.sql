CREATE TABLE scheduled_plan_changes (
 tenant_id text NOT NULL, id text NOT NULL, subscription_id text NOT NULL,
 source_plan_id text NOT NULL, target_plan_id text NOT NULL,
 effective_at timestamptz NOT NULL, charge_id text, payment_review_at timestamptz,
 status text NOT NULL CHECK(status IN ('scheduled','pending_payment','applied','canceled')),
 metadata jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(metadata)='object'),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,id), UNIQUE(tenant_id,charge_id),
 FOREIGN KEY(tenant_id,subscription_id) REFERENCES subscriptions(tenant_id,id),
 FOREIGN KEY(tenant_id,source_plan_id) REFERENCES plans(tenant_id,id),
 FOREIGN KEY(tenant_id,target_plan_id) REFERENCES plans(tenant_id,id),
 FOREIGN KEY(tenant_id,charge_id) REFERENCES charges(tenant_id,id),
 CHECK(source_plan_id<>target_plan_id),
 CHECK((status='scheduled' AND charge_id IS NULL) OR (status IN ('pending_payment','applied') AND charge_id IS NOT NULL) OR status='canceled')
);
CREATE UNIQUE INDEX scheduled_plan_change_live ON scheduled_plan_changes(tenant_id,subscription_id) WHERE status IN ('scheduled','pending_payment');
CREATE OR REPLACE VIEW api_subscriptions AS SELECT s.*,
 (SELECT jsonb_build_object('id',c.id,'plan_id',c.plan_id,'charge_id',c.charge_id,'status',c.status,'effective_at',c.effective_at,'next_at',c.next_at) FROM subscription_changes c WHERE c.tenant_id=s.tenant_id AND c.subscription_id=s.id AND c.status IN ('pending','review') ORDER BY c.created_at DESC LIMIT 1) AS pending_change,
 (SELECT jsonb_build_object('id',c.id,'source_plan_id',c.source_plan_id,'target_plan_id',c.target_plan_id,'effective_at',c.effective_at,'status',c.status,'metadata',c.metadata,'charge_id',c.charge_id,'charge_status',p.status,'created_at',c.created_at,'updated_at',c.updated_at,'payment_review_at',c.payment_review_at) FROM scheduled_plan_changes c LEFT JOIN charges p ON p.tenant_id=c.tenant_id AND p.id=c.charge_id WHERE c.tenant_id=s.tenant_id AND c.subscription_id=s.id AND c.status IN ('scheduled','pending_payment')) AS pending_scheduled_change
 FROM subscriptions s;
INSERT INTO schema_version(version) VALUES(11);
