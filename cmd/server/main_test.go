package main

import (
	"paymenthub/internal/hub"
	"testing"
)

func TestAccountValidation(t *testing.T) {
	for _, tc := range []struct {
		env, key string
		seconds  int
		ok       bool
	}{{"test", "sk_test_example", 240, true}, {"test", "sk_test_example", 82800, false}, {"live", "sk_live_example", 82800, true}, {"live", "sk_test_example", 82800, false}, {"", "sk_test_example", 240, false}} {
		err := hub.ValidateAccount("pagarme", hub.Account{Secret: tc.key, Environment: tc.env, RetryWindowSeconds: tc.seconds})
		if (err == nil) != tc.ok {
			t.Fatalf("%+v %v", tc, err)
		}
	}
}
