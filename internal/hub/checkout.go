package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type Branding struct {
	CheckoutBaseURL string `json:"checkout_base_url"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	LogoURL         string `json:"logo_url"`
	Primary         string `json:"primary_color"`
	Background      string `json:"background_color"`
}
type checkoutSnapshot struct {
	Settings    *CheckoutSettings `json:"settings,omitempty"`
	Branding    Branding          `json:"branding"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Amount      int64             `json:"amount"`
	Currency    string            `json:"currency"`
	Credits     int64             `json:"credits"`
	Plan        string            `json:"plan_id"`
	Unit        string            `json:"interval_unit"`
	Count       int               `json:"interval_count"`
}

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
var checkoutID = regexp.MustCompile(`^[a-f0-9]{64}$`)
var digits = regexp.MustCompile(`^[0-9]+$`)

func validBranding(b Branding) bool {
	if b.CheckoutBaseURL != "" {
		u, e := url.Parse(b.CheckoutBaseURL)
		if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || len(b.CheckoutBaseURL) > 500 {
			return false
		}
	}

	if len(strings.TrimSpace(b.Name)) < 1 || len(b.Name) > 100 || len(b.Description) > 300 || !hexColor.MatchString(b.Primary) || !hexColor.MatchString(b.Background) {
		return false
	}
	if b.LogoURL == "" {
		return true
	}
	u, e := url.Parse(b.LogoURL)
	return e == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && len(b.LogoURL) <= 1000
}
func readBrand(ctx context.Context, q credentialReader, tenant string) (Branding, error) {
	var b Branding
	e := q.QueryRow(ctx, `SELECT COALESCE(b.name,t.name),COALESCE(b.description,''),COALESCE(b.logo_url,''),COALESCE(b.primary_color,'#6e91ef'),COALESCE(b.background_color,'#101720'),COALESCE(b.checkout_base_url,'') FROM tenants t LEFT JOIN checkout_branding b ON b.tenant_id=t.id WHERE t.id=$1`, tenant).Scan(&b.Name, &b.Description, &b.LogoURL, &b.Primary, &b.Background, &b.CheckoutBaseURL)
	return b, e
}
func (a *API) readBranding(w http.ResponseWriter, r *http.Request, tenant string) {
	b, e := readBrand(r.Context(), a.Store.DB, tenant)
	if e != nil {
		fail(w, e)
		return
	}
	respond(w, 200, b)
}
func (a *API) checkoutMutate(r *http.Request, tx pgx.Tx, tenant, path string) (any, error) {
	ctx := r.Context()
	switch path {
	case "v1/branding":
		var b Branding
		if decode(r, &b) != nil || !validBranding(b) {
			return nil, errInvalid
		}
		_, e := tx.Exec(ctx, `INSERT INTO checkout_branding(tenant_id,name,description,logo_url,primary_color,background_color,checkout_base_url) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(tenant_id) DO UPDATE SET name=excluded.name,description=excluded.description,logo_url=excluded.logo_url,primary_color=excluded.primary_color,background_color=excluded.background_color,checkout_base_url=excluded.checkout_base_url`, tenant, b.Name, b.Description, b.LogoURL, b.Primary, b.Background, b.CheckoutBaseURL)
		return b, e
	case "v1/offers":
		var in struct {
			Metadata    json.RawMessage `json:"metadata"`
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Amount      int64           `json:"amount"`
			Currency    string          `json:"currency"`
			Credits     int64           `json:"credits"`
			Plan        string          `json:"plan_id"`
			Unit        string          `json:"interval_unit"`
			Count       int             `json:"interval_count"`
		}
		if decode(r, &in) != nil || len(strings.TrimSpace(in.Name)) < 1 || len(in.Name) > 200 || len(in.Description) > 1000 {
			return nil, errInvalid
		}
		metadata, err := decodeMetadata(in.Metadata)
		if err != nil {
			return nil, err
		}
		if in.Plan == "" && in.Unit != "" {
			if !validInterval(in.Unit, in.Count) || !validAmount(in.Amount) || !validCredits(in.Credits) || in.Currency != "brl" {
				return nil, errInvalid
			}
			in.Plan = token()
			if _, e := tx.Exec(ctx, "INSERT INTO plans(tenant_id,id,name,amount,currency,credits,interval_unit,interval_count) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", tenant, in.Plan, in.Name, in.Amount, in.Currency, in.Credits, in.Unit, in.Count); e != nil {
				return nil, e
			}
		}
		if in.Plan != "" {
			if e := tx.QueryRow(ctx, "SELECT amount,currency,credits FROM plans WHERE tenant_id=$1 AND id=$2", tenant, in.Plan).Scan(&in.Amount, &in.Currency, &in.Credits); e != nil {
				return nil, e
			}
		}
		if !validAmount(in.Amount) || !validCredits(in.Credits) || in.Currency != "brl" {
			return nil, errInvalid
		}
		id := token()
		_, e := tx.Exec(ctx, `INSERT INTO offers(tenant_id,id,name,description,amount,currency,credits,plan_id,metadata) VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),COALESCE($9::jsonb,'{}'::jsonb))`, tenant, id, in.Name, in.Description, in.Amount, in.Currency, in.Credits, in.Plan, in.Metadata)
		return map[string]any{"id": id, "plan_id": in.Plan, "metadata": metadata}, e
	case "v1/checkout-sessions":
		var in struct {
			Metadata  json.RawMessage `json:"metadata"`
			Offer     string          `json:"offer_id"`
			Customer  string          `json:"customer_id"`
			Reference string          `json:"reference"`
		}
		if decode(r, &in) != nil || len(in.Reference) < 1 || len(in.Reference) > 200 {
			return nil, errInvalid
		}
		metadata, err := decodeMetadata(in.Metadata)
		if err != nil {
			return nil, err
		}
		var snap checkoutSnapshot
		e := tx.QueryRow(ctx, `SELECT o.name,o.description,o.amount,o.currency,o.credits,COALESCE(o.plan_id,''),COALESCE(p.interval_unit,''),COALESCE(p.interval_count,0) FROM offers o LEFT JOIN plans p ON p.tenant_id=o.tenant_id AND p.id=o.plan_id WHERE o.tenant_id=$1 AND o.id=$2 AND o.active`, tenant, in.Offer).Scan(&snap.Name, &snap.Description, &snap.Amount, &snap.Currency, &snap.Credits, &snap.Plan, &snap.Unit, &snap.Count)
		if e != nil {
			return nil, e
		}
		if a.Accounts == nil {
			return nil, errInvalid
		}
		ac, e := a.Accounts.Get(ctx, tenant, "pagarme")
		if e != nil {
			return nil, e
		}
		cfg, e := loadCheckoutSettings(ctx, tx, tenant)
		if e != nil {
			return nil, e
		}
		if snap.Plan != "" {
			if !cfg.Card {
				return nil, errInvalid
			}
			cfg.Pix = false
			cfg.Boleto = false
			cfg.MaxInstallments = 1
		}
		if cfg.Card && ac.PublicKey == "" {
			return nil, errInvalid
		}
		snap.Settings = &cfg
		snap.Branding, e = readBrand(ctx, tx, tenant)
		if e != nil {
			return nil, e
		}
		b, e := json.Marshal(snap)
		if e != nil {
			return nil, e
		}
		id := token()
		var expires time.Time
		e = tx.QueryRow(ctx, `INSERT INTO checkout_sessions(tenant_id,id,offer_id,customer_id,reference,snapshot,account_fingerprint,metadata) VALUES($1,$2,$3,$4,$5,$6,$7,COALESCE($8::jsonb,'{}'::jsonb)) RETURNING expires_at`, tenant, id, in.Offer, in.Customer, in.Reference, b, hash(ac.Secret), in.Metadata).Scan(&expires)
		return map[string]any{"metadata": metadata, "id": id, "path": "/checkout/" + id, "expires_at": expires, "url": strings.TrimRight(checkoutBase(snap.Branding.CheckoutBaseURL, a.PublicWebhookBaseURL), "/") + "/checkout/" + id}, e
	}
	return nil, errNotFound
}
func checkoutBase(custom, fallback string) string {
	if custom != "" {
		return custom
	}
	return fallback
}

