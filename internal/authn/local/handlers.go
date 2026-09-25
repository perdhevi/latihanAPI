package local

import (
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/perdhevi/latihanAPI/internal/httpjson"
)

const (
	// NIST SP 800-63B: favour length over composition rules. The upper bound
	// caps hashing work per request.
	minPasswordRunes = 12
	maxPasswordRunes = 128
)

type credentialsInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type refreshInput struct {
	RefreshToken string `json:"refresh_token"`
}

// RegisterRoutes mounts the account endpoints and the public key set. They
// run without authentication; each protects itself.
func (p *Provider) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/register", p.register)
	mux.HandleFunc("POST /api/v1/auth/login", p.login)
	mux.HandleFunc("POST /api/v1/auth/refresh", p.refresh)
	mux.HandleFunc("POST /api/v1/auth/logout", p.logout)
	mux.HandleFunc("GET /.well-known/jwks.json", p.publishKeys)
	for path, allow := range map[string]string{
		"/api/v1/auth/register": "POST", "/api/v1/auth/login": "POST",
		"/api/v1/auth/refresh": "POST", "/api/v1/auth/logout": "POST",
		"/.well-known/jwks.json": "GET, HEAD",
	} {
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Allow", allow)
			httpjson.Error(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		})
	}
}

func (p *Provider) register(w http.ResponseWriter, r *http.Request) {
	in, ok := httpjson.Decode[credentialsInput](w, r)
	if !ok {
		return
	}
	email, ok := normalizeEmail(in.Email)
	if !ok {
		httpjson.Error(w, http.StatusBadRequest, "validation_failed", "email must be a plain address of at most 254 characters")
		return
	}
	if n := utf8.RuneCountInString(in.Password); n < minPasswordRunes || n > maxPasswordRunes {
		httpjson.Error(w, http.StatusBadRequest, "validation_failed", "password must be 12 to 128 characters")
		return
	}
	hash, err := p.hasher.hash(r.Context(), in.Password)
	if err != nil {
		p.internalError(w, r, err)
		return
	}
	id := uuid.New()
	// Answering 409 reveals that the address has an account. Hiding it needs an
	// email-verification round trip, which this provider does not have.
	if err := p.store.createCredential(r.Context(), id, email, hash); errors.Is(err, errEmailTaken) {
		httpjson.Error(w, http.StatusConflict, "email_taken", "an account with this email already exists")
		return
	} else if err != nil {
		p.internalError(w, r, err)
		return
	}
	p.writeTokens(w, r, http.StatusCreated, id, uuid.New())
}

func (p *Provider) login(w http.ResponseWriter, r *http.Request) {
	in, ok := httpjson.Decode[credentialsInput](w, r)
	if !ok {
		return
	}
	email, _ := normalizeEmail(in.Email)
	c, err := p.store.credentialByEmail(r.Context(), email)
	if err != nil && !errors.Is(err, errNoCredential) {
		p.internalError(w, r, err)
		return
	}
	locked := c.lockedUntil != nil && p.now().Before(*c.lockedUntil)
	if errors.Is(err, errNoCredential) || locked {
		// Same work and same answer as a wrong password: no account enumeration,
		// and a locked account does not confirm that it exists.
		_, _, _ = p.hasher.verify(r.Context(), in.Password, p.hasher.dummy)
		invalidCredentials(w)
		return
	}
	match, rehash, err := p.hasher.verify(r.Context(), in.Password, c.passwordHash)
	if err != nil {
		p.internalError(w, r, err)
		return
	}
	if !match {
		if err := p.store.recordFailure(r.Context(), c.id, maxFailedLogins, p.now().Add(lockDuration)); err != nil {
			p.internalError(w, r, err)
			return
		}
		invalidCredentials(w)
		return
	}
	newHash := ""
	if rehash {
		if newHash, err = p.hasher.hash(r.Context(), in.Password); err != nil {
			p.internalError(w, r, err)
			return
		}
	}
	if err := p.store.recordSuccess(r.Context(), c.id, newHash); err != nil {
		p.internalError(w, r, err)
		return
	}
	p.writeTokens(w, r, http.StatusOK, c.id, uuid.New())
}

