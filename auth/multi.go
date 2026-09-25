package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// maxRoutingTokenBytes bounds the work done on a token before any verifier sees it.
const maxRoutingTokenBytes = 8 << 10

type multi struct {
	byIssuer map[string]Authenticator
	members  []Authenticator
}

func newMulti(ctx context.Context, names []string, deps Deps) (Authenticator, error) {
	m := &multi{byIssuer: map[string]Authenticator{}}
	for _, name := range names {
		name = strings.TrimSpace(name)
		a, err := New(ctx, name, deps)
		if err != nil {
			return nil, err
		}
		bound, ok := a.(IssuerBound)
		if !ok || bound.Issuer() == "" {
			return nil, fmt.Errorf("auth: provider %q cannot be combined: it does not report a single issuer", name)
		}
		if _, dup := m.byIssuer[bound.Issuer()]; dup {
			return nil, fmt.Errorf("auth: two providers trust issuer %q", bound.Issuer())
		}
		m.byIssuer[bound.Issuer()] = a
		m.members = append(m.members, a)
	}
	return m, nil
}

// Authenticate reads the token's "iss" claim without verifying it, only to
// choose a verifier; the chosen verifier then checks everything, including
// that same claim. A forged issuer therefore only picks which verifier
// rejects the token.
func (m *multi) Authenticate(r *http.Request) (Identity, error) {
	token, ok := BearerToken(r)
	if !ok || len(token) > maxRoutingTokenBytes {
		return Identity{}, ErrUnauthenticated
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Identity{}, ErrUnauthenticated
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Identity{}, ErrUnauthenticated
	}
	var claims struct {
		Issuer string `json:"iss"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Identity{}, ErrUnauthenticated
	}
	a, ok := m.byIssuer[claims.Issuer]
	if !ok {
		return Identity{}, ErrUnauthenticated
	}
	return a.Authenticate(r)
}

// RegisterRoutes mounts the endpoints of every member that serves any.
func (m *multi) RegisterRoutes(mux *http.ServeMux) {
	for _, a := range m.members {
		if r, ok := a.(RouteRegistrar); ok {
			r.RegisterRoutes(mux)
		}
	}
}
