package hub

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// Select an explicit allowlist: provider credentials and checkout snapshots never enter events.
func financialEventData(ctx context.Context, tx pgx.Tx, tenant, chargeID string) (map[string]any, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT
 jsonb_build_object('charge_id',c.id,'customer_id',c.customer_id,'status',c.status,'credits',c.credits,'charge_metadata',c.metadata,'customer_metadata',u.metadata)
 || CASE WHEN s.id IS NULL THEN '{}'::jsonb ELSE jsonb_build_object('subscription_id',s.id,'plan_id',s.plan_id,'subscription_metadata',s.metadata,'plan_metadata',p.metadata) END
 || COALESCE((SELECT jsonb_build_object('session_id',x.id,'offer_id',x.offer_id,'reference',x.reference,'checkout_session_metadata',x.metadata,'offer_metadata',o.metadata)
 FROM checkout_sessions x JOIN offers o ON o.tenant_id=x.tenant_id AND o.id=x.offer_id
 WHERE x.tenant_id=c.tenant_id AND x.charge_id=c.id ORDER BY x.created_at,x.id LIMIT 1),'{}'::jsonb)
 FROM charges c JOIN customers u ON u.tenant_id=c.tenant_id AND u.id=c.customer_id
 LEFT JOIN subscriptions s ON s.tenant_id=c.tenant_id AND s.id=c.subscription_id
 LEFT JOIN plans p ON p.tenant_id=s.tenant_id AND p.id=s.plan_id
 WHERE c.tenant_id=$1 AND c.id=$2`, tenant, chargeID).Scan(&raw)
	if err != nil {
		return nil, err
	}
	return decodeEventData(raw)
}

func subscriptionEventData(ctx context.Context, tx pgx.Tx, tenant, subscriptionID string) (map[string]any, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT
 jsonb_build_object('subscription_id',s.id,'customer_id',s.customer_id,'plan_id',s.plan_id,'status',s.status,'subscription_metadata',s.metadata,'customer_metadata',u.metadata,'plan_metadata',p.metadata)
 || COALESCE((SELECT jsonb_build_object('session_id',x.id,'offer_id',x.offer_id,'reference',x.reference,'checkout_session_metadata',x.metadata,'offer_metadata',o.metadata)
 FROM checkout_sessions x JOIN offers o ON o.tenant_id=x.tenant_id AND o.id=x.offer_id
 WHERE x.tenant_id=s.tenant_id AND x.subscription_id=s.id ORDER BY x.created_at,x.id LIMIT 1),'{}'::jsonb)
 FROM subscriptions s JOIN customers u ON u.tenant_id=s.tenant_id AND u.id=s.customer_id
 JOIN plans p ON p.tenant_id=s.tenant_id AND p.id=s.plan_id
 WHERE s.tenant_id=$1 AND s.id=$2`, tenant, subscriptionID).Scan(&raw)
	if err != nil {
		return nil, err
	}
	return decodeEventData(raw)
}

// Opaque metadata may contain numbers larger or more precise than float64.
func decodeEventData(raw []byte) (map[string]any, error) {
	var data map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	err := decoder.Decode(&data)
	return data, err
}

func emitFinancialEvent(ctx context.Context, tx pgx.Tx, tenant, kind, chargeID string, extra map[string]any) error {
	data, err := financialEventData(ctx, tx, tenant, chargeID)
	if err != nil {
		return err
	}
	for key, value := range extra {
		data[key] = value
	}
	return emit(ctx, tx, tenant, kind, data)
}

func emitSubscriptionEvent(ctx context.Context, tx pgx.Tx, tenant, kind, subscriptionID string, extra map[string]any) error {
	data, err := subscriptionEventData(ctx, tx, tenant, subscriptionID)
	if err != nil {
		return err
	}
	for key, value := range extra {
		data[key] = value
	}
	return emit(ctx, tx, tenant, kind, data)
}
