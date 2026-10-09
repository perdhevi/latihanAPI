//go:build integration

package httpapi

import (
	"io"
	"log/slog"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/perdhevi/latihanAPI/internal/profile"
	"github.com/perdhevi/latihanAPI/internal/testdb"
	"github.com/perdhevi/latihanAPI/internal/training"
)

func TestCatalogPlansAndProgressIntegration(t *testing.T) {
	pool := testdb.Open(t)
	h := contract(t, NewRouter(training.NewService(training.NewPostgresRepository(pool)), profile.NewService(profile.NewPostgresRepository(pool)), pool, testAuth{}, slog.New(slog.NewJSONHandler(io.Discard, nil)), Options{}))
	call(t, h, "POST", "/api/v1/users", profile.UserInput{DisplayName: "Athlete"}, 201, nil)
	callAs(t, h, "other", "POST", "/api/v1/users", profile.UserInput{DisplayName: "Other"}, 201, nil)

	// The seeded catalog is readable, filterable and pageable.
	var catalog struct {
		Items      []training.CatalogExercise `json:"items"`
		NextCursor *string                    `json:"next_cursor"`
	}
	call(t, h, "GET", "/api/v1/exercises?limit=100", nil, 200, &catalog)
	byName := map[string]training.CatalogExercise{}
	for _, e := range catalog.Items {
		byName[e.Name] = e
	}
	squat, running, stretch := byName["Barbell Back Squat"], byName["Running"], byName["Hamstring Stretch"]
	if len(catalog.Items) < 30 || squat.ID == uuid.Nil || running.ID == uuid.Nil || stretch.ID == uuid.Nil {
		t.Fatalf("catalog not seeded: %d items", len(catalog.Items))
	}
	call(t, h, "GET", "/api/v1/exercises?limit=5", nil, 200, &catalog)
	if len(catalog.Items) != 5 || catalog.NextCursor == nil {
		t.Fatal("catalog pagination")
	}
	// Alphabetical by name, and paging in small steps visits the same entries.
	var paged []string
	for path := "/api/v1/exercises?limit=7"; ; {
		call(t, h, "GET", path, nil, 200, &catalog)
		for _, e := range catalog.Items {
			paged = append(paged, e.Name)
		}
		if catalog.NextCursor == nil {
			break
		}
		path = "/api/v1/exercises?limit=7&cursor=" + *catalog.NextCursor
	}
	var all []string
	call(t, h, "GET", "/api/v1/exercises?limit=100", nil, 200, &catalog)
	for _, e := range catalog.Items {
		all = append(all, e.Name)
	}
	if !slices.IsSorted(all) || !slices.Equal(all, paged) || len(paged) < 30 {
		t.Fatalf("catalog not alphabetical or paging differs: %v vs %v", all, paged)
	}
	call(t, h, "GET", "/api/v1/exercises?category=stretching&limit=100", nil, 200, &catalog)
	for _, e := range catalog.Items {
		if e.Category != "stretching" {
			t.Fatalf("category filter returned %q", e.Category)
		}
	}
	var one training.CatalogExercise
	call(t, h, "GET", "/api/v1/exercises/"+squat.ID.String(), nil, 200, &one)
	call(t, h, "GET", "/api/v1/exercises/"+uuid.NewString(), nil, 404, nil)

	// A catalog link fills in the name and kind; bad links are refused.
	planInput := training.PlanInput{Name: "Week plan", Exercises: []training.ExerciseInput{
		{ExerciseID: &squat.ID, Notes: "Slow descent", Sets: []training.Set{{Repetitions: 8, WeightKG: number(50)}, {Repetitions: 8, WeightKG: number(50)}}},
		{ExerciseID: &running.ID, Minutes: number(30)},
	}}
	var plan training.Plan
	call(t, h, "POST", "/api/v1/plans", planInput, 201, &plan)
	if plan.Exercises[0].Name != "Barbell Back Squat" || plan.Exercises[0].Kind != "strength" || plan.Exercises[0].Notes != "Slow descent" || plan.Exercises[1].Kind != "cardio" {
		t.Fatalf("catalog link not resolved: %+v", plan.Exercises)
	}
	unknown := uuid.New()
	call(t, h, "POST", "/api/v1/plans", training.PlanInput{Name: "Bad", Exercises: []training.ExerciseInput{{ExerciseID: &unknown, Minutes: number(5)}}}, 400, nil)
	call(t, h, "POST", "/api/v1/plans", training.PlanInput{Name: "Bad", Exercises: []training.ExerciseInput{{ExerciseID: &running.ID, Kind: "strength", Sets: []training.Set{{Repetitions: 1, WeightKG: number(1)}}}}}, 400, nil)
	call(t, h, "POST", "/api/v1/plans", training.PlanInput{Name: "Bad", Exercises: []training.ExerciseInput{{Name: "Free", Kind: "mobility"}}}, 400, nil)

	squatPlanned, runPlanned := plan.Exercises[0].ID, plan.Exercises[1].ID
	monday := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	// Followed the plan, did more reps at more weight, ran less, and added extras.
	first := training.SessionInput{Name: "Monday", PerformedAt: monday, PlanID: &plan.ID, Exercises: []training.SessionExercise{
		{PlanExerciseID: &squatPlanned, ExerciseInput: training.ExerciseInput{ExerciseID: &squat.ID, Sets: []training.Set{{Repetitions: 10, WeightKG: number(55)}}}},
		{PlanExerciseID: &runPlanned, ExerciseInput: training.ExerciseInput{ExerciseID: &running.ID, Minutes: number(25)}},
		{ExerciseInput: training.ExerciseInput{ExerciseID: &stretch.ID, Minutes: number(10)}},
	}}
	// Skipped the run.
	second := training.SessionInput{Name: "Wednesday", PerformedAt: monday.Add(48 * time.Hour), PlanID: &plan.ID, Exercises: []training.SessionExercise{
		{PlanExerciseID: &squatPlanned, ExerciseInput: training.ExerciseInput{ExerciseID: &squat.ID, Sets: []training.Set{{Repetitions: 8, WeightKG: number(50)}, {Repetitions: 8, WeightKG: number(50)}}}},
	}}
	// A free run the next week, with no plan.
	third := training.SessionInput{Name: "Free run", PerformedAt: monday.Add(7 * 24 * time.Hour), Exercises: []training.SessionExercise{
		{ExerciseInput: training.ExerciseInput{ExerciseID: &running.ID, Minutes: number(20)}},
	}}
	for _, in := range []training.SessionInput{first, second, third} {
		call(t, h, "POST", "/api/v1/sessions", in, 201, nil)
	}

	type progress struct {
		Bucket string                   `json:"bucket"`
		Data   []training.ProgressPoint `json:"data"`
	}
	window := "from=2026-09-14T00:00:00Z&to=2026-10-05T00:00:00Z"
	var got progress
	call(t, h, "GET", "/api/v1/stats/progress?bucket=week&"+window, nil, 200, &got)
	if len(got.Data) != 2 {
		t.Fatalf("want 2 weekly buckets, got %+v", got.Data)
	}
	week1, week2 := got.Data[0], got.Data[1]
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-6 }
	if !week1.Bucket.Equal(monday.Truncate(24*time.Hour)) || week1.Sessions != 2 ||
		week1.Planned.VolumeKG != 1600 || week1.Planned.Minutes != 60 || week1.Planned.Sets != 4 ||
		week1.Actual.VolumeKG != 1350 || week1.Actual.Minutes != 35 || week1.Unplanned.Minutes != 10 ||
		week1.Delta.VolumeKG != -250 || week1.VolumeDonePct == nil || !near(*week1.VolumeDonePct, 84.375) ||
		week1.MinutesDonePct == nil || !near(*week1.MinutesDonePct, 25.0/60*100) {
		t.Fatalf("week 1: %+v", week1)
	}
	if week2.Sessions != 1 || week2.Planned.Minutes != 0 || week2.Actual.Minutes != 20 || week2.Unplanned.Minutes != 20 || week2.VolumeDonePct != nil || week2.MinutesDonePct != nil {
		t.Fatalf("week 2: %+v", week2)
	}

	// Filtering by catalog exercise, and the day bucket.
	call(t, h, "GET", "/api/v1/stats/progress?exercise_id="+squat.ID.String()+"&"+window, nil, 200, &got)
	if len(got.Data) != 2 || got.Data[0].Planned.VolumeKG != 1600 || got.Data[0].Actual.VolumeKG != 1350 || got.Data[0].Actual.Minutes != 0 {
		t.Fatalf("squat only: %+v", got.Data)
	}
	call(t, h, "GET", "/api/v1/stats/progress?bucket=day&"+window, nil, 200, &got)
	if len(got.Data) != 3 {
		t.Fatalf("want 3 daily buckets, got %d", len(got.Data))
	}

	// Other users see nothing of it.
	callAs(t, h, "other", "GET", "/api/v1/stats/progress?"+window, nil, 200, &got)
	if len(got.Data) != 0 {
		t.Fatal("progress leaked into another user's statistics")
	}

	// A used plan keeps its targets; renaming it is fine.
	planInput.Exercises[0].Sets[0].Repetitions = 12
	call(t, h, "PUT", "/api/v1/plans/"+plan.ID.String(), planInput, 409, nil)
	planInput.Exercises[0].Sets[0].Repetitions = 8
	planInput.Name = "Renamed"
	call(t, h, "PUT", "/api/v1/plans/"+plan.ID.String(), planInput, 200, &plan)
	if plan.Exercises[0].ID != squatPlanned {
		t.Fatal("rename changed exercise ids")
	}
	// A plan nobody uses can be rewritten freely.
	var spare training.Plan
	call(t, h, "POST", "/api/v1/plans", planInput, 201, &spare)
	planInput.Exercises[0].Sets[0].Repetitions = 12
	call(t, h, "PUT", "/api/v1/plans/"+spare.ID.String(), planInput, 200, &spare)
	if spare.Exercises[0].Sets[0].Repetitions != 12 {
		t.Fatal("unused plan was not rewritten")
	}
	var reread training.Plan
	call(t, h, "GET", "/api/v1/plans/"+spare.ID.String(), nil, 200, &reread)
	if len(reread.Exercises) != 2 || reread.Exercises[0].Sets[0].Repetitions != 12 {
		t.Fatalf("stored plan: %+v", reread)
	}

	// Parameter validation.
	call(t, h, "GET", "/api/v1/stats/progress?bucket=year", nil, 400, nil)
	call(t, h, "GET", "/api/v1/stats/progress?from=2026-10-05T00:00:00Z&to=2026-09-14T00:00:00Z", nil, 400, nil)
}
