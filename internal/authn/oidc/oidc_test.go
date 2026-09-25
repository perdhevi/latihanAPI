package oidc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/perdhevi/latihanAPI/auth"
	"github.com/perdhevi/latihanAPI/auth/authtest"
)

func deps(iss *authtest.Issuer, env map[string]string) auth.Deps {
	return auth.Deps{Getenv: func(k string) string { return env[k] }, HTTPClient: iss.Client}
}

// Keys are found through OpenID Connect discovery when no JWKS URL is set.
func TestDiscoveryConformance(t *testing.T) {
	authtest.Run(t, authtest.Suite{
		New: func(t *testing.T, iss *authtest.Issuer) auth.Authenticator {
			a, err := New(t.Context(), deps(iss, map[string]string{"AUTH_OIDC_ISSUER": iss.URL, "AUTH_OIDC_AUDIENCE": "latihan-api"}))
			if err != nil {
				t.Fatal(err)
			}
			return a
		},
		Claims: func(iss *authtest.Issuer) map[string]any {
			return map[string]any{"iss": iss.URL, "aud": "latihan-api", "sub": "oidc-user"}
		},
	})
}

func TestSettingsValidation(t *testing.T) {
	iss := authtest.NewIssuer(t)
	spoof := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": "https://someone-else.example", "jwks_uri": iss.JWKSURL})
	}))
	defer spoof.Close()
	for name, env := range map[string]map[string]string{
		"missing issuer":   {"AUTH_OIDC_AUDIENCE": "api"},
		"missing audience": {"AUTH_OIDC_ISSUER": iss.URL},
		"http issuer":      {"AUTH_OIDC_ISSUER": "http://id.example.com", "AUTH_OIDC_AUDIENCE": "api"},
		"issuer mismatch":  {"AUTH_OIDC_ISSUER": spoof.URL, "AUTH_OIDC_AUDIENCE": "api"},
		"symmetric alg":    {"AUTH_OIDC_ISSUER": iss.URL, "AUTH_OIDC_AUDIENCE": "api", "AUTH_OIDC_ALGORITHMS": "RS256,HS256"},
	} {
		t.Run(name, func(t *testing.T) {
			d := deps(iss, env)
			d.HTTPClient = spoof.Client() // trusts both test servers' shared certificate
			if _, err := New(t.Context(), d); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
