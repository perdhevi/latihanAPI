package training

import (
	"errors"
	"github.com/google/uuid"
	"latihanApi/internal/validation"
	"strings"
	"time"
)

var ErrNotFound = errors.New("session or plan not found")
var ErrReference = errors.New("user or plan does not exist for this user")
var ErrConflict = errors.New("record has dependent sessions")

type Set struct {
	Repetitions int      `json:"repetitions"`
	WeightKG    *float64 `json:"weight_kg"`
}

// ExerciseInput is self-contained: no public catalog is needed to log a workout.
type ExerciseInput struct {
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Sets    []Set    `json:"sets,omitempty"`
	Minutes *float64 `json:"minutes,omitempty"`
	AvgBPM  *int     `json:"avg_bpm,omitempty"`
}
type PlanExercise struct {
	ID uuid.UUID `json:"id"`
	ExerciseInput
}
type SessionExercise struct {
	PlanExerciseID *uuid.UUID `json:"plan_exercise_id,omitempty"`
	ExerciseInput
}
type PlanInput struct {
	UserID    uuid.UUID       `json:"user_id"`
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
	UserID      uuid.UUID         `json:"user_id"`
	Name        string            `json:"name"`
	Notes       string            `json:"notes"`
	PerformedAt time.Time         `json:"performed_at"`
	PlanID      *uuid.UUID        `json:"plan_id"`
	Exercises   []SessionExercise `json:"exercises"`
}
type Session struct {
	ID uuid.UUID `json:"id"`
	SessionInput
	PlanSnapshot *Plan     `json:"plan_snapshot"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func validateExercise(in *ExerciseInput) error {
	if err := validation.Text("exercise name", &in.Name, 200, true); err != nil {
		return err
	}
	in.Kind = strings.ToLower(strings.TrimSpace(in.Kind))
	switch in.Kind {
	case "strength":
		if len(in.Sets) < 1 || len(in.Sets) > 100 {
			return validation.Invalid("strength exercises require 1..100 sets")
		}
		if in.Minutes != nil || in.AvgBPM != nil {
			return validation.Invalid("strength exercises cannot have cardio metrics")
		}
		for _, set := range in.Sets {
			if set.Repetitions < 1 || set.Repetitions > 1000 || set.WeightKG == nil || !validation.Number(*set.WeightKG, 0, 2000) {
				return validation.Invalid("each set requires repetitions 1..1000 and weight_kg 0..2000")
			}
		}
	case "cardio":
		if len(in.Sets) != 0 {
			return validation.Invalid("cardio exercises cannot have sets")
		}
		if in.Minutes == nil || !validation.Number(*in.Minutes, 0, 1440) || *in.Minutes == 0 {
			return validation.Invalid("cardio minutes must be greater than 0 and at most 1440")
		}
		if in.AvgBPM != nil && (*in.AvgBPM < 1 || *in.AvgBPM > 300) {
			return validation.Invalid("avg_bpm must be 1..300 when provided")
		}
	default:
		return validation.Invalid("exercise kind must be strength or cardio")
	}
	return nil
}

func validateHeader(userID uuid.UUID, name, notes *string, count int) error {
	if userID == uuid.Nil {
		return validation.Invalid("user_id is required and must not be the nil UUID")
	}
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

// validatePlanLinks also runs inside the save transaction, against the exact snapshot
// that will be stored. An omitted link represents an additional, unplanned exercise.
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
			return validation.Invalid("plan_exercise_id must belong to the session's plan snapshot")
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
