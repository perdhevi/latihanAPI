//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/perdhevi/latihanAPI/internal/profile"
	"github.com/perdhevi/latihanAPI/internal/testdb"
	"github.com/perdhevi/latihanAPI/internal/training"
	"github.com/perdhevi/latihanAPI/internal/validation"
)

func number(v float64) *float64 { return &v }
func bpm(v int) *int            { return &v }
func plannedExercises() []training.ExerciseInput {
	return []training.ExerciseInput{
		{Name: "Squat", Kind: "strength", Sets: []training.Set{{Repetitions: 8, WeightKG: number(50)}, {Repetitions: 8, WeightKG: number(50)}}},
		{Name: "Running", Kind: "cardio", Minutes: number(30), AvgBPM: bpm(140)},
	}
}
func call(t *testing.T, h http.Handler, method, path string, body any, status int, out any) {
	t.Helper()
	callAs(t, h, "athlete", method, path, body, status, out)
}
func callAs(t *testing.T, h http.Handler, subject, method, path string, body any, status int, out any) {
	t.Helper()
	raw := ""
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		raw = string(encoded)
	}
	w := requestAs(h, "Bearer test|"+subject, method, path, raw)
	if w.Code != status {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, status, w.Body)
	}
	if out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	if status == 201 && w.Header().Get("Location") == "" {
		t.Fatal("missing Location")
	}
	if status == 204 && w.Body.Len() != 0 {
		t.Fatal("204 must have no body")
	}
}

