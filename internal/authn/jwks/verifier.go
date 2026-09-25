// Package jwks verifies JWTs signed with keys published as a JSON Web Key Set.
// It is the shared engine behind the firebase, cognito and oidc providers.
package jwks

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/perdhevi/latihanAPI/auth"
)

const (
	// Clock leeway tolerated on exp, nbf and iat.
	leeway = 30 * time.Second
	// Tokens larger than this are rejected before any parsing work.
	maxTokenBytes = 8 << 10
)

// Config describes which tokens to trust.
type Config struct {
	// Issuer must equal the token's "iss" claim exactly.
	Issuer string
	// Audience must appear in the claim named by AudienceClaim.
	Audience string
	// AudienceClaim is "aud" (the default) or another claim holding the audience
	// as a single string, such as Cognito's "client_id".
	AudienceClaim string
	// JWKSURL publishes the issuer's signing keys. It must use HTTPS.
	JWKSURL string
	// Algorithms lists the accepted "alg" values. The token's header never widens it.
	Algorithms []string
	// RequiredClaims must be present in the token with exactly these string values.
	RequiredClaims map[string]string
	HTTPClient     *http.Client
	Logger         *slog.Logger
}

// Verifier implements auth.Authenticator.
type Verifier struct {
	cfg    Config
	keys   *keySet
	parser *jwt.Parser
}

var supportedAlgorithms = map[string]bool{
	"RS256": true, "RS384": true, "RS512": true,
	"PS256": true, "PS384": true, "PS512": true,
	"ES256": true, "ES384": true, "ES512": true,
	"EdDSA": true,
}

// New validates cfg and loads the key set, failing fast on misconfiguration.
func New(ctx context.Context, cfg Config) (*Verifier, error) {
	if cfg.Issuer == "" || cfg.Audience == "" {
		return nil, errors.New("issuer and audience are required")
	}
	if err := requireHTTPS(cfg.JWKSURL); err != nil {
		return nil, fmt.Errorf("JWKS URL: %w", err)
	}
	if len(cfg.Algorithms) == 0 {
		return nil, errors.New("at least one algorithm is required")
	}
	for _, alg := range cfg.Algorithms {
		// Symmetric algorithms and "none" can never be verified with public keys.
		if !supportedAlgorithms[alg] {
			return nil, fmt.Errorf("unsupported algorithm %q", alg)
		}
	}
	if cfg.AudienceClaim == "" {
		cfg.AudienceClaim = "aud"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	opts := []jwt.ParserOption{
		jwt.WithValidMethods(cfg.Algorithms),
		jwt.WithIssuer(cfg.Issuer),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(leeway),
	}
	if cfg.AudienceClaim == "aud" {
		opts = append(opts, jwt.WithAudience(cfg.Audience))
	}
	v := &Verifier{
		cfg:    cfg,
		keys:   &keySet{url: cfg.JWKSURL, client: cfg.HTTPClient, logger: cfg.Logger, now: time.Now},
		parser: jwt.NewParser(opts...),
	}
	if err := v.keys.refresh(ctx); err != nil {
		return nil, fmt.Errorf("loading signing keys from %s: %w", cfg.JWKSURL, err)
	}
	return v, nil
}

// Authenticate verifies the request's bearer token.
func (v *Verifier) Authenticate(r *http.Request) (auth.Identity, error) {
	raw, ok := auth.BearerToken(r)
	if !ok || len(raw) > maxTokenBytes {
		return auth.Identity{}, auth.ErrUnauthenticated
	}
	claims := jwt.MapClaims{}
	_, err := v.parser.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		return v.keyFor(r.Context(), t)
	})
	if errors.Is(err, errKeysUnavailable) {
		return auth.Identity{}, err
	}
	if err != nil {
		return auth.Identity{}, auth.ErrUnauthenticated
	}
	if v.cfg.AudienceClaim != "aud" {
		if got, _ := claims[v.cfg.AudienceClaim].(string); got != v.cfg.Audience {
			return auth.Identity{}, auth.ErrUnauthenticated
		}
	}
	for name, want := range v.cfg.RequiredClaims {
		if got, _ := claims[name].(string); got != want {
			return auth.Identity{}, auth.ErrUnauthenticated
		}
	}
	sub, err := claims.GetSubject()
	if err != nil || sub == "" {
		return auth.Identity{}, auth.ErrUnauthenticated
	}
	email, _ := claims["email"].(string)
	verified, _ := claims["email_verified"].(bool)
	return auth.Identity{Issuer: v.cfg.Issuer, Subject: sub, Email: email, EmailVerified: verified}, nil
}

func (v *Verifier) keyFor(ctx context.Context, t *jwt.Token) (any, error) {
	kid, _ := t.Header["kid"].(string)
	if kid == "" {
		return nil, errors.New("token has no kid")
	}
	k, ok, err := v.keys.key(ctx, kid)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("unknown signing key")
	}
	alg := t.Method.Alg()
	if k.alg != "" && k.alg != alg {
		return nil, errors.New("key is pinned to a different algorithm")
	}
	// Match the key type to the algorithm family explicitly; the JWT library
	// also checks, but key confusion is too costly to rely on one layer.
	switch {
	case strings.HasPrefix(alg, "RS"), strings.HasPrefix(alg, "PS"):
		if _, ok := k.key.(*rsa.PublicKey); ok {
			return k.key, nil
		}
	case strings.HasPrefix(alg, "ES"):
		if _, ok := k.key.(*ecdsa.PublicKey); ok {
			return k.key, nil
		}
	case alg == "EdDSA":
		if _, ok := k.key.(ed25519.PublicKey); ok {
			return k.key, nil
		}
	}
	return nil, errors.New("key type does not match algorithm")
}

func requireHTTPS(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("must be an absolute https:// URL")
	}
	return nil
}
