//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/perdhevi/latihanAPI/internal/idempotency"
	"github.com/perdhevi/latihanAPI/internal/profile"
	"github.com/perdhevi/latihanAPI/internal/testdb"
	"github.com/perdhevi/latihanAPI/internal/training"
)

func safeRouter(t *testing.T, pool *pgxpool.Pool, opts Options) http.Handler {
	opts.Idempotency = idempotency.NewStore(pool)
	return contract(t, NewRouter(training.NewService(training.NewPostgresRepository(pool)), profile.NewService(profile.NewPostgresRepository(pool)), pool, testAuth{}, slog.New(slog.NewJSONHandler(io.Discard, nil)), opts))
}

// send makes one request as subject with optional extra headers.
func send(h http.Handler, subject, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	raw := ""
	if body != nil {
		encoded, _ := json.Marshal(body)
		raw = string(encoded)
	}
	r := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer test."+subject)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func walk(minutes float64) training.SessionInput {
	return training.SessionInput{Name: "Walk", PerformedAt: time.Date(2026, 9, 25, 7, 0, 0, 0, time.UTC),
		Exercises: []training.SessionExercise{{ExerciseInput: training.ExerciseInput{Name: "Walk", Kind: "cardio", Minutes: number(minutes)}}}}
}

func countSessions(t *testing.T, h http.Handler, subject string) int {
	t.Helper()
	var page struct{ Items []training.Session }
	w := send(h, subject, "GET", "/api/v1/sessions?limit=100", nil, nil)
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return len(page.Items)
}

func TestIdempotencyKeysIntegration(t *testing.T) {
	pool := testdb.Open(t)
	h := safeRouter(t, pool, Options{})
	for _, who := range []string{"athlete", "other"} {
		send(h, who, "POST", "/api/v1/users", profile.UserInput{DisplayName: who}, nil)
	}
	key := map[string]string{"Idempotency-Key": "8e0f6a4c-first-try"}

	first := send(h, "athlete", "POST", "/api/v1/sessions", walk(20), key)
	if first.Code != 201 || first.Header().Get("Idempotent-Replayed") != "" {
		t.Fatalf("first: %d %s", first.Code, first.Body)
	}
	// The response was "lost"; the client retries with the same key.
	retry := send(h, "athlete", "POST", "/api/v1/sessions", walk(20), key)
	if retry.Code != 201 || retry.Header().Get("Idempotent-Replayed") != "true" || retry.Body.String() != first.Body.String() ||
		retry.Header().Get("Location") != first.Header().Get("Location") || retry.Header().Get("ETag") != first.Header().Get("ETag") {
		t.Fatalf("retry was not an exact replay: %d %v %s", retry.Code, retry.Header(), retry.Body)
	}
	if n := countSessions(t, h, "athlete"); n != 1 {
		t.Fatalf("retry created a duplicate: %d sessions", n)
	}
	if w := send(h, "athlete", "POST", "/api/v1/sessions", walk(25), key); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("same key, different body: %d", w.Code)
	}
	if w := send(h, "athlete", "POST", "/api/v1/plans", training.PlanInput{Name: "P", Exercises: plannedExercises()}, key); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("same key, different endpoint: %d", w.Code)
	}
	// Keys are private: another user's identical key is a new request.
	if w := send(h, "other", "POST", "/api/v1/sessions", walk(20), key); w.Code != 201 || w.Header().Get("Idempotent-Replayed") != "" {
		t.Fatalf("another user's key collided: %d %v", w.Code, w.Header())
	}

	// A client error is the request's answer too, and is replayed.
	bad := map[string]string{"Idempotency-Key": "bad-body"}
	if w := send(h, "athlete", "POST", "/api/v1/sessions", training.SessionInput{Name: "No exercises"}, bad); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := send(h, "athlete", "POST", "/api/v1/sessions", training.SessionInput{Name: "No exercises"}, bad); w.Code != 400 || w.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("client error not replayed: %d", w.Code)
	}

	for _, invalid := range []string{"has space", strings.Repeat("k", 256), "tab\tkey"} {
		if w := send(h, "athlete", "POST", "/api/v1/sessions", walk(20), map[string]string{"Idempotency-Key": invalid}); w.Code != 400 {
			t.Errorf("key %q accepted: %d", invalid, w.Code)
		}
	}

	// Still running: a retry must not run the request a second time.
	if _, err := pool.Exec(t.Context(), `UPDATE idempotency_keys SET status_code=NULL WHERE key=$1`, key["Idempotency-Key"]); err != nil {
		t.Fatal(err)
	}
	if w := send(h, "athlete", "POST", "/api/v1/sessions", walk(20), key); w.Code != http.StatusConflict || w.Header().Get("Retry-After") == "" {
		t.Fatalf("in-progress key: %d", w.Code)
	}
	// Abandoned for over a minute (the process died): the retry takes over and runs.
	if _, err := pool.Exec(t.Context(), `UPDATE idempotency_keys SET created_at=created_at - interval '2 minutes' WHERE key=$1`, key["Idempotency-Key"]); err != nil {
		t.Fatal(err)
	}
	if w := send(h, "athlete", "POST", "/api/v1/sessions", walk(20), key); w.Code != 201 || w.Header().Get("Idempotent-Replayed") != "" {
		t.Fatalf("abandoned claim not taken over: %d %v", w.Code, w.Header())
	}

	// Requests without a key are unaffected.
	send(h, "athlete", "POST", "/api/v1/sessions", walk(20), nil)
	send(h, "athlete", "POST", "/api/v1/sessions", walk(20), nil)
	if n := countSessions(t, h, "athlete"); n != 4 {
		t.Fatalf("want 4 sessions, have %d", n)
	}
}

