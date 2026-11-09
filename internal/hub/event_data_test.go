package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Identical local IDs across tenants catch joins that forget the tenant key.
func eventFixture(t *testing.T, s *Store, marker string) (string, string) {
	t.Helper()
	tenant, key, _ := newTenant(t, s)
	for _, q := range []string{
		`INSERT INTO customers(tenant_id,id,external_id,metadata) VALUES($1,'customer','customer',jsonb_build_object('owner',$2::text,'nested',jsonb_build_object('keep',true)))`,
		`INSERT INTO wallets(tenant_id,customer_id) VALUES($1,'customer')`,
		`INSERT INTO plans(tenant_id,id,name,amount,currency,credits,interval_unit,interval_count,metadata) VALUES($1,'plan','Plan',1000,'brl',10,'month',1,jsonb_build_object('owner',$2::text))`,
		`INSERT INTO subscriptions(tenant_id,id,customer_id,plan_id,provider,provider_customer,payment_method,account_fingerprint,anchor,next_at,cycle,metadata) VALUES($1,'sub','customer','plan','stripe','cus_private','pm_private','fp_private',now()-interval '15 days',now()+interval '15 days',1,jsonb_build_object('owner',$2::text))`,
		`INSERT INTO charges(tenant_id,id,customer_id,subscription_id,cycle,provider,provider_customer,payment_method,account_fingerprint,provider_id,provider_charge_id,client_secret,amount,currency,credits,status,credited,next_at,metadata) VALUES($1,'charge','customer','sub',0,'stripe','cus_private','pm_private','fp_private','pi_private','ch_private','secret_private',1000,'brl',10,'paid',true,'infinity',jsonb_build_object('owner',$2::text))`,
		`INSERT INTO offers(tenant_id,id,name,description,amount,currency,credits,plan_id,metadata) VALUES($1,'offer','Offer','',1000,'brl',10,'plan',jsonb_build_object('owner',$2::text))`,
		`INSERT INTO checkout_sessions(tenant_id,id,offer_id,customer_id,reference,snapshot,account_fingerprint,charge_id,subscription_id,metadata) VALUES($1,$1||'-session','offer','customer','purchase', '{"buyer":"pii_private","secret":"snapshot_private"}', 'fp_private','charge','sub',jsonb_build_object('owner',$2::text))`,
	} {
		args := []any{tenant, marker}
		if !strings.Contains(q, "$2") {
			args = args[:1]
		}
		if _, err := s.DB.Exec(context.Background(), q, args...); err != nil {
			t.Fatal(err)
		}
	}
	return tenant, key
}

func eventData(t *testing.T, s *Store, tenant, kind string) map[string]any {
	t.Helper()
	var raw []byte
	if err := s.DB.QueryRow(context.Background(), `SELECT data FROM events WHERE tenant_id=$1 AND type=$2 ORDER BY id DESC LIMIT 1`, tenant, kind).Scan(&raw); err != nil {
		t.Fatal(kind, err)
	}
	return metadataObject(t, raw)
}

func requireEventCorrelation(t *testing.T, got map[string]any, want map[string]any) {
	t.Helper()
	for key, value := range want {
		if !reflect.DeepEqual(got[key], value) {
			t.Errorf("%s = %#v, want %#v (payload %#v)", key, got[key], value, got)
		}
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"cus_private", "pm_private", "fp_private", "pi_private", "ch_private", "secret_private", "pii_private", "snapshot_private", "foreign", "provider_customer", "payment_method", "account_fingerprint", "client_secret", "provider_id", "provider_charge_id", "snapshot"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("event leaks %q: %s", forbidden, raw)
		}
	}
}