func TestTrainingAPIIntegration(t *testing.T) {
	pool := testdb.Open(t)
	repo := training.NewPostgresRepository(pool)
	h := NewRouter(training.NewService(repo), profile.NewService(profile.NewPostgresRepository(pool)), pool, testAuth{}, slog.New(slog.NewJSONHandler(io.Discard, nil)), Options{})
	var user, other profile.User
	call(t, h, "POST", "/api/v1/users", profile.UserInput{DisplayName: " Athlete "}, 201, &user)
	callAs(t, h, "other", "POST", "/api/v1/users", profile.UserInput{DisplayName: "Other"}, 201, &other)
	// One identity, one profile.
	call(t, h, "POST", "/api/v1/users", profile.UserInput{DisplayName: "Twin"}, 409, nil)
	if user.DisplayName != "Athlete" || user.ID == uuid.Nil {
		t.Fatalf("user: %+v", user)
	}
	userURL := "/api/v1/users/" + user.ID.String()
	call(t, h, "PUT", userURL, profile.UserInput{DisplayName: "Updated"}, 200, &user)
	call(t, h, "GET", userURL, nil, 200, nil)

	planInput := training.PlanInput{Name: "Leg day", Exercises: plannedExercises()}
	var plan training.Plan
	call(t, h, "POST", "/api/v1/plans", planInput, 201, &plan)
	planURL := "/api/v1/plans/" + plan.ID.String()
	if plan.Exercises[0].ID == plan.Exercises[1].ID || plan.CreatedAt.IsZero() {
		t.Fatal("invalid plan IDs/timestamps")
	}
	call(t, h, "GET", planURL, nil, 200, nil)
	var planPage struct {
		Items []training.Plan `json:"items"`
	}
	call(t, h, "GET", "/api/v1/plans", nil, 200, &planPage)
	if len(planPage.Items) != 1 {
		t.Fatal("plan missing from list")
	}
	callAs(t, h, "other", "GET", "/api/v1/plans", nil, 200, &planPage)
	if len(planPage.Items) != 0 {
		t.Fatal("plan leaked into another user's list")
	}

	squatID, runningID := plan.Exercises[0].ID, plan.Exercises[1].ID
	in := training.SessionInput{Name: "Morning training", PerformedAt: time.Date(2026, 9, 23, 8, 0, 0, 0, time.UTC), PlanID: &plan.ID, Exercises: []training.SessionExercise{
		{PlanExerciseID: &squatID, ExerciseInput: training.ExerciseInput{Name: "Squat", Kind: "strength", Sets: []training.Set{{Repetitions: 10, WeightKG: number(55)}}}},
		{PlanExerciseID: &runningID, ExerciseInput: training.ExerciseInput{Name: "Running", Kind: "cardio", Minutes: number(25), AvgBPM: bpm(145)}},
	}}
	var session training.Session
	call(t, h, "POST", "/api/v1/sessions", in, 201, &session)
	if _, err := pool.Exec(t.Context(), `UPDATE sessions SET user_id=$2 WHERE id=$1`, session.ID, other.ID); err == nil {
		t.Fatal("database allowed a session to reference another user's plan")
	}
	sessionURL := "/api/v1/sessions/" + session.ID.String()
	comparisonURL := planURL + "/comparison?session_id=" + session.ID.String()
	var comparison training.Comparison
	call(t, h, "GET", comparisonURL, nil, 200, &comparison)
	if comparison.Delta.VolumeKG != -250 || comparison.Delta.Minutes != -5 || *comparison.Exercises[1].AvgBPMDelta != 5 {
		t.Fatalf("comparison: %+v", comparison)
	}
	call(t, h, "DELETE", planURL, nil, 409, nil)
	call(t, h, "DELETE", userURL, nil, 409, nil)

	// A later edit of the reusable plan must not rewrite history.
	originalPlanTime := plan.UpdatedAt
	planInput.Exercises[0].Sets[0].WeightKG = number(100)
	call(t, h, "PUT", planURL, planInput, 200, &plan)
	if !plan.UpdatedAt.After(originalPlanTime) {
		t.Fatal("plan timestamp did not advance")
	}
	call(t, h, "GET", comparisonURL, nil, 200, &comparison)
	if comparison.PlannedTotals.VolumeKG != 800 || !comparison.PlanUpdatedAt.Equal(originalPlanTime) {
		t.Fatal("plan edit changed historical comparison")
	}
	in.Name = "Edited session"
	oldCreated, oldUpdated := session.CreatedAt, session.UpdatedAt
	call(t, h, "PUT", sessionURL, in, 200, &session)
	if !session.CreatedAt.Equal(oldCreated) || !session.UpdatedAt.After(oldUpdated) || !session.PlanSnapshot.UpdatedAt.Equal(originalPlanTime) {
		t.Fatal("same-plan update must retain snapshot")
	}
	call(t, h, "GET", sessionURL, nil, 200, &session)

	// Bad links and cross-user writes must leave the stored session intact.
	callAs(t, h, "other", "PUT", sessionURL, in, 404, nil)
	wrong := in
	wrong.Exercises = append([]training.SessionExercise(nil), in.Exercises...)
	wrong.Exercises[0].PlanExerciseID = &plan.Exercises[0].ID
	call(t, h, "PUT", sessionURL, wrong, 400, nil)
	call(t, h, "GET", comparisonURL, nil, 200, &comparison)
	if comparison.PlannedTotals.VolumeKG != 800 {
		t.Fatal("failed update changed session")
	}
	callAs(t, h, "other", "PUT", planURL, planInput, 404, nil)

	// Standalone session, per-user filtering, chronological ordering and pagination.
	standalone := training.SessionInput{Name: "Walk", PerformedAt: in.PerformedAt.Add(time.Hour), Exercises: []training.SessionExercise{{ExerciseInput: training.ExerciseInput{Name: "Walk", Kind: "cardio", Minutes: number(15)}}}}
	var second training.Session
	call(t, h, "POST", "/api/v1/sessions", standalone, 201, &second)
	if second.PlanSnapshot != nil || second.PlanID != nil {
		t.Fatal("standalone session has a plan")
	}
	var page struct {
		Items      []training.Session `json:"items"`
		NextCursor *string            `json:"next_cursor"`
	}
	call(t, h, "GET", "/api/v1/sessions?limit=1", nil, 200, &page)
	if len(page.Items) != 1 || page.Items[0].ID != second.ID || page.NextCursor == nil {
		t.Fatal("session ordering/pagination wrong")
	}
	call(t, h, "GET", "/api/v1/sessions?limit=1&cursor="+*page.NextCursor, nil, 200, &page)
	if len(page.Items) != 1 || page.Items[0].ID != session.ID || page.NextCursor != nil {
		t.Fatal("session second page wrong")
	}
	callAs(t, h, "other", "GET", "/api/v1/sessions", nil, 200, &page)
	if len(page.Items) != 0 {
		t.Fatal("session leaked into another user's list")
	}
	call(t, h, "GET", planURL+"/comparison?session_id="+second.ID.String(), nil, 404, nil)

	// Detaching removes the snapshot; reattaching captures the current plan version.
	detached := in
	detached.PlanID = nil
	detached.Exercises = append([]training.SessionExercise(nil), in.Exercises...)
	for i := range detached.Exercises {
		detached.Exercises[i].PlanExerciseID = nil
	}
	call(t, h, "PUT", sessionURL, detached, 200, &session)
	if session.PlanSnapshot != nil {
		t.Fatal("detached plan snapshot remains")
	}
	detached.PlanID = &plan.ID
	detached.Exercises[0].PlanExerciseID = &plan.Exercises[0].ID
	detached.Exercises[1].PlanExerciseID = &plan.Exercises[1].ID
	call(t, h, "PUT", sessionURL, detached, 200, &session)
	call(t, h, "GET", comparisonURL, nil, 200, &comparison)
	if comparison.PlannedTotals.VolumeKG != 1200 {
		t.Fatal("reattach did not take current plan")
	}

	// User profile history uses measurement time, not insertion time.
	mInput := profile.MeasurementInput{MeasuredAt: in.PerformedAt, WeightKG: number(80), HeightCM: number(180), BodyFatPercent: number(15), WaistCM: number(80), ChestCM: number(100), HipCM: number(90)}
	var measurement, older profile.Measurement
	measurementsURL := userURL + "/measurements"
	call(t, h, "POST", measurementsURL, mInput, 201, &measurement)
	mInput.MeasuredAt = mInput.MeasuredAt.Add(-24 * time.Hour)
	mInput.WeightKG = number(81)
	call(t, h, "POST", measurementsURL, mInput, 201, &older)
	var history struct {
		Items      []profile.Measurement `json:"items"`
		NextCursor *string               `json:"next_cursor"`
	}
	call(t, h, "GET", measurementsURL+"?limit=1", nil, 200, &history)
	if len(history.Items) != 1 || history.Items[0].ID != measurement.ID || history.NextCursor == nil {
		t.Fatal("history ordering incorrect")
	}
	call(t, h, "GET", measurementsURL+"?limit=1&cursor="+*history.NextCursor, nil, 200, &history)
	if len(history.Items) != 1 || history.Items[0].ID != older.ID {
		t.Fatal("history cursor incorrect")
	}
	measurementURL := measurementsURL + "/" + measurement.ID.String()
	otherURL := "/api/v1/users/" + other.ID.String() + "/measurements/" + measurement.ID.String()
	call(t, h, "GET", otherURL, nil, 404, nil)
	call(t, h, "PUT", otherURL, mInput, 404, nil)
	call(t, h, "DELETE", otherURL, nil, 404, nil)
	mInput = profile.MeasurementInput{MeasuredAt: in.PerformedAt, WeightKG: number(79)}
	call(t, h, "PUT", measurementURL, mInput, 200, &measurement)
	if measurement.HeightCM != nil || *measurement.WeightKG != 79 {
		t.Fatal("measurement replacement failed")
	}
	call(t, h, "GET", measurementURL, nil, 200, nil)
	call(t, h, "DELETE", measurementURL, nil, 204, nil)
	call(t, h, "DELETE", measurementURL, nil, 404, nil)
	call(t, h, "DELETE", measurementsURL+"/"+older.ID.String(), nil, 204, nil)

	call(t, h, "DELETE", sessionURL, nil, 204, nil)
	call(t, h, "GET", sessionURL, nil, 404, nil)
	call(t, h, "PUT", sessionURL, in, 404, nil)
	call(t, h, "DELETE", sessionURL, nil, 404, nil)
	call(t, h, "DELETE", "/api/v1/sessions/"+second.ID.String(), nil, 204, nil)
	call(t, h, "DELETE", planURL, nil, 204, nil)
	call(t, h, "GET", planURL, nil, 404, nil)
	call(t, h, "PUT", planURL, planInput, 404, nil)
	call(t, h, "DELETE", userURL, nil, 204, nil)
	// The deleted user's identity has no profile any more.
	call(t, h, "GET", "/api/v1/plans", nil, 403, nil)
	// The deleted user's paths, requested by someone who still has a profile.
	callAs(t, h, "other", "GET", userURL, nil, 404, nil)
	callAs(t, h, "other", "GET", measurementsURL, nil, 404, nil)
	callAs(t, h, "other", "POST", measurementsURL, mInput, 404, nil)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := repo.ListSessions(ctx, other.ID, validation.Page{Limit: 20}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not propagated: %v", err)
	}
}

