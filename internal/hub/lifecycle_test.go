package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestProrationRounding(t *testing.T) {
	if got := prorated(101, time.Second, 3*time.Second, false); got != 33 {
		t.Fatal(got)
	}
	if got := prorated(101, time.Second, 3*time.Second, true); got != 34 {
		t.Fatal(got)
	}
	if got := prorated(1000000000, 3650*24*time.Hour, 3650*24*time.Hour, true); got != 1000000000 {
		t.Fatal(got)
	}
}

func TestPostgresSubscriptionLifecycle(t *testing.T) {
	s := testStore(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	tenant, key, customer := newTenant(t, s)
	_, other, _ := newTenant(t, s)
	ctx := context.Background()
	_, err := s.DB.Exec(ctx, `INSERT INTO plans(tenant_id,id,name,amount,currency,credits,interval_unit,interval_count) VALUES($1,'old','Old',1000,'brl',100,'day',30),($1,'new','New',2000,'brl',200,'month',1),($1,'cheap','Cheap',100,'brl',10,'day',7)`, tenant)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO subscriptions(tenant_id,id,customer_id,plan_id,provider,provider_customer,payment_method,anchor,next_at,cycle) VALUES($1,'sub',$2,'old','stripe','cus','pm',now()-interval '15 days',now()+interval '15 days',1);`, tenant, customer)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO charges(tenant_id,id,customer_id,provider,provider_customer,payment_method,amount,currency,credits,subscription_id,cycle,status,credited,next_at) VALUES($1,'initial',$2,'stripe','cus','pm',1000,'brl',100,'sub',0,'paid',true,'infinity')`, tenant, customer)
	if err != nil {
		t.Fatal(err)
	}
	h := (&API{Store: s}).Handler()
	post := func(path, idem, body string) map[string]any {
		t.Helper()
		r := request(t, h, "POST", "/v1/subscriptions/sub/"+path, key, idem, body)
		if r.Code != 200 && r.Code != 201 {
			t.Fatalf("%s %d %s", path, r.Code, r.Body.String())
		}
		var v map[string]any
		if err := json.Unmarshal(r.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if r := request(t, h, "POST", "/v1/subscriptions/sub/pause", other, "isolated", "{}"); r.Code != 404 {
		t.Fatal(r.Code, r.Body.String())
	}
	post("pause", "pause", "{}")
	var before, after time.Time
	if err = s.DB.QueryRow(ctx, "SELECT next_at FROM subscriptions WHERE tenant_id=$1 AND id='sub'", tenant).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, "UPDATE subscriptions SET paused_at=paused_at-interval '2 days',status='past_due' WHERE tenant_id=$1 AND id='sub'", tenant); err != nil {
		t.Fatal(err)
	}
	post("resume", "resume", "{}")
	var status string
	if err = s.DB.QueryRow(ctx, "SELECT next_at,status FROM subscriptions WHERE tenant_id=$1 AND id='sub'", tenant).Scan(&after, &status); err != nil {
		t.Fatal(err)
	}
	if after.Sub(before) < 48*time.Hour || status != "past_due" {
		t.Fatal(after.Sub(before), status)
	}
	if _, err = s.DB.Exec(ctx, "UPDATE subscriptions SET status='active' WHERE tenant_id=$1 AND id='sub'", tenant); err != nil {
		t.Fatal(err)
	}
	preview := post("change-plan/preview", "preview", `{"plan_id":"new"}`)
	if preview["amount_due"].(float64) <= 1000 || preview["amount_due"].(float64) >= 2000 {
		t.Fatal(preview)
	}
	if r := request(t, h, "POST", "/v1/subscriptions/sub/change-plan", key, "price-cap", `{"plan_id":"new","max_amount":1}`); r.Code != 409 {
		t.Fatal(r.Code, r.Body.String())
	}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := request(t, h, "POST", "/v1/subscriptions/sub/change-plan", key, fmt.Sprintf("concurrent-%d", i), `{"plan_id":"new"}`)
			codes <- r.Code
		}(i)
	}
	wg.Wait()
	close(codes)
	success := 0
	for c := range codes {
		if c == 200 || c == 201 {
			success++
		} else if c != 409 {
			t.Fatal(c)
		}
	}
	if success != 1 {
		t.Fatal(success)
	}
	var charge string
	if err = s.DB.QueryRow(ctx, "SELECT charge_id FROM subscription_changes WHERE tenant_id=$1 AND status='pending'", tenant).Scan(&charge); err != nil {
		t.Fatal(err)
	}
	if r := request(t, h, "POST", "/v1/subscriptions/sub/cancel", key, "cancel-pending", "{}"); r.Code != 409 {
		t.Fatal(r.Code, r.Body.String())
	}
	lease := token()
	if _, err = s.DB.Exec(ctx, "UPDATE charges SET lease_token=$3 WHERE tenant_id=$1 AND id=$2", tenant, charge, lease); err != nil {
		t.Fatal(err)
	}
	w := &Worker{Store: s}
	c := Charge{Tenant: tenant, ID: charge, Customer: customer, Lease: lease, Recurring: true}
	if err = w.apply(ctx, c, Payment{Status: "failed", ID: "pi_failure"}); err != nil {
		t.Fatal(err)
	}
	var plan string
	if err = s.DB.QueryRow(ctx, "SELECT plan_id FROM subscriptions WHERE tenant_id=$1 AND id='sub'", tenant).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if plan != "old" {
		t.Fatal(plan)
	}
	cheap := post("change-plan", "cheap", `{"plan_id":"cheap"}`)
	if cheap["status"] != "applied" {
		t.Fatal(cheap)
	}
	replay := post("change-plan", "cheap", `{"plan_id":"cheap"}`)
	if replay["change_id"] != cheap["change_id"] {
		t.Fatal(replay)
	}
	var balance int64
	var n int
	if err = s.DB.QueryRow(ctx, "SELECT billing_credit FROM subscriptions WHERE tenant_id=$1 AND id='sub'", tenant).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance <= 0 {
		t.Fatal(balance)
	}
	if err = s.DB.QueryRow(ctx, "SELECT count(*) FROM billing_ledger WHERE tenant_id=$1", tenant).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal(n)
	}
	if _, err = s.DB.Exec(ctx, "UPDATE billing_ledger SET delta=0 WHERE tenant_id=$1", tenant); err == nil {
		t.Fatal("billing ledger allowed mutation")
	}
	// A positive replacement remains on the old plan until settlement and
	// applies its wallet/billing entries exactly once across repeated polling.
	positive := post("change-plan", "positive", `{"plan_id":"new","max_amount":2000}`)
	positiveCharge := positive["charge_id"].(string)
	positiveLease := token()
	if _, err = s.DB.Exec(ctx, "UPDATE charges SET lease_token=$3 WHERE tenant_id=$1 AND id=$2", tenant, positiveCharge, positiveLease); err != nil {
		t.Fatal(err)
	}
	pc := Charge{Tenant: tenant, ID: positiveCharge, Customer: customer, Lease: positiveLease, Recurring: true}
	if err = w.apply(ctx, pc, Payment{Status: "paid", ID: "pi_success"}); err != nil {
		t.Fatal(err)
	}
	if err = w.apply(ctx, pc, Payment{Status: "paid", ID: "pi_success"}); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRow(ctx, "SELECT count(*) FROM billing_ledger WHERE tenant_id=$1", tenant).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatal(n)
	}
	// Refunding the upgrade removes its exact signed entitlement once, even
	// when provider polling delivers the same reversal repeatedly.
	var beforeRefund, afterRefund, usage int64
	if err = s.DB.QueryRow(ctx, "SELECT balance FROM wallets WHERE tenant_id=$1 AND customer_id=$2", tenant, customer).Scan(&beforeRefund); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRow(ctx, "SELECT usage_credits FROM subscription_changes WHERE tenant_id=$1 AND id=$2", tenant, positive["change_id"]).Scan(&usage); err != nil {
		t.Fatal(err)
	}
	if usage <= 0 {
		t.Fatal(usage)
	}
	for i := 0; i < 2; i++ {
		pc.Lease = token()
		if _, err = s.DB.Exec(ctx, "UPDATE charges SET lease_token=$3 WHERE tenant_id=$1 AND id=$2", tenant, pc.ID, pc.Lease); err != nil {
			t.Fatal(err)
		}
		if err = w.apply(ctx, pc, Payment{Status: "reversed", ID: "pi_success"}); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.DB.QueryRow(ctx, "SELECT balance FROM wallets WHERE tenant_id=$1 AND customer_id=$2", tenant, customer).Scan(&afterRefund); err != nil {
		t.Fatal(err)
	}
	if afterRefund != beforeRefund-usage {
		t.Fatal(beforeRefund, afterRefund, usage)
	}
	var reversalEntries int
	if err = s.DB.QueryRow(ctx, "SELECT count(*) FROM ledger WHERE tenant_id=$1 AND reason='reversal' AND reference=$2", tenant, "plan-change:"+positive["change_id"].(string)).Scan(&reversalEntries); err != nil {
		t.Fatal(err)
	}
	if reversalEntries != 1 {
		t.Fatal(reversalEntries)
	}
	// A failed old proposal settling late cannot override the accepted replacement.
	lease = token()
	c.Lease = lease
	if _, err = s.DB.Exec(ctx, "UPDATE charges SET lease_token=$3 WHERE tenant_id=$1 AND id=$2", tenant, charge, lease); err != nil {
		t.Fatal(err)
	}
	if err = w.apply(ctx, c, Payment{Status: "paid", ID: "pi_failure"}); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRow(ctx, "SELECT plan_id,status FROM subscriptions WHERE tenant_id=$1 AND id='sub'", tenant).Scan(&plan, &status); err != nil {
		t.Fatal(err)
	}
	if plan != "new" || status != "past_due" {
		t.Fatal(plan, status)
	}
}