func TestFinancialEventDataEmissions(t *testing.T) {
	s := testStore(t)
	tenant, key := eventFixture(t, s, "own")
	eventFixture(t, s, "foreign")
	a := &API{Store: s, Accounts: Accounts{tenant: {"stripe": {Secret: "sk_test_example", Environment: "test"}}}}
	h := a.Handler()
	out := request(t, h, "POST", "/v1/charges", key, "charge", `{"customer_id":"customer","provider":"stripe","provider_customer":"cus_private","payment_method":"pm_private","amount":1000,"currency":"brl","credits":10,"metadata":{"purchase":"direct"}}`)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body)
	}
	id := metadataObject(t, out.Body.Bytes())["id"].(string)
	want := map[string]any{"charge_id": id, "customer_id": "customer", "status": "queued", "charge_metadata": map[string]any{"purchase": "direct"}, "customer_metadata": map[string]any{"owner": "own", "nested": map[string]any{"keep": true}}}
	requireEventCorrelation(t, eventData(t, s, tenant, "charge.queued"), want)
	worker := &Worker{Store: s}
	c, err := worker.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != id {
		t.Fatal("unexpected claim", c.ID)
	}
	if err = worker.apply(context.Background(), c, Payment{Status: "paid", ID: "new_pi_private", ClientSecret: "secret_private", Origin: "ch_private"}); err != nil {
		t.Fatal(err)
	}
	want["status"] = "paid"
	requireEventCorrelation(t, eventData(t, s, tenant, "charge.paid"), want)
}

func TestSubscriptionEventDataLifecycle(t *testing.T) {
	s := testStore(t)
	tenant, key := eventFixture(t, s, "own")
	eventFixture(t, s, "foreign")
	a := &API{Store: s, Accounts: Accounts{tenant: {"stripe": {Secret: "sk_test_example", Environment: "test"}}}}
	h := a.Handler()
	out := request(t, h, "POST", "/v1/subscriptions", key, "sub", `{"customer_id":"customer","plan_id":"plan","provider":"stripe","provider_customer":"cus_private","payment_method":"pm_private","metadata":{"purchase":"recurring"}}`)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body)
	}
	id := metadataObject(t, out.Body.Bytes())["id"].(string)
	want := map[string]any{"subscription_id": id, "customer_id": "customer", "plan_id": "plan", "status": "active", "subscription_metadata": map[string]any{"purchase": "recurring"}, "customer_metadata": map[string]any{"owner": "own", "nested": map[string]any{"keep": true}}, "plan_metadata": map[string]any{"owner": "own"}}
	requireEventCorrelation(t, eventData(t, s, tenant, "subscription.created"), want)
	for _, action := range []string{"pause", "resume", "cancel"} {
		out = request(t, h, "POST", "/v1/subscriptions/"+id+"/"+action, key, action, `{}`)
		if out.Code != 200 {
			t.Fatal(action, out.Code, out.Body)
		}
		if action == "cancel" {
			want["status"] = "canceled"
		}
		kind := map[string]string{"pause": "paused", "resume": "resumed", "cancel": "canceled"}[action]
		requireEventCorrelation(t, eventData(t, s, tenant, "subscription."+kind), want)
	}
	// An immediate plan change must report the authoritative replacement plan.
	if _, err := s.DB.Exec(context.Background(), `INSERT INTO plans(tenant_id,id,name,amount,currency,credits,interval_unit,interval_count,metadata) VALUES($1,'cheap','Cheap',1,'brl',1,'month',1,'{"target":true}')`, tenant); err != nil {
		t.Fatal(err)
	}
	out = request(t, h, "POST", "/v1/subscriptions/sub/change-plan", key, "change", `{"plan_id":"cheap"}`)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body)
	}
	requireEventCorrelation(t, eventData(t, s, tenant, "subscription.change_applied"), map[string]any{"subscription_id": "sub", "customer_id": "customer", "plan_id": "cheap", "status": "active", "plan_metadata": map[string]any{"target": true}, "subscription_metadata": map[string]any{"owner": "own"}})
}

