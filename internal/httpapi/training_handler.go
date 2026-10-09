package httpapi

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/perdhevi/latihanAPI/internal/conditional"
	"github.com/perdhevi/latihanAPI/internal/training"
	"github.com/perdhevi/latihanAPI/internal/validation"
)

func (h *handlers) createPlan(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeBody[training.PlanInput](w, r)
	if !ok {
		return
	}
	plan, err := h.training.SavePlan(r.Context(), owner(r), uuid.Nil, in, true, conditional.Any)
	if err != nil {
		h.fail(w, r, err, "plan")
		return
	}
	w.Header().Set("Location", "/api/v1/plans/"+plan.ID.String())
	setETag(w, plan.UpdatedAt)
	writeJSON(w, http.StatusCreated, plan)
}
func (h *handlers) getPlan(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	plan, err := h.training.GetPlan(r.Context(), owner(r), id)
	if err != nil {
		h.fail(w, r, err, "plan")
		return
	}
	setETag(w, plan.UpdatedAt)
	writeJSON(w, http.StatusOK, plan)
}
func (h *handlers) updatePlan(w http.ResponseWriter, r *http.Request) {
	match, ok := h.ifMatch(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	in, ok := decodeBody[training.PlanInput](w, r)
	if !ok {
		return
	}
	plan, err := h.training.SavePlan(r.Context(), owner(r), id, in, false, match)
	if err != nil {
		h.fail(w, r, err, "plan")
		return
	}
	setETag(w, plan.UpdatedAt)
	writeJSON(w, http.StatusOK, plan)
}
func (h *handlers) deletePlan(w http.ResponseWriter, r *http.Request) {
	match, ok := h.ifMatch(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := h.training.DeletePlan(r.Context(), owner(r), id, match); err != nil {
		h.fail(w, r, err, "plan")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *handlers) listPlans(w http.ResponseWriter, r *http.Request) {
	q, ok := queryValues(w, r, "limit", "cursor")
	if !ok {
		return
	}
	page, ok := pagination(w, q)
	if !ok {
		return
	}
	plans, err := h.training.ListPlans(r.Context(), owner(r), page)
	if err != nil {
		h.fail(w, r, err, "plan")
		return
	}
	writePage(w, plans, page, func(p training.Plan) validation.Cursor { return validation.Cursor{Time: p.CreatedAt, ID: p.ID} })
}
func (h *handlers) comparison(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	q, ok := queryValues(w, r, "session_id")
	if !ok {
		return
	}
	sessionID, ok := parseUUID(w, q.Get("session_id"))
	if !ok {
		return
	}
	result, err := h.training.Compare(r.Context(), owner(r), id, sessionID)
	if err != nil {
		h.fail(w, r, err, "comparison")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (h *handlers) createSession(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeBody[training.SessionInput](w, r)
	if !ok {
		return
	}
	session, err := h.training.SaveSession(r.Context(), owner(r), uuid.Nil, in, true, conditional.Any)
	if err != nil {
		h.fail(w, r, err, "session")
		return
	}
	w.Header().Set("Location", "/api/v1/sessions/"+session.ID.String())
	setETag(w, session.UpdatedAt)
	writeJSON(w, http.StatusCreated, session)
}
func (h *handlers) getSession(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	session, err := h.training.GetSession(r.Context(), owner(r), id)
	if err != nil {
		h.fail(w, r, err, "session")
		return
	}
	setETag(w, session.UpdatedAt)
	writeJSON(w, http.StatusOK, session)
}
func (h *handlers) updateSession(w http.ResponseWriter, r *http.Request) {
	match, ok := h.ifMatch(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	in, ok := decodeBody[training.SessionInput](w, r)
	if !ok {
		return
	}
	session, err := h.training.SaveSession(r.Context(), owner(r), id, in, false, match)
	if err != nil {
		h.fail(w, r, err, "session")
		return
	}
	setETag(w, session.UpdatedAt)
	writeJSON(w, http.StatusOK, session)
}
func (h *handlers) deleteSession(w http.ResponseWriter, r *http.Request) {
	match, ok := h.ifMatch(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := h.training.DeleteSession(r.Context(), owner(r), id, match); err != nil {
		h.fail(w, r, err, "session")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *handlers) listSessions(w http.ResponseWriter, r *http.Request) {
	q, ok := queryValues(w, r, "limit", "cursor")
	if !ok {
		return
	}
	page, ok := pagination(w, q)
	if !ok {
		return
	}
	sessions, err := h.training.ListSessions(r.Context(), owner(r), page)
	if err != nil {
		h.fail(w, r, err, "session")
		return
	}
	writePage(w, sessions, page, func(s training.Session) validation.Cursor { return validation.Cursor{Time: s.PerformedAt, ID: s.ID} })
}

func (h *handlers) listCatalog(w http.ResponseWriter, r *http.Request) {
	q, ok := queryValues(w, r, "limit", "cursor", "category")
	if !ok {
		return
	}
	page, ok := pagination(w, q)
	if !ok {
		return
	}
	entries, err := h.training.ListCatalog(r.Context(), q.Get("category"), page)
	if err != nil {
		h.fail(w, r, err, "exercise")
		return
	}
	writePage(w, entries, page, func(e training.CatalogExercise) validation.Cursor {
		return validation.Cursor{Time: e.CreatedAt, ID: e.ID}
	})
}
func (h *handlers) getCatalogExercise(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	entry, err := h.training.GetCatalogExercise(r.Context(), id)
	if err != nil {
		h.fail(w, r, err, "exercise")
		return
	}
	setETag(w, entry.UpdatedAt)
	writeJSON(w, http.StatusOK, entry)
}

// progress answers "did I do what I planned?" for the caller's own sessions.
func (h *handlers) progress(w http.ResponseWriter, r *http.Request) {
	q, ok := queryValues(w, r, "exercise_id", "from", "to", "bucket")
	if !ok {
		return
	}
	query := training.ProgressQuery{Bucket: q.Get("bucket")}
	if query.Bucket == "" {
		query.Bucket = "week"
	}
	if q.Has("exercise_id") {
		id, ok := parseUUID(w, q.Get("exercise_id"))
		if !ok {
			return
		}
		query.ExerciseID = &id
	}
	for name, target := range map[string]*time.Time{"from": &query.From, "to": &query.To} {
		if !q.Has(name) {
			continue
		}
		t, err := time.Parse(time.RFC3339, q.Get(name))
		if err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", name+" must be an RFC3339 timestamp")
			return
		}
		*target = t
	}
	points, err := h.training.Progress(r.Context(), owner(r), query)
	if err != nil {
		h.fail(w, r, err, "exercise")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bucket": query.Bucket, "data": points})
}
