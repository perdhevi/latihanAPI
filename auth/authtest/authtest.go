// Package authtest is a conformance suite for JWT-based providers.
//
// It runs a fake identity provider (an RSA key published as a JWKS over TLS)
// and checks that the provider under test accepts a valid token and rejects the
// attacks every verifier must reject: expired and not-yet-valid tokens, wrong
// issuer or audience, alg "none", HMAC key confusion, unknown or wrong keys and
// tampered payloads.
//
//	func TestConformance(t *testing.T) {
//		authtest.Run(t, authtest.Suite{
//			New: func(t *testing.T, iss *authtest.Issuer) auth.Authenticator { ... },
//			Claims: func(iss *authtest.Issuer) map[string]any {
//				return map[string]any{"iss": iss.URL, "aud": "my-api", "sub": "user-1"}
//			},
//		})
//	}
package authtest

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"maps"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/perdhevi/latihanAPI/auth"
)

// Issuer is a fake identity provider for tests.
type Issuer struct {
	// URL is the issuer's HTTPS base URL; use it as the "iss" claim.
	URL string
	// JWKSURL serves the issuer's public keys.
	JWKSURL string
	// Client trusts the issuer's test TLS certificate.
	Client *http.Client

	key *rsa.PrivateKey
	kid string
}

// NewIssuer starts a fake identity provider that is shut down when the test ends.
// Its server also answers OpenID Connect discovery.
func NewIssuer(t testing.TB) *Issuer {
	t.Helper()
	key := newKey(t)
	iss := &Issuer{key: key, kid: "test-key-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{rsaJWK(iss.kid, &key.PublicKey)}})
	})
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": iss.URL, "jwks_uri": iss.JWKSURL})
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	iss.URL = srv.URL
	iss.JWKSURL = srv.URL + "/jwks.json"
	iss.Client = srv.Client()
	return iss
}

// Sign returns an RS256 token over claims, signed with the issuer's published key.
// Time claims default to a token issued now that expires in five minutes.
func (i *Issuer) Sign(t testing.TB, claims map[string]any) string {
	t.Helper()
	return sign(t, map[string]any{"alg": "RS256", "typ": "JWT", "kid": i.kid}, withTimes(claims), i.key)
}

