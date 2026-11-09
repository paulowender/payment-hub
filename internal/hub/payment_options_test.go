package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPagarmeAlternativePaymentContracts(t *testing.T) {
	for _, method := range []string{"pix", "boleto", "card"} {
		t.Run(method, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var b struct {
					Payments []map[string]any `json:"payments"`
				}
				json.NewDecoder(r.Body).Decode(&b)
				p := b.Payments[0]
				want := method
				if want == "card" {
					want = "credit_card"
				}
				if p["payment_method"] != want {
					t.Error("method", p)
				}
				if method == "card" && p["credit_card"].(map[string]any)["installments"] != float64(3) {
					t.Error("installments")
				}
				w.Write([]byte(`{"id":"or_test","status":"pending","amount":3000,"currency":"BRL","charges":[{"id":"ch_test","status":"pending","last_transaction":{"qr_code":"000201test","qr_code_url":"https://api.pagar.me/qr.png","line":"123456789","url":"https://api.pagar.me/boleto","expires_at":"2030-01-01T00:00:00Z"}}]}`))
			}))
			defer server.Close()
			g := NewGateway()
			g.pagarmeURL = server.URL
			p, e := g.Create(context.Background(), Account{Secret: "sk_test"}, Charge{ID: "test", Provider: "pagarme", ProviderCustomer: "cus_test", PaymentMethod: "card_test", Amount: 3000, Currency: "brl", Method: method, Installments: 3, PixExpires: 1800, BoletoDueDays: 3})
			if e != nil || p.Status != "pending" || p.Instructions.PixCode != "000201test" {
				t.Fatal(p, e)
			}
		})
	}
}

func TestPostgresFlexibleOfferAndPaymentSettings(t *testing.T) {
	s := testStore(t)
	tenant, key, customer := newTenant(t, s)
	a := &API{Store: s, Accounts: Accounts{tenant: {"pagarme": {Secret: "sk_test_options", PublicKey: "pk_test_options", Environment: "test"}}}}
	h := a.Handler()
	for _, period := range []struct {
		unit  string
		count int
	}{{"day", 7}, {"day", 14}, {"day", 19}, {"month", 1}, {"month", 12}} {
		body := fmt.Sprintf(`{"name":"Plano Flexível","description":"","amount":8990,"currency":"brl","credits":25,"interval_unit":%q,"interval_count":%d}`, period.unit, period.count)
		offer := request(t, h, "POST", "/v1/offers", key, fmt.Sprintf("offer-%s-%d", period.unit, period.count), body)
		if offer.Code != 200 {
			t.Fatal(offer.Code, offer.Body.String())
		}
		var result struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(offer.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		var amount int64
		var unit string
		var count int
		e := s.DB.QueryRow(context.Background(), "SELECT p.amount,p.interval_unit,p.interval_count FROM offers o JOIN plans p ON p.tenant_id=o.tenant_id AND p.id=o.plan_id WHERE o.tenant_id=$1 AND o.id=$2", tenant, result.ID).Scan(&amount, &unit, &count)
		if e != nil || amount != 8990 || unit != period.unit || count != period.count {
			t.Fatal("incorrect recurring price/period", amount, unit, count, e)
		}
	}
	cfg := `{"card":true,"pix":true,"boleto":true,"max_installments":6,"min_installment":500,"pix_expires":1800,"boleto_due_days":3,"buyer_cancel":true,"buyer_update_card":true}`
	if out := request(t, h, "POST", "/v1/checkout-settings", key, "settings", cfg); out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	offers := request(t, h, "POST", "/v1/offers", key, "once", `{"name":"Avulso","description":"","amount":3000,"currency":"brl","credits":10}`)
	var offer struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(offers.Body.Bytes(), &offer); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"pix", "boleto", "card"} {
		session := request(t, h, "POST", "/v1/checkout-sessions", key, "session-"+method, fmt.Sprintf(`{"offer_id":%q,"customer_id":%q,"reference":%q}`, offer.ID, customer, method))
		var link map[string]any
		json.Unmarshal(session.Body.Bytes(), &link)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/cards") {
				respond(w, 200, map[string]string{"id": "card_options"})
			} else {
				respond(w, 200, map[string]string{"id": "cus_options"})
			}
		}))
		g := NewGateway()
		g.pagarmeURL = server.URL
		a.CheckoutGateway = g
		pay := func(n int) *httptest.ResponseRecorder {
			token := ""
			if method == "card" {
				token = "token_test"
			}
			body := fmt.Sprintf(`{"method":%q,"installments":%d,"card_token":%q,"buyer":{"name":"Cliente Teste","email":"teste@example.com","document":"12345678909","phone":"11999999999","street":"Rua Teste","number":"1","zip_code":"01001000","city":"São Paulo","state":"SP"}}`, method, n, token)
			r := httptest.NewRequest("POST", link["path"].(string)+"/pay", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			out := httptest.NewRecorder()
			h.ServeHTTP(out, r)
			return out
		}
		if out := pay(7); out.Code != 400 {
			t.Fatal("invalid installments accepted", method, out.Code)
		}
		n := 1
		if method == "card" {
			n = 3
		}
		if out := pay(n); out.Code != 200 {
			t.Fatal(method, out.Code, out.Body.String())
		}
		var got string
		var installments int
		e := s.DB.QueryRow(context.Background(), "SELECT c.method,c.installments FROM checkout_sessions s JOIN charges c ON c.tenant_id=s.tenant_id AND c.id=s.charge_id WHERE s.id=$1", link["id"]).Scan(&got, &installments)
		if e != nil || got != method || installments != n {
			t.Fatal(method, got, installments, e)
		}
		server.Close()
	}
}

