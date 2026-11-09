package hub

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostgresPortalLifecycleGuard(t *testing.T) {
	s := testStore(t)
	tenant, key, _ := newTenant(t, s)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO plans(tenant_id,id,name,amount,currency,credits,interval_unit,interval_count) VALUES($1,'guard-plan','Guard',1000,'brl',0,'month',1)`,
		`INSERT INTO subscriptions(tenant_id,id,customer_id,plan_id,provider,provider_customer,payment_method,anchor,next_at,paused_at) SELECT $1,'guard-sub',id,'guard-plan','pagarme','cus','card',now(),now()+interval '1 month',now() FROM customers WHERE tenant_id=$1`,
		`INSERT INTO subscription_changes(tenant_id,id,subscription_id,plan_id,status,effective_at,next_at,unused_amount,credit_delta,usage_credits) VALUES($1,'guard-change','guard-sub','guard-plan','review',now(),now(),0,0,0)`,
	} {
		if _, err := s.DB.Exec(ctx, q, tenant); err != nil {
			t.Fatal(err)
		}
	}
	h := (&API{Store: s, Accounts: Accounts{tenant: {"pagarme": {Secret: "sk_test_guard", PublicKey: "pk_test_guard", Environment: "test"}}}}).Handler()
	r := request(t, h, "POST", "/v1/subscriptions/guard-sub/portal", key, "guard-portal", "{}")
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	var session map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	path := session["path"].(string)
	r = request(t, h, "GET", path+"/data", "", "", "")
	var public struct {
		Subscription struct {
			PausedAt any `json:"paused_at"`
		} `json:"subscription"`
	}
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &public) != nil || public.Subscription.PausedAt == nil {
		t.Fatal(r.Code, r.Body.String())
	}
	req := httptest.NewRequest("POST", path+"/cancel", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	r = httptest.NewRecorder()
	h.ServeHTTP(r, req)
	if r.Code != 409 {
		t.Fatal(r.Code, r.Body.String())
	}
	var status string
	if err := s.DB.QueryRow(ctx, "SELECT status FROM subscriptions WHERE tenant_id=$1 AND id='guard-sub'", tenant).Scan(&status); err != nil || status != "active" {
		t.Fatal(err, status)
	}
}
