package hub

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// managementAPI is reached only after Bearer authentication (or console authorization).
func (a *API) managementAPI(w http.ResponseWriter, r *http.Request, tenant string) bool {
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(p) < 2 || (p[1] != "providers" && p[1] != "notifications") {
		return false
	}
	if a.Vault == nil {
		fail(w, errNotFound)
		return true
	}
	if r.Method == http.MethodGet {
		if p[1] == "notifications" {
			a.notificationAPI(w, r, tenant, p[2:])
			return true
		}
		if len(p) != 2 {
			fail(w, errNotFound)
			return true
		}
		data := []map[string]any{}
		for _, provider := range []string{"pagarme", "stripe"} {
			item := map[string]any{"provider": provider, "configured": false}
			ac, err := a.Vault.Get(r.Context(), tenant, provider)
			if err == nil {
				item["configured"] = true
				item["environment"] = ac.Environment
				item["public_key"] = ac.PublicKey
				item["webhook_configured"] = ac.WebhookUser != ""
				item["retry_window_seconds"] = int(ac.Window().Seconds())
			} else if !errors.Is(err, pgx.ErrNoRows) {
				fail(w, err)
				return true
			}
			data = append(data, item)
		}
		respond(w, 200, map[string]any{"data": data})
		return true
	}
	if r.Method != http.MethodPost {
		respond(w, 405, map[string]string{"error": "method_not_allowed"})
		return true
	}
	key := r.Header.Get("Idempotency-Key")
	if len(key) < 1 || len(key) > 200 {
		fail(w, errInvalid)
		return true
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		fail(w, errInvalid)
		return true
	}
	fingerprint := hash(r.Method + ":" + r.URL.EscapedPath() + ":" + string(body))
	// Encrypt replay material: notification signing secrets must not appear in the
	// plaintext idempotency table. The binding prevents moving results across keys.
	binding := tenant + ":management:" + key
	data, err := a.Store.write(r.Context(), tenant, key, fingerprint, func(tx pgx.Tx) (any, error) {
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		result, err := a.managementMutate(r, tx, tenant, p)
		if err != nil {
			return nil, err
		}
		plain, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		encrypted, err := a.Vault.seal(binding, plain)
		return map[string][]byte{"encrypted_management_response": encrypted}, err
	})
	if err != nil {
		fail(w, err)
		return true
	}
	var envelope map[string][]byte
	if err = json.Unmarshal(data, &envelope); err != nil {
		fail(w, err)
		return true
	}
	plain, err := a.Vault.open(binding, envelope["encrypted_management_response"])
	if err != nil {
		fail(w, err)
		return true
	}
	var result managementResult
	if err = json.Unmarshal(plain, &result); err != nil {
		fail(w, err)
		return true
	}
	respond(w, result.Status, result.Body)
	return true
}

type managementResult struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

func managementOK(v any) (managementResult, error) {
	b, err := json.Marshal(v)
	return managementResult{Status: 200, Body: b}, err
}

