package hub

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"net/http"
)

type CheckoutSettings struct {
	Card            bool  `json:"card"`
	Pix             bool  `json:"pix"`
	Boleto          bool  `json:"boleto"`
	MaxInstallments int   `json:"max_installments"`
	MinInstallment  int64 `json:"min_installment"`
	PixExpires      int   `json:"pix_expires"`
	BoletoDueDays   int   `json:"boleto_due_days"`
	BuyerCancel     bool  `json:"buyer_cancel"`
	BuyerUpdateCard bool  `json:"buyer_update_card"`
}

func defaultCheckoutSettings() CheckoutSettings {
	return CheckoutSettings{Card: true, MaxInstallments: 1, MinInstallment: 500, PixExpires: 1800, BoletoDueDays: 3, BuyerCancel: true, BuyerUpdateCard: true}
}
func loadCheckoutSettings(ctx context.Context, q credentialReader, tenant string) (CheckoutSettings, error) {
	cfg := defaultCheckoutSettings()
	var raw []byte
	e := q.QueryRow(ctx, "SELECT config FROM checkout_settings WHERE tenant_id=$1", tenant).Scan(&raw)
	if e == pgx.ErrNoRows {
		return cfg, nil
	}
	if e != nil {
		return cfg, e
	}
	e = json.Unmarshal(raw, &cfg)
	return cfg, e
}
func validCheckoutSettings(c CheckoutSettings) bool {
	return (c.Card || c.Pix || c.Boleto) && c.MaxInstallments >= 1 && c.MaxInstallments <= 12 && c.MinInstallment >= 100 && c.MinInstallment <= 99999999 && c.PixExpires >= 300 && c.PixExpires <= 86400 && c.BoletoDueDays >= 1 && c.BoletoDueDays <= 30
}
func (a *API) saveCheckoutSettings(r *http.Request, tx pgx.Tx, tenant string) (any, error) {
	var cfg CheckoutSettings
	if decode(r, &cfg) != nil || !validCheckoutSettings(cfg) {
		return nil, errInvalid
	}
	b, e := json.Marshal(cfg)
	if e != nil {
		return nil, e
	}
	_, e = tx.Exec(r.Context(), "INSERT INTO checkout_settings(tenant_id,config) VALUES($1,$2) ON CONFLICT(tenant_id) DO UPDATE SET config=excluded.config", tenant, b)
	return cfg, e
}
func (a *API) readCheckoutSettings(w http.ResponseWriter, r *http.Request, tenant string) {
	cfg, e := loadCheckoutSettings(r.Context(), a.Store.DB, tenant)
	if e != nil {
		fail(w, e)
		return
	}
	respond(w, 200, cfg)
}

type PaymentInstructions struct {
	PixCode    string `json:"pix_code,omitempty"`
	QRURL      string `json:"qr_url,omitempty"`
	BoletoLine string `json:"boleto_line,omitempty"`
	BoletoURL  string `json:"boleto_url,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
}

func recordChargeHistory(ctx context.Context, tx pgx.Tx, c Charge, kind, status, detail string) error {
	if len(detail) > 200 {
		detail = detail[:200]
	}
	_, e := tx.Exec(ctx, "INSERT INTO charge_history(tenant_id,charge_id,lease,kind,status,detail) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING", c.Tenant, c.ID, c.Lease, kind, status, detail)
	return e
}
func (a *API) readChargeHistory(w http.ResponseWriter, r *http.Request, tenant, id string) {
	var charge []byte
	e := a.Store.DB.QueryRow(r.Context(), "SELECT to_jsonb(c)-'tenant_id' FROM api_charge_details c WHERE tenant_id=$1 AND id=$2", tenant, id).Scan(&charge)
	if e != nil {
		fail(w, e)
		return
	}
	queries := []struct{ name, sql string }{
		{"attempts", `SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM (SELECT id,kind,status,detail,created_at FROM charge_history WHERE tenant_id=$1 AND charge_id=$2 ORDER BY id DESC LIMIT 100) x`},
		{"events", `SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]') FROM (SELECT id,type,data,created_at FROM events WHERE tenant_id=$1 AND data->>'charge_id'=$2 ORDER BY id DESC LIMIT 100) x`},
		{"webhooks", `SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.received_at),'[]') FROM (SELECT event_id,event_type,received_at FROM webhook_receipts WHERE tenant_id=$1 AND order_id=(SELECT provider_id FROM charges WHERE tenant_id=$1 AND id=$2) ORDER BY received_at DESC LIMIT 100) x`},
	}
	out := map[string]any{"charge": json.RawMessage(charge)}
	for _, q := range queries {
		var b []byte
		if e := a.Store.DB.QueryRow(r.Context(), q.sql, tenant, id).Scan(&b); e != nil {
			fail(w, e)
			return
		}
		out[q.name] = json.RawMessage(b)
	}
	respond(w, 200, out)
}
