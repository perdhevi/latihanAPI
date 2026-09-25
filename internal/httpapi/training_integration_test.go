//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/perdhevi/latihanAPI/internal/profile"
	"github.com/perdhevi/latihanAPI/internal/testdb"
	"github.com/perdhevi/latihanAPI/internal/training"
	"github.com/perdhevi/latihanAPI/internal/validation"
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"
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
	raw := ""
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		raw = string(encoded)
	}
	w := request(h, method, path, raw)
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
	h := NewRouter(training.NewService(repo), profile.NewService(profile.NewPostgresRepository(pool)), pool, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	var user, other profile.User
	call(t, h, "POST", "/api/v1/users", profile.UserInput{DisplayName: " Athlete "}, 201, &user)
	call(t, h, "POST", "/api/v1/users", profile.UserInput{DisplayName: "Other"}, 201, &other)
	if user.DisplayName != "Athlete" || user.ID == uuid.Nil {
		t.Fatalf("user: %+v", user)
	}
	userURL := "/api/v1/users/" + user.ID.String()
	call(t, h, "PUT", userURL, profile.UserInput{DisplayName: "Updated"}, 200, &user)
	call(t, h, "GET", userURL, nil, 200, nil)

	planInput := training.PlanInput{UserID: user.ID, Name: "Leg day", Exercises: plannedExercises()}
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
	call(t, h, "GET", "/api/v1/plans?user_id="+user.ID.String(), nil, 200, &planPage)
	if len(planPage.Items) != 1 {
		t.Fatal("plan missing from list")
	}
	call(t, h, "GET", "/api/v1/plans?user_id="+other.ID.String(), nil, 200, &planPage)
	if len(planPage.Items) != 0 {
		t.Fatal("plan leaked into another user's list")
	}

	squatID, runningID := plan.Exercises[0].ID, plan.Exercises[1].ID
	in := training.SessionInput{UserID: user.ID, Name: "Morning training", PerformedAt: time.Date(2026, 9, 23, 8, 0, 0, 0, time.UTC), PlanID: &plan.ID, Exercises: []training.SessionExercise{
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
	wrong := in
	wrong.UserID = other.ID
	call(t, h, "POST", "/api/v1/sessions", wrong, 400, nil)
	call(t, h, "PUT", sessionURL, wrong, 404, nil)
	wrong = in
	wrong.Exercises = append([]training.SessionExercise(nil), in.Exercises...)
	wrong.Exercises[0].PlanExerciseID = &plan.Exercises[0].ID
	call(t, h, "PUT", sessionURL, wrong, 400, nil)
	call(t, h, "GET", comparisonURL, nil, 200, &comparison)
	if comparison.PlannedTotals.VolumeKG != 800 {
		t.Fatal("failed update changed session")
	}
	wrongPlan := planInput
	wrongPlan.UserID = other.ID
	call(t, h, "PUT", planURL, wrongPlan, 404, nil)

	// Standalone session, per-user filtering, chronological ordering and pagination.
	standalone := training.SessionInput{UserID: user.ID, Name: "Walk", PerformedAt: in.PerformedAt.Add(time.Hour), Exercises: []training.SessionExercise{{ExerciseInput: training.ExerciseInput{Name: "Walk", Kind: "cardio", Minutes: number(15)}}}}
	var second training.Session
	call(t, h, "POST", "/api/v1/sessions", standalone, 201, &second)
	if second.PlanSnapshot != nil || second.PlanID != nil {
		t.Fatal("standalone session has a plan")
	}
	var page struct {
		Items []training.Session `json:"items"`
	}
	call(t, h, "GET", "/api/v1/sessions?user_id="+user.ID.String()+"&limit=1", nil, 200, &page)
	if len(page.Items) != 1 || page.Items[0].ID != second.ID {
		t.Fatal("session ordering/pagination wrong")
	}
	call(t, h, "GET", "/api/v1/sessions?user_id="+user.ID.String()+"&limit=1&offset=1", nil, 200, &page)
	if len(page.Items) != 1 || page.Items[0].ID != session.ID {
		t.Fatal("session second page wrong")
	}
	call(t, h, "GET", "/api/v1/sessions?user_id="+other.ID.String(), nil, 200, &page)
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
		Items []profile.Measurement `json:"items"`
	}
	call(t, h, "GET", measurementsURL+"?limit=1", nil, 200, &history)
	if len(history.Items) != 1 || history.Items[0].ID != measurement.ID {
		t.Fatal("history ordering incorrect")
	}
	call(t, h, "GET", measurementsURL+"?limit=1&offset=1", nil, 200, &history)
	if len(history.Items) != 1 || history.Items[0].ID != older.ID {
		t.Fatal("history offset incorrect")
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
	call(t, h, "GET", userURL, nil, 404, nil)
	call(t, h, "GET", measurementsURL, nil, 404, nil)
	call(t, h, "POST", measurementsURL, mInput, 400, nil)
	call(t, h, "POST", "/api/v1/plans", planInput, 400, nil)
	standalone.UserID = user.ID
	call(t, h, "POST", "/api/v1/sessions", standalone, 400, nil)

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
	for _, path := range []string{"../../migrations/000002_create_training.down.sql", "../../migrations/000002_create_training.up.sql"} {
		sql, err := os.ReadFile(path)
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
