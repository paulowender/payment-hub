package hub

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type API struct {
	CheckoutGateway         *Gateway
	WebhookDiagnosticTenant string
	WebhookDiagnosticUntil  time.Time
	PublicWebhookBaseURL    string
	Store                   *Store
	Accounts                AccountSource
	Vault                   *Vault
	AdminSecret             string
}
type tenantContext struct{}

func decode(r *http.Request, v any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, (1<<20)+1))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errInvalid
	}
	return nil
}
func respond(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, err error) {
	code, msg := 500, "internal_error"
	switch {
	case errors.Is(err, errInvalid):
		code, msg = 400, errInvalid.Error()
	case errors.Is(err, errNotFound), errors.Is(err, pgx.ErrNoRows):
		code, msg = 404, "not_found"
	case errors.Is(err, errConflict):
		code, msg = 409, errConflict.Error()
	case errors.Is(err, errFunds):
		code, msg = 409, errFunds.Error()
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "23503":
			code, msg = 404, "referenced_resource_not_found"
		case "23505":
			code, msg = 409, "resource_already_exists"
		case "23514", "22003", "22008", "22007", "22P02", "22P05":
			code, msg = 400, "invalid_request"
		}
	}
	if code == 500 {
		slog.Error("request failed", "error", err.Error())
	}
	respond(w, code, map[string]string{"error": msg})
}
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	a.consoleRoutes(mux)
	a.checkoutRoutes(mux)
	a.portalRoutes(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := a.Store.DB.Ping(ctx); err != nil {
			respond(w, 503, map[string]string{"status": "unavailable"})
			return
		}
		respond(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /admin/tenants", a.provision)
	mux.HandleFunc("POST /admin/tenants/{tenant}/rotate-key", a.rotateKey)
	mux.Handle("/v1/", a.authenticate(http.HandlerFunc(a.business)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		mux.ServeHTTP(w, r)
	})
}
func (a *API) provision(w http.ResponseWriter, r *http.Request) {
	supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(a.AdminSecret) < 32 || subtle.ConstantTimeCompare([]byte(supplied), []byte(a.AdminSecret)) != 1 {
		respond(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if decode(r, &in) != nil || len(strings.TrimSpace(in.Name)) < 1 || len(in.Name) > 200 {
		fail(w, errInvalid)
		return
	}
	id, key := token(), token()
	_, err := a.Store.DB.Exec(r.Context(), "INSERT INTO tenants(id,name,api_hash) VALUES($1,$2,$3)", id, in.Name, hash(key))
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, 201, map[string]string{"id": id, "name": in.Name, "api_key": key})
}
func (a *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") || len(strings.TrimPrefix(auth, "Bearer ")) != 64 {
			respond(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		var tenant string
		err := a.Store.DB.QueryRow(r.Context(), "SELECT id FROM tenants WHERE api_hash=$1", hash(strings.TrimPrefix(auth, "Bearer "))).Scan(&tenant)
		if errors.Is(err, pgx.ErrNoRows) {
			respond(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		if err != nil {
			fail(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tenantContext{}, tenant)))
	})
}
func (a *API) business(w http.ResponseWriter, r *http.Request) {
	tenant := r.Context().Value(tenantContext{}).(string)
	if a.managementAPI(w, r, tenant) {
		return
	}
	if r.Method == http.MethodGet {
		a.read(w, r, tenant)
		return
	}
	if r.Method != http.MethodPost {
		respond(w, 405, map[string]string{"error": "method_not_allowed"})
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if len(key) < 1 || len(key) > 200 {
		fail(w, errInvalid)
		return
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		fail(w, errInvalid)
		return
	}
	fingerprint := hash(r.Method + ":" + r.URL.EscapedPath() + ":" + string(b))
	result, err := a.Store.write(r.Context(), tenant, key, fingerprint, func(tx pgx.Tx) (any, error) {
		r.Body = io.NopCloser(strings.NewReader(string(b)))
		return a.mutate(r, tx, tenant, key)
	})
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	w.Write(result)
}
func (a *API) mutate(r *http.Request, tx pgx.Tx, tenant, key string) (any, error) {
	ctx := r.Context()
	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")
	if len(parts) >= 4 && parts[1] == "subscriptions" {
		if len(parts) == 4 && (parts[3] == "pause" || parts[3] == "resume" || parts[3] == "cancel" || parts[3] == "change-plan") {
			return a.lifecycle(r, tx, tenant, parts[2], parts[3])
		}
		if len(parts) == 5 && parts[3] == "change-plan" && parts[4] == "schedule" {
			return a.lifecycle(r, tx, tenant, parts[2], "schedule")
		}
		if len(parts) == 6 && parts[3] == "change-plan" && parts[4] == "schedule" && parts[5] == "cancel" {
			return a.lifecycle(r, tx, tenant, parts[2], "schedule-cancel")
		}
		if len(parts) == 5 && parts[3] == "change-plan" && parts[4] == "preview" {
			return a.lifecycle(r, tx, tenant, parts[2], "preview")
		}
	}
	switch path {
	case "v1/checkout-settings":
		return a.saveCheckoutSettings(r, tx, tenant)
	case "v1/branding", "v1/offers", "v1/checkout-sessions":
		return a.checkoutMutate(r, tx, tenant, path)

	case "v1/customers":
		var in struct {
			Metadata   json.RawMessage `json:"metadata"`
			ExternalID string          `json:"external_id"`
		}
		if decode(r, &in) != nil || len(in.ExternalID) < 1 || len(in.ExternalID) > 200 {
			return nil, errInvalid
		}
		metadata, err := decodeMetadata(in.Metadata)
		if err != nil {
			return nil, err
		}
		id := token()
		if _, err := tx.Exec(ctx, "INSERT INTO customers(tenant_id,id,external_id,metadata) VALUES($1,$2,$3,COALESCE($4::jsonb,'{}'::jsonb))", tenant, id, in.ExternalID, in.Metadata); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO wallets(tenant_id,customer_id) VALUES($1,$2)", tenant, id); err != nil {
			return nil, err
		}
		return map[string]any{"id": id, "external_id": in.ExternalID, "metadata": metadata}, nil
	case "v1/plans":
		var in struct {
			Metadata json.RawMessage `json:"metadata"`
			Name     string          `json:"name"`
			Amount   int64           `json:"amount"`
			Currency string          `json:"currency"`
			Credits  int64           `json:"credits"`
			Unit     string          `json:"interval_unit"`
			Count    int             `json:"interval_count"`
		}
		if decode(r, &in) != nil || len(strings.TrimSpace(in.Name)) == 0 || len(in.Name) > 200 || !validAmount(in.Amount) || !validCredits(in.Credits) || !validInterval(in.Unit, in.Count) || (in.Currency != "brl" && in.Currency != "usd") {
			return nil, errInvalid
		}
		metadata, err := decodeMetadata(in.Metadata)
		if err != nil {
			return nil, err
		}
		id := token()
		_, err = tx.Exec(ctx, "INSERT INTO plans(tenant_id,id,name,amount,currency,credits,interval_unit,interval_count,metadata) VALUES($1,$2,$3,$4,$5,$6,$7,$8,COALESCE($9::jsonb,'{}'::jsonb))", tenant, id, in.Name, in.Amount, in.Currency, in.Credits, in.Unit, in.Count, in.Metadata)
		return map[string]any{"id": id, "metadata": metadata}, err
	case "v1/charges":
		var in struct {
			Metadata         json.RawMessage `json:"metadata"`
			Customer         string          `json:"customer_id"`
			Provider         string          `json:"provider"`
			ProviderCustomer string          `json:"provider_customer"`
			PaymentMethod    string          `json:"payment_method"`
			Amount           int64           `json:"amount"`
			Currency         string          `json:"currency"`
			Credits          int64           `json:"credits"`
		}
		if decode(r, &in) != nil || !validAmount(in.Amount) || !validCredits(in.Credits) {
			return nil, errInvalid
		}
		metadata, err := decodeMetadata(in.Metadata)
		if err != nil {
			return nil, err
		}
		ac, err := a.paymentAccount(ctx, tx, tenant, in.Provider, in.ProviderCustomer, in.PaymentMethod, in.Currency)
		if err != nil {
			return nil, err
		}
		id := token()
		_, err = tx.Exec(ctx, "INSERT INTO charges(tenant_id,id,customer_id,provider,provider_customer,payment_method,amount,currency,credits,account_fingerprint,metadata) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,COALESCE($11::jsonb,'{}'::jsonb))", tenant, id, in.Customer, in.Provider, in.ProviderCustomer, in.PaymentMethod, in.Amount, in.Currency, in.Credits, hash(ac.Secret), in.Metadata)
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": id, "status": "queued", "metadata": metadata}, emitFinancialEvent(ctx, tx, tenant, "charge.queued", id, nil)
	case "v1/subscriptions":
		var in struct {
			Metadata         json.RawMessage `json:"metadata"`
			Customer         string          `json:"customer_id"`
			Plan             string          `json:"plan_id"`
			Provider         string          `json:"provider"`
			ProviderCustomer string          `json:"provider_customer"`
			PaymentMethod    string          `json:"payment_method"`
			StartAt          *time.Time      `json:"start_at"`
		}
		if decode(r, &in) != nil {
			return nil, errInvalid
		}
		metadata, err := decodeMetadata(in.Metadata)
		if err != nil {
			return nil, err
		}
		var currency string
		if err := tx.QueryRow(ctx, "SELECT currency FROM plans WHERE tenant_id=$1 AND id=$2", tenant, in.Plan).Scan(&currency); err != nil {
			return nil, err
		}
		ac, err := a.paymentAccount(ctx, tx, tenant, in.Provider, in.ProviderCustomer, in.PaymentMethod, currency)
		if err != nil {
			return nil, err
		}
		start := time.Now().UTC()
		if in.StartAt != nil {
			start = in.StartAt.UTC()
			if start.Before(time.Now().Add(-time.Minute)) || start.After(time.Now().AddDate(10, 0, 0)) {
				return nil, errInvalid
			}
		}
		id := token()
		_, err = tx.Exec(ctx, "INSERT INTO subscriptions(tenant_id,id,customer_id,plan_id,provider,provider_customer,payment_method,anchor,next_at,account_fingerprint,metadata) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8,$9,COALESCE($10::jsonb,'{}'::jsonb))", tenant, id, in.Customer, in.Plan, in.Provider, in.ProviderCustomer, in.PaymentMethod, start, hash(ac.Secret), in.Metadata)
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": id, "status": "active", "start_at": start, "metadata": metadata}, emitSubscriptionEvent(ctx, tx, tenant, "subscription.created", id, nil)
	}
	if len(parts) == 4 && parts[1] == "offers" && parts[3] == "archive" {
		var in struct{}
		if decode(r, &in) != nil {
			return nil, errInvalid
		}
		tag, e := tx.Exec(ctx, "UPDATE offers SET active=false WHERE tenant_id=$1 AND id=$2", tenant, parts[2])
		if e != nil {
			return nil, e
		}
		if tag.RowsAffected() == 0 {
			return nil, errNotFound
		}
		return map[string]bool{"archived": true}, nil
	}
	if len(parts) == 4 && parts[1] == "subscriptions" && parts[3] == "portal" {
		var in struct{}
		if decode(r, &in) != nil {
			return nil, errInvalid
		}
		return a.createPortalSession(r, tx, tenant, parts[2])
	}
	if len(parts) == 4 && parts[1] == "wallets" && (parts[3] == "consume" || parts[3] == "grant") {
		var in struct {
			Credits   int64  `json:"credits"`
			Reference string `json:"reference"`
		}
		if decode(r, &in) != nil || in.Credits < 1 || !validCredits(in.Credits) || len(in.Reference) < 1 || len(in.Reference) > 200 {
			return nil, errInvalid
		}
		delta, reason := in.Credits, "grant"
		if parts[3] == "consume" {
			delta = -delta
			reason = "consume"
		}
		// Reference uniqueness belongs to the API key; idempotency keys ensure the request payload is unchanged.
		ref := parts[2] + ":" + in.Reference
		var existingDelta int64
		err := tx.QueryRow(ctx, "SELECT delta FROM ledger WHERE tenant_id=$1 AND reason=$2 AND reference=$3", tenant, reason, ref).Scan(&existingDelta)
		if err == nil {
			return nil, errConflict
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		balance, err := changeWallet(ctx, tx, tenant, parts[2], delta, reason, ref, true)
		if err != nil {
			return nil, err
		}
		return map[string]int64{"balance": balance}, emit(ctx, tx, tenant, "wallet."+reason, map[string]any{"customer_id": parts[2], "delta": delta, "balance": balance, "reference": in.Reference})
	}
	return nil, errNotFound
}
func (a *API) paymentAccount(ctx context.Context, tx pgx.Tx, tenant, provider, customer, method, currency string) (Account, error) {
	if a.Accounts == nil || (provider != "stripe" && provider != "pagarme") || len(customer) < 1 || len(customer) > 200 || len(method) < 1 || len(method) > 200 || (currency != "brl" && !(provider == "stripe" && currency == "usd")) {
		return Account{}, errInvalid
	}
	var ac Account
	var e error
	if v, ok := a.Accounts.(*Vault); ok {
		ac, e = v.load(ctx, tx, tenant, provider)
	} else {
		ac, e = a.Accounts.Get(ctx, tenant, provider)
	}
	if errors.Is(e, pgx.ErrNoRows) || errors.Is(e, errNotFound) {
		return Account{}, errInvalid
	}
	return ac, e
}
func (a *API) read(w http.ResponseWriter, r *http.Request, tenant string) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 4 && parts[1] == "subscriptions" && (parts[3] == "billing-ledger" || parts[3] == "changes") {
		a.readSubscriptionAudit(w, r, tenant, parts[2], parts[3])
		return
	}
	ctx := r.Context()
	if len(parts) == 3 && parts[1] == "wallets" {
		var balance int64
		if err := a.Store.DB.QueryRow(ctx, "SELECT balance FROM wallets WHERE tenant_id=$1 AND customer_id=$2", tenant, parts[2]).Scan(&balance); err != nil {
			fail(w, err)
			return
		}
		respond(w, 200, map[string]any{"customer_id": parts[2], "balance": balance})
		return
	}
	if len(parts) == 2 && parts[1] == "checkout-settings" {
		a.readCheckoutSettings(w, r, tenant)
		return
	}
	if len(parts) == 4 && parts[1] == "charges" && parts[3] == "history" {
		a.readChargeHistory(w, r, tenant, parts[2])
		return
	}
	if len(parts) == 2 && parts[1] == "branding" {
		a.readBranding(w, r, tenant)
		return
	}
	tables := map[string]string{"offers": "offers", "checkout-sessions": "checkout_sessions", "customers": "customers", "plans": "plans", "subscriptions": "api_subscriptions", "charges": "api_charge_details", "events": "events", "ledger": "ledger"}
	if len(parts) < 2 || len(parts) > 3 {
		fail(w, errNotFound)
		return
	}
	table, ok := tables[parts[1]]
	if !ok {
		fail(w, errNotFound)
		return
	}
	if len(parts) == 3 {
		if parts[1] == "events" || parts[1] == "ledger" {
			fail(w, errNotFound)
			return
		}
		var b []byte
		err := a.Store.DB.QueryRow(ctx, "SELECT to_jsonb(t)-'tenant_id'-'account_fingerprint' FROM "+table+" t WHERE tenant_id=$1 AND id=$2", tenant, parts[2]).Scan(&b)
		if err != nil {
			fail(w, err)
			return
		}
		respond(w, 200, json.RawMessage(b))
		return
	}
	cursor := r.URL.Query().Get("after")
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			fail(w, errInvalid)
			return
		}
		limit = n
	}
	var rows pgx.Rows
	var err error
	if parts[1] == "events" || parts[1] == "ledger" {
		var after int64
		if cursor != "" {
			after, err = strconv.ParseInt(cursor, 10, 64)
			if err != nil || after < 0 {
				fail(w, errInvalid)
				return
			}
		}
		if parts[1] == "ledger" {
			rows, err = a.Store.DB.Query(ctx, "SELECT to_jsonb(t)-'tenant_id'-'account_fingerprint' FROM ledger t WHERE tenant_id=$1 AND id>$2 AND ($3='' OR customer_id=$3) ORDER BY id LIMIT $4", tenant, after, r.URL.Query().Get("customer_id"), limit)
		} else {
			rows, err = a.Store.DB.Query(ctx, "SELECT to_jsonb(t)-'tenant_id'-'account_fingerprint' FROM events t WHERE tenant_id=$1 AND id>$2 ORDER BY id LIMIT $3", tenant, after, limit)
		}
	} else if parts[1] == "charges" && (r.URL.Query().Get("customer_id") != "" || r.URL.Query().Get("subscription_id") != "") {
		rows, err = a.Store.DB.Query(ctx, "SELECT to_jsonb(t)-'tenant_id'-'account_fingerprint' FROM api_charge_details t WHERE tenant_id=$1 AND ($2='' OR customer_id=$2) AND ($3='' OR subscription_id=$3) ORDER BY created_at DESC, id DESC LIMIT $4", tenant, r.URL.Query().Get("customer_id"), r.URL.Query().Get("subscription_id"), limit)
	} else {
		rows, err = a.Store.DB.Query(ctx, "SELECT to_jsonb(t)-'tenant_id'-'account_fingerprint' FROM "+table+" t WHERE tenant_id=$1 AND id>$2 ORDER BY id LIMIT $3", tenant, cursor, limit)
	}
	if err != nil {
		fail(w, err)
		return
	}
	defer rows.Close()
	items := []json.RawMessage{}
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			fail(w, err)
			return
		}
		items = append(items, json.RawMessage(b))
	}
	if err = rows.Err(); err != nil {
		fail(w, err)
		return
	}
	respond(w, 200, map[string]any{"data": items})
}

func (a *API) rotateKey(w http.ResponseWriter, r *http.Request) {
	supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(a.AdminSecret) < 32 || subtle.ConstantTimeCompare([]byte(supplied), []byte(a.AdminSecret)) != 1 {
		respond(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	var in struct{}
	if decode(r, &in) != nil {
		fail(w, errInvalid)
		return
	}
	key := token()
	tx, err := a.Store.DB.Begin(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	tag, err := tx.Exec(r.Context(), "UPDATE tenants SET api_hash=$2 WHERE id=$1", r.PathValue("tenant"), hash(key))
	if err != nil {
		fail(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		fail(w, errNotFound)
		return
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM console_sessions WHERE tenant_id=$1", r.PathValue("tenant")); err != nil {
		fail(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		fail(w, err)
		return
	}
	respond(w, 200, map[string]string{"id": r.PathValue("tenant"), "api_key": key})
}
