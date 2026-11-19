package hub

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// Proration uses elapsed UTC time, floors monetary refunds, and rounds unused
// usage entitlement upward. This prevents repeated switches minting credits.
func prorated(value int64, remaining, duration time.Duration, ceil bool) int64 {
	if remaining <= 0 || duration <= 0 {
		return 0
	}
	if remaining > duration {
		remaining = duration
	}
	n := new(big.Int).Mul(big.NewInt(value), big.NewInt(int64(remaining)))
	if ceil {
		n.Add(n, big.NewInt(int64(duration)-1))
	}
	return n.Div(n, big.NewInt(int64(duration))).Int64()
}

func (a *API) lifecycle(r *http.Request, tx pgx.Tx, tenant, id, action string) (any, error) {
	ctx := r.Context()
	var status, customer, oldPlan, provider, pc, method, fp, unit, currency string
	var anchor, next time.Time
	var paused *time.Time
	var cycle, count, epoch int
	var amount, credits, balance int64
	err := tx.QueryRow(ctx, `SELECT s.status,s.customer_id,s.plan_id,s.provider,s.provider_customer,s.payment_method,s.account_fingerprint,s.anchor,s.next_at,s.paused_at,s.cycle,s.billing_epoch,s.billing_credit,p.interval_unit,p.interval_count,p.amount,p.credits,p.currency FROM subscriptions s JOIN plans p ON p.tenant_id=s.tenant_id AND p.id=s.plan_id WHERE s.tenant_id=$1 AND s.id=$2 FOR UPDATE OF s`, tenant, id).Scan(&status, &customer, &oldPlan, &provider, &pc, &method, &fp, &anchor, &next, &paused, &cycle, &epoch, &balance, &unit, &count, &amount, &credits, &currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	var pending bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM subscription_changes WHERE tenant_id=$1 AND subscription_id=$2 AND status IN ('pending','review'))", tenant, id).Scan(&pending); err != nil {
		return nil, err
	}
	if pending {
		return nil, errConflict
	}
	var scheduled, linked bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM scheduled_plan_changes WHERE tenant_id=$1 AND subscription_id=$2 AND status IN ('scheduled','pending_payment')),EXISTS(SELECT 1 FROM scheduled_plan_changes WHERE tenant_id=$1 AND subscription_id=$2 AND status='pending_payment')", tenant, id).Scan(&scheduled, &linked); err != nil {
		return nil, err
	}
	if action == "schedule-cancel" {
		var in struct{}
		if decode(r, &in) != nil {
			return nil, errInvalid
		}
		if !scheduled || linked {
			return nil, errConflict
		}
		if err = cancelScheduledPlanChange(ctx, tx, tenant, id); err != nil {
			return nil, err
		}
		return map[string]any{"id": id, "status": "canceled"}, nil
	}
	if scheduled && (action != "pause" && action != "resume" && action != "cancel" || linked && action != "cancel") {
		return nil, errConflict
	}
	var now time.Time
	if err = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return nil, err
	}
	if action == "reactivate" {
		var in struct{}
		if decode(r, &in) != nil {
			return nil, errInvalid
		}
		if status != "canceled" || !next.After(now) || method == "" {
			return nil, errConflict
		}
		if _, err = tx.Exec(ctx, "UPDATE subscriptions SET status='active' WHERE tenant_id=$1 AND id=$2", tenant, id); err != nil {
			return nil, err
		}
		return map[string]any{"id": id, "status": "active", "next_at": next}, emitSubscriptionEvent(ctx, tx, tenant, "subscription.reactivated", id, nil)
	}
	if action == "pause" || action == "resume" || action == "cancel" {
		var in struct{}
		if decode(r, &in) != nil {
			return nil, errInvalid
		}
		if status == "canceled" {
			return nil, errConflict
		}
		switch action {
		case "pause":
			if paused != nil {
				return nil, errConflict
			}
			_, err = tx.Exec(ctx, "UPDATE subscriptions SET paused_at=$3 WHERE tenant_id=$1 AND id=$2", tenant, id, now)
		case "resume":
			if paused == nil {
				return nil, errConflict
			}
			_, err = tx.Exec(ctx, "UPDATE subscriptions SET anchor=anchor+($3::timestamptz-paused_at),next_at=next_at+($3::timestamptz-paused_at),paused_at=NULL WHERE tenant_id=$1 AND id=$2", tenant, id, now)
		case "cancel":
			status = "canceled"
			_, err = tx.Exec(ctx, "UPDATE subscriptions SET status='canceled' WHERE tenant_id=$1 AND id=$2", tenant, id)
		}
		if err != nil {
			return nil, err
		}
		if action == "cancel" {
			if err = cancelScheduledPlanChange(ctx, tx, tenant, id); err != nil {
				return nil, err
			}
		} else if action == "resume" {
			if _, err = tx.Exec(ctx, "UPDATE scheduled_plan_changes SET effective_at=(SELECT next_at FROM subscriptions WHERE tenant_id=$1 AND id=$2),updated_at=now() WHERE tenant_id=$1 AND subscription_id=$2 AND status='scheduled'", tenant, id); err != nil {
				return nil, err
			}
		}
		event := map[string]string{"pause": "subscription.paused", "resume": "subscription.resumed", "cancel": "subscription.canceled"}[action]
		return map[string]any{"id": id, "status": status, "paused": action == "pause"}, emitSubscriptionEvent(ctx, tx, tenant, event, id, nil)
	}
	var in struct {
		Plan      string `json:"plan_id"`
		MaxAmount *int64 `json:"max_amount"`
	}
	var metadata json.RawMessage
	if action == "schedule" {
		var schedule struct {
			Plan     string          `json:"plan_id"`
			Metadata json.RawMessage `json:"metadata"`
		}
		if decode(r, &schedule) != nil || schedule.Plan == "" {
			return nil, errInvalid
		}
		if _, err = decodeMetadata(schedule.Metadata); err != nil {
			return nil, err
		}
		in.Plan, metadata = schedule.Plan, schedule.Metadata
	} else if decode(r, &in) != nil || in.Plan == "" {
		return nil, errInvalid
	}
	if status != "active" || paused != nil || cycle < 1 || !next.After(now) || in.Plan == oldPlan {
		return nil, errConflict
	}
	var paid bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM charges WHERE tenant_id=$1 AND subscription_id=$2 AND cycle=(SELECT max(cycle) FROM charges WHERE tenant_id=$1 AND subscription_id=$2 AND cycle<$3+1) AND status='paid' AND credited AND NOT reversed) AND NOT EXISTS(SELECT 1 FROM charges WHERE tenant_id=$1 AND subscription_id=$2 AND cycle IS NOT NULL AND status<>'paid')`, tenant, id, cycle-1).Scan(&paid); err != nil {
		return nil, err
	}
	if !paid {
		return nil, errConflict
	}
	var newAmount, newCredits int64
	var newUnit, newCurrency string
	var newCount int
	err = tx.QueryRow(ctx, "SELECT amount,credits,interval_unit,interval_count,currency FROM plans WHERE tenant_id=$1 AND id=$2", tenant, in.Plan).Scan(&newAmount, &newCredits, &newUnit, &newCount, &newCurrency)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if currency != newCurrency {
		return nil, errInvalid
	}
	if action == "schedule" {
		change := token()
		_, err = tx.Exec(ctx, `INSERT INTO scheduled_plan_changes(tenant_id,id,subscription_id,source_plan_id,target_plan_id,effective_at,status,metadata) VALUES($1,$2,$3,$4,$5,$6,'scheduled',COALESCE($7::jsonb,'{}'::jsonb))`, tenant, change, id, oldPlan, in.Plan, next, metadata)
		if err != nil {
			return nil, err
		}
		return map[string]any{"change_id": change, "status": "scheduled", "effective_at": next}, emitScheduledPlanEvent(ctx, tx, tenant, change, "subscription.change_scheduled", "")
	}
	start := cycleAt(anchor, unit, count, cycle-1-epoch)
	unused := prorated(amount, next.Sub(now), next.Sub(start), false)
	usage := newCredits - prorated(credits, next.Sub(now), next.Sub(start), true)
	due := newAmount - unused - balance
	after := int64(0)
	if due < 0 {
		after = -due
		due = 0
	}
	if in.MaxAmount != nil && (*in.MaxAmount < 0 || due > *in.MaxAmount) {
		return nil, errConflict
	}
	end := cycleAt(now, newUnit, newCount, 1)
	preview := map[string]any{"plan_id": in.Plan, "old_plan_id": oldPlan, "amount_due": due, "unused_credit": unused, "billing_credit_after": after, "credits_to_grant": usage, "currency": currency, "new_period_end": end}
	if action == "preview" {
		return preview, nil
	}
	change, charge := token(), ""
	state := "pending"
	if due > 0 {
		charge = token()
		_, err = tx.Exec(ctx, `INSERT INTO charges(tenant_id,id,customer_id,provider,provider_customer,payment_method,account_fingerprint,amount,currency,credits,subscription_id,kind) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,0,$10,'plan_change')`, tenant, charge, customer, provider, pc, method, fp, due, currency, id)
		if err != nil {
			return nil, err
		}
	} else {
		state = "applied"
	}
	_, err = tx.Exec(ctx, `INSERT INTO subscription_changes(tenant_id,id,subscription_id,plan_id,charge_id,status,effective_at,next_at,unused_amount,credit_delta,usage_credits) VALUES($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8,$9,$10,$11)`, tenant, change, id, in.Plan, charge, state, now, end, unused, after-balance, usage)
	if err != nil {
		return nil, err
	}
	if due == 0 {
		if err = applyPlanChange(ctx, tx, tenant, change); err != nil {
			return nil, err
		}
	}
	data := map[string]any{"change_id": change}
	if charge != "" {
		data["charge_id"] = charge
	}
	return map[string]any{"change_id": change, "charge_id": charge, "status": state}, emitSubscriptionEvent(ctx, tx, tenant, "subscription.change_"+state, id, data)
}

func applyPlanChange(ctx context.Context, tx pgx.Tx, tenant, change string) error {
	var id, plan, customer string
	var effective, end time.Time
	var delta, usage int64
	err := tx.QueryRow(ctx, `SELECT c.subscription_id,c.plan_id,s.customer_id,c.effective_at,c.next_at,c.credit_delta,c.usage_credits FROM subscription_changes c JOIN subscriptions s ON s.tenant_id=c.tenant_id AND s.id=c.subscription_id WHERE c.tenant_id=$1 AND c.id=$2`, tenant, change).Scan(&id, &plan, &customer, &effective, &end, &delta, &usage)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO billing_ledger(tenant_id,subscription_id,delta,reference) VALUES($1,$2,$3,$4)`, tenant, id, delta, change); err != nil {
		return err
	}
	if usage != 0 {
		if _, err = changeWallet(ctx, tx, tenant, customer, usage, "grant", "plan-change:"+change, false); err != nil {
			return err
		}
	}
	// Settlement begins the replacement period; the accepted quote fixes the
	// unused-value adjustment while provider confirmation may arrive later.
	var settlement time.Time
	if err = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&settlement); err != nil {
		return err
	}
	// Preserve charge cycle identifiers while resetting the billing calendar.
	var unit string
	var count int
	if err = tx.QueryRow(ctx, "SELECT interval_unit,interval_count FROM plans WHERE tenant_id=$1 AND id=$2", tenant, plan).Scan(&unit, &count); err != nil {
		return err
	}
	effective = settlement
	end = cycleAt(effective, unit, count, 1)
	if _, err = tx.Exec(ctx, "UPDATE subscription_changes SET effective_at=$3,next_at=$4 WHERE tenant_id=$1 AND id=$2", tenant, change, effective, end); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE subscriptions SET plan_id=$3,anchor=$4,next_at=$5,billing_epoch=cycle-1,billing_credit=billing_credit+$6 WHERE tenant_id=$1 AND id=$2", tenant, id, plan, effective, end, delta)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE subscription_changes SET status='applied' WHERE tenant_id=$1 AND id=$2", tenant, change)
	return err
}

