package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/perdhevi/latihanAPI/auth"
	"github.com/perdhevi/latihanAPI/internal/profile"
	"github.com/perdhevi/latihanAPI/internal/training"
)

type testPinger struct {
	err   error
	calls int
	ctx   context.Context
}

func (p *testPinger) Ping(ctx context.Context) error { p.calls++; p.ctx = ctx; return p.err }

type fakeTraining struct {
	training.Repository
	err        error
	panicOnGet bool
	ctx        context.Context
	owner      uuid.UUID
	// entered and release, when set, hold GetSession open to test concurrency.
	entered, release chan struct{}
}

func (s *fakeTraining) GetSession(ctx context.Context, owner, id uuid.UUID) (training.Session, error) {
	if s.release != nil {
		s.entered <- struct{}{}
		<-s.release
	}
	s.ctx = ctx
	s.owner = owner
	if s.panicOnGet {
		panic("private panic")
	}
	return training.Session{ID: id}, s.err
}

// testAuth accepts "Bearer test|<subject>" and fails as if its key server were
// down for "Bearer outage".
type testAuth struct{}

func (testAuth) Authenticate(r *http.Request) (auth.Identity, error) {
	token, _ := auth.BearerToken(r)
	if token == "outage" {
		return auth.Identity{}, errors.New("private key server failure")
	}
	subject, ok := strings.CutPrefix(token, "test|")
	if !ok || subject == "" {
		return auth.Identity{}, auth.ErrUnauthenticated
	}
	return auth.Identity{Issuer: "https://issuer.test", Subject: subject}, nil
}

// fakeProfiles links the "athlete" identity to linkedUser; others have no profile.
type fakeProfiles struct{ profile.Repository }

var linkedUser = uuid.New()

func (fakeProfiles) ResolveIdentity(_ context.Context, id profile.Identity) (uuid.UUID, error) {
	if id.Subject == "athlete" {
		return linkedUser, nil
	}
	return uuid.Nil, profile.ErrNotFound
}
func (fakeProfiles) GetUser(_ context.Context, id uuid.UUID) (profile.User, error) {
	return profile.User{ID: id, DisplayName: "Athlete"}, nil
}

