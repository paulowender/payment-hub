package hub

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

//go:embed migrations/002_console.sql
var consoleSchema string

//go:embed migrations/003_webhook_diagnostics.sql
var diagnosticSchema string

//go:embed migrations/004_checkout.sql
var checkoutSchema string

//go:embed migrations/005_checkout_consent.sql
var checkoutConsentSchema string

//go:embed migrations/006_payment_options.sql
var paymentOptionsSchema string

//go:embed migrations/007_notifications.sql
var notificationSchema string

//go:embed migrations/008_portal.sql
var portalSchema string

//go:embed migrations/009_lifecycle.sql
var lifecycleSchema string

type Store struct{ DB *pgxpool.Pool }

func Open(ctx context.Context, url string) (*Store, error) {
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err = db.Ping(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db}, nil
}
func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(702701)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, schema); err != nil {
		return err
	}
	var migrated bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_version WHERE version=2)").Scan(&migrated); err != nil {
		return err
	}
	if !migrated {
		if _, err = tx.Exec(ctx, consoleSchema); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_version WHERE version=3)").Scan(&migrated); err != nil {
		return err
	}
	if !migrated {
		if _, err = tx.Exec(ctx, diagnosticSchema); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_version WHERE version=4)").Scan(&migrated); err != nil {
		return err
	}
	if !migrated {
		if _, err = tx.Exec(ctx, checkoutSchema); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_version WHERE version=5)").Scan(&migrated); err != nil {
		return err
	}
	if !migrated {
		if _, err = tx.Exec(ctx, checkoutConsentSchema); err != nil {
			return err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_version WHERE version=6)").Scan(&migrated); err != nil {
		return err
	}
	if !migrated {
		if _, err = tx.Exec(ctx, paymentOptionsSchema); err != nil {
			return err
		}
	}
	for _, m := range []struct {
		version int
		sql     string
	}{{7, notificationSchema}, {8, portalSchema}, {9, lifecycleSchema}} {
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_version WHERE version=$1)", m.version).Scan(&migrated); err != nil {
			return err
		}
		if !migrated {
			if _, err = tx.Exec(ctx, m.sql); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}
func emit(ctx context.Context, tx pgx.Tx, tenant, kind string, data any) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,1))", tenant); err != nil {
		return err
	}
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	var eventID int64
	err = tx.QueryRow(ctx, "INSERT INTO events(tenant_id,type,data) VALUES($1,$2,$3) RETURNING id", tenant, kind, b).Scan(&eventID)
	if err != nil {
		return err
	}
	return enqueueNotifications(ctx, tx, tenant, eventID, kind, data)
}

func (s *Store) write(ctx context.Context, tenant, key, fingerprint string, fn func(pgx.Tx) (any, error)) ([]byte, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// Serialize each tenant/key before checking the persisted result, including concurrent first requests.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", tenant+":"+key); err != nil {
		return nil, err
	}
	var old string
	var data []byte
	err = tx.QueryRow(ctx, "SELECT fingerprint,response FROM idempotency WHERE tenant_id=$1 AND key=$2", tenant, key).Scan(&old, &data)
	if err == nil {
		if old != fingerprint {
			return nil, errConflict
		}
		return data, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	result, err := fn(tx)
	if err != nil {
		return nil, err
	}
	data, err = json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if err = tx.QueryRow(ctx, "INSERT INTO idempotency(tenant_id,key,fingerprint,response) VALUES($1,$2,$3,$4) RETURNING response", tenant, key, fingerprint, data).Scan(&data); err != nil {
		return nil, err
	}
	return data, tx.Commit(ctx)
}
func changeWallet(ctx context.Context, tx pgx.Tx, tenant, customer string, delta int64, reason, ref string, requireFunds bool) (int64, error) {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,2))", tenant); err != nil {
		return 0, err
	}
	var balance int64
	err := tx.QueryRow(ctx, "SELECT balance FROM wallets WHERE tenant_id=$1 AND customer_id=$2 FOR UPDATE", tenant, customer).Scan(&balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errNotFound
	}
	if err != nil {
		return 0, err
	}
	if requireFunds && delta < 0 && balance < -delta {
		return 0, errFunds
	}
	tag, err := tx.Exec(ctx, "INSERT INTO ledger(tenant_id,customer_id,delta,reason,reference) VALUES($1,$2,$3,$4,$5) ON CONFLICT(tenant_id,reason,reference) DO NOTHING", tenant, customer, delta, reason, ref)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 0 {
		if requireFunds {
			return 0, errConflict
		}
		return balance, nil
	}
	err = tx.QueryRow(ctx, "UPDATE wallets SET balance=balance+$3 WHERE tenant_id=$1 AND customer_id=$2 RETURNING balance", tenant, customer, delta).Scan(&balance)
	return balance, err
}
