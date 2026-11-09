package hub

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type Worker struct {
	Store    *Store
	Accounts AccountSource
	Gateway  Provider
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.Tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("worker tick failed", "error", err.Error())
			}
		}
	}
}
func (w *Worker) Tick(ctx context.Context) error {
	if err := w.schedule(ctx); err != nil {
		return err
	}
	// Bounded batch keeps shutdown responsive; several instances may share the queue safely.
	for i := 0; i < 10; i++ {
		c, err := w.claim(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = w.process(ctx, c); err != nil {
			return err
		}
	}
	return nil
}
func (w *Worker) schedule(ctx context.Context) error {
	tx, err := w.Store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT s.tenant_id,s.id,s.customer_id,s.provider,s.provider_customer,s.payment_method,s.anchor,s.cycle,s.billing_epoch,s.billing_credit,p.amount,p.currency,p.credits,p.interval_unit,p.interval_count,s.account_fingerprint
 FROM subscriptions s JOIN plans p ON p.tenant_id=s.tenant_id AND p.id=s.plan_id
 WHERE s.status='active' AND s.paused_at IS NULL AND s.next_at<=now()
 AND NOT EXISTS(SELECT 1 FROM subscription_changes ch WHERE ch.tenant_id=s.tenant_id AND ch.subscription_id=s.id AND ch.status='pending')
 AND NOT EXISTS(SELECT 1 FROM charges c WHERE c.tenant_id=s.tenant_id AND c.subscription_id=s.id AND c.status<>'paid' AND c.cycle IS NOT NULL)
 ORDER BY s.next_at LIMIT 50 FOR UPDATE OF s SKIP LOCKED`)
	if err != nil {
		return err
	}
	type due struct {
		tenant, id, customer, provider, providerCustomer, method, currency, unit, fingerprint string
		anchor                                                                                time.Time
		cycle, epoch, count                                                                   int
		amount, credits, billingCredit                                                        int64
	}
	var subs []due
	for rows.Next() {
		var d due
		if err = rows.Scan(&d.tenant, &d.id, &d.customer, &d.provider, &d.providerCustomer, &d.method, &d.anchor, &d.cycle, &d.epoch, &d.billingCredit, &d.amount, &d.currency, &d.credits, &d.unit, &d.count, &d.fingerprint); err != nil {
			rows.Close()
			return err
		}
		subs = append(subs, d)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	// Each transaction schedules one tenant only to avoid cross-tenant event-lock inversions.
	if len(subs) == 0 {
		return tx.Commit(ctx)
	}
	tenant := subs[0].tenant
	for _, d := range subs {
		if d.tenant != tenant {
			continue
		}
		// Re-read after locking: a schedule may have committed after the due-list snapshot.
		var scheduled string
		err = tx.QueryRow(ctx, `SELECT c.id,p.amount,p.currency,p.credits FROM scheduled_plan_changes c JOIN plans p ON p.tenant_id=c.tenant_id AND p.id=c.target_plan_id WHERE c.tenant_id=$1 AND c.subscription_id=$2 AND c.status='scheduled'`, d.tenant, d.id).Scan(&scheduled, &d.amount, &d.currency, &d.credits)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		used := d.billingCredit
		if used > d.amount {
			used = d.amount
		}
		if used < 0 {
			used = 0
		}
		due := d.amount - used
		chargeAmount := due
		id := token()
		_, err = tx.Exec(ctx, `INSERT INTO charges(tenant_id,id,customer_id,provider,provider_customer,payment_method,amount,currency,credits,subscription_id,cycle,account_fingerprint) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, d.tenant, id, d.customer, d.provider, d.providerCustomer, d.method, chargeAmount, d.currency, d.credits, d.id, d.cycle, d.fingerprint)
		if err != nil {
			return err
		}
		if scheduled != "" {
			if _, err = tx.Exec(ctx, "UPDATE scheduled_plan_changes SET charge_id=$3,status='pending_payment',updated_at=now() WHERE tenant_id=$1 AND id=$2", d.tenant, scheduled, id); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, "UPDATE charges SET billing_credit_used=$3 WHERE tenant_id=$1 AND id=$2", d.tenant, id, used); err != nil {
			return err
		}
		if used > 0 {
			if _, err = tx.Exec(ctx, "UPDATE subscriptions SET billing_credit=billing_credit-$3 WHERE tenant_id=$1 AND id=$2", d.tenant, d.id, used); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, "INSERT INTO billing_ledger(tenant_id,subscription_id,delta,reference) VALUES($1,$2,$3,$4)", d.tenant, d.id, -used, "renewal:"+id); err != nil {
				return err
			}
		}
		if due == 0 {
			if _, err = tx.Exec(ctx, "UPDATE charges SET status='paid',credited=true,kind='credit_settlement',next_at='infinity'::timestamptz WHERE tenant_id=$1 AND id=$2", d.tenant, id); err != nil {
				return err
			}
			if d.credits > 0 {
				if _, err = changeWallet(ctx, tx, d.tenant, d.customer, d.credits, "payment", id, false); err != nil {
					return err
				}
			}
		}
		nextCycle := d.cycle + 1
		// Skip missed billing windows after downtime; never issue a backlog of automatic charges.
		for !cycleAt(d.anchor, d.unit, d.count, nextCycle-d.epoch).After(time.Now().UTC()) {
			nextCycle++
		}
		_, err = tx.Exec(ctx, "UPDATE subscriptions SET cycle=$4,next_at=$3 WHERE tenant_id=$1 AND id=$2", d.tenant, d.id, cycleAt(d.anchor, d.unit, d.count, nextCycle-d.epoch), nextCycle)
		if err != nil {
			return err
		}
		event := "charge.queued"
		if due == 0 {
			event = "charge.paid"
		}
		if due == 0 && scheduled != "" {
			if err = settleScheduledPlanChange(ctx, tx, d.tenant, id, "paid", "queued", false); err != nil {
				return err
			}
		}
		if err = emitFinancialEvent(ctx, tx, d.tenant, event, id, nil); err != nil {
			return err
		}

	}
	return tx.Commit(ctx)
}
func (w *Worker) claim(ctx context.Context) (Charge, error) {
	var c Charge
	err := w.Store.DB.QueryRow(ctx, `WITH job AS (
 SELECT tenant_id,id FROM charges WHERE (status IN ('queued','pending','requires_action','paid') OR (status='failed' AND provider_id IS NOT NULL) OR (status='manual_review' AND last_error='payment_underpaid' AND provider_id IS NOT NULL)) AND next_at<=now() AND (lease_until IS NULL OR lease_until<now())
 ORDER BY next_at LIMIT 1 FOR UPDATE SKIP LOCKED)
 UPDATE charges c SET lease_until=now()+interval '60 seconds',lease_token=$1,first_attempt=COALESCE(first_attempt,now()),attempts=attempts+1
 FROM job WHERE c.tenant_id=job.tenant_id AND c.id=job.id
 RETURNING c.tenant_id,c.id,c.customer_id,c.provider,c.provider_customer,c.payment_method,c.amount,c.currency,c.credits,COALESCE(c.provider_id,''),c.status,c.lease_token,c.first_attempt,c.subscription_id IS NOT NULL,COALESCE(c.cycle,(SELECT s.cycle FROM subscriptions s WHERE s.tenant_id=c.tenant_id AND s.id=c.subscription_id),0),COALESCE((SELECT origin.provider_charge_id FROM charges origin WHERE origin.tenant_id=c.tenant_id AND origin.subscription_id=c.subscription_id AND origin.cycle<COALESCE(c.cycle,(SELECT s.cycle FROM subscriptions s WHERE s.tenant_id=c.tenant_id AND s.id=c.subscription_id)) AND origin.payment_method=c.payment_method AND origin.status='paid' ORDER BY origin.cycle LIMIT 1),''),c.account_fingerprint,c.last_error,c.method,c.installments,c.pix_expires,c.boleto_due_days,
 NOT EXISTS(SELECT 1 FROM charges prior WHERE prior.tenant_id=c.tenant_id AND prior.subscription_id=c.subscription_id AND prior.cycle<COALESCE(c.cycle,(SELECT s.cycle FROM subscriptions s WHERE s.tenant_id=c.tenant_id AND s.id=c.subscription_id)) AND prior.payment_method=c.payment_method AND prior.status='paid')`, token()).Scan(&c.Tenant, &c.ID, &c.Customer, &c.Provider, &c.ProviderCustomer, &c.PaymentMethod, &c.Amount, &c.Currency, &c.Credits, &c.ProviderID, &c.Status, &c.Lease, &c.FirstAttempt, &c.Recurring, &c.Cycle, &c.Origin, &c.AccountFingerprint, &c.LastError, &c.Method, &c.Installments, &c.PixExpires, &c.BoletoDueDays, &c.RecurringFirst)
	return c, err
}
func (w *Worker) process(ctx context.Context, c Charge) error {
	ac, accountErr := w.Accounts.Get(ctx, c.Tenant, c.Provider)
	if accountErr != nil || ac.Secret == "" {
		return w.retry(ctx, c, "provider_not_configured", false)
	}
	if c.AccountFingerprint != "" && c.AccountFingerprint != hash(ac.Secret) {
		return w.retry(ctx, c, "provider_credentials_changed", true)
	}
	if c.ProviderID == "" && time.Since(c.FirstAttempt) >= ac.Window() {
		return w.retry(ctx, c, "idempotency_window_expired", true)
	}
	var p Payment
	var err error
	if c.ProviderID == "" {
		p, err = w.Gateway.Create(ctx, ac, c)
	} else {
		p, err = w.Gateway.Fetch(ctx, ac, c)
	}
	if err != nil {
		return w.retry(ctx, c, err.Error(), permanentProviderError(err))
	}
	return w.apply(ctx, c, p)
}
func (w *Worker) retry(ctx context.Context, c Charge, message string, manual bool) error {
	tx, err := w.Store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT 1 FROM subscriptions WHERE tenant_id=$1 AND id=(SELECT subscription_id FROM charges WHERE tenant_id=$1 AND id=$2) FOR UPDATE", c.Tenant, c.ID); err != nil {
		return err
	}
	status := c.Status
	if manual {
		status = "manual_review"
	}
	storedMessage := message
	if !manual && c.Status == "manual_review" && c.LastError == "payment_underpaid" {
		storedMessage = c.LastError
	}
	tag, err := tx.Exec(ctx, "UPDATE charges SET status=$4,last_error=$5,next_at=now()+interval '60 seconds',lease_until=NULL,lease_token=NULL,updated_at=now() WHERE tenant_id=$1 AND id=$2 AND lease_token=$3", c.Tenant, c.ID, c.Lease, status, storedMessage)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		if err = recordChargeHistory(ctx, tx, c, attemptKind(c), status, message); err != nil {
			return err
		}
	}
	if manual && tag.RowsAffected() > 0 {
		var subscription string
		e := tx.QueryRow(ctx, "UPDATE subscription_changes SET status='review' WHERE tenant_id=$1 AND charge_id=$2 AND status='pending' RETURNING subscription_id", c.Tenant, c.ID).Scan(&subscription)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if subscription != "" {
			if _, err = tx.Exec(ctx, "UPDATE subscriptions SET status='past_due' WHERE tenant_id=$1 AND id=$2 AND status<>'canceled'", c.Tenant, subscription); err != nil {
				return err
			}
			if err = emitSubscriptionEvent(ctx, tx, c.Tenant, "subscription.change_review", subscription, map[string]any{"charge_id": c.ID, "reason": message}); err != nil {
				return err
			}
		}

		if err = settleScheduledPlanChange(ctx, tx, c.Tenant, c.ID, "manual_review", c.Status, false); err != nil {
			return err
		}
		if err = emitFinancialEvent(ctx, tx, c.Tenant, "charge.manual_review", c.ID, map[string]any{"reason": message}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func (w *Worker) apply(ctx context.Context, c Charge, p Payment) error {
	tx, err := w.Store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Lifecycle actions and scheduler lock the subscription before its charges.
	if _, err = tx.Exec(ctx, "SELECT 1 FROM subscriptions WHERE tenant_id=$1 AND id=(SELECT subscription_id FROM charges WHERE tenant_id=$1 AND id=$2) FOR UPDATE", c.Tenant, c.ID); err != nil {
		return err
	}
	var credited, reversed bool
	var previous string
	var subscriptionID string
	err = tx.QueryRow(ctx, "SELECT status,credited,reversed,COALESCE(subscription_id,'') FROM charges WHERE tenant_id=$1 AND id=$2 AND lease_token=$3 FOR UPDATE", c.Tenant, c.ID, c.Lease).Scan(&previous, &credited, &reversed, &subscriptionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var scheduledIneligible, scheduledReviewed bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM scheduled_plan_changes sc JOIN subscriptions s ON s.tenant_id=sc.tenant_id AND s.id=sc.subscription_id WHERE sc.tenant_id=$1 AND sc.charge_id=$2 AND (sc.status='canceled' OR s.status='canceled' OR s.paused_at IS NOT NULL OR sc.payment_review_at IS NOT NULL)),EXISTS(SELECT 1 FROM scheduled_plan_changes WHERE tenant_id=$1 AND charge_id=$2 AND payment_review_at IS NOT NULL)`, c.Tenant, c.ID).Scan(&scheduledIneligible, &scheduledReviewed); err != nil {
		return err
	}
	previouslyCredited := credited
	// A late pending response must never undo a confirmed payment or a reversal.
	if reversed {
		p.Status = "reversed"
	} else if (credited || scheduledReviewed) && p.Status != "reversed" {
		if p.Status == "failed" {
			p.Detail = strings.Replace(p.Detail, "payment_declined", "subsequent_operation_failed", 1)
			if p.Detail == "" {
				p.Detail = "subsequent_operation_failed"
			}
		} else if p.Status == "pending" {
			p.Detail = "subsequent_operation_pending"
		} else if p.Status != "paid" {
			p.Detail = "provider_status_conflict"
		}
		p.Status = "paid"
	}
	var changeID, changeStatus string
	changeErr := tx.QueryRow(ctx, "SELECT id,status FROM subscription_changes WHERE tenant_id=$1 AND charge_id=$2 FOR UPDATE", c.Tenant, c.ID).Scan(&changeID, &changeStatus)
	if changeErr != nil && !errors.Is(changeErr, pgx.ErrNoRows) {
		return changeErr
	}
	if changeID != "" {
		if p.Status == "reversed" && !reversed {
			if err = reversePlanUsage(ctx, tx, c.Tenant, c.Customer, changeID); err != nil {
				return err
			}
		}

		if p.Status == "paid" && !credited && changeStatus == "pending" {
			var subscriptionStatus string
			if err = tx.QueryRow(ctx, "SELECT status FROM subscriptions WHERE tenant_id=$1 AND id=(SELECT subscription_id FROM subscription_changes WHERE tenant_id=$1 AND id=$2)", c.Tenant, changeID).Scan(&subscriptionStatus); err != nil {
				return err
			}
			if subscriptionStatus != "active" {
				changeStatus = "review"
			}
		}

		if p.Status == "paid" && !credited && changeStatus == "pending" {
			if err = applyPlanChange(ctx, tx, c.Tenant, changeID); err != nil {
				return err
			}
			if err = emitSubscriptionEvent(ctx, tx, c.Tenant, "subscription.change_applied", subscriptionID, map[string]any{"change_id": changeID, "charge_id": c.ID}); err != nil {
				return err
			}
		}
		if p.Status == "failed" && changeStatus == "pending" {
			if _, err = tx.Exec(ctx, "UPDATE subscription_changes SET status='failed' WHERE tenant_id=$1 AND id=$2", c.Tenant, changeID); err != nil {
				return err
			}
		}
		if (p.Status == "paid" && !credited && (changeStatus == "failed" || changeStatus == "review")) || (p.Status == "reversed" && changeStatus == "applied") {
			if _, err = tx.Exec(ctx, "UPDATE subscription_changes SET status='review' WHERE tenant_id=$1 AND id=$2", c.Tenant, changeID); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, "UPDATE subscriptions SET status='past_due' WHERE tenant_id=$1 AND id=(SELECT subscription_id FROM subscription_changes WHERE tenant_id=$1 AND id=$2) AND status<>'canceled'", c.Tenant, changeID); err != nil {
				return err
			}
			if err = emitSubscriptionEvent(ctx, tx, c.Tenant, "subscription.change_review", subscriptionID, map[string]any{"change_id": changeID, "charge_id": c.ID}); err != nil {
				return err
			}
		}
	}
	if c.Recurring && changeID == "" && (p.Status == "failed" || p.Status == "reversed") {
		if _, err = tx.Exec(ctx, "UPDATE subscriptions SET status='past_due' WHERE tenant_id=$1 AND id=(SELECT subscription_id FROM charges WHERE tenant_id=$1 AND id=$2) AND status='active'", c.Tenant, c.ID); err != nil {
			return err
		}
	}
	if p.Status == "paid" && !credited && !scheduledIneligible {
		if c.Credits > 0 {
			if _, err = changeWallet(ctx, tx, c.Tenant, c.Customer, c.Credits, "payment", c.ID, false); err != nil {
				return err
			}
		}
		credited = true
	}
	if p.Status == "reversed" && !reversed && changeID == "" {
		rows, e := tx.Query(ctx, "SELECT id FROM subscription_changes WHERE tenant_id=$1 AND subscription_id=(SELECT subscription_id FROM charges WHERE tenant_id=$1 AND id=$2) ORDER BY created_at,id", c.Tenant, c.ID)
		if e != nil {
			return e
		}
		var changes []string
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			changes = append(changes, id)
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			return e
		}
		for _, id := range changes {
			if err = reversePlanUsage(ctx, tx, c.Tenant, c.Customer, id); err != nil {
				return err
			}
		}

		if _, err = tx.Exec(ctx, "UPDATE subscription_changes SET status='review' WHERE tenant_id=$1 AND subscription_id=(SELECT subscription_id FROM charges WHERE tenant_id=$1 AND id=$2) AND status IN ('pending','applied')", c.Tenant, c.ID); err != nil {
			return err
		}

		var used int64
		if err = tx.QueryRow(ctx, "SELECT billing_credit_used FROM charges WHERE tenant_id=$1 AND id=$2", c.Tenant, c.ID).Scan(&used); err != nil {
			return err
		}
		if used > 0 {
			if err = emitSubscriptionEvent(ctx, tx, c.Tenant, "subscription.billing_credit_review", subscriptionID, map[string]any{"charge_id": c.ID, "billing_credit_used": used}); err != nil {
				return err
			}
		}
	}
	if p.Status == "reversed" && !reversed {
		if credited && c.Credits > 0 {
			if _, err = changeWallet(ctx, tx, c.Tenant, c.Customer, -c.Credits, "reversal", c.ID, false); err != nil {
				return err
			}
		}
		reversed = true
	}
	instructions, err := json.Marshal(p.Instructions)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE charges SET payment_instructions=CASE WHEN $10::jsonb='{}'::jsonb THEN payment_instructions ELSE $10::jsonb END,status=$4,provider_id=$5,client_secret=$6,credited=$7,reversed=$8,provider_charge_id=$9,last_error=$11,lease_until=NULL,lease_token=NULL,
 next_at=now()+CASE WHEN $4 IN ('paid','failed') THEN interval '6 hours' ELSE interval '30 seconds' END,updated_at=now() WHERE tenant_id=$1 AND id=$2 AND lease_token=$3`, c.Tenant, c.ID, c.Lease, p.Status, p.ID, p.ClientSecret, credited, reversed, p.Origin, instructions, p.Detail)
	if err != nil {
		return err
	}
	if err = settleScheduledPlanChange(ctx, tx, c.Tenant, c.ID, p.Status, previous, previouslyCredited); err != nil {
		return err
	}
	if err = recordChargeHistory(ctx, tx, c, attemptKind(c), p.Status, p.Detail); err != nil {
		return err
	}
	if p.Status != previous {
		if err = emitFinancialEvent(ctx, tx, c.Tenant, "charge."+p.Status, c.ID, map[string]any{"reason": p.Detail}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func attemptKind(c Charge) string {
	if c.ProviderID == "" {
		return "create"
	}
	return "verify"
}

func permanentProviderError(err error) bool {
	code, e := strconv.Atoi(strings.TrimPrefix(err.Error(), "provider_http_"))
	return e == nil && code >= 400 && code < 500 && code != 408 && code != 409 && code != 429
}
