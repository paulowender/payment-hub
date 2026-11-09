package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var diagnosticKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)
var diagnosticID = regexp.MustCompile(`^(hook|or|ch|cus|ev|evt|cb|card)_[A-Za-z0-9_-]{1,100}$`)
var diagnosticNumber = regexp.MustCompile(`^[0-9]{1,10}$`)
var diagnosticToken = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,79}$`)

func redactWebhookValue(value any, key string, depth int) any {
	if depth > 8 {
		return "[depth limit]"
	}
	switch v := value.(type) {
	case map[string]any:
		result := map[string]any{}
		n := 0
		for k, item := range v {
			if n >= 50 {
				result["_truncated"] = true
				break
			}
			name := k
			if !diagnosticKey.MatchString(k) {
				name = fmt.Sprintf("redacted_key_%d", n)
			}
			result[name] = redactWebhookValue(item, k, depth+1)
			n++
		}
		return result
	case []any:
		result := []any{}
		for i, item := range v {
			if i >= 20 {
				result = append(result, "[truncated]")
				break
			}
			result = append(result, redactWebhookValue(item, key, depth+1))
		}
		return result
	case string:
		if (key == "id" || strings.HasSuffix(key, "_id")) && diagnosticID.MatchString(v) {
			return v
		}
		if (key == "type" || key == "status" || key == "currency" || key == "object") && diagnosticToken.MatchString(v) {
			return v
		}
		return fmt.Sprintf("[redacted string; %d chars]", utf8.RuneCountInString(v))
	case nil:
		return nil
	default:
		return "[redacted scalar]"
	}
}
func diagnosticHeaders(r *http.Request) map[string]any {
	result := map[string]any{}
	for key, values := range r.Header {
		if len(result) >= 80 {
			result["_truncated"] = true
			break
		}
		name := http.CanonicalHeaderKey(key)
		result[name] = map[string]any{"present": true, "values": len(values)}
		if name == "Content-Type" {
			media := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get(key), ";")[0]))
			if media == "application/json" || media == "application/x-www-form-urlencoded" || media == "text/plain" {
				result[name] = media
			}
		}
		if name == "Content-Length" && diagnosticNumber.MatchString(r.Header.Get(key)) {
			result[name] = r.Header.Get(key)
		}
	}
	_, _, basic := r.BasicAuth()
	result["_authentication"] = map[string]any{"authorization_present": r.Header.Get("Authorization") != "", "basic_auth_parsed": basic}
	return result
}
func (a *API) captureWebhookDiagnostic(r *http.Request, tenant string, authenticated bool) (bool, error) {
	if tenant != a.WebhookDiagnosticTenant || !time.Now().Before(a.WebhookDiagnosticUntil) {
		return false, nil
	}
	ctx := r.Context()
	tx, e := a.Store.DB.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,3))", tenant); e != nil {
		return false, e
	}
	var count int
	e = tx.QueryRow(ctx, "SELECT count(*) FROM webhook_diagnostics WHERE tenant_id=$1 AND capture_until=$2", tenant, a.WebhookDiagnosticUntil).Scan(&count)
	if e != nil {
		return false, e
	}
	if count >= 20 || !time.Now().Before(a.WebhookDiagnosticUntil) {
		return false, nil
	}
	raw, e := io.ReadAll(r.Body)
	if e != nil {
		return false, errInvalid
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var body any
	payload := map[string]any{"parse_error": "invalid_json"}
	if json.Unmarshal(raw, &body) == nil {
		payload = map[string]any{"body": redactWebhookValue(body, "", 0)}
	}
	headers, _ := json.Marshal(diagnosticHeaders(r))
	encoded, _ := json.Marshal(payload)
	if len(encoded) > 64<<10 {
		encoded = []byte(`{"parse_error":"sanitized_payload_too_large"}`)
	}
	_, e = tx.Exec(ctx, "INSERT INTO webhook_diagnostics(tenant_id,capture_until,authenticated,headers,payload,body_bytes) VALUES($1,$2,$3,$4,$5,$6)", tenant, a.WebhookDiagnosticUntil, authenticated, headers, encoded, len(raw))
	if e != nil {
		return false, e
	}
	return true, tx.Commit(ctx)
}
func (a *API) readWebhookDiagnostics(w http.ResponseWriter, r *http.Request, tenant string) {
	rows, e := a.Store.DB.Query(r.Context(), "SELECT to_jsonb(d)-'tenant_id'-'capture_until' FROM (SELECT * FROM webhook_diagnostics WHERE tenant_id=$1 ORDER BY id DESC LIMIT 20) d", tenant)
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
	remaining := 0
	active := tenant == a.WebhookDiagnosticTenant && time.Now().Before(a.WebhookDiagnosticUntil)
	if active {
		var n int
		e = a.Store.DB.QueryRow(r.Context(), "SELECT count(*) FROM webhook_diagnostics WHERE tenant_id=$1 AND capture_until=$2", tenant, a.WebhookDiagnosticUntil).Scan(&n)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			fail(w, e)
			return
		}
		remaining = 20 - n
		if remaining <= 0 {
			active = false
		}
	}
	respond(w, 200, map[string]any{"data": items, "capture_active": active, "capture_until": a.WebhookDiagnosticUntil, "remaining": remaining})
}
