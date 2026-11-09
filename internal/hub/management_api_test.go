package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostgresManagementAPI(t *testing.T) {
	s := testStore(t)
	tenant, key, _ := newTenant(t, s)
	_, other, _ := newTenant(t, s)
	vault, err := NewVault(s, strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	h := (&API{Store: s, Vault: vault, Accounts: vault}).Handler()
	body := `{"url":"https://example.com/events","enabled":true}`
	if w := request(t, h, "POST", "/v1/notifications/config", key, "", body); w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
	first := request(t, h, "POST", "/v1/notifications/config", key, "notify-create", body)
	replay := request(t, h, "POST", "/v1/notifications/config", key, "notify-create", body)
	if first.Code != 200 || replay.Code != 200 || !bytes.Equal(first.Body.Bytes(), replay.Body.Bytes()) {
		t.Fatal(first.Code, first.Body, replay.Code, replay.Body)
	}
	var result map[string]any
	if err = json.Unmarshal(first.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	secret, ok := result["signing_secret"].(string)
	if !ok || len(secret) != 64 {
		t.Fatal(result)
	}
	var persisted []byte
	if err = s.DB.QueryRow(context.Background(), "SELECT response FROM idempotency WHERE tenant_id=$1 AND key='notify-create'", tenant).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(persisted, []byte(secret)) {
		t.Fatal("plaintext secret in replay storage")
	}
	for _, auth := range []string{key, other} {
		got := request(t, h, "GET", "/v1/notifications/config", auth, "", "")
		if got.Code != 200 || strings.Contains(got.Body.String(), secret) || got.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(got.Code, got.Body)
		}
		if auth == other && strings.Contains(got.Body.String(), "example.com") {
			t.Fatal("cross tenant config")
		}
	}
	if w := request(t, h, "POST", "/v1/notifications/config", key, "notify-create", `{"url":"https://example.com/other","enabled":true}`); w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	reset := request(t, h, "POST", "/v1/notifications/config/reset-secret", key, "reset", "{}")
	again := request(t, h, "POST", "/v1/notifications/config/reset-secret", key, "reset", "{}")
	if reset.Code != 200 || !bytes.Equal(reset.Body.Bytes(), again.Body.Bytes()) || strings.Contains(reset.Body.String(), secret) {
		t.Fatal(reset.Code, reset.Body, again.Body)
	}
	provider := `{"environment":"test","secret":"sk_test_example123","webhook_user":"user","webhook_password":"password"}`
	save := request(t, h, "POST", "/v1/providers/stripe", key, "provider", provider)
	if save.Code != 200 {
		t.Fatal(save.Code, save.Body)
	}
	saveAgain := request(t, h, "POST", "/v1/providers/stripe", key, "provider", provider)
	if saveAgain.Code != 200 || !bytes.Equal(save.Body.Bytes(), saveAgain.Body.Bytes()) {
		t.Fatal(saveAgain.Code, saveAgain.Body)
	}
	preserve := request(t, h, "POST", "/v1/providers/stripe", key, "preserve", `{"environment":"test","retry_window_seconds":120}`)
	if preserve.Code != 200 {
		t.Fatal(preserve.Code, preserve.Body)
	}
	saved, err := vault.Get(context.Background(), tenant, "stripe")
	if err != nil || saved.Secret != "sk_test_example123" || saved.WebhookPassword != "password" {
		t.Fatal("saved credential preservation failed", err)
	}
	loginReq := httptest.NewRequest("POST", "/console/login", strings.NewReader(`{"key":"`+key+`"}`))
	loginReq.Header.Set("Origin", "http://example.com")
	loginReq.Header.Set("X-Requested-With", "PaymentHub")
	login := httptest.NewRecorder()
	h.ServeHTTP(login, loginReq)
	if login.Code != 200 {
		t.Fatal(login.Code, login.Body)
	}
	for _, path := range []string{"notifications/deliveries/1/retry"} {
		req := httptest.NewRequest("POST", "/console/api/projects/"+tenant+"/v1/"+path, strings.NewReader("{}"))
		req.AddCookie(login.Result().Cookies()[0])
		req.Header.Set("Origin", "http://example.com")
		req.Header.Set("X-Requested-With", "PaymentHub")
		req.Header.Set("Idempotency-Key", "proxy")
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		if out.Code != 403 {
			t.Fatal("management console proxy exposed", path, out.Code, out.Body)
		}
	}

	list := request(t, h, "GET", "/v1/providers", key, "", "")
	if list.Code != 200 || strings.Contains(list.Body.String(), "sk_test_example123") || strings.Contains(list.Body.String(), "password") {
		t.Fatal(list.Code, list.Body)
	}
	isolated := request(t, h, "GET", "/v1/providers", other, "", "")
	if isolated.Code != 200 || strings.Contains(isolated.Body.String(), `"configured":true`) {
		t.Fatal(isolated.Code, isolated.Body)
	}
	if w := request(t, h, "POST", "/v1/notifications/deliveries/1/retry", other, "retry", "{}"); w.Code != 404 {
		t.Fatal(w.Code, w.Body)
	}
}