func withTimes(claims map[string]any) map[string]any {
	out := map[string]any{"iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix()}
	maps.Copy(out, claims)
	for k, v := range out {
		if v == nil { // nil removes a claim
			delete(out, k)
		}
	}
	return out
}

// Suite describes the provider under test.
type Suite struct {
	// New builds the provider configured to trust iss.
	New func(t *testing.T, iss *Issuer) auth.Authenticator
	// Claims returns claims the provider must accept. Every attack is derived from them.
	Claims func(iss *Issuer) map[string]any
	// AudienceClaim names the claim holding the audience: "aud" when empty.
	// Cognito access tokens, for example, use "client_id".
	AudienceClaim string
}

// Run checks the provider against the conformance cases.
func Run(t *testing.T, s Suite) {
	iss := NewIssuer(t)
	a := s.New(t, iss)
	valid := s.Claims(iss)
	audience := s.AudienceClaim
	if audience == "" {
		audience = "aud"
	}
	with := func(changes map[string]any) map[string]any {
		out := maps.Clone(valid)
		maps.Copy(out, changes)
		return out
	}
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": iss.kid}
	now := time.Now()

	t.Run("valid token", func(t *testing.T) {
		id, err := a.Authenticate(bearer(t, iss.Sign(t, valid)))
		if err != nil {
			t.Fatalf("valid token rejected: %v", err)
		}
		if id.Issuer != valid["iss"] || id.Subject != valid["sub"] {
			t.Fatalf("identity %+v does not match claims iss=%v sub=%v", id, valid["iss"], valid["sub"])
		}
	})

	otherKey := newKey(t)
	pemKey := publicKeyPEM(t, &iss.key.PublicKey)
	rejected := map[string]func(t *testing.T) *http.Request{
		"no credentials": func(t *testing.T) *http.Request { return request(t, "") },
		"basic scheme":   func(t *testing.T) *http.Request { return request(t, "Basic dXNlcjpwYXNz") },
		"malformed":      func(t *testing.T) *http.Request { return bearer(t, "not-a-jwt") },
		"expired": func(t *testing.T) *http.Request {
			return bearer(t, iss.Sign(t, with(map[string]any{"iat": now.Add(-2 * time.Hour).Unix(), "exp": now.Add(-time.Hour).Unix()})))
		},
		"not yet valid": func(t *testing.T) *http.Request {
			return bearer(t, iss.Sign(t, with(map[string]any{"nbf": now.Add(time.Hour).Unix()})))
		},
		"issued in the future": func(t *testing.T) *http.Request {
			return bearer(t, iss.Sign(t, with(map[string]any{"iat": now.Add(time.Hour).Unix(), "exp": now.Add(2 * time.Hour).Unix()})))
		},
		"missing expiry": func(t *testing.T) *http.Request {
			return bearer(t, iss.Sign(t, with(map[string]any{"exp": nil})))
		},
		"wrong issuer": func(t *testing.T) *http.Request {
			return bearer(t, iss.Sign(t, with(map[string]any{"iss": "https://attacker.example"})))
		},
		"wrong audience": func(t *testing.T) *http.Request {
			return bearer(t, iss.Sign(t, with(map[string]any{audience: "someone-else"})))
		},
		"missing audience": func(t *testing.T) *http.Request {
			return bearer(t, iss.Sign(t, with(map[string]any{audience: nil})))
		},
		"missing subject": func(t *testing.T) *http.Request {
			return bearer(t, iss.Sign(t, with(map[string]any{"sub": nil})))
		},
		"alg none": func(t *testing.T) *http.Request {
			return bearer(t, sign(t, map[string]any{"alg": "none", "typ": "JWT", "kid": iss.kid}, withTimes(valid), nil))
		},
		"HS256 signed with the public key": func(t *testing.T) *http.Request {
			return bearer(t, sign(t, map[string]any{"alg": "HS256", "typ": "JWT", "kid": iss.kid}, withTimes(valid), pemKey))
		},
		"signed by another key with a trusted kid": func(t *testing.T) *http.Request {
			return bearer(t, sign(t, header, withTimes(valid), otherKey))
		},
		"unknown kid": func(t *testing.T) *http.Request {
			return bearer(t, sign(t, map[string]any{"alg": "RS256", "typ": "JWT", "kid": "unknown"}, withTimes(valid), otherKey))
		},
		"tampered payload": func(t *testing.T) *http.Request {
			token := iss.Sign(t, valid)
			forged := sign(t, header, withTimes(with(map[string]any{"sub": "attacker"})), otherKey)
			return bearer(t, part(token, 0)+"."+part(forged, 1)+"."+part(token, 2))
		},
	}
	for name, build := range rejected {
		t.Run(name, func(t *testing.T) {
			_, err := a.Authenticate(build(t))
			if !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("want auth.ErrUnauthenticated, got %v", err)
			}
		})
	}
}

func request(t *testing.T, authorization string) *http.Request {
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	return r
}

func bearer(t *testing.T, token string) *http.Request { return request(t, "Bearer "+token) }

func part(token string, i int) string { return strings.Split(token, ".")[i] }

// sign encodes a JWS. key is an *rsa.PrivateKey (RS256), a []byte (HS256) or nil (alg none).
func sign(t testing.TB, header, claims map[string]any, key any) string {
	t.Helper()
	h, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	c, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	input := b64(h) + "." + b64(c)
	var sig []byte
	switch k := key.(type) {
	case *rsa.PrivateKey:
		digest := sha256.Sum256([]byte(input))
		if sig, err = rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, digest[:]); err != nil {
			t.Fatal(err)
		}
	case []byte:
		mac := hmac.New(sha256.New, k)
		mac.Write([]byte(input))
		sig = mac.Sum(nil)
	case nil:
	default:
		t.Fatalf("unsupported key %T", key)
	}
	return input + "." + b64(sig)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func newKey(t testing.TB) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func rsaJWK(kid string, pub *rsa.PublicKey) map[string]string {
	return map[string]string{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid,
		"n": b64(pub.N.Bytes()), "e": b64(big.NewInt(int64(pub.E)).Bytes()),
	}
}

func publicKeyPEM(t testing.TB, pub *rsa.PublicKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}
