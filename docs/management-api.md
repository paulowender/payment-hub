# Provider and notification management API

These server-side endpoints use `Authorization: Bearer <project-api-key>` and operate only on that key's tenant. Keep API keys out of browsers. Responses use `Cache-Control: no-store`. A configured encryption vault is required; unavailable management returns 404.

Every POST requires `Idempotency-Key` (1–200 characters). Reuse the same key, method, exact escaped path and exact body to replay the committed HTTP status and JSON result. A changed request with the same tenant/key returns 409. Mutations and replay storage commit together. Responses containing generated signing secrets are encrypted in replay storage with the vault key and bound to tenant/key. There is currently no expiry or cleanup of replay records: an authorized replay can retrieve its original signing secret even after rotation. Protect the encryption key and database backups. Validation/not-found failures are not persisted; provider-test HTTP results, including 502, are persisted, so use a new key to run a fresh diagnostic.

## Providers

| Method and path | Body | Result |
| --- | --- | --- |
| `GET /v1/providers` | — | `{"data":[...]}` for Pagar.me and Stripe, including unconfigured providers |
| `POST /v1/providers/{provider}` | Provider account fields | `{"configured":true}` |
| `POST /v1/providers/{provider}/test` | `{}` | `{"ok":true,"provider":"stripe","environment":"test","checked_at":"..."}` |

`provider` is `pagarme` or `stripe`. A configured list item includes `provider`, `configured`, `environment`, `public_key`, `webhook_configured`, and effective `retry_window_seconds`. It never returns the secret or webhook username/password.

Provider save fields are `environment` (`test` or `live`), `secret`, `public_key`, `webhook_user`, `webhook_password`, and `retry_window_seconds`. Always supply the environment. For updates, an omitted/empty secret or public key preserves its saved value; when both webhook fields are omitted/empty, both saved webhook values are preserved. Other omitted fields use defaults, including retry window 0 (effective 240 seconds). Initial configuration needs a valid secret of 10–512 characters. Environment/key prefixes must agree; public keys must start with `pk_`. Webhook username and password must be supplied together. Retry window is 0 or 30–82800 seconds; Pagar.me test accounts are limited to 240 seconds. Unknown JSON fields are rejected.

The test uses the saved credentials against a fixed read-only provider endpoint, with redirects disabled and a 15-second timeout. It does not create a payment. The response body from the provider is discarded with a 1 MiB read limit. The operation holds its idempotency transaction until the diagnostic completes. A rejected provider request returns 502 with `provider_rejected` and `provider_status`; an unreachable provider returns 502 with `provider_unreachable`.

There is no provider DELETE or disable endpoint. Blank credentials preserve values and cannot disconnect a provider. Provider removal is unsupported by this API.

## Outbound notifications

| Method and path | Body | Result |
| --- | --- | --- |
| `GET /v1/notifications/config` | — | `configured`, `enabled`, and `url` when configured |
| `POST /v1/notifications/config` | `{"url":"https://example.com/events","enabled":true}` | Config fields; `signing_secret` only on initial configuration |
| `POST /v1/notifications/config/reset-secret` | `{}` | Config fields and new `signing_secret` |
| `GET /v1/notifications/deliveries` | — | `{"data":[...]}`, newest 100 deliveries |
| `POST /v1/notifications/deliveries/{event}/retry` | `{}` | `{"queued":true}` |

Configuration writes require a valid HTTPS URL even when disabled; omitted `enabled` defaults to false. Only port 443/default is permitted; credentials, fragments, and nonpublic IPs are rejected. Delivery revalidates resolved addresses and rejects redirects. Disable new deliveries with `enabled:false`; queued deliveries retain their snapshotted endpoint and encrypted signing secret. Updating config preserves the secret. Rotation requires an existing configuration and affects new deliveries only.

Delivery items expose `event_id`, `status` (`pending`, `failed`, `dead`, `delivered`), `attempts`, `status_code`, `last_error`, `next_at`, and `created_at`. They omit payloads, endpoint snapshots, and secrets. Retry accepts a positive event ID owned by the caller's tenant, only in failed/dead state and without an active lease. It resets attempts and queues the same snapshotted delivery. Missing, foreign, delivered, pending, or leased deliveries return 404.

A project Bearer key may retry its own deliveries. The console retains its existing admin-only retry restriction, including its `/v1` bridge; project console sessions receive 403 for retry. These are authenticated management endpoints, not public proxy endpoints.

## Response codes

Successful reads and writes return 200. Invalid JSON, invalid configuration, or missing/invalid idempotency keys return 400; missing/invalid Bearer credentials return 401. Missing resources, invalid provider paths, or unavailable management return 404. Unsupported methods return 405. Reusing an idempotency key for a different request returns 409. Provider diagnostics can return 502; unexpected database/encryption failures return 500. Error JSON uses `{"error":"..."}`; a rejected provider diagnostic also includes `provider_status`.
