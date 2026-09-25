//go:build integration

package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/perdhevi/latihanAPI/internal/profile"
	"github.com/perdhevi/latihanAPI/internal/testdb"
	"github.com/perdhevi/latihanAPI/internal/training"
	"github.com/perdhevi/latihanAPI/internal/validation"
)

// TestAttackerView is the API as seen by an authenticated attacker who knows
// every identifier, header and token shape of a victim's data, exercising the
// features added after the basic ownership checks: idempotency keys,
// conditional requests, cursors and error responses.
func TestAttackerView(t *testing.T) {
	pool := testdb.Open(t)
	h := safeRouter(t, pool, Options{})
	send(h, "victim", "POST", "/api/v1/users", profile.UserInput{DisplayName: "Victim"}, nil)
	send(h, "attacker", "POST", "/api/v1/users", profile.UserInput{DisplayName: "Attacker"}, nil)

	key := map[string]string{"Idempotency-Key": "victims-key"}
	created := send(h, "victim", "POST", "/api/v1/sessions", walk(42), key)
	var victimSession training.Session
	if err := json.Unmarshal(created.Body.Bytes(), &victimSession); err != nil || created.Code != 201 {
		t.Fatalf("victim session: %d %s", created.Code, created.Body)
	}
	plan := send(h, "victim", "POST", "/api/v1/plans", training.PlanInput{Name: "Victim plan", Exercises: plannedExercises()}, nil)
	planURL, planTag := plan.Header().Get("Location"), plan.Header().Get("ETag")

	t.Run("replaying the victim's Idempotency-Key does not return the victim's response", func(t *testing.T) {
		w := send(h, "attacker", "POST", "/api/v1/sessions", walk(42), key)
		var got training.Session
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		if w.Header().Get("Idempotent-Replayed") != "" || got.ID == victimSession.ID || strings.Contains(w.Body.String(), victimSession.ID.String()) {
			t.Fatalf("attacker received the victim's stored response: %d %s", w.Code, w.Body)
		}
	})

	t.Run("a correct ETag does not reveal or unlock the victim's plan", func(t *testing.T) {
		for _, method := range []string{"PUT", "DELETE"} {
			var body any
			if method == "PUT" {
				body = training.PlanInput{Name: "Hijacked", Exercises: plannedExercises()}
			}
			if w := send(h, "attacker", method, planURL, body, map[string]string{"If-Match": planTag}); w.Code != http.StatusNotFound {
				t.Fatalf("%s with the victim's ETag: %d (412 would confirm the plan exists)", method, w.Code)
			}
		}
		if w := send(h, "victim", "GET", planURL, nil, nil); w.Header().Get("ETag") != planTag {
			t.Fatal("victim's plan changed")
		}
	})

	t.Run("a cursor pointing into the victim's history returns only the attacker's data", func(t *testing.T) {
		send(h, "attacker", "POST", "/api/v1/sessions", walk(5), nil)
		cursor := validation.Cursor{Time: victimSession.PerformedAt.Add(time.Hour), ID: uuid.Max}.Encode()
		var page struct {
			Items []training.Session `json:"items"`
		}
		w := send(h, "attacker", "GET", "/api/v1/sessions?cursor="+cursor, nil, nil)
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		for _, s := range page.Items {
			if s.ID == victimSession.ID || s.UserID != page.Items[0].UserID {
				t.Fatalf("cursor leaked another user's session: %+v", s)
			}
		}
	})

	t.Run("a real ID and a made-up ID are indistinguishable", func(t *testing.T) {
		for _, path := range []string{"/api/v1/sessions/", "/api/v1/plans/"} {
			real := victimSession.ID.String()
			if path == "/api/v1/plans/" {
				real = strings.TrimPrefix(planURL, path)
			}
			a := send(h, "attacker", "GET", path+real, nil, nil)
			b := send(h, "attacker", "GET", path+uuid.NewString(), nil, nil)
			if a.Code != b.Code || a.Body.String() != b.Body.String() || a.Header().Get("ETag") != "" {
				t.Fatalf("%s: existing %d %s vs missing %d %s", path, a.Code, a.Body, b.Code, b.Body)
			}
		}
	})

	t.Run("the victim's user ID in paths, bodies and queries is refused", func(t *testing.T) {
		victim := victimSession.UserID.String()
		for _, tc := range []struct {
			method, path string
			body         any
			want         int
		}{
			{"GET", "/api/v1/users/" + victim + "/measurements", nil, 404},
			{"POST", "/api/v1/users/" + victim + "/measurements", profile.MeasurementInput{MeasuredAt: time.Now(), WeightKG: number(1)}, 404},
			{"GET", "/api/v1/sessions?user_id=" + victim, nil, 400},
			{"POST", "/api/v1/plans", map[string]any{"user_id": victim, "name": "x", "exercises": plannedExercises()}, 400},
		} {
			if w := send(h, "attacker", tc.method, tc.path, tc.body, nil); w.Code != tc.want {
				t.Errorf("%s %s: %d, want %d", tc.method, tc.path, w.Code, tc.want)
			}
		}
	})

	// After all of that, the victim still has exactly their own data.
	if n := countSessions(t, h, "victim"); n != 1 {
		t.Fatalf("victim has %d sessions", n)
	}
}
