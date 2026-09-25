package cognito

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/perdhevi/latihanAPI/auth"
	"github.com/perdhevi/latihanAPI/auth/authtest"
	"github.com/perdhevi/latihanAPI/internal/authn/jwks"
)

func TestConfig(t *testing.T) {
	access := Config("ap-southeast-1", "ap-southeast-1_AbC123", "client1", "access")
	if access.Issuer != "https://cognito-idp.ap-southeast-1.amazonaws.com/ap-southeast-1_AbC123" ||
		access.JWKSURL != access.Issuer+"/.well-known/jwks.json" ||
		access.AudienceClaim != "client_id" || access.RequiredClaims["token_use"] != "access" {
		t.Fatalf("access config: %+v", access)
	}
	id := Config("ap-southeast-1", "ap-southeast-1_AbC123", "client1", "id")
	if id.AudienceClaim != "aud" || id.RequiredClaims["token_use"] != "id" {
		t.Fatalf("id config: %+v", id)
	}
}

func TestSettingsValidation(t *testing.T) {
	valid := map[string]string{
		"AUTH_COGNITO_REGION":       "ap-southeast-1",
		"AUTH_COGNITO_USER_POOL_ID": "ap-southeast-1_AbC123",
		"AUTH_COGNITO_CLIENT_ID":    "client1",
		"AUTH_COGNITO_TOKEN_USE":    "access",
	}
	for name, change := range map[string][2]string{
		"bad region":          {"AUTH_COGNITO_REGION", "southeast"},
		"pool in other place": {"AUTH_COGNITO_USER_POOL_ID", "us-east-1_AbC123"},
		"missing client":      {"AUTH_COGNITO_CLIENT_ID", ""},
		"bad token use":       {"AUTH_COGNITO_TOKEN_USE", "refresh"},
	} {
		t.Run(name, func(t *testing.T) {
			env := map[string]string{}
			for k, v := range valid {
				env[k] = v
			}
			env[change[0]] = change[1]
			if _, err := New(t.Context(), auth.Deps{Getenv: func(k string) string { return env[k] }}); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func verifier(t *testing.T, iss *authtest.Issuer, tokenUse string) auth.Authenticator {
	cfg := Config("ap-southeast-1", "ap-southeast-1_AbC123", "client1", tokenUse)
	cfg.Issuer, cfg.JWKSURL, cfg.HTTPClient = iss.URL, iss.JWKSURL, iss.Client
	v, err := jwks.New(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestAccessTokenConformance(t *testing.T) {
	authtest.Run(t, authtest.Suite{
		New: func(t *testing.T, iss *authtest.Issuer) auth.Authenticator { return verifier(t, iss, "access") },
		Claims: func(iss *authtest.Issuer) map[string]any {
			return map[string]any{"iss": iss.URL, "client_id": "client1", "token_use": "access", "sub": "cognito-sub"}
		},
		AudienceClaim: "client_id",
	})
}

func TestIDTokenConformance(t *testing.T) {
	authtest.Run(t, authtest.Suite{
		New: func(t *testing.T, iss *authtest.Issuer) auth.Authenticator { return verifier(t, iss, "id") },
		Claims: func(iss *authtest.Issuer) map[string]any {
			return map[string]any{"iss": iss.URL, "aud": "client1", "token_use": "id", "sub": "cognito-sub"}
		},
	})
}

// An ID token must not be accepted where access tokens are configured, even
// when it names the right client.
func TestTokenUseIsEnforced(t *testing.T) {
	iss := authtest.NewIssuer(t)
	v := verifier(t, iss, "access")
	token := iss.Sign(t, map[string]any{"iss": iss.URL, "client_id": "client1", "aud": "client1", "token_use": "id", "sub": "s"})
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	if _, err := v.Authenticate(r); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("ID token accepted as access token: %v", err)
	}
}