func TestFinancialEventDataCheckout(t *testing.T) {
	s := testStore(t)
	tenant, key := eventFixture(t, s, "own")
	eventFixture(t, s, "foreign")
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := "cus_private"
		if strings.HasSuffix(r.URL.Path, "/cards") {
			id = "pm_private"
		}
		respond(w, 200, map[string]string{"id": id})
	}))
	defer provider.Close()
	gateway := NewGateway()
	gateway.pagarmeURL = provider.URL
	h := (&API{Store: s, CheckoutGateway: gateway, Accounts: Accounts{tenant: {"pagarme": {Secret: "sk_test_example", Environment: "test", PublicKey: "pk_test_example"}}}}).Handler()
	out := request(t, h, "POST", "/v1/checkout-sessions", key, "session", `{"offer_id":"offer","customer_id":"customer","reference":"new-purchase","metadata":{"purchase":"checkout"}}`)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body)
	}
	session := metadataObject(t, out.Body.Bytes())
	body := `{"card_token":"token_private","recurring_consent":true,"buyer":{"name":"Cliente Teste","email":"cliente@example.com","document":"12345678909","phone":"11999999999","street":"Rua Teste","number":"123","zip_code":"01001000","city":"Sao Paulo","state":"SP"}}`
	r := httptest.NewRequest("POST", session["path"].(string)+"/pay", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	out = httptest.NewRecorder()
	h.ServeHTTP(out, r)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body)
	}
	want := map[string]any{"session_id": session["id"], "offer_id": "offer", "reference": "new-purchase", "customer_id": "customer", "plan_id": "plan", "status": "queued", "checkout_session_metadata": map[string]any{"purchase": "checkout"}, "offer_metadata": map[string]any{"owner": "own"}, "charge_metadata": map[string]any{}, "subscription_metadata": map[string]any{}, "plan_metadata": map[string]any{"owner": "own"}}
	for _, kind := range []string{"checkout.submitted", "charge.queued"} {
		requireEventCorrelation(t, eventData(t, s, tenant, kind), want)
	}
	delete(want, "charge_metadata")
	want["status"] = "active"
	requireEventCorrelation(t, eventData(t, s, tenant, "subscription.created"), want)
}

func TestFinancialEventDataTransactionAndIsolation(t *testing.T) {
	s := testStore(t)
	tenant, _ := eventFixture(t, s, "own")
	other, _ := eventFixture(t, s, "foreign")
	empty, _, _ := newTenant(t, s)
	tx, err := s.DB.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(context.Background(), `UPDATE charges SET status='reversed',metadata='{"fresh":true,"nested":{"nullable":null},"large":9007199254740993,"decimal":0.1234567890123456789}' WHERE tenant_id=$1 AND id='charge'`, tenant); err != nil {
		t.Fatal(err)
	}
	got, err := financialEventData(context.Background(), tx, tenant, "charge")
	if err != nil {
		t.Fatal(err)
	}
	requireEventCorrelation(t, got, map[string]any{"charge_id": "charge", "customer_id": "customer", "subscription_id": "sub", "plan_id": "plan", "status": "reversed", "session_id": tenant + "-session", "offer_id": "offer", "reference": "purchase", "charge_metadata": map[string]any{"fresh": true, "nested": map[string]any{"nullable": nil}, "large": json.Number("9007199254740993"), "decimal": json.Number("0.1234567890123456789")}, "subscription_metadata": map[string]any{"owner": "own"}, "plan_metadata": map[string]any{"owner": "own"}, "offer_metadata": map[string]any{"owner": "own"}, "checkout_session_metadata": map[string]any{"owner": "own"}})
	raw, err := json.Marshal(got["charge_metadata"])
	if err != nil {
		t.Fatal(err)
	}
	for _, number := range []string{"9007199254740993", "0.1234567890123456789"} {
		if !strings.Contains(string(raw), number) {
			t.Errorf("opaque metadata number changed: %s", raw)
		}
	}
	if _, err = financialEventData(context.Background(), tx, empty, "charge"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("cross tenant read", err)
	}
	if _, err = financialEventData(context.Background(), tx, other, "missing"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("missing charge", err)
	}
	if _, err = tx.Exec(context.Background(), `INSERT INTO charges(tenant_id,id,customer_id,provider,provider_customer,payment_method,amount,currency,credits) VALUES($1,'oneoff','customer','stripe','cus_private','pm_private',1,'brl',0)`, tenant); err != nil {
		t.Fatal(err)
	}
	got, err = financialEventData(context.Background(), tx, tenant, "oneoff")
	if err != nil {
		t.Fatal(err)
	}
	requireEventCorrelation(t, got, map[string]any{"charge_metadata": map[string]any{}, "status": "queued"})
	for _, key := range []string{"subscription_id", "plan_id", "subscription_metadata", "plan_metadata", "session_id", "offer_id", "reference", "offer_metadata", "checkout_session_metadata"} {
		if _, exists := got[key]; exists {
			t.Errorf("unrelated %s present in one-off event: %#v", key, got)
		}
	}
}

