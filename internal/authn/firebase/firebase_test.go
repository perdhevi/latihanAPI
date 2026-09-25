package firebase

import (
	"testing"

	"github.com/perdhevi/latihanAPI/auth"
	"github.com/perdhevi/latihanAPI/auth/authtest"
	"github.com/perdhevi/latihanAPI/internal/authn/jwks"
)

func TestConfig(t *testing.T) {
	cfg := Config("my-latihan-app")
	if cfg.Issuer != "https://securetoken.google.com/my-latihan-app" || cfg.Audience != "my-latihan-app" ||
		cfg.JWKSURL != jwksURL || len(cfg.Algorithms) != 1 || cfg.Algorithms[0] != "RS256" {
		t.Fatalf("config: %+v", cfg)
	}
}

func TestProjectIDValidation(t *testing.T) {
	for _, id := range []string{"", "ab", "My-Project", "project/../x", "-project", "project-"} {
		_, err := New(t.Context(), auth.Deps{Getenv: func(string) string { return id }})
		if err == nil {
			t.Errorf("%q accepted", id)
		}
	}
}

// The preset's rules, pointed at a fake issuer, must pass the conformance suite.
func TestConformance(t *testing.T) {
	authtest.Run(t, authtest.Suite{
		New: func(t *testing.T, iss *authtest.Issuer) auth.Authenticator {
			cfg := Config("my-latihan-app")
			cfg.Issuer, cfg.JWKSURL, cfg.HTTPClient = iss.URL, iss.JWKSURL, iss.Client
			v, err := jwks.New(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			return v
		},
		Claims: func(iss *authtest.Issuer) map[string]any {
			return map[string]any{"iss": iss.URL, "aud": "my-latihan-app", "sub": "firebase-uid"}
		},
	})
}
