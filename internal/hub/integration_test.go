package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL required for PostgreSQL integration tests")
	}
	admin, e := Open(context.Background(), url)
	if e != nil {
		t.Fatal(e)
	}
	name := "test_" + token()[:16]
	if _, e = admin.DB.Exec(context.Background(), "CREATE SCHEMA "+name); e != nil {
		t.Fatal(e)
	}
	cfg, e := pgxpool.ParseConfig(url)
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = name
	pool, e := pgxpool.NewWithConfig(context.Background(), cfg)
	if e != nil {
		t.Fatal(e)
	}
	s := &Store{DB: pool}
	t.Cleanup(func() {
		pool.Close()
		admin.DB.Exec(context.Background(), "DROP SCHEMA "+name+" CASCADE")
		admin.DB.Close()
	})
	if e = s.Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	return s
}
func newTenant(t *testing.T, s *Store) (string, string, string) {
	t.Helper()
	ctx := context.Background()
	id, key, customer := token(), token(), token()
	if _, e := s.DB.Exec(ctx, "INSERT INTO tenants(id,name,api_hash) VALUES($1,'test',$2)", id, hash(key)); e != nil {
		t.Fatal(e)
	}
	if _, e := s.DB.Exec(ctx, "INSERT INTO customers(tenant_id,id,external_id) VALUES($1,$2,$2)", id, customer); e != nil {
		t.Fatal(e)
	}
	if _, e := s.DB.Exec(ctx, "INSERT INTO wallets(tenant_id,customer_id) VALUES($1,$2)", id, customer); e != nil {
		t.Fatal(e)
	}
	return id, key, customer
}
func request(t *testing.T, h http.Handler, method, path, key, idem, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("Idempotency-Key", idem)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestPostgresAPIIsolationAndIdempotency(t *testing.T) {
	s := testStore(t)
	tenant, key, customer := newTenant(t, s)
	_, other, _ := newTenant(t, s)
	h := (&API{Store: s, Accounts: Accounts{tenant: {"stripe": {Secret: "sk_test"}}}}).Handler()
	body := `{"external_id":"user-1"}`
	a := request(t, h, "POST", "/v1/customers", key, "create-1", body)
	b := request(t, h, "POST", "/v1/customers", key, "create-1", body)
	if a.Code != 200 || b.Code != 200 || !bytes.Equal(a.Body.Bytes(), b.Body.Bytes()) {
		t.Fatalf("%d %s / %d %s", a.Code, a.Body, b.Code, b.Body)
	}
	if got := request(t, h, "POST", "/v1/customers", key, "create-1", `{"external_id":"other"}`).Code; got != 409 {
		t.Fatal(got)
	}
	if got := request(t, h, "GET", "/v1/wallets/"+customer, other, "", "").Code; got != 404 {
		t.Fatal(got)
	}
	if got := request(t, h, "POST", "/v1/charges", other, "charge-1", fmt.Sprintf(`{"customer_id":%q,"provider":"stripe","provider_customer":"cus_x","payment_method":"pm_x","amount":100,"currency":"brl"}`, customer)).Code; got != 400 {
		t.Fatal(got)
	}
}
func TestPostgresConcurrentWalletAndImmutableLedger(t *testing.T) {
	s := testStore(t)
	tenant, key, customer := newTenant(t, s)
	h := (&API{Store: s}).Handler()
	path := "/v1/wallets/" + customer
	grant := request(t, h, "POST", path+"/grant", key, "grant", `{"credits":100,"reference":"bonus"}`)
	if grant.Code != 200 {
		t.Fatal(grant.Body)
	}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes <- request(t, h, "POST", path+"/consume", key, fmt.Sprint(i), fmt.Sprintf(`{"credits":80,"reference":"req-%d"}`, i)).Code
		}(i)
	}
	wg.Wait()
	close(codes)
	success, conflict := 0, 0
	for code := range codes {
		if code == 200 {
			success++
		}
		if code == 409 {
			conflict++
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	var balance int64
	s.DB.QueryRow(context.Background(), "SELECT balance FROM wallets WHERE tenant_id=$1 AND customer_id=$2", tenant, customer).Scan(&balance)
	if balance != 20 {
		t.Fatal(balance)
	}
	if _, err := s.DB.Exec(context.Background(), "UPDATE ledger SET delta=999 WHERE tenant_id=$1", tenant); err == nil {
		t.Fatal("ledger changed")
	}
}
func TestPostgresCrossTenantForeignKey(t *testing.T) {
	s := testStore(t)
	one, _, customer := newTenant(t, s)
	two, _, _ := newTenant(t, s)
	_, err := s.DB.Exec(context.Background(), "INSERT INTO charges(tenant_id,id,customer_id,provider,provider_customer,payment_method,amount,currency,credits) VALUES($1,$2,$3,'stripe','cus','pm',100,'brl',0)", two, token(), customer)
	if err == nil || one == two {
		t.Fatal("cross tenant charge allowed")
	}
}

type fakeProvider struct {
	mu     sync.Mutex
	calls  map[string]int
	status string
	fail   bool
}

func (f *fakeProvider) Create(ctx context.Context, a Account, c Charge) (Payment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[c.ID]++
	if f.fail {
		return Payment{}, fmt.Errorf("provider_transport_error")
	}
	return Payment{ID: "pi_" + c.ID, Status: f.status}, nil
}
func (f *fakeProvider) Fetch(ctx context.Context, a Account, c Charge) (Payment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return Payment{ID: c.ProviderID, Status: f.status}, nil
}
func TestPostgresWorkerPaymentReversalAndLease(t *testing.T) {
	s := testStore(t)
	tenant, key, customer := newTenant(t, s)
	accounts := Accounts{tenant: {"stripe": {Secret: "sk_test"}}}
	h := (&API{Store: s, Accounts: accounts}).Handler()
	out := request(t, h, "POST", "/v1/charges", key, "buy", fmt.Sprintf(`{"customer_id":%q,"provider":"stripe","provider_customer":"cus_x","payment_method":"pm_x","amount":2000,"currency":"brl","credits":100}`, customer))
	if out.Code != 200 {
		t.Fatal(out.Body)
	}
	f := &fakeProvider{calls: map[string]int{}, status: "paid"}
	w := &Worker{Store: s, Accounts: accounts, Gateway: f}
	ctx := context.Background()
	c, err := w.claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.claim(ctx); err != pgx.ErrNoRows {
		t.Fatal("active lease reclaimed", err)
	}
	if err = w.process(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err = w.apply(ctx, c, Payment{ID: "pi_" + c.ID, Status: "paid"}); err != nil {
		t.Fatal(err)
	}
	var count, balance int64
	s.DB.QueryRow(ctx, "SELECT count(*) FROM ledger WHERE tenant_id=$1 AND reason='payment'", tenant).Scan(&count)
	if count != 1 {
		t.Fatal(count)
	}
	out = request(t, h, "POST", "/v1/wallets/"+customer+"/consume", key, "use", `{"credits":80,"reference":"usage"}`)
	if out.Code != 200 {
		t.Fatal(out.Body)
	}
	f.status = "reversed"
	s.DB.Exec(ctx, "UPDATE charges SET next_at=now() WHERE tenant_id=$1", tenant)
	if err = w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	s.DB.QueryRow(ctx, "SELECT balance FROM wallets WHERE tenant_id=$1 AND customer_id=$2", tenant, customer).Scan(&balance)
	if balance != -80 {
		t.Fatal(balance)
	}
	s.DB.QueryRow(ctx, "SELECT count(*) FROM ledger WHERE tenant_id=$1 AND reason='reversal'", tenant).Scan(&count)
	if count != 1 {
		t.Fatal(count)
	}
}
func TestPostgresSubscriptionConcurrentSchedulingAndCancel(t *testing.T) {
	s := testStore(t)
	tenant, key, customer := newTenant(t, s)
	accounts := Accounts{tenant: {"stripe": {Secret: "sk_test"}}}
	h := (&API{Store: s, Accounts: accounts}).Handler()
	out := request(t, h, "POST", "/v1/plans", key, "plan", `{"name":"Pro","amount":1990,"currency":"brl","credits":500,"interval_unit":"month","interval_count":1}`)
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if out.Code != 200 {
		t.Fatal(out.Body)
	}
	out = request(t, h, "POST", "/v1/subscriptions", key, "subscribe", fmt.Sprintf(`{"customer_id":%q,"plan_id":%q,"provider":"stripe","provider_customer":"cus_x","payment_method":"pm_x"}`, customer, p.ID))
	if out.Code != 200 {
		t.Fatal(out.Body)
	}
	var sub map[string]any
	json.Unmarshal(out.Body.Bytes(), &sub)
	w := &Worker{Store: s, Accounts: accounts, Gateway: &fakeProvider{calls: map[string]int{}, status: "paid"}}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := w.schedule(context.Background()); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	var count int
	s.DB.QueryRow(context.Background(), "SELECT count(*) FROM charges WHERE tenant_id=$1", tenant).Scan(&count)
	if count != 1 {
		t.Fatal(count)
	}
	out = request(t, h, "POST", "/v1/subscriptions/"+sub["id"].(string)+"/cancel", key, "cancel", `{}`)
	if out.Code != 200 {
		t.Fatal(out.Body)
	}
	s.DB.Exec(context.Background(), "UPDATE subscriptions SET next_at=now()-interval '1 day' WHERE tenant_id=$1", tenant)
	if e := w.schedule(context.Background()); e != nil {
		t.Fatal(e)
	}
	s.DB.QueryRow(context.Background(), "SELECT count(*) FROM charges WHERE tenant_id=$1", tenant).Scan(&count)
	if count != 1 {
		t.Fatal(count)
	}
}
func TestPostgresExpiredAmbiguousPaymentStops(t *testing.T) {
	s := testStore(t)
	tenant, _, customer := newTenant(t, s)
	ctx := context.Background()
	id := token()
	_, err := s.DB.Exec(ctx, "INSERT INTO charges(tenant_id,id,customer_id,provider,provider_customer,payment_method,amount,currency,credits,first_attempt) VALUES($1,$2,$3,'pagarme','cus','card',100,'brl',0,now()-interval '6 minutes')", tenant, id, customer)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeProvider{calls: map[string]int{}, status: "paid"}
	w := &Worker{Store: s, Accounts: Accounts{tenant: {"pagarme": {Secret: "sk_test", RetryWindowSeconds: 240}}}, Gateway: f}
	if err = w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	s.DB.QueryRow(ctx, "SELECT status FROM charges WHERE tenant_id=$1 AND id=$2", tenant, id).Scan(&status)
	if status != "manual_review" || len(f.calls) != 0 {
		t.Fatal(status, f.calls)
	}
}
func TestPostgresConcurrentSameIdempotencyKey(t *testing.T) {
	s := testStore(t)
	tenant, _, _ := newTenant(t, s)
	var wg sync.WaitGroup
	results := make(chan string, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := s.write(context.Background(), tenant, "same", "fp", func(tx pgx.Tx) (any, error) {
				time.Sleep(time.Millisecond * 10)
				return map[string]string{"id": token()}, nil
			})
			if err != nil {
				t.Error(err)
			}
			results <- string(b)
		}()
	}
	wg.Wait()
	close(results)
	var first string
	for b := range results {
		if first == "" {
			first = b
		}
		if b != first {
			t.Fatal(first, b)
		}
	}
}

func TestPostgresRotateKey(t *testing.T) {
	s := testStore(t)
	tenant, key, customer := newTenant(t, s)
	admin := strings.Repeat("x", 32)
	h := (&API{Store: s, AdminSecret: admin}).Handler()
	out := request(t, h, "POST", "/admin/tenants/"+tenant+"/rotate-key", admin, "", `{}`)
	if out.Code != 200 {
		t.Fatalf("%d %s", out.Code, out.Body)
	}
	var v map[string]string
	json.Unmarshal(out.Body.Bytes(), &v)
	if request(t, h, "GET", "/v1/wallets/"+customer, key, "", "").Code != 401 {
		t.Fatal("old key still accepted")
	}
	if request(t, h, "GET", "/v1/wallets/"+customer, v["api_key"], "", "").Code != 200 {
		t.Fatal("new key rejected")
	}
}

func TestPostgresNoCatchupAvalanche(t *testing.T) {
	s := testStore(t)
	tenant, key, customer := newTenant(t, s)
	accounts := Accounts{tenant: {"stripe": {Secret: "sk_test"}}}
	h := (&API{Store: s, Accounts: accounts}).Handler()
	out := request(t, h, "POST", "/v1/plans", key, "p", `{"name":"Daily","amount":100,"currency":"brl","credits":5,"interval_unit":"day","interval_count":1}`)
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	out = request(t, h, "POST", "/v1/subscriptions", key, "s", fmt.Sprintf(`{"customer_id":%q,"plan_id":%q,"provider":"stripe","provider_customer":"cus","payment_method":"pm"}`, customer, p.ID))
	if out.Code != 200 {
		t.Fatal(out.Body)
	}
	ctx := context.Background()
	s.DB.Exec(ctx, "UPDATE subscriptions SET anchor=now()-interval '30 days',next_at=now()-interval '30 days' WHERE tenant_id=$1", tenant)
	w := &Worker{Store: s, Accounts: accounts, Gateway: &fakeProvider{calls: map[string]int{}, status: "paid"}}
	for i := 0; i < 2; i++ {
		if e := w.Tick(ctx); e != nil {
			t.Fatal(e)
		}
	}
	var n int
	s.DB.QueryRow(ctx, "SELECT count(*) FROM charges WHERE tenant_id=$1", tenant).Scan(&n)
	if n != 1 {
		t.Fatalf("outage produced %d charges", n)
	}
}

func TestPostgresCredentialChangeStopsAutomaticCharge(t *testing.T) {
	s := testStore(t)
	tenant, key, customer := newTenant(t, s)
	accounts := Accounts{tenant: {"stripe": {Secret: "sk_test_old"}}}
	h := (&API{Store: s, Accounts: accounts}).Handler()
	out := request(t, h, "POST", "/v1/charges", key, "charge", fmt.Sprintf(`{"customer_id":%q,"provider":"stripe","provider_customer":"cus","payment_method":"pm","amount":100,"currency":"brl"}`, customer))
	if out.Code != 200 {
		t.Fatal(out.Body)
	}
	accounts[tenant]["stripe"] = Account{Secret: "sk_test_different_account"}
	f := &fakeProvider{calls: map[string]int{}, status: "paid"}
	w := &Worker{Store: s, Accounts: accounts, Gateway: f}
	if e := w.Tick(context.Background()); e != nil {
		t.Fatal(e)
	}
	if len(f.calls) > 0 {
		t.Fatal("credential change allowed charge in a different account")
	}
}
