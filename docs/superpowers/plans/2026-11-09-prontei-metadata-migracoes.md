# Payment Hub Metadata and Scheduled Migrations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend Payment Hub with opaque resource metadata, correlation-rich financial events, and idempotent plan changes scheduled for subscription renewal.

**Architecture:** Keep Payment Hub authoritative for financial state. Metadata is tenant-scoped, size-limited data that the Hub stores and returns without interpreting. Scheduled changes remain financial lifecycle records owned by the Hub; the old plan stays active until the target-plan renewal charge is paid.

**Tech Stack:** Go 1.26, PostgreSQL 17, pgx, embedded SQL migrations, JSON HTTP API, Pagar.me worker, `go test -race`.

**Spec:** `/home/wender/Projetos/WENDERTECH/Pratik/docs/superpowers/specs/2026-11-09-planos-assinaturas-payment-hub-design.md`

## Global Constraints

- Preserve tenant isolation, idempotency, provider credentials, checkout tokenization, and current immediate plan-change behavior.
- Metadata must be a JSON object of at most 8 KiB; never interpret it as authorization or payment state.
- Existing rows and API clients remain valid with `{}` metadata.
- A scheduled change charges the target price at renewal but does not replace `subscriptions.plan_id` before `charge.paid`.
- Pagar.me remains the only hosted recurring checkout in this delivery.
- Update `docs/openapi.json` with every public contract change.

## Review Focus

- A 8193-byte metadata object is rejected while exactly 8192 bytes is accepted (Task 1 test).
- Replayed create/schedule requests return the original objects without duplicate rows (Tasks 1 and 3 tests).
- A failed target-plan renewal leaves the old plan and scheduled change pending during grace (Task 3 test).
- Two workers cannot emit two target-plan renewal charges for one subscription/cycle (Task 3 concurrency test).
- Events never leak provider customer/payment-method values or unrelated tenant metadata (Task 2 test).

---

### Task 1: Metadata persistence and API contracts

**Files:**
- Create: `internal/hub/migrations/010_metadata.sql`
- Create: `internal/hub/metadata.go`
- Create: `internal/hub/metadata_test.go`
- Modify: `internal/hub/store.go`
- Modify: `internal/hub/api.go`
- Modify: `internal/hub/checkout.go`
- Modify: `internal/hub/schema.sql`
- Modify: `internal/hub/integration_test.go`
- Modify: `internal/hub/checkout_test.go`

**Interfaces:**
- Produces: `type Metadata map[string]any`, `decodeMetadata(json.RawMessage) (Metadata, error)`, and metadata columns on customers, plans, offers, checkout sessions, subscriptions, and charges.
- Produces: `POST /v1/offers` response `{id, plan_id, metadata}`; all affected create/get/list responses expose `metadata`.

- [ ] **Step 1: Write failing metadata tests**

Add tests asserting `{}` defaults for legacy rows, tenant isolation, object-only validation, the 8 KiB boundary, metadata snapshots on checkout sessions, and `POST /v1/offers` returning both generated IDs.

- [ ] **Step 2: Verify the tests fail**

Run: `go test ./internal/hub -run 'TestMetadata|TestCheckoutMetadata' -count=1`

- [ ] **Step 3: Add migration 010 and embed it**

Add `metadata jsonb NOT NULL DEFAULT '{}'::jsonb` to the six resources, a `schema_version=10` row, and the matching `//go:embed`/migration entry in `store.go`. Keep `schema.sql` valid for fresh installations.

- [ ] **Step 4: Implement metadata decoding and persistence**

Use `json.RawMessage` at HTTP boundaries, validate top-level object and serialized length, pass it unchanged to PostgreSQL, and return it from creates and existing JSON views/list reads.

- [ ] **Step 5: Run focused and race tests**

Run: `go test -race ./internal/hub -run 'TestMetadata|TestCheckout|TestIntegration' -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/hub
git commit -m "feat: add resource metadata contracts"
```

### Task 2: Correlation-rich outbound events

**Files:**
- Create: `internal/hub/event_data.go`
- Create: `internal/hub/event_data_test.go`
- Modify: `internal/hub/api.go`
- Modify: `internal/hub/checkout.go`
- Modify: `internal/hub/lifecycle.go`
- Modify: `internal/hub/worker.go`
- Modify: `internal/hub/notifications_test.go`

**Interfaces:**
- Consumes: metadata columns from Task 1.
- Produces: `financialEventData(ctx, tx, tenant, chargeID) (map[string]any, error)` and `subscriptionEventData(ctx, tx, tenant, subscriptionID) (map[string]any, error)`.
- Event data contains applicable `charge_id`, `customer_id`, `subscription_id`, `plan_id`, `status`, and per-resource metadata.

