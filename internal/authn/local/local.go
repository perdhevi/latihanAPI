// Package local is the built-in "jwt" provider: email and password accounts
// stored in this service's database, with short-lived EdDSA access tokens and
// rotating refresh tokens. Use it when no external identity provider is
// available.
//
//	AUTH_PROVIDER=jwt
//	AUTH_JWT_ISSUER=https://api.example.com
//	AUTH_JWT_KEY_FILE=/keys/signing.pem
//	AUTH_JWT_PREVIOUS_KEY_FILES=      # optional, comma-separated, verification only
//	AUTH_JWT_AUDIENCE=latihan-api     # optional
//	AUTH_JWT_ACCESS_TTL=15m           # optional, 1m..1h
//	AUTH_JWT_REFRESH_TTL=720h         # optional, 1h..2160h
package local

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/perdhevi/latihanAPI/auth"
	"github.com/perdhevi/latihanAPI/internal/authn/jwks"
)

const (
	maxFailedLogins = 5
	lockDuration    = 15 * time.Minute
	// Concurrent password hashes; each holds about 19 MiB.
	hashConcurrency = 4
	refreshBytes    = 32
)

func init() { auth.Register("jwt", New) }

// Provider issues and verifies this service's own tokens.
type Provider struct {
	issuer     string
	audience   string
	accessTTL  time.Duration
	refreshTTL time.Duration
	keys       *keyRing
	verifier   *jwks.Verifier
	store      store
	hasher     *hasher
	logger     *slog.Logger
	now        func() time.Time
}

func New(_ context.Context, d auth.Deps) (auth.Authenticator, error) {
	if d.DB == nil {
		return nil, errors.New("the jwt provider needs the service database")
	}
	issuer := d.Getenv("AUTH_JWT_ISSUER")
	if u, err := url.Parse(issuer); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("AUTH_JWT_ISSUER must be an absolute URL, such as https://api.example.com")
	}
	keyFile := d.Getenv("AUTH_JWT_KEY_FILE")
	if keyFile == "" {
		return nil, errors.New("AUTH_JWT_KEY_FILE is required; create a key with: api keygen -out /keys/signing.pem")
	}
	keys, err := loadKeyRing(keyFile, strings.Split(d.Getenv("AUTH_JWT_PREVIOUS_KEY_FILES"), ","))
	if err != nil {
		return nil, err
	}
	audience := d.Getenv("AUTH_JWT_AUDIENCE")
	if audience == "" {
		audience = "latihan-api"
	}
	accessTTL, err := duration(d.Getenv("AUTH_JWT_ACCESS_TTL"), 15*time.Minute, time.Minute, time.Hour)
	if err != nil {
		return nil, fmt.Errorf("AUTH_JWT_ACCESS_TTL: %w", err)
	}
	refreshTTL, err := duration(d.Getenv("AUTH_JWT_REFRESH_TTL"), 30*24*time.Hour, time.Hour, 90*24*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("AUTH_JWT_REFRESH_TTL: %w", err)
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return newProvider(issuer, audience, accessTTL, refreshTTL, keys, &sqlStore{db: d.DB}, defaultParams, logger)
}

func newProvider(issuer, audience string, accessTTL, refreshTTL time.Duration, keys *keyRing, s store, params argonParams, logger *slog.Logger) (*Provider, error) {
	verifier, err := jwks.NewStatic(jwks.Config{Issuer: issuer, Audience: audience, Algorithms: []string{"EdDSA"}, Logger: logger}, keys.public)
	if err != nil {
		return nil, err
	}
	h, err := newHasher(params, hashConcurrency)
	if err != nil {
		return nil, err
	}
	return &Provider{
		issuer: issuer, audience: audience, accessTTL: accessTTL, refreshTTL: refreshTTL,
		keys: keys, verifier: verifier, store: s, hasher: h, logger: logger, now: time.Now,
	}, nil
}

func duration(raw string, fallback, lowest, highest time.Duration) (time.Duration, error) {
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < lowest || d > highest {
		return 0, fmt.Errorf("must be a duration between %s and %s", lowest, highest)
	}
	return d, nil
}

// Authenticate verifies access tokens with the same verifier external
// providers use, trusting only this service's keys.
func (p *Provider) Authenticate(r *http.Request) (auth.Identity, error) {
	return p.verifier.Authenticate(r)
}

func (p *Provider) Issuer() string { return p.issuer }

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

// accessToken signs a short-lived access token whose subject is the credential.
func (p *Provider) accessToken(credentialID uuid.UUID) (string, error) {
	now := p.now()
	token := jwtlib.NewWithClaims(jwtlib.SigningMethodEdDSA, jwtlib.RegisteredClaims{
		Issuer:    p.issuer,
		Subject:   credentialID.String(),
		Audience:  jwtlib.ClaimStrings{p.audience},
		IssuedAt:  jwtlib.NewNumericDate(now),
		NotBefore: jwtlib.NewNumericDate(now),
		ExpiresAt: jwtlib.NewNumericDate(now.Add(p.accessTTL)),
		ID:        uuid.NewString(),
	})
	token.Header["kid"] = p.keys.signerID
	token.Header["typ"] = "at+jwt" // RFC 9068 access token
	return token.SignedString(p.keys.signer)
}

// newRefreshToken returns an opaque token and the row that stores its hash.
// 256 random bits make a fast hash sufficient: there is nothing to brute-force.
func (p *Provider) newRefreshToken(credentialID, familyID uuid.UUID) (string, refreshToken, error) {
	raw := make([]byte, refreshBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", refreshToken{}, err
	}
	plain := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(plain))
	return plain, refreshToken{
		id: uuid.New(), credentialID: credentialID, familyID: familyID,
		hash: sum[:], expiresAt: p.now().Add(p.refreshTTL),
	}, nil
}

// refreshHash checks a presented refresh token's shape before hashing it.
func refreshHash(plain string) ([]byte, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(plain)
	if err != nil || len(raw) != refreshBytes {
		return nil, false
	}
	sum := sha256.Sum256([]byte(plain))
	return sum[:], true
}
