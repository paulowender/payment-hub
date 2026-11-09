package hub

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
)

type notificationTransport func(*http.Request) (*http.Response, error)

func (f notificationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNotificationSSRF(t *testing.T) {
	for _, raw := range []string{"http://example.com", "https://u:p@example.com", "https://127.0.0.1", "https://[::1]", "https://169.254.169.254", "https://100.100.100.200", "https://192.0.2.1", "https://example.com:8080"} {
		if notificationURL(raw) == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, ip := range []string{"::ffff:127.0.0.1", "fc00::1", "2001:db8::1", "64:ff9b::7f00:1"} {
		if notificationPublicIP(netip.MustParseAddr(ip)) {
			t.Errorf("accepted %s", ip)
		}
	}
	if notificationURL("https://example.com/hook") != nil {
		t.Fatal("valid endpoint rejected")
	}
	// Actual dial must block private DNS answers, independent of configuration checks.
	client := notificationHTTPClient()
	_, err := client.Get("https://localhost/")
	if err == nil {
		t.Fatal("localhost dial allowed")
	}
	if client.CheckRedirect(&http.Request{}, nil) != http.ErrUseLastResponse {
		t.Fatal("redirect allowed")
	}
}
func TestNotificationDurableSigningAndIsolation(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	tenant, _ := eventFixture(t, s, "own")
	other, _, _ := newTenant(t, s)
	vault, err := NewVault(s, strings.Repeat("12", 32))
	if err != nil {
		t.Fatal(err)
	}
	api := &API{Store: s, Vault: vault}
	call := func(tenant, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/", strings.NewReader(body))
		w := httptest.NewRecorder()
		api.notificationAPI(w, r, tenant, strings.Split(path, "/"))
		return w
	}
	w := call(tenant, "POST", "config", `{"url":"https://example.com/hooks","enabled":true}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var config map[string]any
	if err = json.Unmarshal(w.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	secret := config["signing_secret"].(string)
	if strings.Contains(call(tenant, "GET", "config", "").Body.String(), secret) {
		t.Fatal("secret exposed")
	}
	if strings.Contains(call(tenant, "POST", "config", `{"url":"https://example.com/hooks","enabled":true}`).Body.String(), "signing_secret") {
		t.Fatal("edit exposed secret")
	}
	encrypted := []byte{}
	if err = s.DB.QueryRow(ctx, "SELECT encrypted_secret FROM notification_configs WHERE tenant_id=$1", tenant).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte(secret)) {
		t.Fatal("plaintext stored")
	}
	if _, err = vault.open(other+":outbound", encrypted); err == nil {
		t.Fatal("cross tenant decryption")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var id int64
	if err = emitFinancialEvent(ctx, tx, tenant, "charge.paid", "charge", nil); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, "SELECT id FROM events WHERE tenant_id=$1", tenant).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	rollbackTx, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = emitFinancialEvent(ctx, rollbackTx, tenant, "charge.failed", "charge", nil); err != nil {
		t.Fatal(err)
	}
	if err = rollbackTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var queuedCount int
	if err = s.DB.QueryRow(ctx, "SELECT count(*) FROM notification_deliveries WHERE tenant_id=$1", tenant).Scan(&queuedCount); err != nil || queuedCount != 1 {
		t.Fatal("outbox escaped transaction", queuedCount, err)
	}
	// Rotation must not invalidate queued signatures.
	if w = call(tenant, "POST", "config/reset-secret", ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var bodies [][]byte
	var ids []string
	calls := 0
	client := &http.Client{Transport: notificationTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		ids = append(ids, r.Header.Get("X-PaymentHub-Event-ID"))
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(r.Header.Get("X-PaymentHub-Timestamp") + "."))
		mac.Write(body)
		if r.Header.Get("X-PaymentHub-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
			t.Error("invalid signature")
		}
		code := 503
		if calls > 1 {
			code = 204
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("secret response never retained")), Header: make(http.Header)}, nil
	})}
	d := &NotificationDispatcher{Store: s, Vault: vault, Client: client}
	if err = d.DispatchNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	var status, message string
	var attempts int
	if err = s.DB.QueryRow(ctx, "SELECT status,attempts,last_error FROM notification_deliveries WHERE tenant_id=$1 AND event_id=$2", tenant, id).Scan(&status, &attempts, &message); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || attempts != 1 || message != "endpoint_rejected" {
		t.Fatalf("bad retry state %s %d %s", status, attempts, message)
	}
	if w = call(other, "POST", "deliveries/"+strconv.FormatInt(id, 10)+"/retry", ""); w.Code != 404 {
		t.Fatalf("cross tenant retry %d", w.Code)
	}
	if w = call(tenant, "POST", "deliveries/"+strconv.FormatInt(id, 10)+"/retry", ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if err = d.DispatchNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) || ids[0] != ids[1] || ids[0] != strconv.FormatInt(id, 10) {
		t.Fatal("retry changed immutable event")
	}
	var envelope map[string]any
	if err = json.Unmarshal(bodies[0], &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["tenant_id"] != tenant || envelope["version"] != float64(1) || envelope["created_at"] == nil {
		t.Fatal("incomplete envelope")
	}
	requireEventCorrelation(t, envelope["data"].(map[string]any), map[string]any{"charge_id": "charge", "status": "paid", "charge_metadata": map[string]any{"owner": "own"}, "subscription_metadata": map[string]any{"owner": "own"}, "plan_metadata": map[string]any{"owner": "own"}, "checkout_session_metadata": map[string]any{"owner": "own"}})
	// Exhaustion persists a dead letter; manual retry is still possible.
	if _, err = s.DB.Exec(ctx, "UPDATE notification_deliveries SET status='failed',attempts=7,next_at=now() WHERE tenant_id=$1", tenant); err != nil {
		t.Fatal(err)
	}
	d.Client = &http.Client{Transport: notificationTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}
	if err = d.DispatchNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRow(ctx, "SELECT status,attempts FROM notification_deliveries WHERE tenant_id=$1", tenant).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "dead" || attempts != 8 {
		t.Fatalf("not dead: %s/%d", status, attempts)
	}
}

func TestNotificationMetadata(t *testing.T) {
	s := testStore(t)
	tenant, key := eventFixture(t, s, "own")
	eventFixture(t, s, "foreign")
	vault, err := NewVault(s, strings.Repeat("12", 32))
	if err != nil {
		t.Fatal(err)
	}
	h := (&API{Store: s, Vault: vault, Accounts: Accounts{tenant: {"stripe": {Secret: "sk_test_example", Environment: "test"}}}}).Handler()
	out := request(t, h, "POST", "/v1/notifications/config", key, "config", `{"url":"https://example.com/hook","enabled":true}`)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body)
	}
	out = request(t, h, "POST", "/v1/charges", key, "queued", `{"customer_id":"customer","provider":"stripe","provider_customer":"cus_private","payment_method":"pm_private","amount":1000,"currency":"brl","metadata":{"purchase":"notify"}}`)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body)
	}
	var raw []byte
	if err = s.DB.QueryRow(context.Background(), `SELECT payload FROM notification_deliveries WHERE tenant_id=$1`, tenant).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	envelope := metadataObject(t, raw)
	requireEventCorrelation(t, envelope["data"].(map[string]any), map[string]any{"customer_id": "customer", "status": "queued", "charge_metadata": map[string]any{"purchase": "notify"}, "customer_metadata": map[string]any{"owner": "own", "nested": map[string]any{"keep": true}}})
	if envelope["tenant_id"] != tenant || envelope["type"] != "charge.queued" || envelope["version"] != float64(1) {
		t.Fatal(envelope)
	}
	if _, err = s.DB.Exec(context.Background(), `UPDATE customers SET metadata='{"changed":true}' WHERE tenant_id=$1`, tenant); err != nil {
		t.Fatal(err)
	}
	var after []byte
	if err = s.DB.QueryRow(context.Background(), `SELECT payload FROM notification_deliveries WHERE tenant_id=$1`, tenant).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if string(after) != string(raw) {
		t.Fatal("queued snapshot changed")
	}
}