func TestPostgresBillingCreditRenewal(t *testing.T) {
	s := testStore(t)
	tenant, _, customer := newTenant(t, s)
	ctx := context.Background()
	_, err := s.DB.Exec(ctx, `INSERT INTO plans(tenant_id,id,name,amount,currency,credits,interval_unit,interval_count) VALUES($1,'p','Plan',100,'brl',10,'day',7)`, tenant)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO subscriptions(tenant_id,id,customer_id,plan_id,provider,provider_customer,payment_method,anchor,next_at,billing_credit) VALUES($1,'sub',$2,'p','stripe','cus','pm',now(),now(),250)`, tenant, customer)
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{Store: s}
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
	var n int
	var amount, balance, wallet int64
	var kind, status string
	err = s.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM charges WHERE tenant_id=$1),c.amount,c.kind,c.status,s.billing_credit,w.balance FROM charges c JOIN subscriptions s ON s.tenant_id=c.tenant_id AND s.id=c.subscription_id JOIN wallets w ON w.tenant_id=s.tenant_id AND w.customer_id=s.customer_id WHERE c.tenant_id=$1`, tenant).Scan(&n, &amount, &kind, &status, &balance, &wallet)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || amount != 0 || kind != "credit_settlement" || status != "paid" || balance != 150 || wallet != 10 {
		t.Fatal(n, amount, kind, status, balance, wallet)
	}
	var event int
	if err = s.DB.QueryRow(ctx, "SELECT count(*) FROM events WHERE tenant_id=$1 AND type='charge.paid'", tenant).Scan(&event); err != nil {
		t.Fatal(err)
	}
	if event != 1 {
		t.Fatal(event)
	}
}