func TestPermanentProviderErrors(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 412, 422} {
		if !permanentProviderError(fmt.Errorf("provider_http_%d", code)) {
			t.Fatal(code)
		}
	}
	for _, e := range []error{fmt.Errorf("provider_http_500"), fmt.Errorf("provider_http_429"), fmt.Errorf("provider_http_409"), fmt.Errorf("provider_transport_error")} {
		if permanentProviderError(e) {
			t.Fatal(e)
		}
	}
}

func TestPagarmeNewRecurringCard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Payments []struct {
				Card struct {
					Cycle  string `json:"recurrence_cycle"`
					Origin any    `json:"payment_origin"`
				} `json:"credit_card"`
			} `json:"payments"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		if b.Payments[0].Card.Cycle != "first" || b.Payments[0].Card.Origin != nil {
			t.Error("new card inherited old origin")
		}
		w.Write([]byte(`{"id":"or_newcard","status":"paid","amount":1000,"currency":"BRL","charges":[{"id":"ch_newcard","status":"paid"}]}`))
	}))
	defer server.Close()
	g := NewGateway()
	g.pagarmeURL = server.URL
	p, e := g.Create(context.Background(), Account{Secret: "sk_test"}, Charge{ID: "test", Provider: "pagarme", Amount: 1000, Currency: "brl", PaymentMethod: "card_new", ProviderCustomer: "cus_test", Recurring: true, Cycle: 2, RecurringFirst: true})
	if e != nil || p.Status != "paid" {
		t.Fatal(p, e)
	}
}

func TestPostgresChargeHistoryAndArchivedOffer(t *testing.T) {
	s := testStore(t)
	tenant, key, customer := newTenant(t, s)
	_, other, _ := newTenant(t, s)
	h := (&API{Store: s, Accounts: Accounts{tenant: {"pagarme": {Secret: "sk_test", PublicKey: "pk_test", Environment: "test"}}}}).Handler()
	offer := request(t, h, "POST", "/v1/offers", key, "offer", `{"name":"Avulso","description":"","amount":1000,"currency":"brl","credits":2}`)
	var v struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(offer.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	sessionBody := fmt.Sprintf(`{"offer_id":%q,"customer_id":%q,"reference":"original"}`, v.ID, customer)
	session := request(t, h, "POST", "/v1/checkout-sessions", key, "session", sessionBody)
	if session.Code != 200 {
		t.Fatal(session.Code)
	}
	if out := request(t, h, "POST", "/v1/offers/"+v.ID+"/archive", key, "archive", "{}"); out.Code != 200 {
		t.Fatal(out.Code)
	}
	if out := request(t, h, "POST", "/v1/checkout-sessions", key, "another", strings.Replace(sessionBody, "original", "another", 1)); out.Code != 404 {
		t.Fatal("archived offer reused", out.Code)
	}
	var link map[string]any
	json.Unmarshal(session.Body.Bytes(), &link)
	r := httptest.NewRequest("GET", link["path"].(string)+"/data", nil)
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	if out.Code != 200 {
		t.Fatal("old link changed", out.Code)
	}
	_, e := s.DB.Exec(context.Background(), "INSERT INTO charges(tenant_id,id,customer_id,provider,provider_customer,payment_method,amount,currency,credits) VALUES($1,'history',$2,'pagarme','cus','card',1000,'brl',2)", tenant, customer)
	if e != nil {
		t.Fatal(e)
	}
	if out := request(t, h, "GET", "/v1/charges/history/history", other, "", ""); out.Code != 404 {
		t.Fatal("history tenant leak", out.Code)
	}
	if out := request(t, h, "GET", "/v1/charges/history/history", key, "", ""); out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
}

func TestPostgresSimulatorWalletAndPartialRecovery(t *testing.T) {
	s := testStore(t)
	tenant, key, customer := newTenant(t, s)
	accounts := Accounts{tenant: {"pagarme": {Secret: "sk_test", Environment: "test"}}}
	h := (&API{Store: s, Accounts: accounts}).Handler()
	w := &Worker{Store: s, Accounts: accounts}
	create := func(idem string) Charge {
		r := request(t, h, "POST", "/v1/charges", key, idem, fmt.Sprintf(`{"customer_id":%q,"provider":"pagarme","provider_customer":"cus_test","payment_method":"card_test","amount":1000,"currency":"brl","credits":100}`, customer))
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body.String())
		}
		c, e := w.claim(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		return c
	}
	c := create("partial")
	if e := w.apply(context.Background(), c, Payment{ID: "or_partial", Status: "manual_review", Detail: "payment_underpaid"}); e != nil {
		t.Fatal(e)
	}
	var balance int
	s.DB.QueryRow(context.Background(), "SELECT balance FROM wallets WHERE tenant_id=$1 AND customer_id=$2", tenant, customer).Scan(&balance)
	if balance != 0 {
		t.Fatal("underpaid granted credits")
	}
	s.DB.Exec(context.Background(), "UPDATE charges SET next_at=now() WHERE tenant_id=$1 AND id=$2", tenant, c.ID)
	partial, e := w.claim(context.Background())
	if e != nil || partial.ProviderID != "or_partial" {
		t.Fatal("partial not reverified", e)
	}
	if e = w.retry(context.Background(), partial, "provider_transport_error", false); e != nil {
		t.Fatal(e)
	}
	s.DB.Exec(context.Background(), "UPDATE charges SET next_at=now() WHERE tenant_id=$1 AND id=$2", tenant, c.ID)
	partial, e = w.claim(context.Background())
	if e != nil {
		t.Fatal("retry lost partial recovery", e)
	}
	if e = w.apply(context.Background(), partial, Payment{ID: "or_partial", Status: "paid"}); e != nil {
		t.Fatal(e)
	}
	failed := create("declined")
	if e = w.apply(context.Background(), failed, Payment{ID: "or_failed", Status: "failed", Detail: "payment_declined:51"}); e != nil {
		t.Fatal(e)
	}
	s.DB.QueryRow(context.Background(), "SELECT balance FROM wallets WHERE tenant_id=$1 AND customer_id=$2", tenant, customer).Scan(&balance)
	if balance != 100 {
		t.Fatal("decline or recovered payment credit mismatch", balance)
	}
	var detail string
	s.DB.QueryRow(context.Background(), "SELECT detail FROM charge_history WHERE tenant_id=$1 AND charge_id=$2", tenant, failed.ID).Scan(&detail)
	if detail != "payment_declined:51" {
		t.Fatal("missing sanitized decline", detail)
	}
	over := create("overpaid")
	if e = w.apply(context.Background(), over, Payment{ID: "or_over", Status: "paid", Detail: "payment_overpaid"}); e != nil {
		t.Fatal(e)
	}
	w.apply(context.Background(), over, Payment{ID: "or_over", Status: "paid", Detail: "payment_overpaid"})
	s.DB.QueryRow(context.Background(), "SELECT balance FROM wallets WHERE tenant_id=$1 AND customer_id=$2", tenant, customer).Scan(&balance)
	if balance != 200 {
		t.Fatal("overpayment duplicated credits", balance)
	}
}

func TestPostgresCheckoutAddressRetryUsesNewProviderKey(t *testing.T) {
	s := testStore(t)
	tenant, key, customer := newTenant(t, s)
	var keys, zips []string
	cardCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/cards") {
			cardCalls++
			if cardCalls == 1 {
				respond(w, 422, map[string]string{"error": "invalid_card"})
				return
			}
			respond(w, 200, map[string]string{"id": "card_test"})
			return
		}
		var body struct {
			Address struct {
				Zip string `json:"zip_code"`
			} `json:"address"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		zips = append(zips, body.Address.Zip)
		respond(w, 200, map[string]string{"id": "cus_test"})
	}))
	defer server.Close()
	g := NewGateway()
	g.pagarmeURL = server.URL
	a := &API{Store: s, CheckoutGateway: g, Accounts: Accounts{tenant: {"pagarme": {Secret: "sk_test", PublicKey: "pk_test", Environment: "test"}}}}
	h := a.Handler()
	offer := request(t, h, "POST", "/v1/offers", key, "offer", `{"name":"Teste","description":"","amount":1000,"currency":"brl","credits":1}`)
	var v map[string]any
	json.Unmarshal(offer.Body.Bytes(), &v)
	session := request(t, h, "POST", "/v1/checkout-sessions", key, "session", fmt.Sprintf(`{"offer_id":%q,"customer_id":%q,"reference":"retry"}`, v["id"], customer))
	json.Unmarshal(session.Body.Bytes(), &v)
	for i, zip := range []string{"01046010", "57400000"} {
		r := httptest.NewRequest("POST", v["path"].(string)+"/pay", strings.NewReader(fmt.Sprintf(`{"card_token":"token_test%d","buyer":{"name":"Cliente Teste","email":"teste@example.com","document":"12345678909","phone":"11999999999","street":"Rua Teste","number":"1","zip_code":%q,"city":"São Paulo","state":"SP"}}`, i, zip)))
		r.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		h.ServeHTTP(out, r)
		expected := 502
		if i == 1 {
			expected = 200
		}
		if out.Code != expected {
			t.Fatal(out.Code, out.Body.String())
		}
	}
	if len(keys) != 2 || keys[0] == keys[1] || zips[0] != "01046010" || zips[1] != "57400000" {
		t.Fatal("address replay used stale provider customer", keys, zips)
	}
}