func TestSubscriptionEventDataTransactionAndIsolation(t *testing.T) {
	s := testStore(t)
	tenant, _ := eventFixture(t, s, "own")
	other, _, _ := newTenant(t, s)
	tx, err := s.DB.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(context.Background(), `UPDATE subscriptions SET status='past_due',metadata='{"fresh":true,"large":9007199254740993}' WHERE tenant_id=$1 AND id='sub'`, tenant); err != nil {
		t.Fatal(err)
	}
	got, err := subscriptionEventData(context.Background(), tx, tenant, "sub")
	if err != nil {
		t.Fatal(err)
	}
	requireEventCorrelation(t, got, map[string]any{"subscription_id": "sub", "customer_id": "customer", "plan_id": "plan", "status": "past_due", "subscription_metadata": map[string]any{"fresh": true, "large": json.Number("9007199254740993")}, "plan_metadata": map[string]any{"owner": "own"}, "session_id": tenant + "-session", "offer_id": "offer", "checkout_session_metadata": map[string]any{"owner": "own"}})
	if _, err = subscriptionEventData(context.Background(), tx, other, "sub"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("cross tenant read", err)
	}
	if _, err = subscriptionEventData(context.Background(), tx, tenant, "missing"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("missing subscription", err)
	}
}

func TestSubscriptionEventDataPortal(t *testing.T) {
	s := testStore(t)
	tenant, key := eventFixture(t, s, "own")
	eventFixture(t, s, "foreign")
	if _, err := s.DB.Exec(context.Background(), `UPDATE subscriptions SET provider='pagarme',account_fingerprint=$2 WHERE tenant_id=$1`, tenant, hash("sk_test_example")); err != nil {
		t.Fatal(err)
	}
	h := (&API{Store: s, Accounts: Accounts{tenant: {"pagarme": {Secret: "sk_test_example", PublicKey: "pk_test_example", Environment: "test"}}}}).Handler()
	out := request(t, h, "POST", "/v1/subscriptions/sub/portal", key, "portal", `{}`)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body)
	}
	portal := metadataObject(t, out.Body.Bytes())
	r := httptest.NewRequest("POST", portal["path"].(string)+"/cancel", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	out = httptest.NewRecorder()
	h.ServeHTTP(out, r)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body)
	}
	requireEventCorrelation(t, eventData(t, s, tenant, "subscription.canceled"), map[string]any{"subscription_id": "sub", "customer_id": "customer", "plan_id": "plan", "status": "canceled", "subscription_metadata": map[string]any{"owner": "own"}, "plan_metadata": map[string]any{"owner": "own"}})
}

