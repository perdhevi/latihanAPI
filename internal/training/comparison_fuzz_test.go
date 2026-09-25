package training

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/google/uuid"
)

// Any plan and session that pass validation compare without panicking and
// without NaN or infinite totals, deltas or per-exercise differences.
func FuzzCompare(f *testing.F) {
	f.Add([]byte(`[{"name":"Squat","kind":"strength","sets":[{"repetitions":8,"weight_kg":50}]}]`),
		[]byte(`[{"name":"Squat","kind":"strength","sets":[{"repetitions":10,"weight_kg":55}]},{"name":"Run","kind":"cardio","minutes":30,"avg_bpm":140}]`), uint8(1))
	f.Add([]byte(`[{"name":"Run","kind":"cardio","minutes":1440}]`), []byte(`[{"name":"Run","kind":"cardio","minutes":0.0001,"avg_bpm":300}]`), uint8(1))
	f.Fuzz(func(t *testing.T, planned, actual []byte, links uint8) {
		var plan, session []ExerciseInput
		if json.Unmarshal(planned, &plan) != nil || json.Unmarshal(actual, &session) != nil ||
			len(plan) == 0 || len(session) == 0 || len(plan) > 100 || len(session) > 100 {
			return
		}
		p := Plan{ID: uuid.New()}
		for i := range plan {
			if validateExercise(&plan[i]) != nil {
				return
			}
			p.Exercises = append(p.Exercises, PlanExercise{ID: uuid.New(), ExerciseInput: plan[i]})
		}
		s := Session{ID: uuid.New(), PlanSnapshot: &p, SessionInput: SessionInput{PlanID: &p.ID}}
		for i := range session {
			if validateExercise(&session[i]) != nil {
				return
			}
			entry := SessionExercise{ExerciseInput: session[i]}
			// Link some entries to the plan entry at the same position, when kinds match.
			if links&(1<<(i%8)) != 0 && i < len(p.Exercises) && p.Exercises[i].Kind == session[i].Kind {
				entry.PlanExerciseID = &p.Exercises[i].ID
			}
			s.Exercises = append(s.Exercises, entry)
		}
		if validatePlanLinks(s.SessionInput, &p) != nil {
			return
		}
		raw, err := json.Marshal(Compare(s))
		if err != nil {
			t.Fatalf("comparison cannot be encoded (NaN or Inf?): %v", err)
		}
		var generic any
		if err := json.Unmarshal(raw, &generic); err != nil {
			t.Fatal(err)
		}
		requireFinite(t, generic)
	})
}

func requireFinite(t *testing.T, v any) {
	t.Helper()
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			t.Fatalf("non-finite number %v", x)
		}
	case []any:
		for _, e := range x {
			requireFinite(t, e)
		}
	case map[string]any:
		for _, e := range x {
			requireFinite(t, e)
		}
	}
}
