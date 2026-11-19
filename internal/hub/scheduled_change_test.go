package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type scheduledFixture struct {
	s                     *Store
	tenant, key, customer string
	h                     http.Handler
}

func newScheduledFixture(t *testing.T) scheduledFixture {
	t.Helper()
	s := testStore(t)
	tenant, key, customer := newTenant(t, s)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO plans(tenant_id,id,name,amount,currency,credits,interval_unit,interval_count,metadata) VALUES($1,'old','Old',1000,'brl',100,'day',30,'{"tier":"old"}'),($1,'new','New',2000,'brl',200,'month',1,'{"tier":"new"}'),($1,'usd','USD',2000,'usd',200,'month',1,'{}')`,
		`INSERT INTO subscriptions(tenant_id,id,customer_id,plan_id,provider,provider_customer,payment_method,anchor,next_at,cycle,metadata) VALUES($1,'sub',$2,'old','stripe','cus','pm',now()-interval '15 days',now()+interval '15 days',1,'{"workspace":"prontei"}')`,
		`INSERT INTO charges(tenant_id,id,customer_id,provider,provider_customer,payment_method,amount,currency,credits,subscription_id,cycle,status,credited,next_at) VALUES($1,'initial',$2,'stripe','cus','pm',1000,'brl',100,'sub',0,'paid',true,'infinity')`,
	} {
		var err error
		if bytes.Contains([]byte(q), []byte("$2")) {
			_, err = s.DB.Exec(ctx, q, tenant, customer)
		} else {
			_, err = s.DB.Exec(ctx, q, tenant)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return scheduledFixture{s, tenant, key, customer, (&API{Store: s}).Handler()}
}
func (f scheduledFixture) post(t *testing.T, action, idem, body string, code int) map[string]any {
	t.Helper()
	r := request(t, f.h, "POST", "/v1/subscriptions/sub/"+action, f.key, idem, body)
	if r.Code != code {
		t.Fatalf("%s: %d %s, want %d", action, r.Code, r.Body, code)
	}
	var v map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func (f scheduledFixture) read(t *testing.T) map[string]any {
	t.Helper()
	r := request(t, f.h, "GET", "/v1/subscriptions/sub", f.key, "", "")
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	var v map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func TestScheduledPlanChangeScheduleCancel(t *testing.T) {
	f := newScheduledFixture(t)
	body := `{"plan_id":"new","metadata":{"operation":"downgrade","large":9007199254740993,"nested":{"keep":null}}}`
	first := request(t, f.h, "POST", "/v1/subscriptions/sub/change-plan/schedule", f.key, "schedule", body)
	if first.Code != 200 {
		t.Fatalf("schedule: %d %s", first.Code, first.Body)
	}
	replay := request(t, f.h, "POST", "/v1/subscriptions/sub/change-plan/schedule", f.key, "schedule", body)
	if replay.Code != 200 || !bytes.Equal(first.Body.Bytes(), replay.Body.Bytes()) {
		t.Fatal("idempotent schedule changed", replay.Code, replay.Body)
	}
	sub := f.read(t)
	change, ok := sub["pending_scheduled_change"].(map[string]any)
	if !ok || change["source_plan_id"] != "old" || change["target_plan_id"] != "new" || change["status"] != "scheduled" || change["charge_id"] != nil || sub["plan_id"] != "old" {
		t.Fatal(sub)
	}
	f.post(t, "change-plan/schedule", "conflict", `{"plan_id":"new"}`, 409)
	f.post(t, "change-plan/schedule/cancel", "cancel", "{}", 200)
	f.post(t, "change-plan/schedule/cancel", "cancel", "{}", 200)
	if sub = f.read(t); sub["pending_scheduled_change"] != nil || sub["plan_id"] != "old" {
		t.Fatal(sub)
	}
	var n int
	if err := f.s.DB.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND type IN ('subscription.change_scheduled','subscription.change_schedule_canceled') AND data->'scheduled_change_metadata'->>'large'='9007199254740993' AND data->>'subscription_id'='sub' AND data->'subscription_metadata'->>'workspace'='prontei'`, f.tenant).Scan(&n); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	f.post(t, "change-plan/schedule", "reschedule", `{"plan_id":"new"}`, 200)
}
func TestScheduledPlanChangeValidation(t *testing.T) {
	for _, tc := range []struct {
		name, sql, body string
		code            int
	}{
		{"same_plan", "", `{"plan_id":"old"}`, 409},
		{"missing_plan", "", `{"plan_id":"missing"}`, 404},
		{"currency", "", `{"plan_id":"usd"}`, 400},
		{"metadata", "", `{"plan_id":"new","metadata":[]}`, 400},
		{"inactive", `UPDATE subscriptions SET status='past_due'`, `{"plan_id":"new"}`, 409},
		{"paused", `UPDATE subscriptions SET paused_at=now()`, `{"plan_id":"new"}`, 409},
		{"canceled", `UPDATE subscriptions SET status='canceled'`, `{"plan_id":"new"}`, 409},
		{"unpaid", `UPDATE charges SET status='pending',credited=false`, `{"plan_id":"new"}`, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newScheduledFixture(t)
			if tc.sql != "" {
				if _, err := f.s.DB.Exec(context.Background(), tc.sql); err != nil {
					t.Fatal(err)
				}
			}
			f.post(t, "change-plan/schedule", "schedule", tc.body, tc.code)
		})
	}
	t.Run("isolation", func(t *testing.T) {
		f := newScheduledFixture(t)
		_, other, _ := newTenant(t, f.s)
		r := request(t, f.h, "POST", "/v1/subscriptions/sub/change-plan/schedule", other, "schedule", `{"plan_id":"new"}`)
		if r.Code != 404 {
			t.Fatal(r.Code, r.Body)
		}
	})
	t.Run("immediate_pending", func(t *testing.T) {
		f := newScheduledFixture(t)
		f.post(t, "change-plan", "immediate", `{"plan_id":"new"}`, 200)
		f.post(t, "change-plan/schedule", "schedule", `{"plan_id":"new"}`, 409)
	})
}
func TestScheduledPlanChangeConcurrentSchedulers(t *testing.T) {
	f := newScheduledFixture(t)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes <- request(t, f.h, "POST", "/v1/subscriptions/sub/change-plan/schedule", f.key, fmt.Sprint(i), `{"plan_id":"new"}`).Code
		}(i)
	}
	wg.Wait()
	close(codes)
	success, conflict := 0, 0
	for code := range codes {
		if code == 200 {
			success++
		} else if code == 409 {
			conflict++
		} else {
			t.Fatal(code)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
}

func (f scheduledFixture) due(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.s.DB.Exec(ctx, `UPDATE subscriptions SET next_at=now()-interval '1 second' WHERE tenant_id=$1`, f.tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.DB.Exec(ctx, `UPDATE scheduled_plan_changes SET effective_at=(SELECT next_at FROM subscriptions WHERE tenant_id=$1 AND id='sub') WHERE tenant_id=$1`, f.tenant); err != nil {
		t.Fatal(err)
	}
}
func (f scheduledFixture) settle(t *testing.T, charge, status string, manual bool) {
	t.Helper()
	ctx := context.Background()
	c := Charge{Tenant: f.tenant, ID: charge, Customer: f.customer, Recurring: true, Lease: token()}
	if err := f.s.DB.QueryRow(ctx, `UPDATE charges SET lease_token=$3 WHERE tenant_id=$1 AND id=$2 RETURNING credits,amount,status`, f.tenant, charge, c.Lease).Scan(&c.Credits, &c.Amount, &c.Status); err != nil {
		t.Fatal(err)
	}
	w := &Worker{Store: f.s}
	var err error
	if manual {
		err = w.retry(ctx, c, "provider_review", true)
	} else {
		err = w.apply(ctx, c, Payment{ID: "provider-" + charge, Status: status})
	}
	if err != nil {
		t.Fatal(err)
	}
}
func TestScheduledPlanChangeRenewal(t *testing.T) {
	for _, outcome := range []string{"paid", "pending", "failed", "manual_review", "credit_settlement"} {
		t.Run(outcome, func(t *testing.T) {
			f := newScheduledFixture(t)
			ctx := context.Background()
			f.post(t, "change-plan/schedule", "schedule", `{"plan_id":"new","metadata":{"operation":"renewal"}}`, 200)
			f.post(t, "change-plan", "immediate", `{"plan_id":"new"}`, 409)
			f.due(t)
			if outcome == "credit_settlement" {
				if _, err := f.s.DB.Exec(ctx, `UPDATE subscriptions SET billing_credit=2000 WHERE tenant_id=$1`, f.tenant); err != nil {
					t.Fatal(err)
				}
			}
			w := &Worker{Store: f.s}
			var wg sync.WaitGroup
			errs := make(chan error, 2)
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); errs <- w.schedule(ctx) }()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			var charge, currency, plan string
			var amount, credits int64
			var n int
			if err := f.s.DB.QueryRow(ctx, `SELECT count(*) FROM charges WHERE tenant_id=$1 AND cycle=1`, f.tenant).Scan(&n); err != nil || n != 1 {
				t.Fatal(n, err)
			}
			if err := f.s.DB.QueryRow(ctx, `SELECT id,amount,currency,credits FROM charges WHERE tenant_id=$1 AND cycle=1`, f.tenant).Scan(&charge, &amount, &currency, &credits); err != nil {
				t.Fatal(err)
			}
			expected := int64(2000)
			if outcome == "credit_settlement" {
				expected = 0
			}
			if amount != expected || currency != "brl" || credits != 200 {
				t.Fatal(amount, currency, credits)
			}
			sub := f.read(t)
			if outcome != "credit_settlement" {
				if sub["plan_id"] != "old" || sub["pending_scheduled_change"].(map[string]any)["charge_id"] != charge {
					t.Fatal(sub)
				}
				f.post(t, "change-plan/schedule/cancel", "cancel", "{}", 409)
				f.post(t, "pause", "pause", "{}", 409)
				f.settle(t, charge, outcome, outcome == "manual_review")
				if outcome != "paid" {
					sub = f.read(t)
					change := sub["pending_scheduled_change"].(map[string]any)
					if sub["plan_id"] != "old" || change["charge_status"] != outcome {
						t.Fatal(sub)
					}
					var balance int64
					if err := f.s.DB.QueryRow(ctx, `SELECT balance FROM wallets WHERE tenant_id=$1 AND customer_id=$2`, f.tenant, f.customer).Scan(&balance); err != nil || balance != 0 {
						t.Fatal(balance, err)
					}
					if err := w.schedule(ctx); err != nil {
						t.Fatal(err)
					}
					f.settle(t, charge, "paid", false)
				}
			}
			sub = f.read(t)
			if sub["plan_id"] != "new" || sub["status"] != "active" || sub["pending_scheduled_change"] != nil {
				t.Fatal(sub)
			}
			f.settle(t, charge, "paid", false)
			var calendar bool
			if err := f.s.DB.QueryRow(ctx, `SELECT s.plan_id,s.anchor=c.effective_at AND s.next_at=c.effective_at+interval '1 month' AND s.billing_epoch=1 FROM subscriptions s JOIN scheduled_plan_changes c USING(tenant_id) WHERE s.tenant_id=$1`, f.tenant).Scan(&plan, &calendar); err != nil || !calendar {
				var detail string
				f.s.DB.QueryRow(ctx, `SELECT jsonb_build_object('anchor',s.anchor,'effective',c.effective_at,'next',s.next_at,'epoch',s.billing_epoch,'cycle',s.cycle)::text FROM subscriptions s JOIN scheduled_plan_changes c USING(tenant_id) WHERE s.tenant_id=$1`, f.tenant).Scan(&detail)
				t.Fatal(plan, calendar, err, detail)
			}
			if err := f.s.DB.QueryRow(ctx, `SELECT count(*) FROM events WHERE tenant_id=$1 AND type='subscription.change_schedule_applied' AND data->>'plan_id'='new' AND data->>'charge_id'=$2 AND data->'scheduled_change_metadata'->>'operation'='renewal'`, f.tenant, charge).Scan(&n); err != nil || n != 1 {
				t.Fatal(n, err)
			}
			var balance int64
			if err := f.s.DB.QueryRow(ctx, `SELECT balance FROM wallets WHERE tenant_id=$1 AND customer_id=$2`, f.tenant, f.customer).Scan(&balance); err != nil || balance != 200 {
				t.Fatal(balance, err)
			}
		})
	}
}
func TestScheduledPlanChangePauseAndCancellation(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(fmt.Sprint(linked), func(t *testing.T) {
			f := newScheduledFixture(t)
			ctx := context.Background()
			f.post(t, "change-plan/schedule", "schedule", `{"plan_id":"new"}`, 200)
			f.post(t, "pause", "pause", "{}", 200)
			if _, err := f.s.DB.Exec(ctx, `UPDATE subscriptions SET paused_at=paused_at-interval '2 days' WHERE tenant_id=$1`, f.tenant); err != nil {
				t.Fatal(err)
			}
			f.post(t, "resume", "resume", "{}", 200)
			var aligned bool
			if err := f.s.DB.QueryRow(ctx, `SELECT c.effective_at=s.next_at FROM scheduled_plan_changes c JOIN subscriptions s USING(tenant_id) WHERE c.tenant_id=$1`, f.tenant).Scan(&aligned); err != nil || !aligned {
				t.Fatal(aligned, err)
			}
			var charge string
			if linked {
				f.due(t)
				if err := (&Worker{Store: f.s}).schedule(ctx); err != nil {
					t.Fatal(err)
				}
				if err := f.s.DB.QueryRow(ctx, `SELECT charge_id FROM scheduled_plan_changes WHERE tenant_id=$1`, f.tenant).Scan(&charge); err != nil {
					t.Fatal(err)
				}
			}
			f.post(t, "cancel", "cancel", "{}", 200)
			if linked {
				f.settle(t, charge, "paid", false)
				var balance int64
				var credited bool
				if err := f.s.DB.QueryRow(ctx, `SELECT w.balance,c.credited FROM wallets w JOIN charges c ON c.tenant_id=w.tenant_id AND c.customer_id=w.customer_id WHERE c.tenant_id=$1 AND c.id=$2`, f.tenant, charge).Scan(&balance, &credited); err != nil || balance != 0 || credited {
					t.Fatal("canceled payment granted rights", balance, credited, err)
				}
				f.settle(t, charge, "paid", false)
				f.settle(t, charge, "pending", false)
				f.settle(t, charge, "paid", false)
				var count int
				if err := f.s.DB.QueryRow(ctx, `SELECT count(*) FROM events WHERE tenant_id=$1 AND type='subscription.change_schedule_failed' AND data->>'reason'='subscription_not_eligible'`, f.tenant).Scan(&count); err != nil || count != 1 {
					t.Fatal(count, err)
				}
				f.settle(t, charge, "reversed", false)
				if err := f.s.DB.QueryRow(ctx, `SELECT balance FROM wallets WHERE tenant_id=$1`, f.tenant).Scan(&balance); err != nil || balance != 0 {
					t.Fatal("ungranted credit reversed", balance, err)
				}
			}
			sub := f.read(t)
			if sub["status"] != "canceled" || sub["plan_id"] != "old" || sub["pending_scheduled_change"] != nil {
				t.Fatal(sub)
			}
			var status string
			if err := f.s.DB.QueryRow(ctx, `SELECT status FROM scheduled_plan_changes WHERE tenant_id=$1`, f.tenant).Scan(&status); err != nil || status != "canceled" {
				t.Fatal(status, err)
			}
			if linked {
				ev := eventData(t, f.s, f.tenant, "subscription.change_schedule_failed")
				if ev["charge_id"] != charge || ev["reason"] != "reversed" {
					t.Fatal(ev)
				}
			}
		})
	}
}