func TestMigrationRoundTrip(t *testing.T) {
	pool := testdb.Open(t)
	legacyID := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO exercises (id,name,category) VALUES ($1,'Legacy','strength')`, legacyID); err != nil {
		t.Fatal(err)
	}
	// Roll back every migration after the original catalog, newest first, then reapply them.
	downs, err := filepath.Glob("../../migrations/*.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(downs)
	downs = slices.DeleteFunc(downs, func(p string) bool { return strings.Contains(p, "000001_") })
	ups, err := filepath.Glob("../../migrations/*.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	ups = slices.DeleteFunc(ups, func(p string) bool { return strings.Contains(p, "000001_") })
	for _, path := range append(downs, ups...) {
		sql, err := os.ReadFile(path) //nolint:gosec // G304: fixed glob of this repository's migrations
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(t.Context(), string(sql)); err != nil {
			t.Fatal(err)
		}
	}
	// The original exercise catalog survives the upgrade and rollback.
	var name string
	if err := pool.QueryRow(t.Context(), `SELECT name FROM exercises WHERE id=$1`, legacyID).Scan(&name); err != nil || name != "Legacy" {
		t.Fatal(err)
	}
}

func TestIdentityLinkingIntegration(t *testing.T) {
	pool := testdb.Open(t)
	h := NewRouter(training.NewService(training.NewPostgresRepository(pool)), profile.NewService(profile.NewPostgresRepository(pool)), pool, testAuth{}, slog.New(slog.NewJSONHandler(io.Discard, nil)), Options{})
	callAs(t, h, "newcomer", "GET", "/api/v1/sessions", nil, 403, nil)
	var user profile.User
	callAs(t, h, "newcomer", "POST", "/api/v1/users", profile.UserInput{DisplayName: "New"}, 201, &user)
	callAs(t, h, "newcomer", "GET", "/api/v1/sessions", nil, 200, nil)
	// Deleting the profile releases the identity, which can then start over.
	callAs(t, h, "newcomer", "DELETE", "/api/v1/users/"+user.ID.String(), nil, 204, nil)
	callAs(t, h, "newcomer", "GET", "/api/v1/sessions", nil, 403, nil)
	callAs(t, h, "newcomer", "POST", "/api/v1/users", profile.UserInput{DisplayName: "Again"}, 201, nil)
}

// TestCrossUserAccessIntegration is the authorization contract: an
// authenticated intruder who knows every ID of a victim's records can neither
// read nor change them, and learns nothing beyond "not found".
func TestCrossUserAccessIntegration(t *testing.T) {
	pool := testdb.Open(t)
	h := NewRouter(training.NewService(training.NewPostgresRepository(pool)), profile.NewService(profile.NewPostgresRepository(pool)), pool, testAuth{}, slog.New(slog.NewJSONHandler(io.Discard, nil)), Options{})
	var victim, intruder profile.User
	callAs(t, h, "victim", "POST", "/api/v1/users", profile.UserInput{DisplayName: "Victim"}, 201, &victim)
	callAs(t, h, "intruder", "POST", "/api/v1/users", profile.UserInput{DisplayName: "Intruder"}, 201, &intruder)

	planInput := training.PlanInput{Name: "Private plan", Exercises: plannedExercises()}
	var plan training.Plan
	callAs(t, h, "victim", "POST", "/api/v1/plans", planInput, 201, &plan)
	squatID := plan.Exercises[0].ID
	sessionInput := training.SessionInput{Name: "Private session", PerformedAt: time.Date(2026, 9, 23, 8, 0, 0, 0, time.UTC), PlanID: &plan.ID, Exercises: []training.SessionExercise{
		{PlanExerciseID: &squatID, ExerciseInput: training.ExerciseInput{Name: "Squat", Kind: "strength", Sets: []training.Set{{Repetitions: 5, WeightKG: number(60)}}}},
	}}
	var session training.Session
	callAs(t, h, "victim", "POST", "/api/v1/sessions", sessionInput, 201, &session)
	var measurement profile.Measurement
	measurementInput := profile.MeasurementInput{MeasuredAt: sessionInput.PerformedAt, WeightKG: number(70)}
	callAs(t, h, "victim", "POST", "/api/v1/users/me/measurements", measurementInput, 201, &measurement)

	userURL := "/api/v1/users/" + victim.ID.String()
	planURL := "/api/v1/plans/" + plan.ID.String()
	sessionURL := "/api/v1/sessions/" + session.ID.String()
	measurementURL := userURL + "/measurements/" + measurement.ID.String()
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"GET", userURL, nil},
		{"PUT", userURL, profile.UserInput{DisplayName: "Hacked"}},
		{"DELETE", userURL, nil},
		{"GET", userURL + "/measurements", nil},
		{"POST", userURL + "/measurements", measurementInput},
		{"GET", measurementURL, nil},
		{"PUT", measurementURL, measurementInput},
		{"DELETE", measurementURL, nil},
		{"GET", planURL, nil},
		{"PUT", planURL, planInput},
		{"DELETE", planURL, nil},
		{"GET", planURL + "/comparison?session_id=" + session.ID.String(), nil},
		{"GET", sessionURL, nil},
		{"PUT", sessionURL, sessionInput},
		{"DELETE", sessionURL, nil},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			callAs(t, h, "intruder", tc.method, tc.path, tc.body, 404, nil)
		})
	}

	// Lists only ever show the caller's own records.
	var plans struct{ Items []training.Plan }
	callAs(t, h, "intruder", "GET", "/api/v1/plans", nil, 200, &plans)
	var sessions struct{ Items []training.Session }
	callAs(t, h, "intruder", "GET", "/api/v1/sessions", nil, 200, &sessions)
	if len(plans.Items) != 0 || len(sessions.Items) != 0 {
		t.Fatal("another user's records appeared in a list")
	}
	// Attaching someone else's plan, or linking to its entries, is refused.
	stolen := sessionInput
	callAs(t, h, "intruder", "POST", "/api/v1/sessions", stolen, 400, nil)
	stolen.PlanID = nil
	callAs(t, h, "intruder", "POST", "/api/v1/sessions", stolen, 400, nil)

	// The victim's data is exactly as it was.
	var got training.Plan
	callAs(t, h, "victim", "GET", planURL, nil, 200, &got)
	if got.Name != "Private plan" || !got.UpdatedAt.Equal(plan.UpdatedAt) {
		t.Fatalf("plan changed: %+v", got)
	}
	callAs(t, h, "victim", "GET", sessionURL, nil, 200, nil)
	callAs(t, h, "victim", "GET", "/api/v1/users/me/measurements/"+measurement.ID.String(), nil, 200, nil)
	var me profile.User
	callAs(t, h, "victim", "GET", "/api/v1/users/me", nil, 200, &me)
	if me.ID != victim.ID || me.DisplayName != "Victim" {
		t.Fatalf("profile changed: %+v", me)
	}
}

// Paging with cursors visits every item exactly once, even when items with the
// same timestamp straddle a page boundary or new items arrive mid-way.
func TestCursorPaginationIntegration(t *testing.T) {
	pool := testdb.Open(t)
	h := NewRouter(training.NewService(training.NewPostgresRepository(pool)), profile.NewService(profile.NewPostgresRepository(pool)), pool, testAuth{}, slog.New(slog.NewJSONHandler(io.Discard, nil)), Options{})
	call(t, h, "POST", "/api/v1/users", profile.UserInput{DisplayName: "Pager"}, 201, nil)
	base := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	want := map[uuid.UUID]bool{}
	for i := range 7 {
		// Pairs share a timestamp so ties must be broken by ID.
		at := base.Add(time.Duration(i/2) * time.Hour)
		var s training.Session
		call(t, h, "POST", "/api/v1/sessions", training.SessionInput{Name: "S", PerformedAt: at, Exercises: []training.SessionExercise{{ExerciseInput: training.ExerciseInput{Name: "Walk", Kind: "cardio", Minutes: number(10)}}}}, 201, &s)
		want[s.ID] = true
	}
	seen := map[uuid.UUID]bool{}
	path := "/api/v1/sessions?limit=3"
	var previous time.Time
	for pages := 0; ; pages++ {
		var page struct {
			Items      []training.Session `json:"items"`
			NextCursor *string            `json:"next_cursor"`
		}
		call(t, h, "GET", path, nil, 200, &page)
		for _, s := range page.Items {
			if seen[s.ID] {
				t.Fatalf("session %s returned twice", s.ID)
			}
			if !previous.IsZero() && s.PerformedAt.After(previous) {
				t.Fatal("pages out of order")
			}
			seen[s.ID], previous = true, s.PerformedAt
		}
		if pages == 0 {
			// A newer session arriving mid-way must not shift later pages.
			call(t, h, "POST", "/api/v1/sessions", training.SessionInput{Name: "Late", PerformedAt: base.Add(24 * time.Hour), Exercises: []training.SessionExercise{{ExerciseInput: training.ExerciseInput{Name: "Walk", Kind: "cardio", Minutes: number(10)}}}}, 201, nil)
		}
		if page.NextCursor == nil {
			break
		}
		path = "/api/v1/sessions?limit=3&cursor=" + *page.NextCursor
	}
	if len(seen) != len(want) {
		t.Fatalf("saw %d of %d sessions", len(seen), len(want))
	}
}
