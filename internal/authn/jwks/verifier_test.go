package jwks

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/perdhevi/latihanAPI/auth"
	"github.com/perdhevi/latihanAPI/auth/authtest"
)

func TestConformance(t *testing.T) {
	authtest.Run(t, authtest.Suite{
		New: func(t *testing.T, iss *authtest.Issuer) auth.Authenticator {
			v, err := New(t.Context(), Config{Issuer: iss.URL, Audience: "latihan-api", JWKSURL: iss.JWKSURL, Algorithms: []string{"RS256"}, HTTPClient: iss.Client})
			if err != nil {
				t.Fatal(err)
			}
			return v
		},
		Claims: func(iss *authtest.Issuer) map[string]any {
			return map[string]any{"iss": iss.URL, "aud": "latihan-api", "sub": "user-1"}
		},
	})
}

func TestIdentityClaims(t *testing.T) {
	iss := authtest.NewIssuer(t)
	v, err := New(t.Context(), Config{Issuer: iss.URL, Audience: "api", JWKSURL: iss.JWKSURL, Algorithms: []string{"RS256"}, HTTPClient: iss.Client})
	if err != nil {
		t.Fatal(err)
	}
	token := iss.Sign(t, map[string]any{"iss": iss.URL, "aud": []string{"other", "api"}, "sub": "u1", "email": "a@example.com", "email_verified": true})
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	id, err := v.Authenticate(r)
	if err != nil {
		t.Fatal(err)
	}
	if id != (auth.Identity{Issuer: iss.URL, Subject: "u1", Email: "a@example.com", EmailVerified: true}) {
		t.Fatalf("identity: %+v", id)
	}
	r.Header.Set("Authorization", "Bearer "+string(make([]byte, maxTokenBytes+1)))
	if _, err := v.Authenticate(r); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("oversized token accepted")
	}
}

func TestConfigValidation(t *testing.T) {
	iss := authtest.NewIssuer(t)
	valid := Config{Issuer: iss.URL, Audience: "api", JWKSURL: iss.JWKSURL, Algorithms: []string{"RS256"}, HTTPClient: iss.Client}
	for name, change := range map[string]func(*Config){
		"no issuer":        func(c *Config) { c.Issuer = "" },
		"no audience":      func(c *Config) { c.Audience = "" },
		"plain HTTP keys":  func(c *Config) { c.JWKSURL = "http://example.com/jwks.json" },
		"no algorithms":    func(c *Config) { c.Algorithms = nil },
		"symmetric alg":    func(c *Config) { c.Algorithms = []string{"HS256"} },
		"alg none":         func(c *Config) { c.Algorithms = []string{"none"} },
		"unreachable keys": func(c *Config) { c.JWKSURL = iss.URL + "/missing" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := valid
			change(&cfg)
			if _, err := New(t.Context(), cfg); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestKeySetRefreshPolicy(t *testing.T) {
	var fetches atomic.Int32
	var fail atomic.Bool
	edPub, _, _ := ed25519.GenerateKey(rand.Reader)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{
			{"kty": "OKP", "crv": "Ed25519", "kid": "ed", "x": base64.RawURLEncoding.EncodeToString(edPub)},
			{"kty": "RSA", "kid": "weak", "n": "AQAB", "e": "AQAB"},                // too short: skipped
			{"kty": "OKP", "crv": "Ed25519", "kid": "enc", "use": "enc", "x": "x"}, // not for signatures: skipped
		}})
	}))
	defer srv.Close()
	now := time.Now()
	s := &keySet{url: srv.URL, client: srv.Client(), logger: discardLogger(), now: func() time.Time { return now }}
	if err := s.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.key(t.Context(), "ed"); !ok {
		t.Fatal("Ed25519 key missing")
	}
	for _, kid := range []string{"weak", "enc", "unknown", "unknown-2"} {
		if _, ok, _ := s.key(t.Context(), kid); ok {
			t.Fatalf("key %q should be unusable", kid)
		}
	}
	if got := fetches.Load(); got != 1 {
		t.Fatalf("unknown kids within a minute must not refetch: %d fetches", got)
	}
	now = now.Add(minRefetchInterval)
	_, _, _ = s.key(t.Context(), "unknown")
	if got := fetches.Load(); got != 2 {
		t.Fatalf("unknown kid after a minute should refetch: %d fetches", got)
	}
	fail.Store(true)
	now = now.Add(refreshAfter)
	if _, ok, err := s.key(t.Context(), "ed"); !ok || err != nil {
		t.Fatalf("known keys must survive a key-server outage: %v %v", ok, err)
	}
	empty := &keySet{url: srv.URL, client: srv.Client(), logger: discardLogger(), now: time.Now}
	if _, _, err := empty.key(t.Context(), "ed"); !errors.Is(err, errKeysUnavailable) {
		t.Fatalf("never-loaded key set should be unavailable: %v", err)
	}
}

func TestECKeys(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := priv.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	good := jwk{Kty: "EC", Crv: "P-256", X: enc(raw[1:33]), Y: enc(raw[33:])}
	if _, err := good.publicKey(); err != nil {
		t.Fatal(err)
	}
	offCurve := good
	offCurve.Y = enc(make([]byte, 32))
	if _, err := offCurve.publicKey(); err == nil {
		t.Fatal("point off the curve accepted")
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
