package hub

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestCredentialEncryption(t *testing.T) {
	v, e := NewVault(nil, strings.Repeat("ab", 32))
	if e != nil {
		t.Fatal(e)
	}
	raw := []byte(`{"secret":"sk_test_example"}`)
	cipher, e := v.seal("tenant:stripe", raw)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(cipher, []byte("sk_test")) {
		t.Fatal("plaintext")
	}
	got, e := v.open("tenant:stripe", cipher)
	if e != nil || !bytes.Equal(got, raw) {
		t.Fatal(e)
	}
	if _, e = v.open("different:stripe", cipher); e == nil {
		t.Fatal("cross tenant decrypt")
	}
	cipher[len(cipher)-1] ^= 1
	if _, e = v.open("tenant:stripe", cipher); e == nil {
		t.Fatal("tamper accepted")
	}
}
func TestPostgresVaultPersistsAndIsolates(t *testing.T) {
	s := testStore(t)
	one, _, _ := newTenant(t, s)
	two, _, _ := newTenant(t, s)
	v, e := NewVault(s, strings.Repeat("ab", 32))
	if e != nil {
		t.Fatal(e)
	}
	ac := Account{Secret: "sk_test_example", Environment: "test", RetryWindowSeconds: 240}
	if e = v.Save(context.Background(), one, "pagarme", ac); e != nil {
		t.Fatal(e)
	}
	fresh, _ := NewVault(s, strings.Repeat("ab", 32))
	got, e := fresh.Get(context.Background(), one, "pagarme")
	if e != nil || got.Secret != ac.Secret {
		t.Fatal(e)
	}
	if _, e = fresh.Get(context.Background(), two, "pagarme"); e == nil {
		t.Fatal("cross tenant credentials")
	}
}
