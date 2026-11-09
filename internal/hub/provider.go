package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Account struct {
	PublicKey          string `json:"public_key,omitempty"`
	WebhookUser        string `json:"webhook_user,omitempty"`
	WebhookPassword    string `json:"webhook_password,omitempty"`
	Environment        string `json:"environment"`
	Secret             string `json:"secret"`
	RetryWindowSeconds int    `json:"retry_window_seconds"`
}

func (a Account) Window() time.Duration {
	if a.RetryWindowSeconds > 0 {
		return time.Duration(a.RetryWindowSeconds) * time.Second
	}
	return 240 * time.Second
}

type Accounts map[string]map[string]Account

type Charge struct {
	LastError          string    `json:"-"`
	Method             string    `json:"method"`
	Installments       int       `json:"installments"`
	PixExpires         int       `json:"-"`
	BoletoDueDays      int       `json:"-"`
	RecurringFirst     bool      `json:"-"`
	AccountFingerprint string    `json:"-"`
	Tenant             string    `json:"-"`
	ID                 string    `json:"id"`
	Customer           string    `json:"customer_id"`
	Provider           string    `json:"provider"`
	ProviderCustomer   string    `json:"provider_customer"`
	PaymentMethod      string    `json:"payment_method"`
	Amount             int64     `json:"amount"`
	Currency           string    `json:"currency"`
	Credits            int64     `json:"credits"`
	ProviderID         string    `json:"provider_id"`
	Status             string    `json:"status"`
	Lease              string    `json:"-"`
	FirstAttempt       time.Time `json:"-"`
	Recurring          bool      `json:"-"`
	Cycle              int       `json:"-"`
	Origin             string    `json:"-"`
}
type Payment struct {
	ID, Status, ClientSecret, Origin, Detail string
	Instructions                             PaymentInstructions
}
type Provider interface {
	Create(context.Context, Account, Charge) (Payment, error)
	Fetch(context.Context, Account, Charge) (Payment, error)
}
type Gateway struct {
	Client                *http.Client
	stripeURL, pagarmeURL string
}