func TestPostgresDocumentedCardTransitions(t *testing.T) {
	cases := []struct {
		name                   string
		orders, charges, wants []string
		balances               []int
	}{
		{"0036_processing_success", []string{"pending", "paid"}, []string{"processing", "paid"}, []string{"pending", "paid"}, []int{0, 10}},
		{"0044_processing_failure", []string{"pending", "failed"}, []string{"processing", "failed"}, []string{"pending", "failed"}, []int{0, 0}},
		{"0077_refund_processing_success", []string{"paid", "pending", "canceled"}, []string{"paid", "processing", "refunded"}, []string{"paid", "paid", "reversed"}, []int{10, 10, 0}},
		{"0093_refund_processing_rejected", []string{"paid", "pending", "failed", "paid"}, []string{"paid", "processing", "failed", "paid"}, []string{"paid", "paid", "paid", "paid"}, []int{10, 10, 10, 10}},
		{"0051_processing_canceled", []string{"pending", "canceled"}, []string{"processing", "canceled"}, []string{"pending", "reversed"}, []int{0, 0}},
		{"0069_paid_chargeback", []string{"paid", "paid"}, []string{"paid", "chargedback"}, []string{"paid", "reversed"}, []int{10, 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testStore(t)
			tenant, key, customer := newTenant(t, s)
			accounts := Accounts{tenant: {"pagarme": {Secret: "sk_test"}}}
			h := (&API{Store: s, Accounts: accounts}).Handler()
			phase := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"id":"or_sequence","amount":1000,"currency":"BRL","status":%q,"charges":[{"id":"ch_sequence","status":%q}]}`, tc.orders[phase], tc.charges[phase])
			}))
			defer server.Close()
			g := NewGateway()
			g.pagarmeURL = server.URL
			worker := &Worker{Store: s, Accounts: accounts, Gateway: g}
			out := request(t, h, "POST", "/v1/charges", key, "sequence", fmt.Sprintf(`{"customer_id":%q,"provider":"pagarme","provider_customer":"cus_test","payment_method":"card_test","amount":1000,"currency":"brl","credits":10}`, customer))
			if out.Code != 200 {
				t.Fatal(out.Code)
			}
			var created struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(out.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
			for i := range tc.orders {
				phase = i
				s.DB.Exec(context.Background(), "UPDATE charges SET next_at=now() WHERE tenant_id=$1 AND id=$2", tenant, created.ID)
				c, e := worker.claim(context.Background())
				if e != nil {
					t.Fatal(i, e)
				}
				if e = worker.process(context.Background(), c); e != nil {
					t.Fatal(i, e)
				}
				var status string
				var balance int
				s.DB.QueryRow(context.Background(), "SELECT status FROM charges WHERE tenant_id=$1 AND id=$2", tenant, c.ID).Scan(&status)
				s.DB.QueryRow(context.Background(), "SELECT balance FROM wallets WHERE tenant_id=$1 AND customer_id=$2", tenant, customer).Scan(&balance)
				if i > 0 && tc.balances[i-1] > 0 && (tc.orders[i] == "pending" || tc.orders[i] == "failed") {
					var reason string
					s.DB.QueryRow(context.Background(), "SELECT last_error FROM charges WHERE tenant_id=$1 AND id=$2", tenant, c.ID).Scan(&reason)
					want := "subsequent_operation_pending"
					if tc.orders[i] == "failed" {
						want = "subsequent_operation_failed"
					}
					if reason != want {
						t.Fatal("misleading subsequent operation reason", reason, want)
					}
				}
				if status != tc.wants[i] || balance != tc.balances[i] {
					t.Fatal(i, status, balance, tc.wants[i], tc.balances[i])
				}
			}
		})
	}
}