- [ ] **Step 1: Write failing event payload tests**

Assert checkout, charge, subscription, lifecycle, and notification payloads contain only the current tenant's correlation fields and metadata; assert secrets/provider identifiers are absent.

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/hub -run 'TestFinancialEventData|TestSubscriptionEventData|TestNotificationMetadata' -count=1`

- [ ] **Step 3: Implement event-data builders and replace ad-hoc maps**

Read authoritative rows inside the emitting transaction. Keep event IDs/types and at-least-once delivery behavior unchanged.

- [ ] **Step 4: Run event and notification tests**

Run: `go test -race ./internal/hub -run 'Test.*Event|TestNotification' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/hub
git commit -m "feat: enrich billing event correlation"
```

### Task 3: Plan changes scheduled for renewal

**Files:**
- Create: `internal/hub/migrations/011_scheduled_plan_changes.sql`
- Create: `internal/hub/scheduled_change_test.go`
- Modify: `internal/hub/store.go`
- Modify: `internal/hub/api.go`
- Modify: `internal/hub/lifecycle.go`
- Modify: `internal/hub/lifecycle_read.go`
- Modify: `internal/hub/worker.go`
- Modify: `internal/hub/integration_test.go`

**Interfaces:**
- Produces: `POST /v1/subscriptions/{id}/change-plan/schedule` with `{plan_id, metadata}`.
- Produces: `POST /v1/subscriptions/{id}/change-plan/schedule/cancel` with `{}`.
- Produces: pending-change data in `GET /v1/subscriptions/{id}` and events `subscription.change_scheduled`, `subscription.change_schedule_canceled`, `subscription.change_schedule_applied`, and `subscription.change_schedule_failed`.

- [ ] **Step 1: Write failing lifecycle tests**

Cover schedule/cancel, same-plan rejection, inactive/paused/canceled/conflicting subscription rejection, metadata, target-price renewal, old rights before payment, application only after `charge.paid`, failed charge preservation, idempotent replay, and two concurrent schedulers/workers.

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/hub -run 'TestScheduledPlanChange' -count=1`

- [ ] **Step 3: Add migration 011 and API routing**

Create a tenant-scoped scheduled-change record with source/target plan, effective renewal, status, metadata, charge link, timestamps, and one live change per subscription. Embed the migration and expose authenticated idempotent routes through the existing lifecycle dispatcher.

- [ ] **Step 4: Make renewal scheduling select the target plan atomically**

Under the existing subscription lock, create exactly one renewal charge using target price/currency/credits while retaining the current `subscriptions.plan_id`; link the charge to the scheduled change.

- [ ] **Step 5: Apply the target plan only on confirmed payment**

On `charge.paid`, update `subscriptions.plan_id`, mark the schedule applied, and emit the applied event in the same transaction. Failed/pending/review charges leave the old plan effective and expose the financial state for Prontei's grace handling.

- [ ] **Step 6: Run lifecycle, worker, PostgreSQL, and race tests**

Run: `TEST_DATABASE_URL="$TEST_DATABASE_URL" go test -race ./internal/hub -run 'TestScheduledPlanChange|Test.*Lifecycle|Test.*Worker' -count=1`
Expected: PASS with PostgreSQL tests executed, not skipped.

- [ ] **Step 7: Commit**

```bash
git add internal/hub
git commit -m "feat: schedule plan changes at renewal"
```

### Task 4: Public contract and full verification

**Files:**
- Modify: `docs/openapi.json`
- Modify: `README.md`
- Modify: `docs/subscription-lifecycle.md`
- Modify: `docs/notifications.md`
- Create: `docs/prontei-integration.md`

**Interfaces:**
- Consumes: Tasks 1-3 public contracts.
- Produces: complete API documentation for the Prontei implementer.

- [ ] **Step 1: Update OpenAPI and integration documentation**

Document metadata schemas/limits, enriched event examples, schedule/cancel routes, states, idempotency, and the rule that `subscription.created` does not prove payment.

- [ ] **Step 2: Run all automated checks**

Run: `TEST_DATABASE_URL="$TEST_DATABASE_URL" go test -race -count=1 ./...`
Run: `go vet ./...`
Run: `go build ./cmd/server`
Expected: all pass; database tests are not skipped.

- [ ] **Step 3: Validate a disposable Pagar.me sandbox renewal**

Create a disposable customer/subscription, schedule a target plan, drive the sandbox renewal, verify event metadata and plan application only after payment, then cancel/refund all test objects. Save only sanitized evidence.

- [ ] **Step 4: Commit**

```bash
git add README.md docs
git commit -m "docs: publish Prontei billing contract"
```

