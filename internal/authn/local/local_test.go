package local

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/perdhevi/latihanAPI/auth"
)

// Cheap parameters keep tests fast; production uses defaultParams.
var testParams = argonParams{memoryKiB: 8 * 1024, passes: 1, lanes: 1}

type memStore struct {
	mu          sync.Mutex
	credentials map[string]*memCredential // by email
	tokens      map[string]*memToken      // by hash
}
type memCredential struct {
	credential
	failures int
}
type memToken struct {
	refreshToken
	used, revoked bool
}

func newMemStore() *memStore {
	return &memStore{credentials: map[string]*memCredential{}, tokens: map[string]*memToken{}}
}
func (s *memStore) createCredential(_ context.Context, id uuid.UUID, email, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.credentials[email]; ok {
		return errEmailTaken
	}
	s.credentials[email] = &memCredential{credential: credential{id: id, passwordHash: hash}}
	return nil
}
func (s *memStore) credentialByEmail(_ context.Context, email string) (credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.credentials[email]
	if !ok {
		return credential{}, errNoCredential
	}
	return c.credential, nil
}
func (s *memStore) byID(id uuid.UUID) *memCredential {
	for _, c := range s.credentials {
		if c.id == id {
			return c
		}
	}
	return nil
}
func (s *memStore) recordFailure(_ context.Context, id uuid.UUID, maxFailures int, lockUntil time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.byID(id)
	if c.failures++; c.failures >= maxFailures {
		c.failures, c.lockedUntil = 0, &lockUntil
	}
	return nil
}
func (s *memStore) recordSuccess(_ context.Context, id uuid.UUID, newHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.byID(id)
	c.failures, c.lockedUntil = 0, nil
	if newHash != "" {
		c.passwordHash = newHash
	}
	return nil
}
func (s *memStore) createRefreshToken(_ context.Context, t refreshToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[string(t.hash)] = &memToken{refreshToken: t}
	return nil
}
func (s *memStore) rotateRefreshToken(_ context.Context, oldHash []byte, now time.Time, next refreshToken) (uuid.UUID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[string(oldHash)]
	if !ok || t.revoked || !now.Before(t.expiresAt) {
		return uuid.Nil, errInvalidRefresh
	}
	if t.used {
		s.revokeLocked(t.familyID)
		return uuid.Nil, errRefreshReuse
	}
	t.used = true
	next.credentialID, next.familyID = t.credentialID, t.familyID
	s.tokens[string(next.hash)] = &memToken{refreshToken: next}
	return t.credentialID, nil
}
func (s *memStore) revokeFamily(_ context.Context, hash []byte, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tokens[string(hash)]; ok {
		s.revokeLocked(t.familyID)
	}
	return nil
}
func (s *memStore) deleteCredential(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for email, c := range s.credentials {
		if c.id == id {
			delete(s.credentials, email)
		}
	}
	for hash, t := range s.tokens {
		if t.credentialID == id {
			delete(s.tokens, hash)
		}
	}
	return nil
}
func (s *memStore) revokeLocked(family uuid.UUID) {
	for _, t := range s.tokens {
		if t.familyID == family {
			t.revoked = true
		}
	}
}

type harness struct {
	p   *Provider
	mux *http.ServeMux
	now time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	keyFile := filepath.Join(t.TempDir(), "signing.pem")
	if _, err := GenerateKeyFile(keyFile); err != nil {
		t.Fatal(err)
	}
	keys, err := loadKeyRing(keyFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := newProvider("https://api.test", "latihan-api", 15*time.Minute, 24*time.Hour, keys, newMemStore(), testParams, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{p: p, mux: http.NewServeMux(), now: time.Now()}
	p.now = func() time.Time { return h.now }
	p.RegisterRoutes(h.mux)
	return h
}

func (h *harness) post(t *testing.T, path string, body any) (*httptest.ResponseRecorder, tokenResponse) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.mux.ServeHTTP(w, r)
	var tokens tokenResponse
	_ = json.Unmarshal(w.Body.Bytes(), &tokens)
	return w, tokens
}

func (h *harness) authenticate(t *testing.T, access string) (auth.Identity, error) {
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+access)
	return h.p.Authenticate(r)
}

