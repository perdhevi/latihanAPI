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
}

func (s *fakeTraining) GetSession(ctx context.Context, id uuid.UUID) (training.Session, error) {
	s.ctx = ctx
	if s.panicOnGet {
		panic("private panic")
	}
	return training.Session{ID: id}, s.err
}
func testRouter(repo *fakeTraining, p *testPinger) http.Handler {
	return NewRouter(training.NewService(repo), profile.NewService(nil), p, slog.New(slog.NewJSONHandler(io.Discard, nil)))
}
func request(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
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
		{"user required", "GET", "/api/v1/sessions", "", 400},
		{"zero limit", "GET", "/api/v1/sessions?user_id=" + id + "&limit=0", "", 400},
		{"overflow offset", "GET", "/api/v1/plans?user_id=" + id + "&offset=99999999999999999999", "", 400},
		{"duplicate query", "GET", "/api/v1/plans?user_id=" + id + "&limit=1&limit=2", "", 400},
		{"unknown query", "GET", "/api/v1/plans?user_id=" + id + "&sort=name", "", 400},
		{"comparison needs session", "GET", "/api/v1/plans/" + id + "/comparison", "", 400},
		{"invalid JSON", "POST", "/api/v1/users", "{", 400},
		{"empty JSON", "POST", "/api/v1/users", "", 400},
		{"null JSON", "POST", "/api/v1/users", "null", 400},
		{"array JSON", "POST", "/api/v1/users", "[]", 400},
		{"multiple JSON", "POST", "/api/v1/users", `{"display_name":"A"} {}`, 400},
		{"privileged id", "POST", "/api/v1/users", `{"display_name":"A","id":"` + id + `"}`, 400},
		{"blank name", "POST", "/api/v1/users", `{"display_name":" "}`, 400},
		{"empty plan", "POST", "/api/v1/plans", `{"user_id":"` + id + `","name":"Plan","exercises":[]}`, 400},
		{"missing metrics", "POST", "/api/v1/sessions", `{"user_id":"` + id + `","name":"Done","performed_at":"2026-09-23T08:00:00Z","exercises":[{"name":"Squat","kind":"strength"}]}`, 400},
		{"nested privileged id", "POST", "/api/v1/plans", `{"user_id":"` + id + `","name":"Plan","exercises":[{"id":"` + id + `","name":"Run","kind":"cardio","minutes":20}]}`, 400},
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
	h = NewRouter(training.NewService(&fakeTraining{panicOnGet: true}), profile.NewService(nil), p, slog.New(slog.NewJSONHandler(&logs, nil)))
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
	h.ServeHTTP(httptest.NewRecorder(), r)
	if !errors.Is(repo.ctx.Err(), context.Canceled) {
		t.Fatal("cancellation lost")
	}
	r = httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/users", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 415 {
		t.Fatalf("media type: %d", w.Code)
	}
}
