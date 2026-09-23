package training

import (
	"github.com/google/uuid"
	"time"
)

type Totals struct {
	Sets        int     `json:"sets"`
	Repetitions int     `json:"repetitions"`
	VolumeKG    float64 `json:"volume_kg"`
	Minutes     float64 `json:"minutes"`
}
type SetComparison struct {
	Number           int      `json:"number"`
	Planned          *Set     `json:"planned"`
	Actual           *Set     `json:"actual"`
	RepetitionsDelta *int     `json:"repetitions_delta"`
	WeightKGDelta    *float64 `json:"weight_kg_delta"`
}
type ExerciseComparison struct {
	PlanExerciseID *uuid.UUID      `json:"plan_exercise_id"`
	Status         string          `json:"status"`
	Planned        *ExerciseInput  `json:"planned"`
	Actual         *ExerciseInput  `json:"actual"`
	PlannedTotals  Totals          `json:"planned_totals"`
	ActualTotals   Totals          `json:"actual_totals"`
	Delta          Totals          `json:"delta"`
	AvgBPMDelta    *int            `json:"avg_bpm_delta"`
	Sets           []SetComparison `json:"sets"`
}
type Comparison struct {
	PlanID        uuid.UUID            `json:"plan_id"`
	SessionID     uuid.UUID            `json:"session_id"`
	UserID        uuid.UUID            `json:"user_id"`
	PlanUpdatedAt time.Time            `json:"plan_updated_at"`
	PlannedTotals Totals               `json:"planned_totals"`
	ActualTotals  Totals               `json:"actual_totals"`
	Delta         Totals               `json:"delta"`
	Exercises     []ExerciseComparison `json:"exercises"`
}

func totals(e *ExerciseInput) Totals {
	var t Totals
	if e == nil {
		return t
	}
	t.Sets = len(e.Sets)
	for _, s := range e.Sets {
		t.Repetitions += s.Repetitions
		t.VolumeKG += float64(s.Repetitions) * (*s.WeightKG)
	}
	if e.Minutes != nil {
		t.Minutes = *e.Minutes
	}
	return t
}
func add(a, b Totals) Totals {
	return Totals{a.Sets + b.Sets, a.Repetitions + b.Repetitions, a.VolumeKG + b.VolumeKG, a.Minutes + b.Minutes}
}
func subtract(actual, planned Totals) Totals {
	return Totals{actual.Sets - planned.Sets, actual.Repetitions - planned.Repetitions, actual.VolumeKG - planned.VolumeKG, actual.Minutes - planned.Minutes}
}

func compareExercise(id *uuid.UUID, planned, actual *ExerciseInput) ExerciseComparison {
	c := ExerciseComparison{PlanExerciseID: id, Planned: planned, Actual: actual, Status: "matched", PlannedTotals: totals(planned), ActualTotals: totals(actual), Sets: make([]SetComparison, 0)}
	c.Delta = subtract(c.ActualTotals, c.PlannedTotals)
	if actual == nil {
		c.Status = "missed"
	}
	if planned == nil {
		c.Status = "unplanned"
	}
	var plannedSets, actualSets []Set
	if planned != nil {
		plannedSets = planned.Sets
	}
	if actual != nil {
		actualSets = actual.Sets
	}
	for i := 0; i < max(len(plannedSets), len(actualSets)); i++ {
		set := SetComparison{Number: i + 1}
		if i < len(plannedSets) {
			set.Planned = &plannedSets[i]
		}
		if i < len(actualSets) {
			set.Actual = &actualSets[i]
		}
		if set.Planned != nil && set.Actual != nil {
			reps := set.Actual.Repetitions - set.Planned.Repetitions
			weight := *set.Actual.WeightKG - *set.Planned.WeightKG
			set.RepetitionsDelta = &reps
			set.WeightKGDelta = &weight
		}
		c.Sets = append(c.Sets, set)
	}
	if planned != nil && actual != nil && planned.AvgBPM != nil && actual.AvgBPM != nil {
		delta := *actual.AvgBPM - *planned.AvgBPM
		c.AvgBPMDelta = &delta
	}
	return c
}

// Compare uses the immutable plan snapshot, never the plan's current contents.
// Deltas are actual minus planned; missing BPM readings remain unknown (null).
func Compare(session Session) Comparison {
	plan := session.PlanSnapshot
	c := Comparison{PlanID: plan.ID, SessionID: session.ID, UserID: session.UserID, PlanUpdatedAt: plan.UpdatedAt, Exercises: make([]ExerciseComparison, 0)}
	actual := make(map[uuid.UUID]*ExerciseInput)
	for i := range session.Exercises {
		e := &session.Exercises[i]
		if e.PlanExerciseID != nil {
			actual[*e.PlanExerciseID] = &e.ExerciseInput
		}
	}
	for _, e := range plan.Exercises {
		id := e.ID
		c.Exercises = append(c.Exercises, compareExercise(&id, &e.ExerciseInput, actual[e.ID]))
	}
	for i := range session.Exercises {
		e := &session.Exercises[i]
		if e.PlanExerciseID == nil {
			c.Exercises = append(c.Exercises, compareExercise(nil, nil, &e.ExerciseInput))
		}
	}
	for _, e := range c.Exercises {
		c.PlannedTotals = add(c.PlannedTotals, e.PlannedTotals)
		c.ActualTotals = add(c.ActualTotals, e.ActualTotals)
	}
	c.Delta = subtract(c.ActualTotals, c.PlannedTotals)
	return c
}
