package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostgresConsoleAndWebhookIsolation(t *testing.T) {
	s := testStore(t)
	tenant, key, customer := newTenant(t, s)
	other, _, _ := newTenant(t, s)
	vault, _ := NewVault(s, strings.Repeat("ab", 32))
	api := &API{Store: s, Accounts: vault, Vault: vault, AdminSecret: strings.Repeat("m", 64), PublicWebhookBaseURL: "https://payments.example.com"}
	h := api.Handler()
	rlogin := httptest.NewRequest("POST", "/console/login", strings.NewReader(`{"key":"`+key+`"}`))
	rlogin.Header.Set("Origin", "http://example.com")
	rlogin.Header.Set("X-Requested-With", "PaymentHub")
	login := httptest.NewRecorder()
	h.ServeHTTP(login, rlogin)
	if login.Code != 200 {
		t.Fatalf("login %d %s", login.Code, login.Body)
	}
	cookie := login.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe session cookie")
	}
	call := func(method, path, body string, csrf bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(cookie)
		if csrf {
			r.Header.Set("Origin", "http://example.com")
			r.Header.Set("X-Requested-With", "PaymentHub")
		}
		out := httptest.NewRecorder()
		h.ServeHTTP(out, r)
		return out
	}
	base := "/console/api/projects/" + tenant
	if me := call("GET", "/console/api/me", "", false); !bytes.Contains(me.Body.Bytes(), []byte("https://payments.example.com")) {
		t.Fatal("missing public webhook origin")
	}
	body := `{"secret":"sk_test_example","environment":"test","retry_window_seconds":240,"webhook_user":"hook-user","webhook_password":"hook-password"}`
	if got := call("POST", base+"/providers/pagarme", body, false); got.Code != 403 {
		t.Fatalf("csrf %d", got.Code)
	}
	if got := call("POST", base+"/providers/pagarme", body, true); got.Code != 200 {
		t.Fatalf("save %d %s", got.Code, got.Body)
	}
	if got := call("GET", base+"/providers", "", false); got.Code != 200 || bytes.Contains(got.Body.Bytes(), []byte("sk_test_example")) || bytes.Contains(got.Body.Bytes(), []byte("hook-password")) {
		t.Fatal("credential leak", got.Code)
	}
	if got := call("GET", "/console/api/projects/"+other+"/providers", "", false); got.Code != 404 {
		t.Fatal("tenant isolation", got.Code)
	}
	if got := call("GET", base+"/v1/wallets/"+customer, "", false); got.Code != 200 {
		t.Fatal("business bridge", got.Code)
	}
	// An authenticated webhook schedules verification; payload status alone never grants credits.
	_, e := s.DB.Exec(context.Background(), "INSERT INTO charges(tenant_id,id,customer_id,provider,provider_customer,payment_method,amount,currency,credits,provider_id,status,next_at) VALUES($1,'webhook-charge',$2,'pagarme','cus','card',100,'brl',50,'or_test','pending',now()+interval '1 hour')", tenant, customer)
	if e != nil {
		t.Fatal(e)
	}
	payload := `{"id":"hook_test","type":"order.paid","data":{"id":"or_test","status":"paid"}}`
	hook := func(password string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/webhooks/pagarme/"+tenant, strings.NewReader(payload))
		r.SetBasicAuth("hook-user", password)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, r)
		return out
	}
	if got := hook("wrong"); got.Code != 401 {
		t.Fatal("hook auth", got.Code)
	}
	for i := 0; i < 2; i++ {
		if got := hook("hook-password"); got.Code != 200 {
			t.Fatal("hook", got.Code, got.Body)
		}
	}
	var n, balance int
	s.DB.QueryRow(context.Background(), "SELECT count(*) FROM webhook_receipts WHERE tenant_id=$1", tenant).Scan(&n)
	s.DB.QueryRow(context.Background(), "SELECT balance FROM wallets WHERE tenant_id=$1 AND customer_id=$2", tenant, customer).Scan(&balance)
	if n != 1 || balance != 0 {
		t.Fatal("webhook replay or unverified credit", n, balance)
	}
	got := call("GET", base+"/webhooks", "", false)
	var receipts struct {
		Data []json.RawMessage `json:"data"`
	}
	json.Unmarshal(got.Body.Bytes(), &receipts)
	if len(receipts.Data) != 1 {
		t.Fatal("missing webhook feed")
	}
}

func TestPostgresRotateRevokesConsoleSession(t *testing.T) {
	s := testStore(t)
	tenant, _, _ := newTenant(t, s)
	ctx := context.Background()
	session := token()
	_, e := s.DB.Exec(ctx, "INSERT INTO console_sessions(token_hash,tenant_id,expires_at) VALUES($1,$2,now()+interval '1 hour')", hash(session), tenant)
	if e != nil {
		t.Fatal(e)
	}
	admin := strings.Repeat("m", 64)
	h := (&API{Store: s, AdminSecret: admin}).Handler()
	out := request(t, h, "POST", "/admin/tenants/"+tenant+"/rotate-key", admin, "", `{}`)
	if out.Code != 200 {
		t.Fatal(out.Code)
	}
	var count int
	s.DB.QueryRow(ctx, "SELECT count(*) FROM console_sessions WHERE tenant_id=$1", tenant).Scan(&count)
	if count != 0 {
		t.Fatal("rotated project key leaves active console sessions")
	}
}

func TestPostgresNewChargebackEventSchedulesVerification(t *testing.T) {
	s := testStore(t)
	tenant, _, customer := newTenant(t, s)
	ctx := context.Background()
	accounts := Accounts{tenant: {"pagarme": {Secret: "sk_test_example", WebhookUser: "hook-user", WebhookPassword: "hook-password"}}}
	h := (&API{Store: s, Accounts: accounts}).Handler()
	_, e := s.DB.Exec(ctx, "INSERT INTO charges(tenant_id,id,customer_id,provider,provider_customer,payment_method,amount,currency,credits,provider_id,status,next_at) VALUES($1,'chargeback-test',$2,'pagarme','cus','card',100,'brl',50,'or_chargeback','paid',now()+interval '6 hours')", tenant, customer)
	if e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("POST", "/webhooks/pagarme/"+tenant, strings.NewReader(`{"id":"hook_cb_new","type":"chargeback.received","data":{"id":"cb_test"}}`))
	r.SetBasicAuth("hook-user", "hook-password")
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	if out.Code != 200 {
		t.Fatal(out.Code)
	}
	var due bool
	s.DB.QueryRow(ctx, "SELECT next_at<=now() FROM charges WHERE tenant_id=$1 AND id='chargeback-test'", tenant).Scan(&due)
	if !due {
		t.Fatal("new chargeback event did not schedule provider verification")
	}
}