const password = "correct horse battery staple"

func TestRegisterAndAuthenticate(t *testing.T) {
	h := newHarness(t)
	w, tokens := h.post(t, "/api/v1/auth/register", credentialsInput{Email: " Alex@Example.com ", Password: password})
	if w.Code != http.StatusCreated || tokens.TokenType != "Bearer" || tokens.ExpiresIn != 900 || tokens.RefreshToken == "" {
		t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("token response may be cached")
	}
	id, err := h.authenticate(t, tokens.AccessToken)
	if err != nil || id.Issuer != "https://api.test" || uuid.Validate(id.Subject) != nil {
		t.Fatalf("access token: %+v %v", id, err)
	}
	// The same mailbox, differently cased, is the same account.
	if w, _ := h.post(t, "/api/v1/auth/register", credentialsInput{Email: "alex@example.com", Password: password}); w.Code != http.StatusConflict {
		t.Fatalf("duplicate email: %d", w.Code)
	}
	for name, in := range map[string]credentialsInput{
		"display name":   {Email: "Alex <alex2@example.com>", Password: password},
		"not an address": {Email: "alex", Password: password},
		"short password": {Email: "b@example.com", Password: "short"},
		"long password":  {Email: "b@example.com", Password: strings.Repeat("x", 129)},
	} {
		if w, _ := h.post(t, "/api/v1/auth/register", in); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if w, _ := h.post(t, "/api/v1/auth/register", map[string]string{"email": "c@example.com", "password": password, "role": "admin"}); w.Code != http.StatusBadRequest {
		t.Fatal("unknown field accepted")
	}
}

func TestLoginAndLockout(t *testing.T) {
	h := newHarness(t)
	h.post(t, "/api/v1/auth/register", credentialsInput{Email: "alex@example.com", Password: password})
	if w, tokens := h.post(t, "/api/v1/auth/login", credentialsInput{Email: "ALEX@example.com", Password: password}); w.Code != http.StatusOK || tokens.AccessToken == "" {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	unknown, _ := h.post(t, "/api/v1/auth/login", credentialsInput{Email: "nobody@example.com", Password: password})
	wrong, _ := h.post(t, "/api/v1/auth/login", credentialsInput{Email: "alex@example.com", Password: "wrong password!!"})
	if unknown.Code != http.StatusUnauthorized || unknown.Body.String() != wrong.Body.String() {
		t.Fatalf("unknown account must look like a wrong password: %s vs %s", unknown.Body, wrong.Body)
	}
	for range maxFailedLogins - 1 { // one failure already recorded above
		h.post(t, "/api/v1/auth/login", credentialsInput{Email: "alex@example.com", Password: "wrong password!!"})
	}
	locked, _ := h.post(t, "/api/v1/auth/login", credentialsInput{Email: "alex@example.com", Password: password})
	if locked.Code != http.StatusUnauthorized || locked.Body.String() != wrong.Body.String() {
		t.Fatalf("locked account accepted the right password or answered differently: %d %s", locked.Code, locked.Body)
	}
	h.now = h.now.Add(lockDuration)
	if w, _ := h.post(t, "/api/v1/auth/login", credentialsInput{Email: "alex@example.com", Password: password}); w.Code != http.StatusOK {
		t.Fatalf("lock did not expire: %d", w.Code)
	}
}

func TestRefreshRotationAndReuse(t *testing.T) {
	h := newHarness(t)
	_, first := h.post(t, "/api/v1/auth/register", credentialsInput{Email: "alex@example.com", Password: password})
	w, second := h.post(t, "/api/v1/auth/refresh", refreshInput{RefreshToken: first.RefreshToken})
	if w.Code != http.StatusOK || second.RefreshToken == first.RefreshToken || second.AccessToken == "" {
		t.Fatalf("refresh: %d %s", w.Code, w.Body)
	}
	firstID, _ := h.authenticate(t, first.AccessToken)
	secondID, err := h.authenticate(t, second.AccessToken)
	if err != nil || secondID.Subject != firstID.Subject {
		t.Fatal("refreshed token names a different account")
	}
	// Replaying the used token revokes the family, including the token that replaced it.
	if w, _ := h.post(t, "/api/v1/auth/refresh", refreshInput{RefreshToken: first.RefreshToken}); w.Code != http.StatusUnauthorized {
		t.Fatalf("reuse accepted: %d", w.Code)
	}
	if w, _ := h.post(t, "/api/v1/auth/refresh", refreshInput{RefreshToken: second.RefreshToken}); w.Code != http.StatusUnauthorized {
		t.Fatal("family survived reuse detection")
	}
	// Other sessions of the same account are separate families and survive.
	_, other := h.post(t, "/api/v1/auth/login", credentialsInput{Email: "alex@example.com", Password: password})
	if w, _ := h.post(t, "/api/v1/auth/refresh", refreshInput{RefreshToken: other.RefreshToken}); w.Code != http.StatusOK {
		t.Fatal("unrelated session was revoked")
	}
	for name, token := range map[string]string{"garbage": "not-a-token", "unknown": base64.RawURLEncoding.EncodeToString(make([]byte, 32))} {
		if w, _ := h.post(t, "/api/v1/auth/refresh", refreshInput{RefreshToken: token}); w.Code != http.StatusUnauthorized {
			t.Errorf("%s refresh token: %d", name, w.Code)
		}
	}
}

func TestRefreshExpiryAndLogout(t *testing.T) {
	h := newHarness(t)
	_, tokens := h.post(t, "/api/v1/auth/register", credentialsInput{Email: "alex@example.com", Password: password})
	h.now = h.now.Add(24 * time.Hour)
	if w, _ := h.post(t, "/api/v1/auth/refresh", refreshInput{RefreshToken: tokens.RefreshToken}); w.Code != http.StatusUnauthorized {
		t.Fatal("expired refresh token accepted")
	}
	_, tokens = h.post(t, "/api/v1/auth/login", credentialsInput{Email: "alex@example.com", Password: password})
	if w, _ := h.post(t, "/api/v1/auth/logout", refreshInput{RefreshToken: tokens.RefreshToken}); w.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", w.Code)
	}
	if w, _ := h.post(t, "/api/v1/auth/refresh", refreshInput{RefreshToken: tokens.RefreshToken}); w.Code != http.StatusUnauthorized {
		t.Fatal("refresh token survived logout")
	}
	if w, _ := h.post(t, "/api/v1/auth/logout", refreshInput{RefreshToken: "whatever"}); w.Code != http.StatusNoContent {
		t.Fatal("logout must not reveal whether a token existed")
	}
}

func TestPublishedKeysVerifyTokens(t *testing.T) {
	h := newHarness(t)
	_, tokens := h.post(t, "/api/v1/auth/register", credentialsInput{Email: "alex@example.com", Password: password})
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/.well-known/jwks.json", nil)
	w := httptest.NewRecorder()
	h.mux.ServeHTTP(w, r)
	var set struct {
		Keys []map[string]string `json:"keys"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &set); err != nil || len(set.Keys) != 1 {
		t.Fatalf("jwks: %s", w.Body)
	}
	header, _ := base64.RawURLEncoding.DecodeString(strings.Split(tokens.AccessToken, ".")[0])
	var h2 map[string]string
	_ = json.Unmarshal(header, &h2)
	if h2["kid"] != set.Keys[0]["kid"] || h2["alg"] != "EdDSA" || h2["typ"] != "at+jwt" || set.Keys[0]["d"] != "" {
		t.Fatalf("token header %v does not match published key %v", h2, set.Keys[0])
	}
}

func TestKeyRotation(t *testing.T) {
	dir := t.TempDir()
	oldFile, newFile := filepath.Join(dir, "old.pem"), filepath.Join(dir, "new.pem")
	for _, f := range []string{oldFile, newFile} {
		if created, err := GenerateKeyFile(f); err != nil || !created {
			t.Fatal(err)
		}
	}
	if created, err := GenerateKeyFile(oldFile); err != nil || created {
		t.Fatal("keygen overwrote an existing key")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	oldKeys, _ := loadKeyRing(oldFile, nil)
	before, _ := newProvider("https://api.test", "latihan-api", time.Minute, time.Hour, oldKeys, newMemStore(), testParams, logger)
	token, err := before.accessToken(uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := loadKeyRing(newFile, []string{oldFile})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := newProvider("https://api.test", "latihan-api", time.Minute, time.Hour, rotated, newMemStore(), testParams, logger)
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	if _, err := after.Authenticate(r); err != nil {
		t.Fatalf("token signed by the previous key rejected after rotation: %v", err)
	}
	retired, _ := loadKeyRing(newFile, nil)
	gone, _ := newProvider("https://api.test", "latihan-api", time.Minute, time.Hour, retired, newMemStore(), testParams, logger)
	if _, err := gone.Authenticate(r); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("token accepted after its key was retired")
	}
}

// RFC 8037 appendix A.3 gives the thumbprint of its example Ed25519 key.
func TestKeyIDIsRFC7638Thumbprint(t *testing.T) {
	x, _ := base64.RawURLEncoding.DecodeString("11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo")
	if got := keyID(ed25519.PublicKey(x)); got != "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k" {
		t.Fatalf("thumbprint %s", got)
	}
}

func TestPasswordHashing(t *testing.T) {
	h, err := newHasher(testParams, 1)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := h.hash(t.Context(), password)
	if err != nil || !strings.HasPrefix(encoded, "$argon2id$v=19$m=8192,t=1,p=1$") {
		t.Fatalf("hash %q: %v", encoded, err)
	}
	if ok, rehash, err := h.verify(t.Context(), password, encoded); !ok || rehash || err != nil {
		t.Fatal("correct password rejected")
	}
	if ok, _, _ := h.verify(t.Context(), "wrong", encoded); ok {
		t.Fatal("wrong password accepted")
	}
	stronger, _ := newHasher(argonParams{memoryKiB: 16 * 1024, passes: 1, lanes: 1}, 1)
	if ok, rehash, _ := stronger.verify(t.Context(), password, encoded); !ok || !rehash {
		t.Fatal("changed parameters should request a rehash")
	}
	for _, bad := range []string{"", "$argon2i$v=19$m=8192,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA", "$argon2id$v=19$m=4194304,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA"} {
		if _, _, err := h.verify(t.Context(), password, bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSettingsValidation(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "k.pem")
	if _, err := GenerateKeyFile(keyFile); err != nil {
		t.Fatal(err)
	}
	// sql.Open connects lazily, so no database is needed to test settings.
	db, err := sql.Open("pgx", "postgres://unused")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	valid := map[string]string{"AUTH_JWT_ISSUER": "https://api.test", "AUTH_JWT_KEY_FILE": keyFile}
	if _, err := New(t.Context(), auth.Deps{Getenv: func(k string) string { return valid[k] }, DB: db}); err != nil {
		t.Fatalf("valid settings rejected: %v", err)
	}
	if _, err := New(t.Context(), auth.Deps{Getenv: func(k string) string { return valid[k] }}); err == nil {
		t.Fatal("missing database accepted")
	}
	for name, change := range map[string][2]string{
		"relative issuer":  {"AUTH_JWT_ISSUER", "api.test"},
		"missing key file": {"AUTH_JWT_KEY_FILE", ""},
		"absent key file":  {"AUTH_JWT_KEY_FILE", keyFile + ".missing"},
		"tiny access TTL":  {"AUTH_JWT_ACCESS_TTL", "1s"},
		"huge refresh TTL": {"AUTH_JWT_REFRESH_TTL", "10000h"},
	} {
		t.Run(name, func(t *testing.T) {
			env := map[string]string{}
			for k, v := range valid {
				env[k] = v
			}
			env[change[0]] = change[1]
			if _, err := New(t.Context(), auth.Deps{Getenv: func(k string) string { return env[k] }, DB: db}); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
