package httpapi

import (
	"github.com/google/uuid"
	"latihanApi/internal/training"
	"net/http"
)

func (h *handlers) createPlan(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeBody[training.PlanInput](w, r)
	if !ok {
		return
	}
	plan, err := h.training.SavePlan(r.Context(), uuid.Nil, in, true)
	if err != nil {
		h.fail(w, r, err, "plan")
		return
	}
	w.Header().Set("Location", "/api/v1/plans/"+plan.ID.String())
	writeJSON(w, 201, plan)
}
func (h *handlers) getPlan(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	plan, err := h.training.GetPlan(r.Context(), id)
	if err != nil {
		h.fail(w, r, err, "plan")
		return
	}
	writeJSON(w, 200, plan)
}
func (h *handlers) updatePlan(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	in, ok := decodeBody[training.PlanInput](w, r)
	if !ok {
		return
	}
	plan, err := h.training.SavePlan(r.Context(), id, in, false)
	if err != nil {
		h.fail(w, r, err, "plan")
		return
	}
	writeJSON(w, 200, plan)
}
func (h *handlers) deletePlan(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := h.training.DeletePlan(r.Context(), id); err != nil {
		h.fail(w, r, err, "plan")
		return
	}
	w.WriteHeader(204)
}
func (h *handlers) listPlans(w http.ResponseWriter, r *http.Request) {
	q, ok := queryValues(w, r, "user_id", "limit", "offset")
	if !ok {
		return
	}
	userID, ok := parseUUID(w, q.Get("user_id"))
	if !ok {
		return
	}
	page, ok := pagination(w, q)
	if !ok {
		return
	}
	plans, err := h.training.ListPlans(r.Context(), userID, page)
	if err != nil {
		h.fail(w, r, err, "plan")
		return
	}
	writePage(w, plans, page)
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
	result, err := h.training.Compare(r.Context(), id, sessionID)
	if err != nil {
		h.fail(w, r, err, "comparison")
		return
	}
	writeJSON(w, 200, result)
}
func (h *handlers) createSession(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeBody[training.SessionInput](w, r)
	if !ok {
		return
	}
	session, err := h.training.SaveSession(r.Context(), uuid.Nil, in, true)
	if err != nil {
		h.fail(w, r, err, "session")
		return
	}
	w.Header().Set("Location", "/api/v1/sessions/"+session.ID.String())
	writeJSON(w, 201, session)
}
func (h *handlers) getSession(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	session, err := h.training.GetSession(r.Context(), id)
	if err != nil {
		h.fail(w, r, err, "session")
		return
	}
	writeJSON(w, 200, session)
}
func (h *handlers) updateSession(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	in, ok := decodeBody[training.SessionInput](w, r)
	if !ok {
		return
	}
	session, err := h.training.SaveSession(r.Context(), id, in, false)
	if err != nil {
		h.fail(w, r, err, "session")
		return
	}
	writeJSON(w, 200, session)
}
func (h *handlers) deleteSession(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := h.training.DeleteSession(r.Context(), id); err != nil {
		h.fail(w, r, err, "session")
		return
	}
	w.WriteHeader(204)
}
func (h *handlers) listSessions(w http.ResponseWriter, r *http.Request) {
	q, ok := queryValues(w, r, "user_id", "limit", "offset")
	if !ok {
		return
	}
	userID, ok := parseUUID(w, q.Get("user_id"))
	if !ok {
		return
	}
	page, ok := pagination(w, q)
	if !ok {
		return
	}
	sessions, err := h.training.ListSessions(r.Context(), userID, page)
	if err != nil {
		h.fail(w, r, err, "session")
		return
	}
	writePage(w, sessions, page)
}
