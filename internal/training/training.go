package training

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/perdhevi/latihanAPI/internal/validation"
)

var ErrNotFound = errors.New("session or plan not found")
var ErrReference = errors.New("user, plan or exercise does not exist for this user")
var ErrConflict = errors.New("record has dependent sessions")

// ErrPlanInUse reports an attempt to change the exercises of a plan that
// sessions already refer to. Their comparisons and the progress statistics rely
// on the planned targets staying what they were; a plan's name and notes can
// still change, and new targets belong in a new plan.
var ErrPlanInUse = errors.New("plan exercises cannot change once sessions use the plan")

// errNoOwner means a caller reached the service without an authenticated owner:
// a wiring bug, never a client error, so it surfaces as a 500.
var errNoOwner = errors.New("training: owner is required")

type Set struct {
	Repetitions int      `json:"repetitions"`
	WeightKG    *float64 `json:"weight_kg"`
}

// ExerciseInput describes one exercise, planned or performed. ExerciseID links
// the shared catalog, from which a missing name and kind are filled in; without
// it the exercise is free-form and name and kind are required. Strength
// exercises have sets; the other kinds are timed.
type ExerciseInput struct {
	ExerciseID *uuid.UUID `json:"exercise_id,omitempty"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	Notes      string     `json:"notes"`
	Sets       []Set      `json:"sets,omitempty"`
	Minutes    *float64   `json:"minutes,omitempty"`
	AvgBPM     *int       `json:"avg_bpm,omitempty"`
}
type PlanExercise struct {
	ID uuid.UUID `json:"id"`
	ExerciseInput
}
type SessionExercise struct {
	PlanExerciseID *uuid.UUID `json:"plan_exercise_id,omitempty"`
	ExerciseInput
}

// PlanInput and SessionInput carry no owner: it always comes from the
// authenticated caller, so a client cannot write into another user's account.
type PlanInput struct {
	Name      string          `json:"name"`
	Notes     string          `json:"notes"`
	Exercises []ExerciseInput `json:"exercises"`
}
type Plan struct {
	ID        uuid.UUID      `json:"id"`
	UserID    uuid.UUID      `json:"user_id"`
	Name      string         `json:"name"`
	Notes     string         `json:"notes"`
	Exercises []PlanExercise `json:"exercises"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}
type SessionInput struct {
	Name        string            `json:"name"`
	Notes       string            `json:"notes"`
	PerformedAt time.Time         `json:"performed_at"`
	PlanID      *uuid.UUID        `json:"plan_id"`
	Exercises   []SessionExercise `json:"exercises"`
}
type Session struct {
	ID     uuid.UUID `json:"id"`
	UserID uuid.UUID `json:"user_id"`
	SessionInput
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Kinds are the catalog's categories. Strength exercises are counted in sets;
// all others in minutes.
func validateExercise(in *ExerciseInput) error {
	if err := validation.Text("exercise name", &in.Name, 200, true); err != nil {
		return err
	}
	if err := validation.Text("exercise notes", &in.Notes, 1000, false); err != nil {
		return err
	}
	in.Kind = strings.ToLower(strings.TrimSpace(in.Kind))
	switch in.Kind {
	case "strength":
		if len(in.Sets) < 1 || len(in.Sets) > 100 {
			return validation.Invalid("strength exercises require 1..100 sets")
		}
		if in.Minutes != nil || in.AvgBPM != nil {
			return validation.Invalid("strength exercises cannot have minutes or avg_bpm")
		}
		for _, set := range in.Sets {
			if set.Repetitions < 1 || set.Repetitions > 1000 || set.WeightKG == nil || !validation.Number(*set.WeightKG, 0, 2000) {
				return validation.Invalid("each set requires repetitions 1..1000 and weight_kg 0..2000")
			}
		}
	case "cardio", "mobility", "stretching":
		if len(in.Sets) != 0 {
			return validation.Invalid(in.Kind + " exercises cannot have sets")
		}
		if in.Minutes == nil || !validation.Number(*in.Minutes, 0, 1440) || *in.Minutes == 0 {
			return validation.Invalid(in.Kind + " minutes must be greater than 0 and at most 1440")
		}
		if in.AvgBPM != nil && (*in.AvgBPM < 1 || *in.AvgBPM > 300) {
			return validation.Invalid("avg_bpm must be 1..300 when provided")
		}
	default:
		return validation.Invalid("exercise kind must be strength, cardio, mobility or stretching")
	}
	return nil
}

func validateHeader(name, notes *string, count int) error {
	if err := validation.Text("name", name, 200, true); err != nil {
		return err
	}
	if err := validation.Text("notes", notes, 4000, false); err != nil {
		return err
	}
	if count < 1 || count > 100 {
		return validation.Invalid("exercises must contain 1..100 entries")
	}
	return nil
}

// validatePlanLinks also runs inside the save transaction, against the plan
// exactly as it is stored. An omitted link represents an additional, unplanned exercise.
func validatePlanLinks(in SessionInput, plan *Plan) error {
	entries := make(map[uuid.UUID]PlanExercise)
	if plan != nil {
		for _, e := range plan.Exercises {
			entries[e.ID] = e
		}
	}
	used := make(map[uuid.UUID]bool)
	for _, e := range in.Exercises {
		if e.PlanExerciseID == nil {
			continue
		}
		p, ok := entries[*e.PlanExerciseID]
		if !ok {
			return validation.Invalid("plan_exercise_id must belong to the session's plan")
		}
		if used[p.ID] {
			return validation.Invalid("plan_exercise_id must not be repeated")
		}
		if p.Kind != e.Kind {
			return validation.Invalid("actual exercise kind must match its planned exercise")
		}
		used[p.ID] = true
	}
	return nil
}