func (a *API) managementMutate(r *http.Request, tx pgx.Tx, tenant string, p []string) (managementResult, error) {
	ctx := r.Context()
	if p[1] == "providers" {
		if len(p) < 3 || (p[2] != "stripe" && p[2] != "pagarme") {
			return managementResult{}, errNotFound
		}
		if len(p) == 4 && p[3] == "test" {
			var in struct{}
			if decode(r, &in) != nil {
				return managementResult{}, errInvalid
			}
			recorder := httptest.NewRecorder()
			tester := *a
			tester.Accounts = a.Vault
			tester.testConnection(recorder, r, tenant, p[2])
			return managementResult{Status: recorder.Code, Body: recorder.Body.Bytes()}, nil
		}
		if len(p) != 3 {
			return managementResult{}, errNotFound
		}
		var ac Account
		if decode(r, &ac) != nil {
			return managementResult{}, errInvalid
		}
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,8))", tenant+":"+p[2]); err != nil {
			return managementResult{}, err
		}
		old, err := a.Vault.load(ctx, tx, tenant, p[2])
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return managementResult{}, err
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
		if err = ValidateAccount(p[2], ac); err != nil {
			return managementResult{}, err
		}
		plain, err := json.Marshal(ac)
		if err != nil {
			return managementResult{}, err
		}
		encrypted, err := a.Vault.seal(tenant+":"+p[2], plain)
		if err != nil {
			return managementResult{}, err
		}
		_, err = tx.Exec(ctx, "INSERT INTO provider_connections(tenant_id,provider,encrypted_config) VALUES($1,$2,$3) ON CONFLICT(tenant_id,provider) DO UPDATE SET encrypted_config=excluded.encrypted_config,updated_at=now()", tenant, p[2], encrypted)
		if err != nil {
			return managementResult{}, err
		}
		return managementOK(map[string]bool{"configured": true})
	}
	if len(p) == 3 && p[2] == "config" || len(p) == 4 && p[2] == "config" && p[3] == "reset-secret" {
		reset := len(p) == 4
		var in struct {
			URL     string `json:"url"`
			Enabled bool   `json:"enabled"`
		}
		if reset {
			var empty struct{}
			if decode(r, &empty) != nil {
				return managementResult{}, errInvalid
			}
		} else if decode(r, &in) != nil || notificationURL(in.URL) != nil {
			return managementResult{}, errInvalid
		}
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,7))", tenant); err != nil {
			return managementResult{}, err
		}
		var endpoint string
		var enabled bool
		var encrypted []byte
		err := tx.QueryRow(ctx, "SELECT url,enabled,encrypted_secret FROM notification_configs WHERE tenant_id=$1 FOR UPDATE", tenant).Scan(&endpoint, &enabled, &encrypted)
		fresh := errors.Is(err, pgx.ErrNoRows)
		if err != nil && !fresh {
			return managementResult{}, err
		}
		if reset && fresh {
			return managementResult{}, errNotFound
		}
		if !reset {
			endpoint, enabled = in.URL, in.Enabled
		}
		result := map[string]any{"configured": true, "url": endpoint, "enabled": enabled}
		if fresh || reset {
			secret := token()
			encrypted, err = a.Vault.seal(tenant+":outbound", []byte(secret))
			if err != nil {
				return managementResult{}, err
			}
			result["signing_secret"] = secret
		}
		_, err = tx.Exec(ctx, `INSERT INTO notification_configs(tenant_id,url,enabled,encrypted_secret) VALUES($1,$2,$3,$4) ON CONFLICT(tenant_id) DO UPDATE SET url=excluded.url,enabled=excluded.enabled,encrypted_secret=excluded.encrypted_secret,updated_at=now()`, tenant, endpoint, enabled, encrypted)
		if err != nil {
			return managementResult{}, err
		}
		return managementOK(result)
	}
	if len(p) == 5 && p[2] == "deliveries" && p[4] == "retry" {
		var in struct{}
		if decode(r, &in) != nil {
			return managementResult{}, errInvalid
		}
		id, err := strconv.ParseInt(p[3], 10, 64)
		if err != nil || id < 1 {
			return managementResult{}, errInvalid
		}
		tag, err := tx.Exec(ctx, `UPDATE notification_deliveries SET status='pending',attempts=0,next_at=now(),last_error='',lease_until=NULL,lease_token=NULL,updated_at=now() WHERE tenant_id=$1 AND event_id=$2 AND status IN ('failed','dead') AND (lease_until IS NULL OR lease_until<now())`, tenant, id)
		if err != nil {
			return managementResult{}, err
		}
		if tag.RowsAffected() == 0 {
			return managementResult{}, errNotFound
		}
		return managementOK(map[string]bool{"queued": true})
	}
	return managementResult{}, errNotFound
}
