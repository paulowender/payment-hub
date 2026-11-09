package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func metadataObject(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func requireMetadata(t *testing.T, got map[string]any, want string) {
	t.Helper()
	b, err := json.Marshal(got["metadata"])
	if err != nil || string(b) != want {
		t.Fatalf("metadata = %s, want %s", b, want)
	}
}

func TestMetadataResourceContracts(t *testing.T) {
	s := testStore(t)
	tenant, key, customer := newTenant(t, s)
	_, other, _ := newTenant(t, s)
	h := (&API{Store: s, Accounts: Accounts{tenant: {"stripe": {Secret: "sk_test"}, "pagarme": {Secret: "sk_test", PublicKey: "pk_test"}}}}).Handler()
	legacy := request(t, h, "GET", "/v1/customers/"+customer, key, "", "")
	requireMetadata(t, metadataObject(t, legacy.Body.Bytes()), `{}`)
	plan := ""
	for _, resource := range []struct{ path, body string }{
		{"customers", `{"external_id":"metadata-customer"}`},
		{"plans", `{"name":"Pro","amount":100,"currency":"brl","credits":5,"interval_unit":"month","interval_count":1}`},
		{"offers", `{"name":"Offer","amount":100,"currency":"brl","credits":5}`},
		{"subscriptions", `{"customer_id":%q,"plan_id":%q,"provider":"stripe","provider_customer":"cus","payment_method":"pm"}`},
		{"charges", `{"customer_id":%q,"provider":"stripe","provider_customer":"cus","payment_method":"pm","amount":100,"currency":"brl"}`},
	} {
		body := resource.body
		if resource.path == "subscriptions" {
			body = fmt.Sprintf(body, customer, plan)
		}
		if resource.path == "charges" {
			body = fmt.Sprintf(body, customer)
		}
		t.Run(resource.path, func(t *testing.T) {
			for _, withMetadata := range []bool{false, true} {
				payload := body
				want := `{}`
				if resource.path == "customers" && withMetadata {
					payload = `{"external_id":"metadata-customer-explicit"}`
				}
				if withMetadata {
					payload = strings.TrimSuffix(payload, "}") + `,"metadata":{"nested":{"key":[1,true,null]},"source":"prontei"}}`
					want = `{"nested":{"key":[1,true,null]},"source":"prontei"}`
				}
				out := request(t, h, "POST", "/v1/"+resource.path, key, fmt.Sprintf("%s-%t", resource.path, withMetadata), payload)
				if out.Code != http.StatusOK {
					t.Fatalf("create: %d %s", out.Code, out.Body)
				}
				got := metadataObject(t, out.Body.Bytes())
				requireMetadata(t, got, want)
				id := got["id"].(string)
				if resource.path == "plans" {
					plan = id
				}
				replay := request(t, h, "POST", "/v1/"+resource.path, key, fmt.Sprintf("%s-%t", resource.path, withMetadata), payload)
				if replay.Code != 200 || replay.Body.String() != out.Body.String() {
					t.Fatal("idempotent replay changed", replay.Body)
				}
				read := request(t, h, "GET", "/v1/"+resource.path+"/"+id, key, "", "")
				if read.Code != 200 {
					t.Fatal(read.Code, read.Body)
				}
				requireMetadata(t, metadataObject(t, read.Body.Bytes()), want)
				if leak := request(t, h, "GET", "/v1/"+resource.path+"/"+id, other, "", ""); leak.Code != 404 {
					t.Fatal("tenant leak", leak.Code, leak.Body)
				}
			}
			list := request(t, h, "GET", "/v1/"+resource.path, key, "", "")
			if list.Code != 200 {
				t.Fatal(list.Body)
			}
			for _, item := range metadataObject(t, list.Body.Bytes())["data"].([]any) {
				if _, ok := item.(map[string]any)["metadata"].(map[string]any); !ok {
					t.Fatal("missing metadata", item)
				}
			}
			list = request(t, h, "GET", "/v1/"+resource.path, other, "", "")
			for _, item := range metadataObject(t, list.Body.Bytes())["data"].([]any) {
				requireMetadata(t, item.(map[string]any), `{}`)
			}
		})
	}
}

func TestMetadataHTTPValidation(t *testing.T) {
	s := testStore(t)
	tenant, key, customer := newTenant(t, s)
	h := (&API{Store: s, Accounts: Accounts{tenant: {"stripe": {Secret: "sk_test"}, "pagarme": {Secret: "sk_test", PublicKey: "pk_test"}}}}).Handler()
	plan := request(t, h, "POST", "/v1/plans", key, "setup-plan", `{"name":"Pro","amount":100,"currency":"brl","interval_unit":"month","interval_count":1}`)
	planID := metadataObject(t, plan.Body.Bytes())["id"].(string)
	offer := request(t, h, "POST", "/v1/offers", key, "setup-offer", `{"name":"Offer","amount":100,"currency":"brl"}`)
	offerID := metadataObject(t, offer.Body.Bytes())["id"].(string)
	for _, resource := range []struct{ path, body string }{
		{"customers", `{"external_id":"validation"}`},
		{"plans", `{"name":"Pro","amount":100,"currency":"brl","interval_unit":"month","interval_count":1}`},
		{"offers", `{"name":"Offer","amount":100,"currency":"brl"}`},
		{"checkout-sessions", fmt.Sprintf(`{"offer_id":%q,"customer_id":%q,"reference":"validation"}`, offerID, customer)},
		{"subscriptions", fmt.Sprintf(`{"customer_id":%q,"plan_id":%q,"provider":"stripe","provider_customer":"cus","payment_method":"pm"}`, customer, planID)},
		{"charges", fmt.Sprintf(`{"customer_id":%q,"provider":"stripe","provider_customer":"cus","payment_method":"pm","amount":100,"currency":"brl"}`, customer)},
	} {
		t.Run(resource.path, func(t *testing.T) {
			for i, raw := range []string{`null`, `[]`, `"text"`, `1`, `true`, `{"x":"` + strings.Repeat("x", 8185) + `"}`} {
				out := request(t, h, "POST", "/v1/"+resource.path, key, fmt.Sprintf("bad-%s-%d", resource.path, i), strings.TrimSuffix(resource.body, "}")+`,"metadata":`+raw+`}`)
				if out.Code != 400 {
					t.Fatalf("invalid metadata accepted: %d %s", out.Code, out.Body)
				}
			}
			raw := `{"x":"` + strings.Repeat("x", 8184) + `"}`
			out := request(t, h, "POST", "/v1/"+resource.path, key, "boundary-"+resource.path, strings.TrimSuffix(resource.body, "}")+`,"metadata":`+raw+`}`)
			if out.Code != 200 {
				t.Fatalf("8192-byte metadata rejected: %d %s", out.Code, out.Body)
			}
			requireMetadata(t, metadataObject(t, out.Body.Bytes()), raw)
		})
	}
}

func TestMetadataMigrationLegacyRows(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// Restore a version-9 shape to exercise backfilling rather than new-row defaults.
	if _, err := s.DB.Exec(ctx, `DROP VIEW api_subscriptions; DROP VIEW api_charge_details; ALTER TABLE customers DROP COLUMN IF EXISTS metadata; ALTER TABLE plans DROP COLUMN IF EXISTS metadata; ALTER TABLE offers DROP COLUMN IF EXISTS metadata; ALTER TABLE checkout_sessions DROP COLUMN IF EXISTS metadata; ALTER TABLE subscriptions DROP COLUMN IF EXISTS metadata; ALTER TABLE charges DROP COLUMN IF EXISTS metadata; DELETE FROM schema_version WHERE version=10`); err != nil {
		t.Fatal(err)
	}
	// Keep the version-9 views present while the migration adds new columns.
	if _, err := s.DB.Exec(ctx, `CREATE VIEW api_subscriptions AS SELECT s.*, NULL::jsonb AS pending_change FROM subscriptions s;
 CREATE VIEW api_charge_details AS SELECT c.tenant_id,c.id,c.customer_id,c.provider,c.provider_id,c.amount,c.currency,c.credits,c.subscription_id,c.cycle,c.status,c.client_secret,c.last_error,c.created_at,c.updated_at,c.method,c.installments,c.payment_instructions,c.attempts,c.first_attempt,c.next_at,c.kind,c.billing_credit_used FROM charges c;`); err != nil {
		t.Fatal(err)
	}

	tenant, key, customer := newTenant(t, s)
	plan, offer, sub, charge, session := token(), token(), token(), token(), token()
	for _, query := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO plans(tenant_id,id,name,amount,currency,credits,interval_unit,interval_count) VALUES($1,$2,'legacy',100,'brl',0,'month',1)`, []any{tenant, plan}},
		{`INSERT INTO offers(tenant_id,id,name,description,amount,currency,credits,plan_id) VALUES($1,$2,'legacy','',100,'brl',0,$3)`, []any{tenant, offer, plan}},
		{`INSERT INTO subscriptions(tenant_id,id,customer_id,plan_id,provider,provider_customer,payment_method,anchor,next_at) VALUES($1,$2,$3,$4,'stripe','cus','pm',now(),now())`, []any{tenant, sub, customer, plan}},
		{`INSERT INTO charges(tenant_id,id,customer_id,provider,provider_customer,payment_method,amount,currency,credits) VALUES($1,$2,$3,'stripe','cus','pm',100,'brl',0)`, []any{tenant, charge, customer}},
		{`INSERT INTO checkout_sessions(tenant_id,id,offer_id,customer_id,reference,snapshot,account_fingerprint) VALUES($1,$2,$3,$4,'legacy','{}','')`, []any{tenant, session, offer, customer}},
	} {
		if _, err := s.DB.Exec(ctx, query.sql, query.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal("repeat migration", err)
	}
	h := (&API{Store: s}).Handler()
	for resource, id := range map[string]string{"customers": customer, "plans": plan, "offers": offer, "checkout-sessions": session, "subscriptions": sub, "charges": charge} {
		out := request(t, h, "GET", "/v1/"+resource+"/"+id, key, "", "")
		if out.Code != 200 {
			t.Fatal(resource, out.Code, out.Body)
		}
		requireMetadata(t, metadataObject(t, out.Body.Bytes()), `{}`)
	}
	var version int
	if err := s.DB.QueryRow(ctx, "SELECT version FROM schema_version WHERE version=10").Scan(&version); err != nil {
		t.Fatal(err)
	}
}

func TestMetadataDecode(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"omitted", "", true}, {"empty", `{}`, true}, {"nested", `{"a":[null,true,{"b":2}]}`, true},
		{"large integer", `{"n":9007199254740993}`, true},
		{"null", `null`, false}, {"array", `[]`, false}, {"string", `"x"`, false}, {"number", `1`, false}, {"boolean", `false`, false},
		{"malformed", `{"x":`, false}, {"trailing", `{} {}`, false},
		{"8192 bytes", `{"x":"` + strings.Repeat("x", 8184) + `"}`, true},
		{"8193 bytes", `{"x":"` + strings.Repeat("x", 8185) + `"}`, false},
		{"multibyte bytes", `{"x":"` + strings.Repeat("é", 4093) + `"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeMetadata(json.RawMessage(tc.raw))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t error=%v", tc.valid, err)
			}
			if !tc.valid {
				return
			}
			if got == nil {
				t.Fatal("nil metadata")
			}
			if tc.name == "large integer" {
				raw, e := json.Marshal(got)
				if e != nil || string(raw) != tc.raw {
					t.Fatal("number changed", string(raw), e)
				}
			}
		})
	}
}
