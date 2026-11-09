package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestPostgresCheckoutSnapshotAndIsolation(t *testing.T) {
	s := testStore(t)
	tenant, key, customer := newTenant(t, s)
	_, other, _ := newTenant(t, s)
	a := &API{Store: s, Accounts: Accounts{tenant: {"pagarme": {Secret: "sk_test_example", Environment: "test", PublicKey: "pk_test_example"}}}}
	h := a.Handler()
	create := request(t, h, "POST", "/v1/offers", key, "offer", `{"name":"Créditos Pro","description":"100 créditos","amount":2990,"currency":"brl","credits":100}`)
	if create.Code != 200 {
		t.Fatalf("offer %d %s", create.Code, create.Body.String())
	}
	var offer map[string]any
	json.Unmarshal(create.Body.Bytes(), &offer)
	payload := `{"offer_id":"` + offer["id"].(string) + `","customer_id":"` + customer + `","reference":"order-1"}`
	session := request(t, h, "POST", "/v1/checkout-sessions", key, "session", payload)
	if session.Code != 200 {
		t.Fatalf("session %d %s", session.Code, session.Body.String())
	}
	var link map[string]any
	json.Unmarshal(session.Body.Bytes(), &link)
	if got := request(t, h, "POST", "/v1/branding", key, "brand-after", `{"name":"Nova marca","description":"","logo_url":"","primary_color":"#123456","background_color":"#101720"}`); got.Code != 200 {
		t.Fatal("branding", got.Code)
	}
	replay := request(t, h, "POST", "/v1/checkout-sessions", key, "session", payload)
	if replay.Body.String() != session.Body.String() {
		t.Fatal("non-idempotent session")
	}
	r := httptest.NewRequest("GET", link["path"].(string)+"/data", nil)
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	if out.Code != 200 || !strings.Contains(out.Body.String(), `"amount":2990`) || strings.Contains(out.Body.String(), "sk_test") || strings.Contains(out.Body.String(), "Nova marca") {
		t.Fatal(out.Code, out.Body.String())
	}
	if got := request(t, h, "GET", "/v1/checkout-sessions/"+link["id"].(string), other, "", ""); got.Code != 404 {
		t.Fatal("tenant leak", got.Code)
	}
	bad := request(t, h, "POST", "/v1/checkout-sessions", key, "tamper", strings.TrimSuffix(payload, "}")+`,"amount":1}`)
	if bad.Code != 400 {
		t.Fatal("price tampering", bad.Code)
	}
	req := httptest.NewRequest(http.MethodPost, link["path"].(string)+"/pay", strings.NewReader(`{"card_number":"4111111111111111"}`))
	req.Header.Set("Content-Type", "application/json")
	out = httptest.NewRecorder()
	h.ServeHTTP(out, req)
	if out.Code != 400 {
		t.Fatal("raw card accepted", out.Code)
	}
}