func TestFinancialEventDataRenewalAndManualReview(t *testing.T) {
	for _, credit := range []int{0, 1000} {
		t.Run(fmt.Sprint(credit), func(t *testing.T) {
			s := testStore(t)
			tenant, _ := eventFixture(t, s, "own")
			eventFixture(t, s, "foreign")
			ctx := context.Background()
			if _, err := s.DB.Exec(ctx, `UPDATE subscriptions SET next_at=now(),billing_credit=$2 WHERE tenant_id=$1`, tenant, credit); err != nil {
				t.Fatal(err)
			}
			worker := &Worker{Store: s}
			if err := worker.schedule(ctx); err != nil {
				t.Fatal(err)
			}
			kind, status := "charge.queued", "queued"
			if credit > 0 {
				kind, status = "charge.paid", "paid"
			}
			want := map[string]any{"customer_id": "customer", "subscription_id": "sub", "plan_id": "plan", "status": status, "subscription_metadata": map[string]any{"owner": "own"}, "plan_metadata": map[string]any{"owner": "own"}, "charge_metadata": map[string]any{}}
			got := eventData(t, s, tenant, kind)
			requireEventCorrelation(t, got, want)
			if credit == 0 {
				c, err := worker.claim(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if err = worker.retry(ctx, c, "provider_configuration", true); err != nil {
					t.Fatal(err)
				}
				want["status"] = "manual_review"
				want["reason"] = "provider_configuration"
				requireEventCorrelation(t, eventData(t, s, tenant, "charge.manual_review"), want)
			} else {
				if _, err := s.DB.Exec(ctx, `UPDATE charges SET next_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, got["charge_id"]); err != nil {
					t.Fatal(err)
				}
				c, err := worker.claim(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if err = worker.apply(ctx, c, Payment{Status: "reversed"}); err != nil {
					t.Fatal(err)
				}
				requireEventCorrelation(t, eventData(t, s, tenant, "subscription.billing_credit_review"), map[string]any{"subscription_id": "sub", "customer_id": "customer", "plan_id": "plan", "status": "past_due", "subscription_metadata": map[string]any{"owner": "own"}, "billing_credit_used": float64(1000)})
				want["status"] = "reversed"
				requireEventCorrelation(t, eventData(t, s, tenant, "charge.reversed"), want)
			}
		})
	}
}

func TestSubscriptionEventDataPlanChangeWorker(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprint(manual), func(t *testing.T) {
			s := testStore(t)
			tenant, key := eventFixture(t, s, "own")
			eventFixture(t, s, "foreign")
			ctx := context.Background()
			if _, err := s.DB.Exec(ctx, `INSERT INTO plans(tenant_id,id,name,amount,currency,credits,interval_unit,interval_count,metadata) VALUES($1,'target','Target',2000,'brl',20,'month',1,'{"target":true}')`, tenant); err != nil {
				t.Fatal(err)
			}
			h := (&API{Store: s}).Handler()
			out := request(t, h, "POST", "/v1/subscriptions/sub/change-plan", key, "change", `{"plan_id":"target"}`)
			if out.Code != 200 {
				t.Fatal(out.Code, out.Body)
			}
			change := metadataObject(t, out.Body.Bytes())
			want := map[string]any{"subscription_id": "sub", "customer_id": "customer", "plan_id": "plan", "status": "active", "change_id": change["change_id"], "subscription_metadata": map[string]any{"owner": "own"}, "plan_metadata": map[string]any{"owner": "own"}}
			requireEventCorrelation(t, eventData(t, s, tenant, "subscription.change_pending"), want)
			worker := &Worker{Store: s}
			c, err := worker.claim(ctx)
			if err != nil {
				t.Fatal(err)
			}
			want["charge_id"] = change["charge_id"]
			if manual {
				if err = worker.retry(ctx, c, "provider_configuration", true); err != nil {
					t.Fatal(err)
				}
				// Existing retry events do not carry a change ID.
				delete(want, "change_id")
				want["status"] = "past_due"
				want["reason"] = "provider_configuration"
				requireEventCorrelation(t, eventData(t, s, tenant, "subscription.change_review"), want)
			} else {
				if err = worker.apply(ctx, c, Payment{Status: "paid", ID: "new_pi_private"}); err != nil {
					t.Fatal(err)
				}
				want["plan_id"] = "target"
				want["plan_metadata"] = map[string]any{"target": true}
				requireEventCorrelation(t, eventData(t, s, tenant, "subscription.change_applied"), want)
				if _, err = s.DB.Exec(ctx, `UPDATE charges SET next_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, c.ID); err != nil {
					t.Fatal(err)
				}
				c, err = worker.claim(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if err = worker.apply(ctx, c, Payment{Status: "reversed", ID: "new_pi_private"}); err != nil {
					t.Fatal(err)
				}
				want["status"] = "past_due"
				requireEventCorrelation(t, eventData(t, s, tenant, "subscription.change_review"), want)
			}
		})
	}
}
