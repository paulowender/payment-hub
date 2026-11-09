package hub

import (
	"context"
	"strings"
	"testing"
)

func TestPostgresSubscriptionAuditIsolation(t *testing.T) {
	s := testStore(t)
	tenant, key, customer := newTenant(t, s)
	_, other, _ := newTenant(t, s)
	ctx := context.Background()
	_, err := s.DB.Exec(ctx, `INSERT INTO plans(tenant_id,id,name,amount,currency,credits,interval_unit,interval_count) VALUES($1,'audit-plan','Audit',1000,'brl',0,'month',1)`, tenant)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO subscriptions(tenant_id,id,customer_id,plan_id,provider,provider_customer,payment_method,anchor,next_at) VALUES($1,'audit-sub',$2,'audit-plan','stripe','cus','pm',now(),now())`, tenant, customer)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO billing_ledger(tenant_id,subscription_id,delta,reference) VALUES($1,'audit-sub',123,'audit')`, tenant)
	if err != nil {
		t.Fatal(err)
	}
	h := (&API{Store: s}).Handler()
	for _, resource := range []string{"billing-ledger", "changes"} {
		path := "/v1/subscriptions/audit-sub/" + resource
		r := request(t, h, "GET", path, key, "", "")
		if r.Code != 200 || strings.Contains(r.Body.String(), tenant) {
			t.Fatal(r.Code, r.Body.String())
		}
		if r = request(t, h, "GET", path, other, "", ""); r.Code != 404 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	if r := request(t, h, "GET", "/v1/subscriptions/audit-sub", key, "", ""); r.Code != 200 || !strings.Contains(r.Body.String(), "pending_change") {
		t.Fatal(r.Code, r.Body.String())
	}
}