// Reverse the exact signed entitlement entry, including after a proposal has
// entered review. The reversal reference makes repeated provider polls safe.
func reversePlanUsage(ctx context.Context, tx pgx.Tx, tenant, customer, change string) error {
	var delta int64
	err := tx.QueryRow(ctx, "SELECT delta FROM ledger WHERE tenant_id=$1 AND customer_id=$2 AND reason='grant' AND reference=$3", tenant, customer, "plan-change:"+change).Scan(&delta)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = changeWallet(ctx, tx, tenant, customer, -delta, "reversal", "plan-change:"+change, false)
	return err
}

// The caller holds the subscription lock, including cancellation from the buyer portal.
func cancelScheduledPlanChange(ctx context.Context, tx pgx.Tx, tenant, subscription string) error {
	var change string
	err := tx.QueryRow(ctx, "UPDATE scheduled_plan_changes SET status='canceled',updated_at=now() WHERE tenant_id=$1 AND subscription_id=$2 AND status IN ('scheduled','pending_payment') RETURNING id", tenant, subscription).Scan(&change)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return emitScheduledPlanEvent(ctx, tx, tenant, change, "subscription.change_schedule_canceled", "")
}

func emitScheduledPlanEvent(ctx context.Context, tx pgx.Tx, tenant, change, kind, reason string) error {
	var subscription string
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT s.subscription_id,jsonb_build_object('change_id',s.id,'source_plan_id',s.source_plan_id,'target_plan_id',s.target_plan_id,'effective_at',s.effective_at,'scheduled_change_status',s.status,'scheduled_change_metadata',s.metadata,'charge_id',s.charge_id,'charge_status',c.status,'payment_review_at',s.payment_review_at) FROM scheduled_plan_changes s LEFT JOIN charges c ON c.tenant_id=s.tenant_id AND c.id=s.charge_id WHERE s.tenant_id=$1 AND s.id=$2`, tenant, change).Scan(&subscription, &raw)
	if err != nil {
		return err
	}
	data, err := decodeEventData(raw)
	if err != nil {
		return err
	}
	if reason != "" {
		data["reason"] = reason
	}
	return emitSubscriptionEvent(ctx, tx, tenant, kind, subscription, data)
}

// Confirm the linked renewal without restarting its calendar at late settlement.
func settleScheduledPlanChange(ctx context.Context, tx pgx.Tx, tenant, charge, status, previous string, credited bool) error {
	var change, state, subscription, subscriptionStatus, plan, unit string
	var effective time.Time
	var paused, reviewed *time.Time
	var count, cycle int
	err := tx.QueryRow(ctx, `SELECT c.id,c.status,c.subscription_id,s.status,s.paused_at,c.target_plan_id,c.effective_at,p.interval_unit,p.interval_count,ch.cycle,c.payment_review_at FROM scheduled_plan_changes c JOIN subscriptions s ON s.tenant_id=c.tenant_id AND s.id=c.subscription_id JOIN plans p ON p.tenant_id=c.tenant_id AND p.id=c.target_plan_id JOIN charges ch ON ch.tenant_id=c.tenant_id AND ch.id=c.charge_id WHERE c.tenant_id=$1 AND c.charge_id=$2 FOR UPDATE OF c`, tenant, charge).Scan(&change, &state, &subscription, &subscriptionStatus, &paused, &plan, &effective, &unit, &count, &cycle, &reviewed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	effective = effective.UTC()
	if status == "paid" && !credited && state != "applied" {
		if reviewed != nil {
			return nil
		}
		if state == "canceled" || subscriptionStatus == "canceled" || paused != nil {
			if _, err = tx.Exec(ctx, "UPDATE scheduled_plan_changes SET payment_review_at=now(),updated_at=now() WHERE tenant_id=$1 AND id=$2", tenant, change); err != nil {
				return err
			}
			return emitScheduledPlanEvent(ctx, tx, tenant, change, "subscription.change_schedule_failed", "subscription_not_eligible")
		}
		nextCycle := cycle + 1
		for !cycleAt(effective, unit, count, nextCycle-cycle).After(time.Now().UTC()) {
			nextCycle++
		}
		if _, err = tx.Exec(ctx, "UPDATE subscriptions SET plan_id=$3,status='active',anchor=$4,next_at=$5,billing_epoch=$6,cycle=$7 WHERE tenant_id=$1 AND id=$2", tenant, subscription, plan, effective, cycleAt(effective, unit, count, nextCycle-cycle), cycle, nextCycle); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE scheduled_plan_changes SET status='applied',updated_at=now() WHERE tenant_id=$1 AND id=$2", tenant, change); err != nil {
			return err
		}
		return emitScheduledPlanEvent(ctx, tx, tenant, change, "subscription.change_schedule_applied", "")
	}
	if status != previous && (status == "failed" || status == "manual_review" || status == "reversed") {
		return emitScheduledPlanEvent(ctx, tx, tenant, change, "subscription.change_schedule_failed", status)
	}
	return nil
}
