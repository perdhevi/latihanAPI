// Package cognito verifies Amazon Cognito user pool tokens.
//
//	AUTH_PROVIDER=cognito
//	AUTH_COGNITO_REGION=ap-southeast-1
//	AUTH_COGNITO_USER_POOL_ID=ap-southeast-1_AbC123xyz
//	AUTH_COGNITO_CLIENT_ID=app-client-id
//	AUTH_COGNITO_TOKEN_USE=access   # or id
package cognito

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/perdhevi/latihanAPI/auth"
	"github.com/perdhevi/latihanAPI/internal/authn/jwks"
)

var (
	region   = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d$`)
	poolID   = regexp.MustCompile(`^[\w-]+_[0-9a-zA-Z]+$`)
	clientID = regexp.MustCompile(`^[\w+]+$`)
)

func init() { auth.Register("cognito", New) }

// Config returns the verification rules for a user pool's tokens.
//
// Cognito access tokens have no "aud" claim: they name the app client in
// "client_id" instead. Checking "token_use" stops an ID token from being
// replayed where an access token is expected, and vice versa.
func Config(region, pool, client, tokenUse string) jwks.Config {
	issuer := "https://cognito-idp." + region + ".amazonaws.com/" + pool
	audienceClaim := "aud"
	if tokenUse == "access" {
		audienceClaim = "client_id"
	}
	return jwks.Config{
		Issuer:         issuer,
		Audience:       client,
		AudienceClaim:  audienceClaim,
		JWKSURL:        issuer + "/.well-known/jwks.json",
		Algorithms:     []string{"RS256"},
		RequiredClaims: map[string]string{"token_use": tokenUse},
	}
}

func New(ctx context.Context, d auth.Deps) (auth.Authenticator, error) {
	r := d.Getenv("AUTH_COGNITO_REGION")
	pool := d.Getenv("AUTH_COGNITO_USER_POOL_ID")
	client := d.Getenv("AUTH_COGNITO_CLIENT_ID")
	tokenUse := d.Getenv("AUTH_COGNITO_TOKEN_USE")
	switch {
	case !region.MatchString(r):
		return nil, errors.New("AUTH_COGNITO_REGION must be an AWS region such as ap-southeast-1")
	case !poolID.MatchString(pool) || !strings.HasPrefix(pool, r+"_"):
		return nil, errors.New("AUTH_COGNITO_USER_POOL_ID must be a user pool ID in AUTH_COGNITO_REGION")
	case !clientID.MatchString(client):
		return nil, errors.New("AUTH_COGNITO_CLIENT_ID must be an app client ID")
	case tokenUse != "access" && tokenUse != "id":
		return nil, errors.New(`AUTH_COGNITO_TOKEN_USE must be "access" or "id"`)
	}
	cfg := Config(r, pool, client, tokenUse)
	cfg.HTTPClient, cfg.Logger = d.HTTPClient, d.Logger
	return jwks.New(ctx, cfg)
}
