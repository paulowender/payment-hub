package hub

import (
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (a *API) createPortalSession(r *http.Request, tx pgx.Tx, tenant, subID string) (any, error) {
	var provider string
	if e := tx.QueryRow(r.Context(), `SELECT provider FROM subscriptions WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, subID).Scan(&provider); e != nil {
		return nil, e
	}
	if provider != "pagarme" {
		return nil, errInvalid
	}
	if _, e := tx.Exec(r.Context(), `UPDATE portal_sessions SET revoked_at=now() WHERE tenant_id=$1 AND subscription_id=$2 AND revoked_at IS NULL`, tenant, subID); e != nil {
		return nil, e
	}
	id := token()
	var expires time.Time
	if e := tx.QueryRow(r.Context(), `INSERT INTO portal_sessions(id,tenant_id,subscription_id) VALUES($1,$2,$3) RETURNING expires_at`, id, tenant, subID).Scan(&expires); e != nil {
		return nil, e
	}
	b, e := readBrand(r.Context(), tx, tenant)
	if e != nil {
		return nil, e
	}
	path := "/portal/" + id
	return map[string]any{"id": id, "path": path, "url": strings.TrimRight(checkoutBase(b.CheckoutBaseURL, a.PublicWebhookBaseURL), "/") + path, "expires_at": expires}, nil
}
func portalHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
func (a *API) portalRoutes(mux *http.ServeMux) {
	for _, asset := range []string{"portal.js", "portal.css"} {
		name := asset
		mux.HandleFunc("GET /portal/assets/"+name, func(w http.ResponseWriter, r *http.Request) {
			kind := "text/css; charset=utf-8"
			if name == "portal.js" {
				kind = "text/javascript; charset=utf-8"
			}
			a.checkoutFile(w, r, name, kind)
		})
	}
	mux.HandleFunc("GET /portal/{session}", func(w http.ResponseWriter, r *http.Request) {
		if !checkoutID.MatchString(r.PathValue("session")) {
			http.NotFound(w, r)
			return
		}
		a.checkoutFile(w, r, "portal.html", "text/html; charset=utf-8")
	})
	mux.HandleFunc("GET /portal/{session}/data", a.portalData)
	mux.HandleFunc("POST /portal/{session}/cancel", a.portalAction)
	mux.HandleFunc("POST /portal/{session}/card", a.portalAction)
}
func (a *API) portalData(w http.ResponseWriter, r *http.Request) {
	portalHeaders(w)
	if !checkoutID.MatchString(r.PathValue("session")) {
		fail(w, errNotFound)
		return
	}
	var tenant, sub string
	e := a.Store.DB.QueryRow(r.Context(), `SELECT tenant_id,subscription_id FROM portal_sessions WHERE id=$1 AND revoked_at IS NULL AND expires_at>now()`, r.PathValue("session")).Scan(&tenant, &sub)
	if e != nil {
		respond(w, 410, map[string]string{"error": "portal_expired"})
		return
	}
	var name, currency, unit, status string
	var amount int64
	var count int
	var next time.Time
	var paused *time.Time
	e = a.Store.DB.QueryRow(r.Context(), `SELECT p.name,p.amount,p.currency,p.interval_unit,p.interval_count,s.status,s.next_at,s.paused_at FROM subscriptions s JOIN plans p ON p.tenant_id=s.tenant_id AND p.id=s.plan_id WHERE s.tenant_id=$1 AND s.id=$2`, tenant, sub).Scan(&name, &amount, &currency, &unit, &count, &status, &next, &paused)
	if e != nil {
		fail(w, e)
		return
	}
	brand, e := readBrand(r.Context(), a.Store.DB, tenant)
	if e != nil {
		fail(w, e)
		return
	}
	settings, e := loadCheckoutSettings(r.Context(), a.Store.DB, tenant)
	if e != nil {
		fail(w, e)
		return
	}
	rows, e := a.Store.DB.Query(r.Context(), `SELECT amount,currency,status,created_at FROM charges WHERE tenant_id=$1 AND subscription_id=$2 ORDER BY created_at DESC,id DESC LIMIT 10`, tenant, sub)
	if e != nil {
		fail(w, e)
		return
	}
	defer rows.Close()
	charges := []map[string]any{}
	for rows.Next() {
		var n int64
		var c, s string
		var at time.Time
		if e = rows.Scan(&n, &c, &s, &at); e != nil {
			fail(w, e)
			return
		}
		charges = append(charges, map[string]any{"amount": n, "currency": c, "status": s, "created_at": at})
	}
	if e = rows.Err(); e != nil {
		fail(w, e)
		return
	}
	var publicKey, environment string
	if settings.BuyerUpdateCard && status != "canceled" {
		ac, err := a.Accounts.Get(r.Context(), tenant, "pagarme")
		if err != nil {
			fail(w, err)
			return
		}
		publicKey, environment = ac.PublicKey, ac.Environment
	}
	respond(w, 200, map[string]any{"branding": brand, "subscription": map[string]any{"name": name, "amount": amount, "currency": currency, "interval_unit": unit, "interval_count": count, "status": status, "next_at": next, "paused_at": paused}, "charges": charges, "buyer_cancel": settings.BuyerCancel, "buyer_update_card": settings.BuyerUpdateCard, "public_key": publicKey, "environment": environment})
}

type portalCardInput struct {
	Token   string        `json:"card_token"`
	Buyer   checkoutBuyer `json:"buyer"`
	Consent bool          `json:"recurring_consent"`
}

func portalSameOrigin(r *http.Request) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return origin == scheme+"://"+r.Host
}
func (a *API) portalAction(w http.ResponseWriter, r *http.Request) {
	portalHeaders(w)
	if !portalSameOrigin(r) || !checkoutID.MatchString(r.PathValue("session")) {
		fail(w, errInvalid)
		return
	}
	// Count requests durably even when validation or the provider fails.
	var tenant, sub string
	e := a.Store.DB.QueryRow(r.Context(), `UPDATE portal_sessions SET attempts=attempts+1 WHERE id=$1 AND revoked_at IS NULL AND expires_at>now() AND attempts<10 RETURNING tenant_id,subscription_id`, r.PathValue("session")).Scan(&tenant, &sub)
	if e != nil {
		respond(w, 410, map[string]string{"error": "portal_expired_or_exhausted"})
		return
	}
	cardAction := strings.HasSuffix(r.URL.Path, "/card")
	var in portalCardInput
	if cardAction {
		if decode(r, &in) != nil || !strings.HasPrefix(in.Token, "token_") || len(in.Token) > 300 || !validBuyer(in.Buyer) || !in.Consent {
			fail(w, errInvalid)
			return
		}
	} else {
		var empty struct{}
		if decode(r, &empty) != nil {
			fail(w, errInvalid)
			return
		}
	}
	ctx := r.Context()
	tx, e := a.Store.DB.Begin(ctx)
	if e != nil {
		fail(w, e)
		return
	}
	defer tx.Rollback(ctx)
	var provider, customer, fp, status string
	e = tx.QueryRow(ctx, `SELECT provider,provider_customer,account_fingerprint,status FROM subscriptions WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, sub).Scan(&provider, &customer, &fp, &status)
	if e != nil {
		fail(w, e)
		return
	}
	// Issuance and actions acquire the subscription lock first, preventing deadlocks.
	var live bool
	e = tx.QueryRow(ctx, `SELECT revoked_at IS NULL AND expires_at>now() FROM portal_sessions WHERE id=$1 FOR UPDATE`, r.PathValue("session")).Scan(&live)
	if e != nil {
		fail(w, e)
		return
	}
	if !live {
		respond(w, 410, map[string]string{"error": "portal_expired"})
		return
	}
	settings, e := loadCheckoutSettings(ctx, tx, tenant)
	if e != nil {
		fail(w, e)
		return
	}
	if provider != "pagarme" || (!cardAction && !settings.BuyerCancel) || (cardAction && (!settings.BuyerUpdateCard || status == "canceled")) {
		respond(w, 403, map[string]string{"error": "portal_action_disabled"})
		return
	}
	if !cardAction && status == "canceled" {
		respond(w, 200, map[string]bool{"updated": true})
		return
	}
	var changing bool
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM subscription_changes WHERE tenant_id=$1 AND subscription_id=$2 AND status IN ('pending','review'))", tenant, sub).Scan(&changing); e != nil {
		fail(w, e)
		return
	}
	if changing {
		respond(w, 409, map[string]string{"error": "subscription_change_in_progress"})
		return
	}
	if cardAction {
		ac, err := a.Accounts.Get(ctx, tenant, "pagarme")
		if err != nil {
			fail(w, err)
			return
		}
		if fp != hash(ac.Secret) {
			respond(w, 409, map[string]string{"error": "provider_changed"})
			return
		}
		g := a.CheckoutGateway
		if g == nil {
			g = NewGateway()
		}
		b := in.Buyer
		card, err := g.checkoutObject(ctx, ac, "/customers/"+url.PathEscape(customer)+"/cards", r.PathValue("session")+"-"+hash(in.Token), map[string]any{"token": in.Token, "billing_address": map[string]string{"line_1": b.Number + ", " + b.Street, "zip_code": b.Zip, "city": b.City, "state": b.State, "country": "BR"}})
		if err != nil {
			respond(w, 502, map[string]string{"error": "portal_card_error"})
			return
		}
		_, e = tx.Exec(ctx, `UPDATE subscriptions SET payment_method=$3 WHERE tenant_id=$1 AND id=$2`, tenant, sub, card)
		if e == nil {
			_, e = tx.Exec(ctx, `UPDATE portal_sessions SET consent_at=now() WHERE id=$1`, r.PathValue("session"))
		}
	} else {
		_, e = tx.Exec(ctx, `UPDATE subscriptions SET status='canceled' WHERE tenant_id=$1 AND id=$2`, tenant, sub)
	}
	if e == nil {
		event := "subscription.canceled"
		if cardAction {
			event = "subscription.card_updated"
		}
		e = emit(ctx, tx, tenant, event, map[string]string{"subscription_id": sub})
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		fail(w, e)
		return
	}
	respond(w, 200, map[string]bool{"updated": true})
}
