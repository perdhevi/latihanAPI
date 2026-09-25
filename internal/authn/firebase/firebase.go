// Package firebase verifies Firebase Authentication ID tokens.
//
//	AUTH_PROVIDER=firebase
//	AUTH_FIREBASE_PROJECT_ID=my-project
package firebase

import (
	"context"
	"errors"
	"regexp"

	"github.com/perdhevi/latihanAPI/auth"
	"github.com/perdhevi/latihanAPI/internal/authn/jwks"
)

// Google publishes the keys that sign every project's ID tokens here.
const jwksURL = "https://www.googleapis.com/service_accounts/v1/jwk/securetoken@system.gserviceaccount.com"

// Firebase project IDs are 6-30 lowercase letters, digits and hyphens.
var projectID = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

func init() { auth.Register("firebase", New) }

// Config returns the verification rules for a project's ID tokens.
func Config(project string) jwks.Config {
	return jwks.Config{
		Issuer:     "https://securetoken.google.com/" + project,
		Audience:   project,
		JWKSURL:    jwksURL,
		Algorithms: []string{"RS256"},
	}
}

func New(ctx context.Context, d auth.Deps) (auth.Authenticator, error) {
	project := d.Getenv("AUTH_FIREBASE_PROJECT_ID")
	if !projectID.MatchString(project) {
		return nil, errors.New("AUTH_FIREBASE_PROJECT_ID must be a Firebase project ID")
	}
	cfg := Config(project)
	cfg.HTTPClient, cfg.Logger = d.HTTPClient, d.Logger
	return jwks.New(ctx, cfg)
}
