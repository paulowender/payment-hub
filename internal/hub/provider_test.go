package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStripeContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			r.ParseForm()
			if r.Form.Get("amount") != "1500" || r.Form.Get("off_session") != "true" || r.Header.Get("Idempotency-Key") != "charge-1" {
				t.Error("invalid Stripe request")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"pi_test","status":"succeeded","amount":1500,"currency":"brl","amount_received":1500}`))
	}))
	defer server.Close()
	p := &Gateway{Client: server.Client(), stripeURL: server.URL}
	out, err := p.Create(context.Background(), Account{Secret: "sk_test"}, Charge{ID: "charge-1", Provider: "stripe", Amount: 1500, Currency: "brl", ProviderCustomer: "cus_test", PaymentMethod: "pm_test"})
	if err != nil || out.Status != "paid" || out.ID != "pi_test" {
		t.Fatalf("%+v %v", out, err)
	}
}
func TestPagarmeContract(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _, ok := r.BasicAuth()
		if !ok || u != "sk_test" {
			t.Error("auth")
		}
		var v map[string]any
		json.NewDecoder(r.Body).Decode(&v)
		if v["customer_id"] != "cus_test" || r.Header.Get("Idempotency-Key") != "charge-1" {
			t.Error(v)
		}
		w.Write([]byte(`{"id":"or_test","status":"paid","amount":1500,"currency":"BRL"}`))
	}))
	defer s.Close()
	p := &Gateway{Client: s.Client(), pagarmeURL: s.URL}
	o, e := p.Create(context.Background(), Account{Secret: "sk_test"}, Charge{ID: "charge-1", Provider: "pagarme", Amount: 1500, Currency: "brl", ProviderCustomer: "cus_test", PaymentMethod: "card_test"})
	if e != nil || o.Status != "paid" {
		t.Fatalf("%+v %v", o, e)
	}
}
func TestGatewayAmbiguousErrors(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"secret":"must not leak"}`))
	}))
	defer s.Close()
	p := &Gateway{Client: s.Client(), stripeURL: s.URL}
	_, e := p.Create(context.Background(), Account{Secret: "x"}, Charge{ID: "x", Provider: "stripe"})
	if e == nil || e.Error() != "provider_http_500" {
		t.Fatal(e)
	}
}
func TestAccountWindow(t *testing.T) {
	if (Account{}).Window() != 240*time.Second || (Account{RetryWindowSeconds: 240}).Window() != 240*time.Second {
		t.Fatal("window")
	}
}

func TestPagarmeRecurringOrigin(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Payments []struct {
				Card struct {
					Cycle  string `json:"recurrence_cycle"`
					Origin struct {
						Charge string `json:"charge_id"`
					} `json:"payment_origin"`
				} `json:"credit_card"`
			} `json:"payments"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if len(in.Payments) != 1 || in.Payments[0].Card.Cycle != "subsequent" || in.Payments[0].Card.Origin.Charge != "ch_first" {
			t.Error("missing original recurring charge")
		}
		w.Write([]byte(`{"id":"or_next","status":"paid","amount":1000,"currency":"BRL","charges":[{"id":"ch_next","status":"paid"}]}`))
	}))
	defer s.Close()
	g := &Gateway{Client: s.Client(), pagarmeURL: s.URL}
	p, e := g.Create(context.Background(), Account{Secret: "x"}, Charge{ID: "next", Provider: "pagarme", ProviderCustomer: "cus_x", PaymentMethod: "card_x", Recurring: true, Cycle: 1, Origin: "ch_first", Amount: 1000, Currency: "brl"})
	if e != nil || p.Origin != "ch_next" {
		t.Fatal(p, e)
	}
}

func TestPagarmeRefundAndReprocessedOrder(t *testing.T) {
	for _, tc := range []struct{ body, want, origin string }{
		{`{"id":"or_x","status":"canceled","amount":1000,"currency":"BRL","charges":[{"id":"ch_x","status":"canceled","paid_amount":1000,"canceled_amount":1000}]}`, "reversed", "ch_x"},
		{`{"id":"or_x","status":"paid","amount":1000,"currency":"BRL","charges":[{"id":"ch_x","status":"paid","paid_amount":1000,"canceled_amount":100}]}`, "reversed", "ch_x"},
		{`{"id":"or_x","status":"paid","amount":1000,"currency":"BRL","charges":[{"id":"ch_old","status":"failed"},{"id":"ch_paid","status":"paid","paid_amount":1000}]}`, "paid", "ch_paid"},
	} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(tc.body)) }))
		g := &Gateway{Client: s.Client(), pagarmeURL: s.URL}
		p, e := g.Fetch(context.Background(), Account{Secret: "x"}, Charge{Provider: "pagarme", ProviderID: "or_x", Amount: 1000, Currency: "brl"})
		s.Close()
		if e != nil || p.Status != tc.want || p.Origin != tc.origin {
			t.Fatalf("got %+v %v, want %s %s", p, e, tc.want, tc.origin)
		}
	}
}

func TestPagarmeSimulatorOutcomes(t *testing.T) {
	cases := []struct {
		name, status, charge, transaction string
		paid                              int
		want, detail                      string
	}{
		{"card_declined", "failed", "failed", "not_authorized", 0, "failed", "payment_declined:51"},
		{"pix_over_500", "failed", "failed", "failed", 0, "failed", "payment_declined:51"},
		{"boleto_underpaid_01046010", "paid", "underpaid", "paid", 900, "manual_review", "payment_underpaid"},
		{"boleto_overpaid_57400000", "paid", "overpaid", "paid", 1100, "paid", "payment_overpaid"},
		{"boleto_unconciled_70070300", "pending", "pending", "generated", 0, "pending", ""},
		{"card_processing_then_failed", "pending", "pending", "processing", 0, "pending", ""},
		{"card_chargeback", "paid", "chargedback", "chargedback", 1000, "reversed", "payment_reversed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"id":"or_test","amount":1000,"currency":"BRL","status":%q,"charges":[{"id":"ch_test","status":%q,"paid_amount":%d,"last_transaction":{"status":%q,"acquirer_return_code":"51","acquirer_message":"DO NOT STORE CUSTOMER OR CARD DATA","gateway_response":{"code":"200"}}}]}`, tc.status, tc.charge, tc.paid, tc.transaction)
			}))
			defer server.Close()
			g := NewGateway()
			g.pagarmeURL = server.URL
			p, e := g.Fetch(context.Background(), Account{Secret: "test"}, Charge{Provider: "pagarme", ProviderID: "or_test", Amount: 1000, Currency: "brl"})
			if e != nil || p.Status != tc.want || p.Detail != tc.detail {
				t.Fatal(p, e, tc.want, tc.detail)
			}
		})
	}
}
