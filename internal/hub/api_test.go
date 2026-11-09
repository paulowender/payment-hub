package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRejectUnauthenticated(t *testing.T) {
	a := &API{AdminSecret: strings.Repeat("a", 32)}
	for _, path := range []string{"/v1/customers", "/admin/tenants"} {
		r := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %d", path, w.Code)
		}
	}
}
func TestStrictJSON(t *testing.T) {
	for _, body := range []string{`{"name":"x","unknown":true}`, `{"name":"x"} {}`, `{"name":`} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		var v struct {
			Name string `json:"name"`
		}
		if decode(r, &v) == nil {
			t.Fatal(body)
		}
	}
}
