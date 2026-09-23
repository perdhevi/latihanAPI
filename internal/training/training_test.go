package training

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"math"
	"strings"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }
func strength(name string, reps int, kg float64) ExerciseInput {
	return ExerciseInput{Name: name, Kind: "strength", Sets: []Set{{Repetitions: reps, WeightKG: ptr(kg)}}}
}
func cardio(name string, minutes float64, bpm *int) ExerciseInput {
	return ExerciseInput{Name: name, Kind: "cardio", Minutes: ptr(minutes), AvgBPM: bpm}
}

func TestExerciseValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entry   ExerciseInput
		invalid bool
	}{
		{"strength", strength("Squat", 8, 50), false},
		{"bodyweight", strength("Push-Up", 10, 0), false},
		{"cardio", cardio("Run", 30, ptr(140)), false},
		{"unknown BPM", cardio("Walk", 20, nil), false},
		{"missing name", strength(" ", 8, 50), true},
		{"long name", strength(strings.Repeat("a", 201), 8, 50), true},
		{"null byte", strength("x\x00", 8, 50), true},
		{"zero repetitions", strength("Squat", 0, 50), true},
		{"negative weight", strength("Squat", 8, -1), true},
		{"missing weight", ExerciseInput{Name: "Squat", Kind: "strength", Sets: []Set{{Repetitions: 8}}}, true},
		{"no sets", ExerciseInput{Name: "Squat", Kind: "strength"}, true},
		{"too many sets", ExerciseInput{Name: "Squat", Kind: "strength", Sets: make([]Set, 101)}, true},
		{"zero minutes", cardio("Run", 0, nil), true},
		{"negative minutes", cardio("Run", -1, nil), true},
		{"bad bpm", cardio("Run", 10, ptr(301)), true},
		{"NaN", cardio("Run", math.NaN(), nil), true},
		{"infinity", strength("Squat", 8, math.Inf(1)), true},
		{"mixed metrics", ExerciseInput{Name: "Squat", Kind: "strength", Sets: []Set{{8, ptr(50.0)}}, Minutes: ptr(10.0)}, true},
		{"cardio sets", ExerciseInput{Name: "Run", Kind: "cardio", Minutes: ptr(10.0), Sets: []Set{{8, ptr(50.0)}}}, true},
		{"unknown kind", ExerciseInput{Name: "Squat", Kind: "other"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateExercise(&tc.entry)
			if (err != nil) != tc.invalid {
				t.Fatalf("error=%v invalid=%v", err, tc.invalid)
			}
		})
	}
}

func TestCompare(t *testing.T) {
	first, second, third := uuid.New(), uuid.New(), uuid.New()
	p := Plan{ID: uuid.New(), UpdatedAt: time.Now().UTC(), Exercises: []PlanExercise{
		{ID: first, ExerciseInput: ExerciseInput{Name: "Squat", Kind: "strength", Sets: []Set{{8, ptr(50.0)}, {8, ptr(50.0)}}}},
		{ID: second, ExerciseInput: cardio("Run", 30, ptr(140))},
		{ID: third, ExerciseInput: strength("Squat", 5, 60)},
	}}
	s := Session{ID: uuid.New(), PlanSnapshot: &p, SessionInput: SessionInput{UserID: uuid.New(), PlanID: &p.ID, Exercises: []SessionExercise{
		{PlanExerciseID: &first, ExerciseInput: strength("Back Squat", 10, 55)},
		{PlanExerciseID: &second, ExerciseInput: cardio("Run", 25, ptr(145))},
		{ExerciseInput: strength("Push-Up", 10, 0)},
	}}}
	c := Compare(s)
	if len(c.Exercises) != 4 || c.Exercises[0].Status != "matched" || c.Exercises[2].Status != "missed" || c.Exercises[3].Status != "unplanned" {
		t.Fatalf("bad matching: %+v", c.Exercises)
	}
	if c.PlannedTotals.VolumeKG != 1100 || c.ActualTotals.VolumeKG != 550 || c.Delta.VolumeKG != -550 || c.Delta.Minutes != -5 || c.Delta.Sets != -1 {
		t.Fatalf("bad totals: %+v", c)
	}
	firstSet := c.Exercises[0].Sets[0]
	if *firstSet.RepetitionsDelta != 2 || *firstSet.WeightKGDelta != 5 || c.Exercises[0].Sets[1].Actual != nil {
		t.Fatalf("bad set comparison: %+v", c.Exercises[0].Sets)
	}
	if *c.Exercises[1].AvgBPMDelta != 5 {
		t.Fatal("BPM difference incorrect")
	}
	s.Exercises[1].AvgBPM = nil
	if Compare(s).Exercises[1].AvgBPMDelta != nil {
		t.Fatal("missing BPM must remain unknown")
	}
}