func (a *API) checkoutRoutes(mux *http.ServeMux) {
	for _, asset := range []string{"checkout.js", "checkout.css"} {
		name := asset
		mux.HandleFunc("GET /checkout/assets/"+name, func(w http.ResponseWriter, r *http.Request) {
			kind := "text/css; charset=utf-8"
			if name == "checkout.js" {
				kind = "text/javascript; charset=utf-8"
			}
			a.checkoutFile(w, r, name, kind)
		})
	}
	mux.HandleFunc("GET /checkout/{session}", func(w http.ResponseWriter, r *http.Request) {
		if !checkoutID.MatchString(r.PathValue("session")) {
			http.NotFound(w, r)
			return
		}
		a.checkoutFile(w, r, "checkout.html", "text/html; charset=utf-8")
	})
	mux.HandleFunc("GET /checkout/{session}/data", a.checkoutData)
	mux.HandleFunc("POST /checkout/{session}/pay", a.checkoutPay)
}
func (a *API) checkoutFile(w http.ResponseWriter, r *http.Request, name, kind string) {
	b, e := consoleFiles.ReadFile("web/" + name)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src https:; connect-src 'self' https://api.pagar.me; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	w.Write(b)
}
func (a *API) checkoutData(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("session")
	if !checkoutID.MatchString(id) {
		fail(w, errNotFound)
		return
	}
	var tenant, fp, status string
	var snap []byte
	var expires time.Time
	var instructions []byte
	var method, reason string
	var installments int
	e := a.Store.DB.QueryRow(r.Context(), `SELECT s.tenant_id,s.snapshot,s.expires_at,s.account_fingerprint,COALESCE(c.status,'open'),COALESCE(c.payment_instructions,'{}'::jsonb),COALESCE(c.method,''),COALESCE(c.installments,1),COALESCE(c.last_error,'') FROM checkout_sessions s LEFT JOIN charges c ON c.tenant_id=s.tenant_id AND c.id=s.charge_id WHERE s.id=$1`, id).Scan(&tenant, &snap, &expires, &fp, &status, &instructions, &method, &installments, &reason)
	if e != nil {
		fail(w, e)
		return
	}
	if status == "open" && time.Now().After(expires) {
		respond(w, 410, map[string]string{"error": "checkout_expired"})
		return
	}
	ac, e := a.Accounts.Get(r.Context(), tenant, "pagarme")
	if e != nil {
		fail(w, e)
		return
	}
	if hash(ac.Secret) != fp {
		respond(w, 409, map[string]string{"error": "provider_changed"})
		return
	}
	respond(w, 200, map[string]any{"offer": json.RawMessage(snap), "status": status, "expires_at": expires, "public_key": ac.PublicKey, "environment": ac.Environment, "instructions": json.RawMessage(instructions), "method": method, "installments": installments, "reason": reason})
}

