package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/perdhevi/latihanAPI/auth"
	"github.com/perdhevi/latihanAPI/internal/profile"
)

// Upper bounds matching the user_identities columns; a provider exceeding
// them is misbehaving and its identities are refused.
const (
	maxIssuerLength  = 500
	maxSubjectLength = 255
)

// principal is the authenticated caller of the current request.
type principal struct {
	identity auth.Identity
	// userID is uuid.Nil until the identity has created its profile.
	userID uuid.UUID
}

type principalKey struct{}

func principalFrom(ctx context.Context) principal {
	p, _ := ctx.Value(principalKey{}).(principal)
	return p
}

// authenticated requires valid credentials. Handlers wrapped with it may run
// for callers without a profile; only profile creation should use it directly.
func (h *handlers) authenticated(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := h.auth.Authenticate(r)
		switch {
		case errors.Is(err, auth.ErrUnauthenticated):
			challenge(w, r)
			return
		case err != nil:
			// The provider could not decide, for example because its key server is down.
			h.logger.ErrorContext(r.Context(), "authentication unavailable", "request_id", r.Context().Value(requestIDKey{}), "error", err)
			writeError(w, http.StatusServiceUnavailable, "auth_unavailable", "authentication is temporarily unavailable")
			return
		case id.Issuer == "" || id.Subject == "" || len(id.Issuer) > maxIssuerLength || len(id.Subject) > maxSubjectLength:
			h.logger.ErrorContext(r.Context(), "provider returned an unusable identity", "request_id", r.Context().Value(requestIDKey{}))
			challenge(w, r)
			return
		}
		if !h.allow(w, r, h.limits.perUser, "user:"+id.Issuer+"|"+id.Subject) {
			return
		}
		userID, err := h.profiles.ResolveIdentity(r.Context(), profile.Identity{Issuer: id.Issuer, Subject: id.Subject})
		if err != nil && !errors.Is(err, profile.ErrNotFound) {
			h.fail(w, r, err, "user")
			return
		}
		ctx := context.WithValue(r.Context(), principalKey{}, principal{identity: id, userID: userID})
		next(w, r.WithContext(ctx))
	}
}

// withUser requires valid credentials linked to a profile.
func (h *handlers) withUser(next http.HandlerFunc) http.HandlerFunc {
	return h.authenticated(func(w http.ResponseWriter, r *http.Request) {
		if principalFrom(r.Context()).userID == uuid.Nil {
			writeError(w, http.StatusForbidden, "profile_required", "create your profile with POST /api/v1/users first")
			return
		}
		next(w, r)
	})
}

// challenge answers with 401 and an RFC 6750 WWW-Authenticate header. Callers
// who sent credentials learn they were invalid, but not why.
func challenge(w http.ResponseWriter, r *http.Request) {
	value := `Bearer realm="latihan"`
	if r.Header.Get("Authorization") != "" {
		value += `, error="invalid_token"`
	}
	w.Header().Set("WWW-Authenticate", value)
	writeError(w, http.StatusUnauthorized, "unauthenticated", "valid bearer token required")
}

// owner is the authenticated caller's user ID. Handlers use it, never a client
// supplied ID, to decide whose records a request touches.
func owner(r *http.Request) uuid.UUID { return principalFrom(r.Context()).userID }

// ownUserID resolves a {userID} path segment. "me" names the caller; the
// caller's own UUID is accepted too. Any other user is reported as not found,
// so callers cannot probe which user IDs exist.
func ownUserID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	caller := owner(r)
	value := r.PathValue("userID")
	if value == "me" {
		return caller, true
	}
	id, ok := parseUUID(w, value)
	if !ok {
		return uuid.Nil, false
	}
	if id != caller {
		writeError(w, http.StatusNotFound, "user_not_found", "user not found")
		return uuid.Nil, false
	}
	return id, true
}