func TestPostgresCheckoutPaymentAndRecurrence(t *testing.T) {
	s := testStore(t)
	tenant, key, customer := newTenant(t, s)
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if u, p, ok := r.BasicAuth(); !ok || u != "sk_test_example" || p != "" {
			t.Error("provider authentication")
		}
		if strings.HasSuffix(r.URL.Path, "/cards") {
			respond(w, 200, map[string]string{"id": "card_checkout"})
		} else {
			respond(w, 200, map[string]string{"id": "cus_checkout"})
		}
	}))
	defer provider.Close()
	gateway := NewGateway()
	gateway.pagarmeURL = provider.URL
	a := &API{Store: s, CheckoutGateway: gateway, Accounts: Accounts{tenant: {"pagarme": {Secret: "sk_test_example", Environment: "test", PublicKey: "pk_test_example"}}}}
	h := a.Handler()
	plan := request(t, h, "POST", "/v1/plans", key, "plan", `{"name":"Mensal","amount":4900,"currency":"brl","credits":50,"interval_unit":"month","interval_count":1}`)
	var v map[string]any
	json.Unmarshal(plan.Body.Bytes(), &v)
	offer := request(t, h, "POST", "/v1/offers", key, "offer", `{"name":"Plano Pro","description":"Acesso mensal","plan_id":"`+v["id"].(string)+`"}`)
	json.Unmarshal(offer.Body.Bytes(), &v)
	session := request(t, h, "POST", "/v1/checkout-sessions", key, "session", `{"offer_id":"`+v["id"].(string)+`","customer_id":"`+customer+`","reference":"purchase-1"}`)
	json.Unmarshal(session.Body.Bytes(), &v)
	pay := func(consent bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", v["path"].(string)+"/pay", strings.NewReader(fmt.Sprintf(`{"card_token":"token_checkout","recurring_consent":%t,"buyer":{"name":"Cliente Teste","email":"teste@example.com","document":"12345678909","phone":"11999999999","street":"Rua Teste","number":"123","zip_code":"01001000","city":"São Paulo","state":"SP"}}`, consent)))
		r.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		h.ServeHTTP(out, r)
		return out
	}
	if got := pay(false); got.Code != 400 || calls != 0 {
		t.Fatal("consent not enforced", got.Code, calls)
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := pay(true); got.Code != 200 {
				t.Error("pay", got.Code, got.Body.String())
			}
		}()
	}
	wg.Wait()
	var n, balance int
	s.DB.QueryRow(context.Background(), "SELECT count(*) FROM charges WHERE tenant_id=$1", tenant).Scan(&n)
	s.DB.QueryRow(context.Background(), "SELECT balance FROM wallets WHERE tenant_id=$1 AND customer_id=$2", tenant, customer).Scan(&balance)
	if n != 1 || balance != 0 || calls != 2 {
		t.Fatal("duplicate/unverified payment", n, balance, calls)
	}
	var cycle int
	s.DB.QueryRow(context.Background(), "SELECT cycle FROM subscriptions WHERE tenant_id=$1", tenant).Scan(&cycle)
	if cycle != 1 {
		t.Fatal("first cycle duplicate")
	}
	var consent bool
	s.DB.QueryRow(context.Background(), "SELECT consent_at IS NOT NULL FROM checkout_sessions WHERE id=$1", v["id"]).Scan(&consent)
	if !consent {
		t.Fatal("consent not persisted")
	}
	worker := &Worker{Store: s, Accounts: a.Accounts, Gateway: &fakeProvider{calls: map[string]int{}, status: "paid"}}
	if e := worker.Tick(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := worker.Tick(context.Background()); e != nil {
		t.Fatal(e)
	}
	s.DB.QueryRow(context.Background(), "SELECT balance FROM wallets WHERE tenant_id=$1 AND customer_id=$2", tenant, customer).Scan(&balance)
	if balance != 50 {
		t.Fatal("confirmed checkout credit", balance)
	}

}

func TestCheckoutBrandingValidation(t *testing.T) {
	b := Branding{Name: "Minha loja", Primary: "#112233", Background: "#101720", LogoURL: "https://example.com/logo.png"}
	if !validBranding(b) {
		t.Fatal("valid brand")
	}
	for _, bad := range []string{"javascript:alert(1)", "data:image/svg+xml,test", "http://example.com/logo.png"} {
		b.LogoURL = bad
		if validBranding(b) {
			t.Fatal("unsafe logo", bad)
		}
	}
	b.LogoURL = ""
	b.Primary = "red; background:url(test)"
	if validBranding(b) {
		t.Fatal("css injection")
	}
}

func TestPostgresCheckoutExpiryAndProviderFailure(t *testing.T) {
	s := testStore(t)
	tenant, key, customer := newTenant(t, s)
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		respond(w, 422, map[string]string{"error": "private provider details"})
	}))
	defer provider.Close()
	g := NewGateway()
	g.pagarmeURL = provider.URL
	a := &API{Store: s, CheckoutGateway: g, Accounts: Accounts{tenant: {"pagarme": {Secret: "sk_test_example", Environment: "test", PublicKey: "pk_test_example"}}}}
	h := a.Handler()
	offer := request(t, h, "POST", "/v1/offers", key, "offer", `{"name":"Teste","description":"","amount":1000,"currency":"brl","credits":1}`)
	var v map[string]any
	json.Unmarshal(offer.Body.Bytes(), &v)
	session := request(t, h, "POST", "/v1/checkout-sessions", key, "session", `{"offer_id":"`+v["id"].(string)+`","customer_id":"`+customer+`","reference":"failure-1"}`)
	json.Unmarshal(session.Body.Bytes(), &v)
	id := v["id"].(string)
	post := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", v["path"].(string)+"/pay", strings.NewReader(`{"card_token":"token_test","buyer":{"name":"Teste Cliente","email":"teste@example.com","document":"12345678909","phone":"11999999999","street":"Rua Teste","number":"10","zip_code":"01001000","city":"São Paulo","state":"SP"}}`))
		r.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		h.ServeHTTP(out, r)
		return out
	}
	if out := post(); out.Code != 502 || strings.Contains(out.Body.String(), "private provider") {
		t.Fatal(out.Code, out.Body.String())
	}
	var n int
	s.DB.QueryRow(context.Background(), "SELECT count(*) FROM charges WHERE tenant_id=$1", tenant).Scan(&n)
	if n != 0 {
		t.Fatal("charge after setup failure")
	}
	s.DB.Exec(context.Background(), "UPDATE checkout_sessions SET attempts=10 WHERE id=$1", id)
	if out := post(); out.Code != 429 || calls != 1 {
		t.Fatal("attempt limit", out.Code, calls)
	}
	s.DB.Exec(context.Background(), "UPDATE checkout_sessions SET expires_at=now()-interval '1 minute' WHERE id=$1", id)
	if out := post(); out.Code != 410 {
		t.Fatal("expired payment", out.Code)
	}
	r := httptest.NewRequest("GET", v["path"].(string)+"/data", nil)
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	if out.Code != 410 {
		t.Fatal("expired session", out.Code)
	}
}

