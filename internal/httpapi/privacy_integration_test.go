//go:build integration

package httpapi

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/perdhevi/latihanAPI/internal/account"
	"github.com/perdhevi/latihanAPI/internal/profile"
	"github.com/perdhevi/latihanAPI/internal/ratelimit"
	"github.com/perdhevi/latihanAPI/internal/testdb"
	"github.com/perdhevi/latihanAPI/internal/training"
)

type exportDoc struct {
	Format       string                `json:"format"`
	ExportedAt   time.Time             `json:"exported_at"`
	User         profile.User          `json:"user"`
	Identities   []account.Identity    `json:"identities"`
	Measurements []profile.Measurement `json:"measurements"`
	Plans        []training.Plan       `json:"plans"`
	Sessions     []training.Session    `json:"sessions"`
}

func userID(t *testing.T, h http.Handler, subject string) uuid.UUID {
	t.Helper()
	var me profile.User
	if err := json.Unmarshal(send(h, subject, "GET", "/api/v1/users/me", nil, nil).Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	return me.ID
}

func count(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestExportIntegration(t *testing.T) {
	pool := testdb.Open(t)
	h := safeRouter(t, pool, Options{Export: ratelimit.MustParse("1/h:1")})
	send(h, "athlete", "POST", "/api/v1/users", profile.UserInput{DisplayName: "Athlete"}, nil)
	send(h, "other", "POST", "/api/v1/users", profile.UserInput{DisplayName: "Other"}, nil)
	me := userID(t, h, "athlete")
	send(h, "athlete", "POST", "/api/v1/plans", training.PlanInput{Name: "Leg day", Exercises: plannedExercises()}, nil)
	send(h, "athlete", "POST", "/api/v1/sessions", walk(20), nil)
	send(h, "other", "POST", "/api/v1/sessions", walk(99), nil)
	// More measurements than one export page, to cover the paging.
	if _, err := pool.Exec(t.Context(), `INSERT INTO measurements (id,user_id,measured_at,weight_kg)
		SELECT gen_random_uuid(), $1, now() - g * interval '1 day', 70 FROM generate_series(1, 250) g`, me); err != nil {
		t.Fatal(err)
	}

	w := send(h, "athlete", "GET", "/api/v1/account/export", nil, nil)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("export: %d %v", w.Code, w.Header())
	}
	var doc exportDoc
	dec := json.NewDecoder(w.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("export is not the documented JSON: %v", err)
	}
	if doc.Format != "latihan-export/1" || doc.User.ID != me || len(doc.Identities) != 1 || doc.Identities[0].Subject != "athlete" ||
		len(doc.Measurements) != 250 || len(doc.Plans) != 1 || len(doc.Sessions) != 1 || *doc.Sessions[0].Exercises[0].Minutes != 20 {
		t.Fatalf("export content: user %v, %d identities, %d measurements, %d plans, %d sessions",
			doc.User.ID, len(doc.Identities), len(doc.Measurements), len(doc.Plans), len(doc.Sessions))
	}
	ids := map[uuid.UUID]bool{}
	for _, m := range doc.Measurements {
		if ids[m.ID] || m.UserID != me {
			t.Fatal("measurement repeated across pages or not the caller's")
		}
		ids[m.ID] = true
	}
	if n := count(t, pool, `SELECT count(*) FROM audit_events WHERE user_id=$1 AND action='export'`, me); n != 1 {
		t.Fatalf("export not audited: %d", n)
	}
	if w := send(h, "athlete", "GET", "/api/v1/account/export", nil, nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("second export within the limit window: %d", w.Code)
	}
}

func TestErasureIntegration(t *testing.T) {
	pool := testdb.Open(t)
	h := safeRouter(t, pool, Options{})
	send(h, "athlete", "POST", "/api/v1/users", profile.UserInput{DisplayName: "Athlete"}, nil)
	send(h, "other", "POST", "/api/v1/users", profile.UserInput{DisplayName: "Other"}, nil)
	me, other := userID(t, h, "athlete"), userID(t, h, "other")
	plan := send(h, "athlete", "POST", "/api/v1/plans", training.PlanInput{Name: "Leg day", Exercises: plannedExercises()}, nil)
	var p training.Plan
	_ = json.Unmarshal(plan.Body.Bytes(), &p)
	session := walk(20)
	session.PlanID = &p.ID
	send(h, "athlete", "POST", "/api/v1/sessions", session, map[string]string{"Idempotency-Key": "erase-me"})
	send(h, "athlete", "POST", "/api/v1/users/me/measurements", profile.MeasurementInput{MeasuredAt: time.Now(), WeightKG: number(80)}, nil)
	send(h, "other", "POST", "/api/v1/sessions", walk(30), nil)

	// A plain delete still refuses to discard history; erasure is explicit.
	if w := send(h, "athlete", "DELETE", "/api/v1/users/me", nil, nil); w.Code != http.StatusConflict {
		t.Fatalf("plain delete with history: %d", w.Code)
	}
	if w := send(h, "athlete", "DELETE", "/api/v1/account", nil, nil); w.Code != http.StatusNoContent {
		t.Fatalf("erase: %d %s", w.Code, w.Body)
	}
	for table, query := range map[string]string{
		"user_profiles":    `SELECT count(*) FROM user_profiles WHERE id=$1`,
		"user_identities":  `SELECT count(*) FROM user_identities WHERE user_id=$1`,
		"measurements":     `SELECT count(*) FROM measurements WHERE user_id=$1`,
		"plans":            `SELECT count(*) FROM plans WHERE user_id=$1`,
		"sessions":         `SELECT count(*) FROM sessions WHERE user_id=$1`,
		"idempotency_keys": `SELECT count(*) FROM idempotency_keys WHERE subject='athlete' AND $1::uuid IS NOT NULL`,
	} {
		if n := count(t, pool, query, me); n != 0 {
			t.Errorf("%s still holds %d rows of the erased user", table, n)
		}
	}
	if n := count(t, pool, `SELECT count(*) FROM sessions WHERE user_id=$1`, other); n != 1 {
		t.Fatalf("another user's data was affected: %d sessions", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM audit_events WHERE user_id=$1 AND action='erase'`, me); n != 1 {
		t.Fatalf("erasure not audited: %d", n)
	}
	if w := send(h, "athlete", "GET", "/api/v1/users/me", nil, nil); w.Code != http.StatusForbidden {
		t.Fatalf("erased identity still has a profile: %d", w.Code)
	}
	// Erasing again is harmless, and the identity can start over.
	if w := send(h, "athlete", "DELETE", "/api/v1/account", nil, nil); w.Code != http.StatusNoContent {
		t.Fatalf("repeated erase: %d", w.Code)
	}
	if w := send(h, "athlete", "POST", "/api/v1/users", profile.UserInput{DisplayName: "Again"}, nil); w.Code != 201 {
		t.Fatalf("start over: %d", w.Code)
	}
	if userID(t, h, "athlete") == me {
		t.Fatal("new profile reused the erased user's ID")
	}
}

func TestAuditTrailIntegration(t *testing.T) {
	pool := testdb.Open(t)
	h := safeRouter(t, pool, Options{})
	send(h, "athlete", "POST", "/api/v1/users", profile.UserInput{DisplayName: "Athlete"}, nil)
	me := userID(t, h, "athlete")
	created := send(h, "athlete", "POST", "/api/v1/plans", training.PlanInput{Name: "Plan", Exercises: plannedExercises()}, nil)
	planURL := created.Header().Get("Location")
	planID := uuid.MustParse(strings.TrimPrefix(planURL, "/api/v1/plans/"))
	send(h, "athlete", "PUT", planURL, training.PlanInput{Name: "Renamed", Exercises: plannedExercises()}, nil)
	send(h, "athlete", "DELETE", planURL, nil, nil)
	// A failed write leaves no trace: the audit row rolls back with it.
	send(h, "athlete", "POST", "/api/v1/plans", training.PlanInput{Name: "", Exercises: plannedExercises()}, nil)

	rows, err := pool.Query(t.Context(), `SELECT action FROM audit_events WHERE user_id=$1 AND resource_type='plans' ORDER BY occurred_at`, me)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	if !slices.Equal(actions, []string{"insert", "update", "delete"}) {
		t.Fatalf("plan audit trail: %v", actions)
	}
	if n := count(t, pool, `SELECT count(*) FROM audit_events WHERE resource_id=$1`, planID); n != 3 {
		t.Fatalf("audit rows for the plan: %d", n)
	}
	// The audit log holds identifiers only, never the data itself.
	cols, err := pool.Query(t.Context(), `SELECT column_name FROM information_schema.columns WHERE table_name='audit_events' AND table_schema=current_schema() ORDER BY ordinal_position`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for cols.Next() {
		var c string
		_ = cols.Scan(&c)
		names = append(names, c)
	}
	if !slices.Equal(names, []string{"id", "occurred_at", "user_id", "action", "resource_type", "resource_id"}) {
		t.Fatalf("audit_events columns changed: %v", names)
	}
}

func TestAuditRetentionIntegration(t *testing.T) {
	pool := testdb.Open(t)
	store := account.NewStore(pool)
	if _, err := store.PurgeAudit(t.Context(), 24*time.Hour); err == nil || !strings.Contains(err.Error(), "at least 30 days") {
		t.Fatalf("a 1-day retention was accepted: %v", err)
	}
	user := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO audit_events (user_id, action, resource_type, resource_id, occurred_at)
		VALUES ($1,'insert','plans',$1, now() - interval '400 days'), ($1,'insert','plans',$1, now() - interval '10 days')`, user); err != nil {
		t.Fatal(err)
	}
	if n, err := store.PurgeAudit(t.Context(), 365*24*time.Hour); err != nil || n != 1 {
		t.Fatalf("purge: %d %v", n, err)
	}
	if n := count(t, pool, `SELECT count(*) FROM audit_events WHERE user_id=$1`, user); n != 1 {
		t.Fatalf("%d audit rows left", n)
	}
}