func NewGateway() *Gateway {
	return &Gateway{Client: &http.Client{Timeout: 20 * time.Second}, stripeURL: "https://api.stripe.com/v1", pagarmeURL: "https://api.pagar.me/core/v5"}
}
func (g *Gateway) Create(ctx context.Context, a Account, c Charge) (Payment, error) {
	if c.Provider == "stripe" {
		v := url.Values{"amount": {fmt.Sprint(c.Amount)}, "currency": {c.Currency}, "customer": {c.ProviderCustomer}, "payment_method": {c.PaymentMethod}, "confirm": {"true"}, "off_session": {"true"}, "payment_method_types[]": {"card"}, "metadata[hub_charge_id]": {c.ID}, "expand[]": {"latest_charge"}}
		return g.call(ctx, a, c, http.MethodPost, g.stripeURL+"/payment_intents", strings.NewReader(v.Encode()), "application/x-www-form-urlencoded")
	}
	installments := c.Installments
	if installments == 0 {
		installments = 1
	}
	card := map[string]any{"card_id": c.PaymentMethod, "installments": installments, "operation_type": "auth_and_capture"}
	if c.Recurring {
		card["recurrence_cycle"] = "first"
		if c.Cycle > 0 && !c.RecurringFirst {
			if c.Origin == "" {
				return Payment{}, fmt.Errorf("missing_recurring_origin")
			}
			card["recurrence_cycle"] = "subsequent"
			card["payment_origin"] = map[string]string{"charge_id": c.Origin}
		}
	}
	code := c.ID
	if len(code) > 52 {
		code = code[:52]
	}
	payment := map[string]any{"payment_method": "credit_card", "credit_card": card}
	switch c.Method {
	case "pix":
		if c.Recurring {
			return Payment{}, fmt.Errorf("recurring_requires_card")
		}
		expiry := c.PixExpires
		if expiry == 0 {
			expiry = 1800
		}
		payment = map[string]any{"payment_method": "pix", "pix": map[string]int{"expires_in": expiry}}
	case "boleto":
		if c.Recurring {
			return Payment{}, fmt.Errorf("recurring_requires_card")
		}
		days := c.BoletoDueDays
		if days == 0 {
			days = 3
		}
		payment = map[string]any{"payment_method": "boleto", "boleto": map[string]any{"due_at": time.Now().UTC().AddDate(0, 0, days).Format(time.RFC3339), "instructions": "Pagar até o vencimento", "type": "DM"}}
	}
	v := map[string]any{"code": code, "customer_id": c.ProviderCustomer, "closed": true, "items": []any{map[string]any{"amount": c.Amount, "quantity": 1, "description": "SaaS subscription or credit purchase", "code": code}}, "payments": []any{payment}, "metadata": map[string]string{"hub_charge_id": c.ID}}

	b, err := json.Marshal(v)
	if err != nil {
		return Payment{}, err
	}
	return g.call(ctx, a, c, http.MethodPost, g.pagarmeURL+"/orders", strings.NewReader(string(b)), "application/json")
}
func (g *Gateway) Fetch(ctx context.Context, a Account, c Charge) (Payment, error) {
	path := g.stripeURL + "/payment_intents/" + url.PathEscape(c.ProviderID) + "?expand[]=latest_charge"
	if c.Provider == "pagarme" {
		path = g.pagarmeURL + "/orders/" + url.PathEscape(c.ProviderID)
	}
	return g.call(ctx, a, c, http.MethodGet, path, nil, "")
}
func (g *Gateway) call(ctx context.Context, a Account, c Charge, method, path string, body io.Reader, contentType string) (Payment, error) {
	req, err := http.NewRequestWithContext(ctx, method, path, body)
	if err != nil {
		return Payment{}, err
	}
	req.SetBasicAuth(a.Secret, "")
	req.Header.Set("Accept", "application/json")
	if c.Provider == "stripe" {
		req.Header.Set("Stripe-Version", "2025-06-30.basil")
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if method == "POST" {
		req.Header.Set("Idempotency-Key", c.ID)
	}
	res, err := g.Client.Do(req)
	if err != nil {
		return Payment{}, fmt.Errorf("provider_transport_error")
	}
	defer res.Body.Close()
	var raw struct {
		ID           string `json:"id"`
		Status       string `json:"status"`
		Amount       int64  `json:"amount"`
		Currency     string `json:"currency"`
		ClientSecret string `json:"client_secret"`
		LatestCharge *struct {
			Refunded       bool  `json:"refunded"`
			AmountRefunded int64 `json:"amount_refunded"`
			Disputed       bool  `json:"disputed"`
		} `json:"latest_charge"`
		Charges []struct {
			LastTransaction struct {
				QR              string `json:"qr_code"`
				QRURL           string `json:"qr_code_url"`
				Line            string `json:"line"`
				URL             string `json:"url"`
				PDF             string `json:"pdf"`
				Expires         string `json:"expires_at"`
				Due             string `json:"due_at"`
				Status          string `json:"status"`
				AcquirerCode    string `json:"acquirer_return_code"`
				GatewayResponse struct {
					Code json.RawMessage `json:"code"`
				} `json:"gateway_response"`
			} `json:"last_transaction"`
			ID             string `json:"id"`
			Status         string `json:"status"`
			PaidAmount     int64  `json:"paid_amount"`
			CanceledAmount int64  `json:"canceled_amount"`
		} `json:"charges"`
		Error *struct {
			PaymentIntent *struct {
				ID           string `json:"id"`
				Status       string `json:"status"`
				ClientSecret string `json:"client_secret"`
			} `json:"payment_intent"`
		} `json:"error"`
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&raw); err != nil {
		return Payment{}, fmt.Errorf("provider_invalid_response")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		// Stripe may return a persisted intent in a confirmation error. Fetch it instead of creating another.
		if c.Provider == "stripe" && res.StatusCode == 402 && raw.Error != nil && raw.Error.PaymentIntent != nil && raw.Error.PaymentIntent.ID != "" {
			return Payment{ID: raw.Error.PaymentIntent.ID, Status: "pending", ClientSecret: raw.Error.PaymentIntent.ClientSecret}, nil
		}
		return Payment{}, fmt.Errorf("provider_http_%d", res.StatusCode)
	}
	if raw.ID == "" || raw.Amount != c.Amount || strings.ToLower(raw.Currency) != c.Currency {
		return Payment{}, fmt.Errorf("provider_payment_mismatch")
	}
	status := "pending"
	if c.Provider == "stripe" {
		switch raw.Status {
		case "succeeded":
			status = "paid"
		case "requires_action":
			status = "requires_action"
		case "canceled", "requires_payment_method":
			status = "failed"
		}
		if raw.LatestCharge != nil && (raw.LatestCharge.Refunded || raw.LatestCharge.AmountRefunded > 0 || raw.LatestCharge.Disputed) {
			status = "reversed"
		}
	} else {
		switch raw.Status {
		case "paid":
			status = "paid"
		case "failed":
			status = "failed"
		case "canceled":
			status = "reversed"
		}
		for _, ch := range raw.Charges {
			if ch.Status == "refunded" || ch.Status == "chargedback" || ch.CanceledAmount > 0 {
				status = "reversed"
			}
		}
	}
	origin := ""
	if len(raw.Charges) > 0 {
		origin = raw.Charges[0].ID
		for _, ch := range raw.Charges {
			if ch.Status == "paid" {
				origin = ch.ID
				break
			}
		}
	}
	detail := ""
	instructions := PaymentInstructions{}
	if len(raw.Charges) > 0 {
		t := raw.Charges[0].LastTransaction
		instructions = PaymentInstructions{PixCode: t.QR, QRURL: safePaymentURL(t.QRURL), BoletoLine: t.Line, BoletoURL: safePaymentURL(t.URL), ExpiresAt: t.Expires}
		if instructions.ExpiresAt == "" {
			instructions.ExpiresAt = t.Due
		}
		if instructions.BoletoURL == "" {
			instructions.BoletoURL = safePaymentURL(t.PDF)
		}
		if status == "pending" && (t.Status == "expired" || t.Status == "failed") {
			status = "failed"
		}
	}
	if c.Provider == "pagarme" {
		underpaid, overpaid := false, false
		for _, ch := range raw.Charges {
			if ch.Status == "underpaid" || (status == "paid" && ch.PaidAmount > 0 && ch.PaidAmount < c.Amount) {
				underpaid = true
			}
			if ch.Status == "overpaid" || ch.PaidAmount > c.Amount {
				overpaid = true
			}
		}
		if status != "reversed" && underpaid {
			status = "manual_review"
			detail = "payment_underpaid"
		} else if status == "paid" && overpaid {
			detail = "payment_overpaid"
		}
		if status == "reversed" {
			detail = "payment_reversed"
		}
		if status == "failed" {
			detail = "payment_declined"
			if len(raw.Charges) > 0 {
				t := raw.Charges[0].LastTransaction
				code := t.AcquirerCode
				if code == "" {
					code = strings.Trim(string(t.GatewayResponse.Code), "\"")
				}
				if safeProviderCode.MatchString(code) {
					detail += ":" + code
				}
			}
		}
	}
	return Payment{ID: raw.ID, Status: status, ClientSecret: raw.ClientSecret, Origin: origin, Instructions: instructions, Detail: detail}, nil
}

func safePaymentURL(value string) string {
	u, e := url.Parse(value)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return ""
	}
	return value
}

var safeProviderCode = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