// Parallel retries with one key create exactly one record.
func TestConcurrentIdempotentRequestsIntegration(t *testing.T) {
	pool := testdb.Open(t)
	h := safeRouter(t, pool, Options{})
	send(h, "athlete", "POST", "/api/v1/users", profile.UserInput{DisplayName: "A"}, nil)
	key := map[string]string{"Idempotency-Key": "parallel"}
	var wg sync.WaitGroup
	codes := make(chan string, 5)
	for range 5 {
		wg.Go(func() {
			w := send(h, "athlete", "POST", "/api/v1/sessions", walk(30), key)
			codes <- http.StatusText(w.Code) + "/" + w.Header().Get("Idempotent-Replayed")
		})
	}
	wg.Wait()
	close(codes)
	created := 0
	for c := range codes {
		switch c {
		case "Created/":
			created++
		case "Created/true", "Conflict/":
		default:
			t.Fatalf("unexpected outcome %s", c)
		}
	}
	if created != 1 || countSessions(t, h, "athlete") != 1 {
		t.Fatalf("%d requests executed, %d sessions exist", created, countSessions(t, h, "athlete"))
	}
}

func TestIdempotencyStore(t *testing.T) {
	pool := testdb.Open(t)
	s := idempotency.NewStore(pool)
	scope := idempotency.Scope{Issuer: "https://issuer.test", Subject: "s", Key: "k"}
	hash := make([]byte, 32)
	if o, _, err := s.Claim(t.Context(), scope, hash); err != nil || o != idempotency.Claimed {
		t.Fatalf("claim: %v %v", o, err)
	}
	// Released claims (server errors) can be claimed again at once.
	if err := s.Release(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	if o, _, _ := s.Claim(t.Context(), scope, hash); o != idempotency.Claimed {
		t.Fatalf("released key not reclaimable: %v", o)
	}
	_ = s.Complete(t.Context(), scope, idempotency.Response{Status: 201, Header: map[string]string{"Location": "/x"}, Body: []byte("{}")})
	if o, resp, _ := s.Claim(t.Context(), scope, hash); o != idempotency.Replay || resp.Status != 201 || resp.Header["Location"] != "/x" {
		t.Fatalf("replay: %v %+v", o, resp)
	}
	// Expired keys are purged, and an expired key can be reused.
	if _, err := pool.Exec(t.Context(), `UPDATE idempotency_keys SET created_at=created_at - interval '25 hours'`); err != nil {
		t.Fatal(err)
	}
	if o, _, _ := s.Claim(t.Context(), scope, []byte(strings.Repeat("x", 32))); o != idempotency.Claimed {
		t.Fatalf("expired key not reusable: %v", o)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE idempotency_keys SET created_at=created_at - interval '25 hours'`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Purge(t.Context()); err != nil || n != 1 {
		t.Fatalf("purge: %d %v", n, err)
	}
}

func TestIfMatchIntegration(t *testing.T) {
	pool := testdb.Open(t)
	h := safeRouter(t, pool, Options{})
	send(h, "athlete", "POST", "/api/v1/users", profile.UserInput{DisplayName: "A"}, nil)
	created := send(h, "athlete", "POST", "/api/v1/plans", training.PlanInput{Name: "Original", Exercises: plannedExercises()}, nil)
	planURL := created.Header().Get("Location")
	etag := created.Header().Get("ETag")
	if etag == "" || send(h, "athlete", "GET", planURL, nil, nil).Header().Get("ETag") != etag {
		t.Fatal("create and read must return the same ETag")
	}
	edit := func(name, ifMatch string) *httptest.ResponseRecorder {
		headers := map[string]string{}
		if ifMatch != "" {
			headers["If-Match"] = ifMatch
		}
		return send(h, "athlete", "PUT", planURL, training.PlanInput{Name: name, Exercises: plannedExercises()}, headers)
	}
	first := edit("Phone edit", etag)
	if first.Code != 200 || first.Header().Get("ETag") == etag {
		t.Fatalf("matching edit: %d, ETag %q", first.Code, first.Header().Get("ETag"))
	}
	// The tablet still holds the old ETag: its edit must not overwrite the phone's.
	stale := edit("Tablet edit", etag)
	if stale.Code != http.StatusPreconditionFailed || !strings.Contains(stale.Body.String(), "precondition_failed") {
		t.Fatalf("stale edit: %d %s", stale.Code, stale.Body)
	}
	var plan training.Plan
	_ = json.Unmarshal(send(h, "athlete", "GET", planURL, nil, nil).Body.Bytes(), &plan)
	if plan.Name != "Phone edit" {
		t.Fatalf("stale edit changed the plan: %q", plan.Name)
	}
	if w := edit("Weak", "W/"+first.Header().Get("ETag")); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("weak ETag matched: %d", w.Code)
	}
	if w := edit("Star", "*"); w.Code != 200 {
		t.Fatalf("If-Match: * : %d", w.Code)
	}
	if w := edit("No header", ""); w.Code != 200 {
		t.Fatalf("If-Match is optional by default: %d", w.Code)
	}
	current := send(h, "athlete", "GET", planURL, nil, nil).Header().Get("ETag")
	if w := send(h, "athlete", "DELETE", planURL, nil, map[string]string{"If-Match": etag}); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale delete: %d", w.Code)
	}
	if w := send(h, "athlete", "DELETE", planURL, nil, map[string]string{"If-Match": current}); w.Code != 204 {
		t.Fatalf("current delete: %d", w.Code)
	}
	// A missing record is 404, not 412, even with If-Match.
	if w := edit("Gone", current); w.Code != http.StatusNotFound {
		t.Fatalf("missing record: %d", w.Code)
	}
	// Another user's record is 404 too: If-Match must not reveal that it exists.
	other := send(h, "athlete", "POST", "/api/v1/plans", training.PlanInput{Name: "Mine", Exercises: plannedExercises()}, nil)
	send(h, "intruder", "POST", "/api/v1/users", profile.UserInput{DisplayName: "I"}, nil)
	if w := send(h, "intruder", "PUT", other.Header().Get("Location"), training.PlanInput{Name: "X", Exercises: plannedExercises()}, map[string]string{"If-Match": `"0"`}); w.Code != http.StatusNotFound {
		t.Fatalf("If-Match on another user's plan: %d", w.Code)
	}

	// Sessions check the version under a row lock; profiles and measurements in SQL.
	session := send(h, "athlete", "POST", "/api/v1/sessions", walk(10), nil)
	sessionTag := session.Header().Get("ETag")
	send(h, "athlete", "PUT", session.Header().Get("Location"), walk(11), nil)
	if w := send(h, "athlete", "PUT", session.Header().Get("Location"), walk(12), map[string]string{"If-Match": sessionTag}); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale session edit: %d", w.Code)
	}
	me := send(h, "athlete", "GET", "/api/v1/users/me", nil, nil).Header().Get("ETag")
	send(h, "athlete", "PUT", "/api/v1/users/me", profile.UserInput{DisplayName: "B"}, nil)
	if w := send(h, "athlete", "PUT", "/api/v1/users/me", profile.UserInput{DisplayName: "C"}, map[string]string{"If-Match": me}); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale profile edit: %d", w.Code)
	}
	m := send(h, "athlete", "POST", "/api/v1/users/me/measurements", profile.MeasurementInput{MeasuredAt: time.Now(), WeightKG: number(70)}, nil)
	mURL := m.Header().Get("Location")
	send(h, "athlete", "PUT", mURL, profile.MeasurementInput{MeasuredAt: time.Now(), WeightKG: number(71)}, nil)
	if w := send(h, "athlete", "DELETE", mURL, nil, map[string]string{"If-Match": m.Header().Get("ETag")}); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale measurement delete: %d", w.Code)
	}
}

// Two writers holding the same ETag: exactly one wins.
func TestConcurrentConditionalWritesIntegration(t *testing.T) {
	pool := testdb.Open(t)
	h := safeRouter(t, pool, Options{})
	send(h, "athlete", "POST", "/api/v1/users", profile.UserInput{DisplayName: "A"}, nil)
	created := send(h, "athlete", "POST", "/api/v1/plans", training.PlanInput{Name: "P", Exercises: plannedExercises()}, nil)
	for round := range 10 {
		etag := send(h, "athlete", "GET", created.Header().Get("Location"), nil, nil).Header().Get("ETag")
		var wg sync.WaitGroup
		codes := make(chan int, 2)
		for writer := range 2 {
			wg.Go(func() {
				codes <- send(h, "athlete", "PUT", created.Header().Get("Location"),
					training.PlanInput{Name: "writer " + string(rune('A'+writer)), Exercises: plannedExercises()}, map[string]string{"If-Match": etag}).Code
			})
		}
		wg.Wait()
		close(codes)
		got := map[int]int{}
		for c := range codes {
			got[c]++
		}
		if got[200] != 1 || got[412] != 1 {
			t.Fatalf("round %d: %v", round, got)
		}
	}
}

func TestRequireIfMatchIntegration(t *testing.T) {
	pool := testdb.Open(t)
	h := safeRouter(t, pool, Options{RequireIfMatch: true})
	send(h, "athlete", "POST", "/api/v1/users", profile.UserInput{DisplayName: "A"}, nil)
	if w := send(h, "athlete", "PUT", "/api/v1/users/me", profile.UserInput{DisplayName: "B"}, nil); w.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match: %d", w.Code)
	}
	etag := send(h, "athlete", "GET", "/api/v1/users/me", nil, nil).Header().Get("ETag")
	if w := send(h, "athlete", "PUT", "/api/v1/users/me", profile.UserInput{DisplayName: "B"}, map[string]string{"If-Match": etag}); w.Code != 200 {
		t.Fatalf("with If-Match: %d", w.Code)
	}
}
