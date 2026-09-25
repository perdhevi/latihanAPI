// Package auth is the contract between latihanAPI and authentication providers.
//
// A provider verifies credentials and reports who the caller is. It never
// decides what the caller may access: the service links identities to its own
// users and enforces ownership itself.
//
// Providers register a Factory under a name, usually from an init function, and
// the service selects one at startup with the AUTH_PROVIDER setting:
//
//	func init() { auth.Register("ldap", New) }
//
// This module depends only on the standard library so that providers living in
// other repositories do not inherit the service's dependencies.
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
)

// Identity is a caller vouched for by a provider. It says nothing about permissions.
type Identity struct {
	// Issuer identifies who vouched for the caller, such as
	// "https://securetoken.google.com/my-project". Required.
	Issuer string
	// Subject is the issuer's stable, never-reused ID for the caller. Required.
	Subject string
	// Email is informational and may be empty. Never use it to link accounts
	// unless EmailVerified is true.
	Email         string
	EmailVerified bool
}

// ErrUnauthenticated reports missing, malformed, expired or otherwise invalid
// credentials. The service answers it with 401. Any other error from
// Authenticate means the provider could not decide (for example, its key
// server is unreachable) and is answered with 503.
var ErrUnauthenticated = errors.New("auth: missing or invalid credentials")

// Authenticator verifies the credentials on a request.
type Authenticator interface {
	Authenticate(r *http.Request) (Identity, error)
}

// RouteRegistrar is implemented by providers that serve their own endpoints,
// such as login or key publication. The service mounts them without
// authentication; the provider is responsible for protecting them.
type RouteRegistrar interface {
	RegisterRoutes(mux *http.ServeMux)
}

// Deps is what the service lends a provider at construction time.
type Deps struct {
	// Getenv reads configuration. Providers should use an AUTH_<NAME>_ prefix.
	Getenv func(string) string
	Logger *slog.Logger
	// HTTPClient has sensible timeouts; use it for outbound calls such as key fetches.
	HTTPClient *http.Client
}

// Factory builds a provider. ctx bounds startup work such as fetching keys.
type Factory func(ctx context.Context, deps Deps) (Authenticator, error)

var (
	mu        sync.RWMutex
	factories = map[string]Factory{}
)

// Register makes a provider available under name. It panics if name is empty,
// contains a comma, or is already registered, mirroring database/sql.Register:
// two providers claiming one name is a build mistake that must not go unnoticed.
func Register(name string, f Factory) {
	if name == "" || strings.ContainsAny(name, ", ") || f == nil {
		panic("auth: Register requires a non-empty name without commas or spaces and a non-nil factory")
	}
	mu.Lock()
	defer mu.Unlock()
	if _, dup := factories[name]; dup {
		panic("auth: provider " + name + " registered twice")
	}
	factories[name] = f
}

// Names lists the registered providers in sorted order.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(factories))
	for name := range factories {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// New builds the provider registered under name.
func New(ctx context.Context, name string, deps Deps) (Authenticator, error) {
	mu.RLock()
	f, ok := factories[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("auth: unknown provider %q (available: %s)", name, strings.Join(Names(), ", "))
	}
	a, err := f(ctx, deps)
	if err != nil {
		return nil, fmt.Errorf("auth: provider %q: %w", name, err)
	}
	if a == nil {
		return nil, fmt.Errorf("auth: provider %q returned no authenticator", name)
	}
	return a, nil
}

// BearerToken extracts the token from an "Authorization: Bearer <token>" header
// (RFC 6750). It reports false when the header is missing or malformed.
func BearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t") {
		return "", false
	}
	return token, true
}