func TestScheduledPlanChangePortalCancellation(t *testing.T) {
	f := newScheduledFixture(t)
	ctx := context.Background()
	if _, err := f.s.DB.Exec(ctx, `UPDATE subscriptions SET provider='pagarme' WHERE tenant_id=$1`, f.tenant); err != nil {
		t.Fatal(err)
	}
	f.h = (&API{Store: f.s, Accounts: Accounts{f.tenant: {"pagarme": {Secret: "sk_test", PublicKey: "pk_test", Environment: "test"}}}}).Handler()
	f.post(t, "change-plan/schedule", "schedule", `{"plan_id":"new"}`, 200)
	session := f.post(t, "portal", "portal", "{}", 200)
	req := httptest.NewRequest("POST", session["path"].(string)+"/cancel", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	r := httptest.NewRecorder()
	f.h.ServeHTTP(r, req)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	var status string
	if err := f.s.DB.QueryRow(ctx, `SELECT status FROM scheduled_plan_changes WHERE tenant_id=$1`, f.tenant).Scan(&status); err != nil || status != "canceled" {
		t.Fatal(status, err)
	}
}

func TestScheduledPlanChangeConcurrentSettlement(t *testing.T) {
	f := newScheduledFixture(t)
	ctx := context.Background()
	f.post(t, "change-plan/schedule", "schedule", `{"plan_id":"new"}`, 200)
	f.due(t)
	w := &Worker{Store: f.s}
	if err := w.schedule(ctx); err != nil {
		t.Fatal(err)
	}
	c, err := w.claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- w.apply(ctx, c, Payment{ID: "provider-renewal", Status: "paid"}) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var grants, events int
	var balance int64
	if err := f.s.DB.QueryRow(ctx, `SELECT balance,(SELECT count(*) FROM ledger WHERE tenant_id=$1 AND reason='payment'),(SELECT count(*) FROM events WHERE tenant_id=$1 AND type='subscription.change_schedule_applied') FROM wallets WHERE tenant_id=$1`, f.tenant).Scan(&balance, &grants, &events); err != nil || balance != 200 || grants != 1 || events != 1 {
		t.Fatal(balance, grants, events, err)
	}
}