func testRouter(repo *fakeTraining, p *testPinger) http.Handler {
	return NewRouter(training.NewService(repo), profile.NewService(fakeProfiles{}), p, testAuth{}, slog.New(slog.NewJSONHandler(io.Discard, nil)), Options{})
}
func request(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	return requestAs(h, "Bearer test|athlete", method, path, body)
}
func requestAs(h http.Handler, authorization, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestInvalidRequests(t *testing.T) {
	id := uuid.NewString()
	for _, tc := range []struct {
		name, method, path, body string
		status                   int
	}{
		{"removed catalog", "GET", "/api/v1/exercises", "", 404},
		{"removed catalog create", "POST", "/api/v1/exercises", "{}", 404},
		{"bad UUID", "GET", "/api/v1/sessions/nope", "", 400},
		{"nil UUID", "GET", "/api/v1/sessions/00000000-0000-0000-0000-000000000000", "", 400},
		{"owner in query", "GET", "/api/v1/sessions?user_id=" + id, "", 400},
		{"zero limit", "GET", "/api/v1/sessions?limit=0", "", 400},
		{"offset no longer supported", "GET", "/api/v1/plans?offset=10", "", 400},
		{"malformed cursor", "GET", "/api/v1/plans?cursor=not-a-cursor", "", 400},
		{"oversized cursor", "GET", "/api/v1/sessions?cursor=" + strings.Repeat("A", 200), "", 400},
		{"duplicate query", "GET", "/api/v1/plans?limit=1&limit=2", "", 400},
		{"unknown query", "GET", "/api/v1/plans?sort=name", "", 400},
		{"comparison needs session", "GET", "/api/v1/plans/" + id + "/comparison", "", 400},
		{"invalid JSON", "POST", "/api/v1/users", "{", 400},
		{"empty JSON", "POST", "/api/v1/users", "", 400},
		{"null JSON", "POST", "/api/v1/users", "null", 400},
		{"array JSON", "POST", "/api/v1/users", "[]", 400},
		{"multiple JSON", "POST", "/api/v1/users", `{"display_name":"A"} {}`, 400},
		{"privileged id", "POST", "/api/v1/users", `{"display_name":"A","id":"` + id + `"}`, 400},
		{"blank name", "POST", "/api/v1/users", `{"display_name":" "}`, 400},
		{"owner in plan body", "POST", "/api/v1/plans", `{"user_id":"` + id + `","name":"Plan","exercises":[{"name":"Run","kind":"cardio","minutes":20}]}`, 400},
		{"owner in session body", "POST", "/api/v1/sessions", `{"user_id":"` + id + `","name":"Done","performed_at":"2026-09-23T08:00:00Z","exercises":[{"name":"Run","kind":"cardio","minutes":20}]}`, 400},
		{"another user's profile", "GET", "/api/v1/users/" + id, "", 404},
		{"another user's measurements", "GET", "/api/v1/users/" + id + "/measurements", "", 404},
		{"empty plan", "POST", "/api/v1/plans", `{"name":"Plan","exercises":[]}`, 400},
		{"missing metrics", "POST", "/api/v1/sessions", `{"name":"Done","performed_at":"2026-09-23T08:00:00Z","exercises":[{"name":"Squat","kind":"strength"}]}`, 400},
		{"nested privileged id", "POST", "/api/v1/plans", `{"name":"Plan","exercises":[{"id":"` + id + `","name":"Run","kind":"cardio","minutes":20}]}`, 400},
		{"large body", "POST", "/api/v1/users", `{"display_name":"` + strings.Repeat("a", maxBodyBytes) + `"}`, 413},
		{"unsupported method", "PATCH", "/api/v1/sessions/" + id, "{}", 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := request(testRouter(&fakeTraining{}, &testPinger{}), tc.method, tc.path, tc.body)
			var response errorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.status || response.Error.Code == "" {
				t.Fatalf("got %d %s", w.Code, w.Body)
			}
			if w.Header().Get("X-Request-ID") == "" {
				t.Fatal("request ID missing")
			}
		})
	}
}
func TestHealthReadinessAndRecovery(t *testing.T) {
	p := &testPinger{err: errors.New("offline")}
	h := testRouter(&fakeTraining{}, p)
	if w := request(h, "GET", "/health", ""); w.Code != 200 || p.calls != 0 {
		t.Fatal("liveness depends on DB")
	}
	if w := request(h, "GET", "/ready", ""); w.Code != 503 {
		t.Fatal("readiness should fail")
	}
	if _, ok := p.ctx.Deadline(); !ok {
		t.Fatal("readiness deadline lost")
	}
	p.err = nil
	if w := request(h, "GET", "/ready", ""); w.Code != 200 {
		t.Fatal("readiness should recover")
	}
	var logs bytes.Buffer
	h = NewRouter(training.NewService(&fakeTraining{panicOnGet: true}), profile.NewService(fakeProfiles{}), p, testAuth{}, slog.New(slog.NewJSONHandler(&logs, nil)), Options{})
	w := request(h, "GET", "/api/v1/sessions/"+uuid.NewString(), "")
	if w.Code != 500 || strings.Contains(w.Body.String(), "private") {
		t.Fatalf("panic: %d %s", w.Code, w.Body)
	}
	for _, field := range []string{`"request_id":`, `"method":"GET"`, `"path":`, `"status":500`, `"duration":`} {
		if !strings.Contains(logs.String(), field) {
			t.Fatalf("missing %s", field)
		}
	}
}
func TestErrorPrivacyAndContext(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{{training.ErrNotFound, 404}, {errors.New("private SQL password"), 500}} {
		w := request(testRouter(&fakeTraining{err: tc.err}, &testPinger{}), "GET", "/api/v1/sessions/"+uuid.NewString(), "")
		if w.Code != tc.status || strings.Contains(w.Body.String(), "private") {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
	repo := &fakeTraining{}
	h := testRouter(repo, &testPinger{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/sessions/"+uuid.NewString(), nil)
	r.Header.Set("Authorization", "Bearer test|athlete")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if !errors.Is(repo.ctx.Err(), context.Canceled) {
		t.Fatal("cancellation lost")
	}
	r = httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/users", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer test|athlete")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 415 {
		t.Fatalf("media type: %d", w.Code)
	}
}
func TestAuthentication(t *testing.T) {
	h := testRouter(&fakeTraining{}, &testPinger{})
	session := "/api/v1/sessions/" + uuid.NewString()
	for _, tc := range []struct {
		name, authorization, method, path string
		status                            int
		code, challenge                   string
	}{
		{"no credentials", "", "GET", session, 401, "unauthenticated", `Bearer realm="latihan"`},
		{"invalid token", "Bearer forged", "GET", session, 401, "unauthenticated", `Bearer realm="latihan", error="invalid_token"`},
		{"wrong scheme", "Basic dXNlcjpwYXNz", "GET", session, 401, "unauthenticated", `Bearer realm="latihan", error="invalid_token"`},
		{"provider outage", "Bearer outage", "GET", session, 503, "auth_unavailable", ""},
		{"no profile yet", "Bearer test|newcomer", "GET", session, 403, "profile_required", ""},
		{"profile creation needs credentials", "", "POST", "/api/v1/users", 401, "unauthenticated", `Bearer realm="latihan"`},
		{"unknown routes stay 404", "", "GET", "/api/v1/nope", 404, "not_found", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := requestAs(h, tc.authorization, tc.method, tc.path, `{"display_name":"A"}`)
			var response errorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.status || response.Error.Code != tc.code {
				t.Fatalf("got %d %s", w.Code, w.Body)
			}
			if got := w.Header().Get("WWW-Authenticate"); got != tc.challenge {
				t.Fatalf("WWW-Authenticate %q, want %q", got, tc.challenge)
			}
			if strings.Contains(w.Body.String(), "private") {
				t.Fatal("provider error leaked")
			}
		})
	}
	for _, path := range []string{"/health", "/ready"} {
		if w := requestAs(h, "", "GET", path, ""); w.Code != 200 {
			t.Fatalf("%s must not require credentials: %d", path, w.Code)
		}
	}
}

type registeringAuth struct{ testAuth }

func (registeringAuth) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
}

func TestProviderRoutesArePublic(t *testing.T) {
	h := NewRouter(training.NewService(&fakeTraining{}), profile.NewService(fakeProfiles{}), &testPinger{}, registeringAuth{}, slog.New(slog.NewJSONHandler(io.Discard, nil)), Options{})
	if w := requestAs(h, "", "POST", "/api/v1/auth/login", "{}"); w.Code != http.StatusNoContent {
		t.Fatalf("provider route not mounted: %d", w.Code)
	}
}
func TestOwnerComesFromPrincipal(t *testing.T) {
	repo := &fakeTraining{}
	h := testRouter(repo, &testPinger{})
	if w := request(h, "GET", "/api/v1/sessions/"+uuid.NewString(), ""); w.Code != 200 || repo.owner != linkedUser {
		t.Fatalf("repository saw owner %s, want the caller %s (%d)", repo.owner, linkedUser, w.Code)
	}
	for _, path := range []string{"/api/v1/users/me", "/api/v1/users/" + linkedUser.String()} {
		w := request(h, "GET", path, "")
		var user profile.User
		if err := json.Unmarshal(w.Body.Bytes(), &user); err != nil || w.Code != 200 || user.ID != linkedUser {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
}