func TestCheckoutMetadataSnapshotAndOfferIDs(t *testing.T) {
	s := testStore(t)
	tenant, key, customer := newTenant(t, s)
	_, other, _ := newTenant(t, s)
	h := (&API{Store: s, Accounts: Accounts{tenant: {"pagarme": {Secret: "sk_test", PublicKey: "pk_test"}}}}).Handler()
	payload := `{"name":"Recurring","amount":100,"currency":"brl","interval_unit":"month","interval_count":1,"metadata":{"local_id":"42"}}`
	out := request(t, h, "POST", "/v1/offers", key, "metadata-offer", payload)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body)
	}
	offer := metadataObject(t, out.Body.Bytes())
	requireMetadata(t, offer, `{"local_id":"42"}`)
	planID, ok := offer["plan_id"].(string)
	if !ok || planID == "" || planID == offer["id"] {
		t.Fatal("missing generated plan ID", offer)
	}
	plan := request(t, h, "GET", "/v1/plans/"+planID, key, "", "")
	if plan.Code != 200 {
		t.Fatal(plan.Body)
	}
	if replay := request(t, h, "POST", "/v1/offers", key, "metadata-offer", payload); replay.Body.String() != out.Body.String() {
		t.Fatal("offer replay changed")
	}
	for i, metadata := range []string{"", `,"metadata":{"purchase":"order-1"}`} {
		payload := fmt.Sprintf(`{"offer_id":%q,"customer_id":%q,"reference":"snapshot-%d"%s}`, offer["id"], customer, i, metadata)
		out = request(t, h, "POST", "/v1/checkout-sessions", key, fmt.Sprintf("metadata-session-%d", i), payload)
		if out.Code != 200 {
			t.Fatal(out.Code, out.Body)
		}
		session := metadataObject(t, out.Body.Bytes())
		replay := request(t, h, "POST", "/v1/checkout-sessions", key, fmt.Sprintf("metadata-session-%d", i), payload)
		if replay.Code != 200 || replay.Body.String() != out.Body.String() {
			t.Fatal("session replay changed", replay.Body)
		}
		want := `{}`
		if i == 1 {
			want = `{"purchase":"order-1"}`
		}
		requireMetadata(t, session, want)
		if _, err := s.DB.Exec(context.Background(), `UPDATE offers SET metadata='{"changed":true}' WHERE tenant_id=$1 AND id=$2`, tenant, offer["id"]); err != nil {
			t.Fatal(err)
		}
		get := request(t, h, "GET", "/v1/checkout-sessions/"+session["id"].(string), key, "", "")
		requireMetadata(t, metadataObject(t, get.Body.Bytes()), want)
		if leak := request(t, h, "GET", "/v1/checkout-sessions/"+session["id"].(string), other, "", ""); leak.Code != 404 {
			t.Fatal("tenant leak", leak.Body)
		}
	}

	list := request(t, h, "GET", "/v1/checkout-sessions", key, "", "")
	if list.Code != 200 {
		t.Fatal(list.Code, list.Body)
	}
	items := metadataObject(t, list.Body.Bytes())["data"].([]any)
	if len(items) != 2 {
		t.Fatal("duplicate sessions", items)
	}
	for _, item := range items {
		if _, ok := item.(map[string]any)["metadata"].(map[string]any); !ok {
			t.Fatal("missing session metadata", item)
		}
	}
	list = request(t, h, "GET", "/v1/checkout-sessions", other, "", "")
	if list.Code != 200 || len(metadataObject(t, list.Body.Bytes())["data"].([]any)) != 0 {
		t.Fatal("tenant session list leak", list.Body)
	}
}
