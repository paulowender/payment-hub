package hub

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed web/*
var consoleFiles embed.FS

type consoleIdentity struct {
	PublicWebhookBaseURL string `json:"public_webhook_base_url,omitempty"`
	Tenant               string `json:"tenant_id"`
	Admin                bool   `json:"is_admin"`
}

func (a *API) consoleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/console", http.StatusSeeOther) })
	mux.HandleFunc("GET /console", func(w http.ResponseWriter, r *http.Request) {
		a.consoleFile(w, r, "index.html", "text/html; charset=utf-8")
	})
	mux.HandleFunc("GET /console/assets/{file}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("file")
		kind := "text/css; charset=utf-8"
		if name == "app.js" || name == "theme.js" || name == "console-extras.js" {
			kind = "text/javascript; charset=utf-8"
		} else if name != "bootstrap.min.css" && name != "app.css" {
			http.NotFound(w, r)
			return
		}
		a.consoleFile(w, r, name, kind)
	})
	mux.HandleFunc("POST /console/login", a.consoleLogin)
	mux.HandleFunc("POST /console/logout", a.consoleLogout)
	mux.HandleFunc("/console/api/", a.consoleAPI)
	mux.HandleFunc("POST /webhooks/pagarme/{tenant}", a.pagarmeWebhook)
}
func (a *API) consoleFile(w http.ResponseWriter, r *http.Request, name, kind string) {
	b, e := consoleFiles.ReadFile("web/" + name)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	w.Write(b)
}
func consoleCSRF(r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	origin := r.Header.Get("Origin")
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return r.Header.Get("X-Requested-With") == "PaymentHub" && origin == scheme+"://"+r.Host
}
func (a *API) consoleLogin(w http.ResponseWriter, r *http.Request) {
	if !consoleCSRF(r) {
		respond(w, 403, map[string]string{"error": "invalid_origin"})
		return
	}
	var in struct {
		Key string `json:"key"`
	}
	if decode(r, &in) != nil || len(in.Key) < 32 || len(in.Key) > 256 {
		respond(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	id := consoleIdentity{}
	if len(a.AdminSecret) >= 32 && subtle.ConstantTimeCompare([]byte(in.Key), []byte(a.AdminSecret)) == 1 {
		id.Admin = true
	} else {
		if e := a.Store.DB.QueryRow(r.Context(), "SELECT id FROM tenants WHERE api_hash=$1", hash(in.Key)).Scan(&id.Tenant); e != nil {
			if !errors.Is(e, pgx.ErrNoRows) {
				fail(w, e)
				return
			}
			respond(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
	}
	session := token()
	var tenant any
	if !id.Admin {
		tenant = id.Tenant
	}
	_, e := a.Store.DB.Exec(r.Context(), "INSERT INTO console_sessions(token_hash,tenant_id,is_admin,expires_at) VALUES($1,$2,$3,now()+interval '12 hours')", hash(session), tenant, id.Admin)
	if e != nil {
		fail(w, e)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "paymenthub_session", Value: session, Path: "/console", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", MaxAge: 43200})
	respond(w, 200, id)
}
func (a *API) identity(r *http.Request) (consoleIdentity, error) {
	var id consoleIdentity
	c, e := r.Cookie("paymenthub_session")
	if e != nil || len(c.Value) != 64 {
		return id, errNotFound
	}
	e = a.Store.DB.QueryRow(r.Context(), "SELECT COALESCE(tenant_id,''),is_admin FROM console_sessions WHERE token_hash=$1 AND expires_at>now()", hash(c.Value)).Scan(&id.Tenant, &id.Admin)
	return id, e
}
func (a *API) consoleLogout(w http.ResponseWriter, r *http.Request) {
	if !consoleCSRF(r) {
		respond(w, 403, map[string]string{"error": "invalid_origin"})
		return
	}
	if c, e := r.Cookie("paymenthub_session"); e == nil {
		if _, e = a.Store.DB.Exec(r.Context(), "DELETE FROM console_sessions WHERE token_hash=$1", hash(c.Value)); e != nil {
			fail(w, e)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: "paymenthub_session", Value: "", Path: "/console", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	respond(w, 200, map[string]bool{"ok": true})
}
func (a *API) consoleAPI(w http.ResponseWriter, r *http.Request) {
	id, e := a.identity(r)
	if e != nil {
		if !errors.Is(e, pgx.ErrNoRows) && !errors.Is(e, errNotFound) {
			fail(w, e)
			return
		}
		respond(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	if !consoleCSRF(r) {
		respond(w, 403, map[string]string{"error": "invalid_origin"})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/console/api/")
	if path == "me" && r.Method == "GET" {
		id.PublicWebhookBaseURL = a.PublicWebhookBaseURL
		respond(w, 200, id)
		return
	}
	if path == "projects" {
		if r.Method == "POST" && id.Admin {
			clone := r.Clone(r.Context())
			clone.Header = clone.Header.Clone()
			clone.Header.Set("Authorization", "Bearer "+a.AdminSecret)
			a.provision(w, clone)
			return
		}
		if r.Method != "GET" {
			respond(w, 403, map[string]string{"error": "forbidden"})
			return
		}
		rows, e := a.Store.DB.Query(r.Context(), "SELECT id,name,created_at FROM tenants WHERE $1 OR id=$2 ORDER BY created_at", id.Admin, id.Tenant)
		if e != nil {
			fail(w, e)
			return
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var tenant, name string
			var created time.Time
			if e = rows.Scan(&tenant, &name, &created); e != nil {
				fail(w, e)
				return
			}
			items = append(items, map[string]any{"id": tenant, "name": name, "created_at": created})
		}
		if e = rows.Err(); e != nil {
			fail(w, e)
			return
		}
		respond(w, 200, map[string]any{"data": items})
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) < 3 || parts[0] != "projects" {
		fail(w, errNotFound)
		return
	}
	tenant := parts[1]
	if !id.Admin && tenant != id.Tenant {
		fail(w, errNotFound)
		return
	}
	var exists bool
	if e = a.Store.DB.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM tenants WHERE id=$1)", tenant).Scan(&exists); e != nil {
		fail(w, e)
		return
	}
	if !exists {
		fail(w, errNotFound)
		return
	}
	if parts[2] == "notifications" {
		if len(parts) == 6 && parts[3] == "deliveries" && parts[5] == "retry" && !id.Admin {
			respond(w, 403, map[string]string{"error": "forbidden"})
			return
		}
		a.notificationAPI(w, r, tenant, parts[3:])
		return
	}
	if parts[2] == "v1" && len(parts) >= 4 {
		// Preserve the dedicated console retry permission through the business bridge.
		if len(parts) == 7 && parts[3] == "notifications" && parts[4] == "deliveries" && parts[6] == "retry" && !id.Admin {
			respond(w, 403, map[string]string{"error": "forbidden"})
			return
		}
		if r.Method == http.MethodGet && len(parts) == 4 {
			tables := map[string]string{"customers": "customers", "plans": "plans", "charges": "api_charge_details", "subscriptions": "api_subscriptions", "events": "events"}
			if table, ok := tables[parts[3]]; ok {
				rows, e := a.Store.DB.Query(r.Context(), "SELECT to_jsonb(t)-'tenant_id'-'account_fingerprint' FROM "+table+" t WHERE tenant_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100", tenant)
				if e != nil {
					fail(w, e)
					return
				}
				defer rows.Close()
				items := []json.RawMessage{}
				for rows.Next() {
					var b []byte
					if e = rows.Scan(&b); e != nil {
						fail(w, e)
						return
					}
					items = append(items, json.RawMessage(b))
				}
				if e = rows.Err(); e != nil {
					fail(w, e)
					return
				}
				respond(w, 200, map[string]any{"data": items})
				return
			}
		}
		clone := r.Clone(context.WithValue(r.Context(), tenantContext{}, tenant))
		url := *r.URL
		url.Path = "/v1/" + strings.Join(parts[3:], "/")
		url.RawPath = ""
		clone.URL = &url
		a.business(w, clone)
		return
	}
	if len(parts) == 3 && parts[2] == "diagnostics" && r.Method == "GET" {
		a.readWebhookDiagnostics(w, r, tenant)
		return
	}
	if parts[2] == "providers" {
		if a.Vault == nil {
			fail(w, errNotFound)
			return
		}
		if len(parts) == 3 && r.Method == "GET" {
			data := []map[string]any{}
			for _, provider := range []string{"pagarme", "stripe"} {
				item := map[string]any{"provider": provider, "configured": false}
				ac, e := a.Vault.Get(r.Context(), tenant, provider)
				if e == nil {
					item["configured"] = true
					item["environment"] = ac.Environment
					item["public_key"] = ac.PublicKey
					item["webhook_configured"] = ac.WebhookUser != ""
					item["retry_window_seconds"] = int(ac.Window().Seconds())
				} else if !errors.Is(e, pgx.ErrNoRows) {
					fail(w, e)
					return
				}
				data = append(data, item)
			}
			respond(w, 200, map[string]any{"data": data})
			return
		}
		if len(parts) == 4 && r.Method == "POST" {
			var ac Account
			if decode(r, &ac) != nil {
				fail(w, errInvalid)
				return
			}
			// Empty secret fields preserve saved values, so edits never need secrets returned to the browser.
			old, e := a.Vault.Get(r.Context(), tenant, parts[3])
			if e != nil && !errors.Is(e, pgx.ErrNoRows) {
				fail(w, e)
				return
			}
			if ac.PublicKey == "" {
				ac.PublicKey = old.PublicKey
			}
			if ac.Secret == "" {
				ac.Secret = old.Secret
			}
			if ac.WebhookUser == "" && ac.WebhookPassword == "" {
				ac.WebhookUser = old.WebhookUser
				ac.WebhookPassword = old.WebhookPassword
			}
			if e = a.Vault.Save(r.Context(), tenant, parts[3], ac); e != nil {
				fail(w, e)
				return
			}
			respond(w, 200, map[string]bool{"configured": true})
			return
		}
		if len(parts) == 5 && parts[4] == "test" && r.Method == "POST" {
			a.testConnection(w, r, tenant, parts[3])
			return
		}
	}
	if len(parts) == 3 && parts[2] == "webhooks" && r.Method == "GET" {
		rows, e := a.Store.DB.Query(r.Context(), "SELECT event_id,event_type,order_id,received_at FROM webhook_receipts WHERE tenant_id=$1 ORDER BY received_at DESC LIMIT 100", tenant)
		if e != nil {
			fail(w, e)
			return
		}
		defer rows.Close()
		data := []map[string]any{}
		for rows.Next() {
			var event, kind, order string
			var at time.Time
			if e = rows.Scan(&event, &kind, &order, &at); e != nil {
				fail(w, e)
				return
			}
			data = append(data, map[string]any{"event_id": event, "type": kind, "order_id": order, "received_at": at})
		}
		if e = rows.Err(); e != nil {
			fail(w, e)
			return
		}
		respond(w, 200, map[string]any{"data": data})
		return
	}
	fail(w, errNotFound)
}
func (a *API) testConnection(w http.ResponseWriter, r *http.Request, tenant, provider string) {
	ac, e := a.Accounts.Get(r.Context(), tenant, provider)
	if e != nil {
		fail(w, e)
		return
	}
	endpoint := "https://api.pagar.me/core/v5/customers?page=1&size=1"
	if provider == "stripe" {
		endpoint = "https://api.stripe.com/v1/balance"
	} else if provider != "pagarme" {
		fail(w, errInvalid)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	req.SetBasicAuth(ac.Secret, "")
	res, e := (&http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if e != nil {
		respond(w, 502, map[string]string{"error": "provider_unreachable"})
		return
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		respond(w, 502, map[string]any{"error": "provider_rejected", "provider_status": res.StatusCode})
		return
	}
	respond(w, 200, map[string]any{"ok": true, "provider": provider, "environment": ac.Environment, "checked_at": time.Now().UTC()})
}

func (a *API) pagarmeWebhook(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	if a.Accounts == nil {
		respond(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	ac, e := a.Accounts.Get(r.Context(), tenant, "pagarme")
	user, password, ok := r.BasicAuth()
	authenticated := e == nil && ok && ac.WebhookUser != "" && ac.WebhookPassword != "" && subtle.ConstantTimeCompare([]byte(hash(user)), []byte(hash(ac.WebhookUser))) == 1 && subtle.ConstantTimeCompare([]byte(hash(password)), []byte(hash(ac.WebhookPassword))) == 1
	if e == nil && ac.Environment == "test" {
		captured, captureErr := a.captureWebhookDiagnostic(r, tenant, authenticated)
		if captureErr != nil {
			fail(w, captureErr)
			return
		}
		if captured && !authenticated {
			respond(w, 200, map[string]any{"captured": true, "authenticated": false, "processed": false})
			return
		}
	}
	if !authenticated {
		slog.Warn("pagarme webhook authentication rejected", "tenant_id", tenant, "authorization_present", r.Header.Get("Authorization") != "", "basic_auth_parsed", ok, "account_loaded", e == nil)
		respond(w, 401, map[string]string{"error": "unauthorized"})
		return
	}

	var event struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Data struct {
			ID    string `json:"id"`
			Order struct {
				ID string `json:"id"`
			} `json:"order"`
		} `json:"data"`
	}
	d := json.NewDecoder(r.Body)
	if d.Decode(&event) != nil || d.Decode(new(any)) != io.EOF || len(event.ID) < 1 || len(event.ID) > 200 || len(event.Type) < 1 || len(event.Type) > 200 {
		fail(w, errInvalid)
		return
	}
	order := ""
	if strings.HasPrefix(event.Type, "order.") {
		order = event.Data.ID
	} else if strings.HasPrefix(event.Type, "charge.") {
		order = event.Data.Order.ID
	}
	if len(order) > 200 {
		fail(w, errInvalid)
		return
	}
	tx, e := a.Store.DB.Begin(r.Context())
	if e != nil {
		fail(w, e)
		return
	}
	defer tx.Rollback(r.Context())
	tag, e := tx.Exec(r.Context(), "INSERT INTO webhook_receipts(tenant_id,provider,event_id,event_type,order_id) VALUES($1,'pagarme',$2,$3,$4) ON CONFLICT DO NOTHING", tenant, event.ID, event.Type, order)
	if e != nil {
		fail(w, e)
		return
	}
	if tag.RowsAffected() > 0 {
		// New chargeback envelopes may lack an order ID; recheck this project’s paid orders, never trust payload status.
		_, e = tx.Exec(r.Context(), `UPDATE charges SET next_at=now() WHERE tenant_id=$1 AND provider='pagarme'
 AND (provider_id=$2 OR provider_charge_id=$3 OR ($4 AND status='paid')) AND (status<>'manual_review' OR (last_error='payment_underpaid' AND provider_id IS NOT NULL))`, tenant, order, event.Data.ID, event.Type == "chargeback.received")
		if e != nil {
			fail(w, e)
			return
		}
	}
	if e = tx.Commit(r.Context()); e != nil {
		fail(w, e)
		return
	}
	respond(w, 200, map[string]bool{"received": true})
}
