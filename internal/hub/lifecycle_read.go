package hub

import (
	"encoding/json"
	"net/http"
)

func (a *API) readSubscriptionAudit(w http.ResponseWriter, r *http.Request, tenant, subscription, resource string) {
	var exists bool
	if err := a.Store.DB.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM subscriptions WHERE tenant_id=$1 AND id=$2)", tenant, subscription).Scan(&exists); err != nil {
		fail(w, err)
		return
	}
	if !exists {
		fail(w, errNotFound)
		return
	}
	table := "billing_ledger"
	if resource == "changes" {
		table = "subscription_changes"
	}
	rows, err := a.Store.DB.Query(r.Context(), "SELECT to_jsonb(t)-'tenant_id' FROM "+table+" t WHERE tenant_id=$1 AND subscription_id=$2 ORDER BY created_at DESC,id DESC LIMIT 100", tenant, subscription)
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
