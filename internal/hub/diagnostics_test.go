package hub

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticRedaction(t *testing.T) {
	var payload any
	json.Unmarshal([]byte(`{"id":"hook_test","type":"order.paid","data":{"id":"or_test","customer":{"email":"secret@example.com","document":"12345678900"},"authentication":{"password":"secret-password"},"amount":1000}}`), &payload)
	b, _ := json.Marshal(redactWebhookValue(payload, "", 0))
	text := string(b)
	for _, secret := range []string{"secret@example.com", "12345678900", "secret-password"} {
		if strings.Contains(text, secret) {
			t.Fatal("sensitive data persisted")
		}
	}
	if !strings.Contains(text, "order.paid") || !strings.Contains(text, "or_test") {
		t.Fatal("event structure lost", text)
	}
}
func TestPostgresDiagnosticModeCapturesWithoutProcessing(t *testing.T) {
	s := testStore(t)
	tenant, _, customer := newTenant(t, s)
	other, _, _ := newTenant(t, s)
	accounts := Accounts{tenant: {"pagarme": {Secret: "sk_test_example", Environment: "test", WebhookUser: "user", WebhookPassword: "password"}}, other: {"pagarme": {Secret: "sk_test_other", Environment: "test", WebhookUser: "user", WebhookPassword: "password"}}}
	api := &API{Store: s, Accounts: accounts, WebhookDiagnosticTenant: tenant, WebhookDiagnosticUntil: time.Now().Add(time.Minute)}
	h := api.Handler()
	ctx := context.Background()
	payload := `{"id":"hook_unverified","type":"order.paid","data":{"id":"or_unverified","customer":{"email":"private@example.com"}}}`
	post := func(id string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/webhooks/pagarme/"+id, strings.NewReader(payload))
		r.Header.Set("X-Secret-Token", "must-not-persist")
		out := httptest.NewRecorder()
		h.ServeHTTP(out, r)
		return out
	}
	if out := post(tenant); out.Code != 200 {
		t.Fatalf("capture %d %s", out.Code, out.Body)
	}
	if out := post(other); out.Code != 401 {
		t.Fatal("bypass leaked to other project", out.Code)
	}
	var captures, receipts, balance int
	var logged []byte
	s.DB.QueryRow(ctx, "SELECT count(*) FROM webhook_diagnostics WHERE tenant_id=$1", tenant).Scan(&captures)
	s.DB.QueryRow(ctx, "SELECT count(*) FROM webhook_receipts WHERE tenant_id=$1", tenant).Scan(&receipts)
	s.DB.QueryRow(ctx, "SELECT balance FROM wallets WHERE tenant_id=$1 AND customer_id=$2", tenant, customer).Scan(&balance)
	s.DB.QueryRow(ctx, "SELECT payload FROM webhook_diagnostics WHERE tenant_id=$1", tenant).Scan(&logged)
	if captures != 1 || receipts != 0 || balance != 0 || strings.Contains(string(logged), "private@example.com") {
		t.Fatal(captures, receipts, balance, string(logged))
	}
	api.WebhookDiagnosticUntil = time.Now().Add(-time.Second)
	if out := post(tenant); out.Code != 401 {
		t.Fatal("expired capture accepted", out.Code)
	}
}

func TestDiagnosticHeadersHideCredentials(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.SetBasicAuth("secret-user", "secret-password")
	r.Header.Set("X-Api-Key", "secret-api-key")
	r.Header.Set("Content-Type", "application/json; secret=secret-token")
	b, _ := json.Marshal(diagnosticHeaders(r))
	for _, value := range []string{"secret-user", "secret-password", "secret-api-key", "secret-token"} {
		if strings.Contains(string(b), value) {
			t.Fatal("header credential persisted")
		}
	}
}
func TestPostgresDiagnosticQuotaAndLiveGuard(t *testing.T) {
	s := testStore(t)
	tenant, _, _ := newTenant(t, s)
	ac := Account{Secret: "sk_test_example", Environment: "test", WebhookUser: "u", WebhookPassword: "p"}
	accounts := Accounts{tenant: {"pagarme": ac}}
	until := time.Now().Add(time.Minute).Truncate(time.Second)
	api := &API{Store: s, Accounts: accounts, WebhookDiagnosticTenant: tenant, WebhookDiagnosticUntil: until}
	h := api.Handler()
	post := func() int {
		r := httptest.NewRequest("POST", "/webhooks/pagarme/"+tenant, strings.NewReader(`{}`))
		out := httptest.NewRecorder()
		h.ServeHTTP(out, r)
		return out.Code
	}
	ac.Environment = "live"
	accounts[tenant]["pagarme"] = ac
	if code := post(); code != 401 {
		t.Fatal("capture permitted for live account")
	}
	ac.Environment = "test"
	accounts[tenant]["pagarme"] = ac
	_, e := s.DB.Exec(context.Background(), "INSERT INTO webhook_diagnostics(tenant_id,capture_until,authenticated,headers,payload,body_bytes) SELECT $1,$2,false,'{}','{}',0 FROM generate_series(1,20)", tenant, until)
	if e != nil {
		t.Fatal(e)
	}
	if code := post(); code != 401 {
		t.Fatal("capture quota exceeded", code)
	}
}