func TestPlanLinks(t *testing.T) {
	id := uuid.New()
	plan := &Plan{Exercises: []PlanExercise{{ID: id, ExerciseInput: strength("Squat", 8, 50)}}}
	entry := SessionExercise{PlanExerciseID: &id, ExerciseInput: strength("Squat", 8, 50)}
	if err := validatePlanLinks(SessionInput{Exercises: []SessionExercise{entry}}, plan); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		entries []SessionExercise
		plan    *Plan
	}{
		{"no attached plan", []SessionExercise{entry}, nil},
		{"duplicate", []SessionExercise{entry, entry}, plan},
		{"foreign entry", []SessionExercise{{PlanExerciseID: ptr(uuid.New()), ExerciseInput: entry.ExerciseInput}}, plan},
		{"different kind", []SessionExercise{{PlanExerciseID: &id, ExerciseInput: cardio("Run", 10, nil)}}, plan},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validatePlanLinks(SessionInput{Exercises: tc.entries}, tc.plan); err == nil {
				t.Fatal("invalid link accepted")
			}
		})
	}
}

type serviceRepo struct {
	Repository
	ctx     context.Context
	plan    Plan
	session SessionInput
	calls   int
	err     error
}

func (r *serviceRepo) SavePlan(ctx context.Context, p Plan, create bool) (Plan, error) {
	r.ctx = ctx
	r.plan = p
	r.calls++
	return p, r.err
}
func (r *serviceRepo) SaveSession(ctx context.Context, id uuid.UUID, in SessionInput, create bool) (Session, error) {
	r.ctx = ctx
	r.session = in
	r.calls++
	return Session{ID: id, SessionInput: in}, r.err
}
func TestServiceValidationAndContext(t *testing.T) {
	repo := &serviceRepo{}
	s := NewService(repo)
	ctx := t.Context()
	user := uuid.New()
	p, err := s.SavePlan(ctx, uuid.Nil, PlanInput{UserID: user, Name: "  Leg day  ", Exercises: []ExerciseInput{strength(" Squat ", 8, 50)}}, true)
	if err != nil || p.ID == uuid.Nil || p.Exercises[0].ID == uuid.Nil || p.Name != "Leg day" || p.Exercises[0].Name != "Squat" || repo.ctx != ctx {
		t.Fatalf("save plan: %+v %v", p, err)
	}
	_, err = s.SaveSession(ctx, uuid.Nil, SessionInput{UserID: user, Name: "Done", Exercises: []SessionExercise{{ExerciseInput: strength("Squat", 8, 50)}}}, true)
	if err == nil || repo.calls != 1 {
		t.Fatal("missing performed_at reached repository")
	}
	_, err = s.SavePlan(ctx, uuid.Nil, PlanInput{UserID: uuid.Nil, Name: "Bad", Exercises: []ExerciseInput{strength("Squat", 8, 50)}}, true)
	if err == nil || repo.calls != 1 {
		t.Fatal("nil user reached repository")
	}
	failure := errors.New("database unavailable")
	repo.err = failure
	_, err = s.SaveSession(ctx, uuid.Nil, SessionInput{UserID: user, Name: "Done", PerformedAt: time.Now(), Exercises: []SessionExercise{{ExerciseInput: strength("Squat", 8, 50)}}}, true)
	if !errors.Is(err, failure) || repo.ctx != ctx {
		t.Fatal("repository error or context lost")
	}
}