type checkoutBuyer struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Document string `json:"document"`
	Phone    string `json:"phone"`
	Street   string `json:"street"`
	Number   string `json:"number"`
	Zip      string `json:"zip_code"`
	City     string `json:"city"`
	State    string `json:"state"`
}

func validBuyer(b checkoutBuyer) bool {
	m, e := mail.ParseAddress(b.Email)
	return e == nil && m.Address == b.Email && len(b.Email) <= 200 && len(strings.TrimSpace(b.Name)) >= 3 && len(b.Name) <= 100 && ((len(b.Document) == 11 || len(b.Document) == 14) && digits.MatchString(b.Document)) && len(b.Phone) >= 10 && len(b.Phone) <= 11 && digits.MatchString(b.Phone) && len(b.Street) >= 2 && len(b.Street) <= 150 && len(b.Number) > 0 && len(b.Number) <= 20 && len(b.Zip) == 8 && digits.MatchString(b.Zip) && len(b.City) >= 2 && len(b.City) <= 100 && regexp.MustCompile(`^[A-Z]{2}$`).MatchString(b.State)
}

// Only tokens reach this handler. PAN, expiry and CVV are rejected by decode.
func (a *API) checkoutPay(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Type") != "application/json" {
		fail(w, errInvalid)
		return
	}
	var in struct {
		Token        string        `json:"card_token"`
		Method       string        `json:"method"`
		Installments int           `json:"installments"`
		Buyer        checkoutBuyer `json:"buyer"`
		Consent      bool          `json:"recurring_consent"`
	}
	if decode(r, &in) != nil || !validBuyer(in.Buyer) {
		fail(w, errInvalid)
		return
	}
	if in.Method == "" {
		in.Method = "card"
	}
	if in.Installments == 0 {
		in.Installments = 1
	}
	if (in.Method != "card" && in.Method != "pix" && in.Method != "boleto") || (in.Method == "card" && (!strings.HasPrefix(in.Token, "token_") || len(in.Token) > 200)) || (in.Method != "card" && (in.Token != "" || in.Installments != 1)) {
		fail(w, errInvalid)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	// Bound provider calls per bearer link; invalid payloads never reach the provider.
	var attempts int
	e := a.Store.DB.QueryRow(ctx, "UPDATE checkout_sessions SET attempts=LEAST(attempts+1,11) WHERE id=$1 AND charge_id IS NULL AND expires_at>now() RETURNING attempts", r.PathValue("session")).Scan(&attempts)
	if e != nil && e != pgx.ErrNoRows {
		fail(w, e)
		return
	}
	if attempts > 10 {
		respond(w, 429, map[string]string{"error": "checkout_attempt_limit"})
		return
	}
	tx, e := a.Store.DB.Begin(ctx)
	if e != nil {
		fail(w, e)
		return
	}
	defer tx.Rollback(ctx)
	var tenant, customer, charge, fp string
	var raw []byte
	var expires time.Time
	e = tx.QueryRow(ctx, `SELECT tenant_id,customer_id,COALESCE(charge_id,''),snapshot,expires_at,account_fingerprint FROM checkout_sessions WHERE id=$1 FOR UPDATE`, r.PathValue("session")).Scan(&tenant, &customer, &charge, &raw, &expires, &fp)
	if e != nil {
		fail(w, e)
		return
	}
	if charge != "" {
		respond(w, 200, map[string]bool{"submitted": true})
		return
	}
	if time.Now().After(expires) {
		respond(w, 410, map[string]string{"error": "checkout_expired"})
		return
	}
	var snap checkoutSnapshot
	if json.Unmarshal(raw, &snap) != nil {
		fail(w, errInvalid)
		return
	}
	cfg := defaultCheckoutSettings()
	if snap.Settings != nil {
		cfg = *snap.Settings
	}
	allowed := (in.Method == "card" && cfg.Card) || (in.Method == "pix" && cfg.Pix) || (in.Method == "boleto" && cfg.Boleto)
	if !allowed || in.Installments < 1 || in.Installments > cfg.MaxInstallments || (in.Installments > 1 && snap.Amount/int64(in.Installments) < cfg.MinInstallment) || (snap.Plan != "" && (in.Method != "card" || in.Installments != 1)) {
		fail(w, errInvalid)
		return
	}
	if snap.Plan != "" && !in.Consent {
		respond(w, 400, map[string]string{"error": "recurring_consent_required"})
		return
	}
	ac, e := a.Accounts.Get(ctx, tenant, "pagarme")
	if e != nil {
		fail(w, e)
		return
	}
	if fp != hash(ac.Secret) {
		respond(w, 409, map[string]string{"error": "provider_changed"})
		return
	}
	g := NewGateway()
	if a.CheckoutGateway != nil {
		g = a.CheckoutGateway
	}
	sid := r.PathValue("session")
	b := in.Buyer
	typ := "individual"
	if len(b.Document) == 14 {
		typ = "company"
	}
	buyerBinding, marshalErr := json.Marshal(b)
	if marshalErr != nil {
		fail(w, marshalErr)
		return
	}
	pc, e := g.checkoutObject(ctx, ac, "/customers", sid+"-"+hash(string(buyerBinding)), map[string]any{"name": b.Name, "email": b.Email, "document": b.Document, "type": typ, "address": map[string]string{"line_1": b.Number + ", " + b.Street, "zip_code": b.Zip, "city": b.City, "state": b.State, "country": "BR"}, "code": sid[:52], "phones": map[string]any{"mobile_phone": map[string]string{"country_code": "55", "area_code": b.Phone[:2], "number": b.Phone[2:]}}})
	if e != nil {
		respond(w, 502, map[string]string{"error": "checkout_provider_error"})
		return
	}
	card := ""
	if in.Method == "card" {
		card, e = g.checkoutObject(ctx, ac, "/customers/"+url.PathEscape(pc)+"/cards", sid+"-"+hash(in.Token), map[string]any{"token": in.Token, "billing_address": map[string]string{"line_1": b.Number + ", " + b.Street, "zip_code": b.Zip, "city": b.City, "state": b.State, "country": "BR"}})
		if e != nil {
			respond(w, 502, map[string]string{"error": "checkout_card_error"})
			return
		}
	}
	charge = token()
	var sub any
	var cycle any
	if snap.Plan != "" {
		sub = token()
		cycle = 0
		now := time.Now().UTC()
		_, e = tx.Exec(ctx, `INSERT INTO subscriptions(tenant_id,id,customer_id,plan_id,provider,provider_customer,payment_method,anchor,next_at,cycle,account_fingerprint) VALUES($1,$2,$3,$4,'pagarme',$5,$6,$7,$8,1,$9)`, tenant, sub, customer, snap.Plan, pc, card, now, cycleAt(now, snap.Unit, snap.Count, 1), fp)
		if e != nil {
			fail(w, e)
			return
		}
	}
	_, e = tx.Exec(ctx, `INSERT INTO charges(tenant_id,id,customer_id,provider,provider_customer,payment_method,amount,currency,credits,account_fingerprint,subscription_id,cycle,method,installments,pix_expires,boleto_due_days) VALUES($1,$2,$3,'pagarme',$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, tenant, charge, customer, pc, card, snap.Amount, snap.Currency, snap.Credits, fp, sub, cycle, in.Method, in.Installments, cfg.PixExpires, cfg.BoletoDueDays)
	if e == nil {
		_, e = tx.Exec(ctx, "UPDATE checkout_sessions SET charge_id=$2,subscription_id=$3,consent_at=CASE WHEN $3::text IS NOT NULL THEN now() ELSE NULL END WHERE id=$1", sid, charge, sub)
	}
	if e == nil {
		e = emitFinancialEvent(ctx, tx, tenant, "checkout.submitted", charge, nil)
	}
	if e == nil {
		e = emitFinancialEvent(ctx, tx, tenant, "charge.queued", charge, nil)
	}
	if e == nil && sub != nil {
		e = emitSubscriptionEvent(ctx, tx, tenant, "subscription.created", sub.(string), nil)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		fail(w, e)
		return
	}
	respond(w, 200, map[string]bool{"submitted": true})
}
func (g *Gateway) checkoutObject(ctx context.Context, ac Account, path, key string, payload any) (string, error) {
	b, e := json.Marshal(payload)
	if e != nil {
		return "", e
	}
	r, e := http.NewRequestWithContext(ctx, "POST", g.pagarmeURL+path, bytes.NewReader(b))
	if e != nil {
		return "", e
	}
	r.SetBasicAuth(ac.Secret, "")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", key)
	res, e := g.Client.Do(r)
	if e != nil {
		return "", fmt.Errorf("checkout_provider_transport")
	}
	defer res.Body.Close()
	var result struct {
		ID string `json:"id"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&result) != nil || res.StatusCode < 200 || res.StatusCode >= 300 || result.ID == "" {
		return "", fmt.Errorf("checkout_provider_rejected")
	}
	return result.ID, nil
}
