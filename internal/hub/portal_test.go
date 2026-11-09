package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestPortalOriginBoundary(t *testing.T) {
	for _, tc := range []struct {
		origin, proto, content string
		ok                     bool
	}{{"https://store.example", "https", "application/json", true}, {"https://evil.example", "https", "application/json", false}, {"null", "https", "application/json", false}, {"", "", "application/json", true}, {"", "", "text/plain", false}} {
		r := httptest.NewRequest("POST", "http://store.example/portal/x/card", nil)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("X-Forwarded-Proto", tc.proto)
		r.Header.Set("Content-Type", tc.content)
		if portalSameOrigin(r) != tc.ok {
			t.Fatal(tc)
		}
	}
	var in portalCardInput
	for _, raw := range []string{`{"card_number":"4111111111111111"}`, `{"card_token":"token_x","buyer":{"cvv":"123"}}`, `{"card_token":"token_x","cvv":"123"}`} {
		if decode(httptest.NewRequest("POST", "/", strings.NewReader(raw)), &in) == nil {
			t.Fatal("raw card accepted", raw)
		}
	}
}
func TestPostgresPortalScopeExpiryCancellationAndCard(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	tenant, _, customer := newTenant(t, s)
	other, _, otherCustomer := newTenant(t, s)
	for _, v := range []struct{ tenant, customer, sub string }{{tenant, customer, "sub_a"}, {tenant, customer, "sub_b"}, {other, otherCustomer, "sub_other"}} {
		if _, e := s.DB.Exec(ctx, `INSERT INTO plans(tenant_id,id,name,amount,currency,credits,interval_unit,interval_count) VALUES($1,$2,'Plano',2500,'brl',50,'month',1)`, v.tenant, v.sub); e != nil {
			t.Fatal(e)
		}
		if _, e := s.DB.Exec(ctx, `INSERT INTO subscriptions(tenant_id,id,customer_id,plan_id,provider,provider_customer,payment_method,account_fingerprint,anchor,next_at) VALUES($1,$2,$3,$2,'pagarme','cus_private','card_old',$4,now(),now()+interval '1 month')`, v.tenant, v.sub, v.customer, hash("sk_test_portal")); e != nil {
			t.Fatal(e)
		}
	}
	_, e := s.DB.Exec(ctx, `INSERT INTO charges(tenant_id,id,customer_id,provider,provider_customer,payment_method,amount,currency,credits,subscription_id,status,credited) VALUES($1,'charge_existing',$2,'pagarme','cus_private','card_old',2500,'brl',50,'sub_a','paid',true)`, tenant, customer)
	if e != nil {
		t.Fatal(e)
	}
	providerCalls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls++
		if r.URL.Path != "/customers/cus_private/cards" {
			t.Error(r.URL.Path)
		}
		var v map[string]any
		if json.NewDecoder(r.Body).Decode(&v) != nil || v["token"] != "token_portal" || v["billing_address"] == nil {
			t.Error("provider payload", v)
		}
		respond(w, 200, map[string]string{"id": "card_new"})
	}))
	defer provider.Close()
	g := NewGateway()
	g.pagarmeURL = provider.URL
	a := &API{Store: s, CheckoutGateway: g, Accounts: Accounts{tenant: {"pagarme": {Secret: "sk_test_portal", PublicKey: "pk_test_portal", Environment: "test"}}}}
	mux := http.NewServeMux()
	a.portalRoutes(mux)
	issue := func(sub string) string {
		t.Helper()
		tx, e := s.DB.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback(ctx)
		v, e := a.createPortalSession(httptest.NewRequest("POST", "/", nil), tx, tenant, sub)
		if e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
		return v.(map[string]any)["path"].(string)
	}
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path+"/data", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	post := func(path, action, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path+action, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	old := issue("sub_a")
	path := issue("sub_a")
	if get(old).Code != 410 {
		t.Fatal("old link not revoked")
	}
	if out := get(path); out.Code != 200 || strings.Contains(out.Body.String(), "cus_private") || strings.Contains(out.Body.String(), "card_old") || strings.Contains(out.Body.String(), "sk_test") {
		t.Fatal(out.Code, out.Body.String())
	}
	if post(path, "/card", `{"card_number":"4111111111111111"}`).Code != 400 || providerCalls != 0 {
		t.Fatal("raw card accepted")
	}
	if _, e = s.DB.Exec(ctx, `UPDATE subscriptions SET status='past_due' WHERE tenant_id=$1 AND id='sub_a'`, tenant); e != nil {
		t.Fatal(e)
	}
	card := `{"card_token":"token_portal","recurring_consent":true,"buyer":{"name":"Cliente Teste","email":"teste@example.com","document":"12345678909","phone":"11999999999","street":"Rua Teste","number":"123","zip_code":"01001000","city":"São Paulo","state":"SP"}}`
	if out := post(path, "/card", card); out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	var status, method string
	var consent bool
	if e = s.DB.QueryRow(ctx, `SELECT status,payment_method FROM subscriptions WHERE tenant_id=$1 AND id='sub_a'`, tenant).Scan(&status, &method); e != nil || status != "past_due" || method != "card_new" {
		t.Fatal(status, method, e)
	}
	if e = s.DB.QueryRow(ctx, `SELECT consent_at IS NOT NULL FROM portal_sessions WHERE id=$1`, strings.TrimPrefix(path, "/portal/")).Scan(&consent); e != nil || !consent {
		t.Fatal("consent missing", e)
	}
	if _, e = s.DB.Exec(ctx, `INSERT INTO checkout_settings(tenant_id,config) VALUES($1,'{"buyer_cancel":false,"buyer_update_card":false}')`, tenant); e != nil {
		t.Fatal(e)
	}
	if post(path, "/cancel", `{}`).Code != 403 || post(path, "/card", card).Code != 403 || providerCalls != 1 {
		t.Fatal("live settings ignored")
	}
	if _, e = s.DB.Exec(ctx, `DELETE FROM checkout_settings WHERE tenant_id=$1`, tenant); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if out := post(path, "/cancel", `{}`); out.Code != 200 {
				t.Error(out.Code, out.Body.String())
			}
		}()
	}
	wg.Wait()
	if post(path, "/card", card).Code != 403 {
		t.Fatal("canceled card update accepted")
	}
	if e = s.DB.QueryRow(ctx, `SELECT status FROM subscriptions WHERE tenant_id=$1 AND id='sub_b'`, tenant).Scan(&status); e != nil || status != "active" {
		t.Fatal("other subscription changed", status, e)
	}
	var credited bool
	if e = s.DB.QueryRow(ctx, `SELECT payment_method,credited FROM charges WHERE tenant_id=$1 AND id='charge_existing'`, tenant).Scan(&method, &credited); e != nil || method != "card_old" || !credited {
		t.Fatal("existing charge changed", e)
	}
	for range 3 {
		post(path, "/cancel", `{}`)
	}
	if post(path, "/cancel", `{}`).Code != 410 {
		t.Fatal("attempt limit ignored")
	}
	path = issue("sub_b")
	if _, e = s.DB.Exec(ctx, `UPDATE portal_sessions SET expires_at=now()-interval '1 second' WHERE id=$1`, strings.TrimPrefix(path, "/portal/")); e != nil {
		t.Fatal(e)
	}
	if get(path).Code != 410 || post(path, "/cancel", `{}`).Code != 410 {
		t.Fatal("expiration ignored")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if _, e = a.createPortalSession(httptest.NewRequest("POST", "/", nil), tx, tenant, "sub_other"); e == nil {
		t.Fatal("cross tenant session allowed")
	}
}