func (p *Provider) refresh(w http.ResponseWriter, r *http.Request) {
	in, ok := httpjson.Decode[refreshInput](w, r)
	if !ok {
		return
	}
	hash, ok := refreshHash(in.RefreshToken)
	if !ok {
		invalidRefresh(w)
		return
	}
	plain, next, err := p.newRefreshToken(uuid.Nil, uuid.Nil)
	if err != nil {
		p.internalError(w, r, err)
		return
	}
	credentialID, err := p.store.rotateRefreshToken(r.Context(), hash, p.now(), next)
	switch {
	case errors.Is(err, errRefreshReuse):
		p.logger.WarnContext(r.Context(), "refresh token reuse detected; session family revoked")
		invalidRefresh(w)
		return
	case errors.Is(err, errInvalidRefresh):
		invalidRefresh(w)
		return
	case err != nil:
		p.internalError(w, r, err)
		return
	}
	access, err := p.accessToken(credentialID)
	if err != nil {
		p.internalError(w, r, err)
		return
	}
	writeTokenResponse(w, http.StatusOK, tokenResponse{AccessToken: access, TokenType: "Bearer", ExpiresIn: int(p.accessTTL.Seconds()), RefreshToken: plain})
}

// logout revokes the refresh token's whole family. It always answers 204 so
// that it reveals nothing about the token. Access tokens already issued stay
// valid until they expire (AUTH_JWT_ACCESS_TTL).
func (p *Provider) logout(w http.ResponseWriter, r *http.Request) {
	in, ok := httpjson.Decode[refreshInput](w, r)
	if !ok {
		return
	}
	if hash, ok := refreshHash(in.RefreshToken); ok {
		if err := p.store.revokeFamily(r.Context(), hash, p.now()); err != nil {
			p.internalError(w, r, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *Provider) publishKeys(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=300")
	httpjson.Write(w, http.StatusOK, p.keys.jwks())
}

// writeTokens starts a new refresh-token family for credentialID.
func (p *Provider) writeTokens(w http.ResponseWriter, r *http.Request, status int, credentialID, familyID uuid.UUID) {
	plain, rt, err := p.newRefreshToken(credentialID, familyID)
	if err != nil {
		p.internalError(w, r, err)
		return
	}
	if err := p.store.createRefreshToken(r.Context(), rt); err != nil {
		p.internalError(w, r, err)
		return
	}
	access, err := p.accessToken(credentialID)
	if err != nil {
		p.internalError(w, r, err)
		return
	}
	writeTokenResponse(w, status, tokenResponse{AccessToken: access, TokenType: "Bearer", ExpiresIn: int(p.accessTTL.Seconds()), RefreshToken: plain})
}

// writeTokenResponse forbids caching, as RFC 6749 section 5.1 requires.
func writeTokenResponse(w http.ResponseWriter, status int, body tokenResponse) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	httpjson.Write(w, status, body)
}

func invalidCredentials(w http.ResponseWriter) {
	httpjson.Error(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
}

func invalidRefresh(w http.ResponseWriter) {
	httpjson.Error(w, http.StatusUnauthorized, "invalid_refresh_token", "refresh token is invalid or expired")
}

func (p *Provider) internalError(w http.ResponseWriter, r *http.Request, err error) {
	p.logger.ErrorContext(r.Context(), "auth request failed", "path", r.URL.Path, "error", err)
	httpjson.Error(w, http.StatusInternalServerError, "internal_error", "internal server error")
}

// normalizeEmail accepts a bare address (no display name or comments) and
// lower-cases it, so that one mailbox cannot hold two accounts.
func normalizeEmail(raw string) (string, bool) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if len(email) < 3 || len(email) > 254 {
		return "", false
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || addr.Name != "" {
		return "", false
	}
	return email, true
}
