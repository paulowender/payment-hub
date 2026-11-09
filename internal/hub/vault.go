package hub

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

type AccountSource interface {
	Get(context.Context, string, string) (Account, error)
}

func (a Accounts) Get(_ context.Context, tenant, provider string) (Account, error) {
	ac, ok := a[tenant][provider]
	if !ok || ac.Secret == "" {
		return Account{}, errNotFound
	}
	return ac, nil
}

type Vault struct {
	Store  *Store
	cipher cipher.AEAD
}

func NewVault(s *Store, key string) (*Vault, error) {
	b, e := hex.DecodeString(key)
	if e != nil || len(b) != 32 {
		return nil, fmt.Errorf("ENCRYPTION_KEY must be 64 hexadecimal characters")
	}
	block, e := aes.NewCipher(b)
	if e != nil {
		return nil, e
	}
	aead, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	return &Vault{Store: s, cipher: aead}, nil
}
func (v *Vault) seal(binding string, data []byte) ([]byte, error) {
	nonce := make([]byte, v.cipher.NonceSize())
	if _, e := rand.Read(nonce); e != nil {
		return nil, e
	}
	return v.cipher.Seal(nonce, nonce, data, []byte(binding)), nil
}
func (v *Vault) open(binding string, data []byte) ([]byte, error) {
	n := v.cipher.NonceSize()
	if len(data) < n {
		return nil, fmt.Errorf("invalid encrypted credentials")
	}
	return v.cipher.Open(nil, data[:n], data[n:], []byte(binding))
}

type credentialReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (v *Vault) Get(ctx context.Context, tenant, provider string) (Account, error) {
	return v.load(ctx, v.Store.DB, tenant, provider)
}
func (v *Vault) load(ctx context.Context, q credentialReader, tenant, provider string) (Account, error) {
	var b []byte
	if e := q.QueryRow(ctx, "SELECT encrypted_config FROM provider_connections WHERE tenant_id=$1 AND provider=$2", tenant, provider).Scan(&b); e != nil {
		return Account{}, e
	}
	plain, e := v.open(tenant+":"+provider, b)
	if e != nil {
		return Account{}, fmt.Errorf("credential_decryption_failed")
	}
	var ac Account
	e = json.Unmarshal(plain, &ac)
	return ac, e
}

func ValidateAccount(provider string, ac Account) error {
	if (provider != "stripe" && provider != "pagarme") || len(ac.Secret) < 10 || len(ac.Secret) > 512 || strings.TrimSpace(ac.Secret) != ac.Secret || strings.ContainsAny(ac.Secret, "\r\n") {
		return errInvalid
	}
	if ac.PublicKey != "" && (len(ac.PublicKey) > 200 || !strings.HasPrefix(ac.PublicKey, "pk_") || strings.ContainsAny(ac.PublicKey, " \r\n")) {
		return errInvalid
	}
	if ac.PublicKey != "" && ((ac.Environment == "test" && !strings.HasPrefix(ac.PublicKey, "pk_test_")) || (ac.Environment == "live" && strings.HasPrefix(ac.PublicKey, "pk_test_"))) {
		return errInvalid
	}
	if ac.Environment != "test" && ac.Environment != "live" {
		return errInvalid
	}
	if ac.Environment == "live" && strings.Contains(ac.Secret, "_test_") {
		return errInvalid
	}
	if ac.Environment == "test" && strings.Contains(ac.Secret, "_live_") {
		return errInvalid
	}
	if ac.RetryWindowSeconds < 0 || ac.RetryWindowSeconds > 82800 || (ac.RetryWindowSeconds > 0 && ac.RetryWindowSeconds < 30) {
		return errInvalid
	}
	if provider == "pagarme" && ac.Environment == "test" && ac.Window() > 240*time.Second {
		return errInvalid
	}
	if (ac.WebhookUser == "") != (ac.WebhookPassword == "") || len(ac.WebhookUser) > 200 || len(ac.WebhookPassword) > 512 {
		return errInvalid
	}
	return nil
}
func (v *Vault) Save(ctx context.Context, tenant, provider string, ac Account) error {
	if e := ValidateAccount(provider, ac); e != nil {
		return e
	}
	plain, e := json.Marshal(ac)
	if e != nil {
		return e
	}
	b, e := v.seal(tenant+":"+provider, plain)
	if e != nil {
		return e
	}
	_, e = v.Store.DB.Exec(ctx, "INSERT INTO provider_connections(tenant_id,provider,encrypted_config) VALUES($1,$2,$3) ON CONFLICT(tenant_id,provider) DO UPDATE SET encrypted_config=excluded.encrypted_config,updated_at=now()", tenant, provider, b)
	return e
}
