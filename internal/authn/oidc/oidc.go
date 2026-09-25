// Package oidc verifies tokens from any OpenID Connect issuer, such as
// Keycloak, Auth0 or Zitadel.
//
//	AUTH_PROVIDER=oidc
//	AUTH_OIDC_ISSUER=https://id.example.com/realms/latihan
//	AUTH_OIDC_AUDIENCE=latihan-api
//	AUTH_OIDC_JWKS_URL=          # optional; discovered from the issuer when empty
//	AUTH_OIDC_ALGORITHMS=RS256   # optional, comma-separated
package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/perdhevi/latihanAPI/auth"
	"github.com/perdhevi/latihanAPI/internal/authn/jwks"
)

func init() { auth.Register("oidc", New) }

func New(ctx context.Context, d auth.Deps) (auth.Authenticator, error) {
	cfg := jwks.Config{
		Issuer:     d.Getenv("AUTH_OIDC_ISSUER"),
		Audience:   d.Getenv("AUTH_OIDC_AUDIENCE"),
		JWKSURL:    d.Getenv("AUTH_OIDC_JWKS_URL"),
		Algorithms: []string{"RS256"},
		HTTPClient: d.HTTPClient,
		Logger:     d.Logger,
	}
	if cfg.Issuer == "" || cfg.Audience == "" {
		return nil, errors.New("AUTH_OIDC_ISSUER and AUTH_OIDC_AUDIENCE are required")
	}
	if algs := d.Getenv("AUTH_OIDC_ALGORITHMS"); algs != "" {
		cfg.Algorithms = strings.Split(algs, ",")
	}
	if cfg.JWKSURL == "" {
		discovered, err := discoverJWKS(ctx, cfg.HTTPClient, cfg.Issuer)
		if err != nil {
			return nil, fmt.Errorf("OIDC discovery: %w", err)
		}
		cfg.JWKSURL = discovered
	}
	return jwks.New(ctx, cfg)
}

// discoverJWKS reads the issuer's discovery document. The document must name
// the configured issuer exactly (OpenID Connect Discovery 1.0, section 4.3), so
// a misconfigured or spoofed endpoint cannot redirect key lookups.
func discoverJWKS(ctx context.Context, client *http.Client, issuer string) (string, error) {
	if !strings.HasPrefix(issuer, "https://") {
		return "", errors.New("AUTH_OIDC_ISSUER must use https://")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("discovery returned %s", resp.Status)
	}
	var doc struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return "", err
	}
	if doc.Issuer != issuer {
		return "", fmt.Errorf("discovery document names issuer %q, want %q", doc.Issuer, issuer)
	}
	return doc.JWKSURI, nil
}
