package httpapi

import (
	"net/http"

	"github.com/perdhevi/latihanAPI/internal/profile"
)

func (h *handlers) createUser(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeBody[profile.UserInput](w, r)
	if !ok {
		return
	}
	caller := principalFrom(r.Context()).identity
	user, err := h.profiles.CreateUser(r.Context(), in, profile.Identity{Issuer: caller.Issuer, Subject: caller.Subject})
	if err != nil {
		h.fail(w, r, err, "user")
		return
	}
	w.Header().Set("Location", "/api/v1/users/"+user.ID.String())
	writeJSON(w, http.StatusCreated, user)
}
func (h *handlers) getUser(w http.ResponseWriter, r *http.Request) {
	id, ok := ownUserID(w, r)
	if !ok {
		return
	}
	user, err := h.profiles.GetUser(r.Context(), id)
	if err != nil {
		h.fail(w, r, err, "user")
		return
	}
	writeJSON(w, http.StatusOK, user)
}
func (h *handlers) updateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := ownUserID(w, r)
	if !ok {
		return
	}
	in, ok := decodeBody[profile.UserInput](w, r)
	if !ok {
		return
	}
	user, err := h.profiles.UpdateUser(r.Context(), id, in)
	if err != nil {
		h.fail(w, r, err, "user")
		return
	}
	writeJSON(w, http.StatusOK, user)
}
func (h *handlers) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := ownUserID(w, r)
	if !ok {
		return
	}
	if err := h.profiles.DeleteUser(r.Context(), id); err != nil {
		h.fail(w, r, err, "user")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *handlers) createMeasurement(w http.ResponseWriter, r *http.Request) {
	userID, ok := ownUserID(w, r)
	if !ok {
		return
	}
	in, ok := decodeBody[profile.MeasurementInput](w, r)
	if !ok {
		return
	}
	m, err := h.profiles.CreateMeasurement(r.Context(), userID, in)
	if err != nil {
		h.fail(w, r, err, "measurement")
		return
	}
	w.Header().Set("Location", "/api/v1/users/"+userID.String()+"/measurements/"+m.ID.String())
	writeJSON(w, http.StatusCreated, m)
}
func (h *handlers) getMeasurement(w http.ResponseWriter, r *http.Request) {
	userID, ok := ownUserID(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	m, err := h.profiles.GetMeasurement(r.Context(), userID, id)
	if err != nil {
		h.fail(w, r, err, "measurement")
		return
	}
	writeJSON(w, http.StatusOK, m)
}
func (h *handlers) updateMeasurement(w http.ResponseWriter, r *http.Request) {
	userID, ok := ownUserID(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	in, ok := decodeBody[profile.MeasurementInput](w, r)
	if !ok {
		return
	}
	m, err := h.profiles.UpdateMeasurement(r.Context(), userID, id, in)
	if err != nil {
		h.fail(w, r, err, "measurement")
		return
	}
	writeJSON(w, http.StatusOK, m)
}
func (h *handlers) deleteMeasurement(w http.ResponseWriter, r *http.Request) {
	userID, ok := ownUserID(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := h.profiles.DeleteMeasurement(r.Context(), userID, id); err != nil {
		h.fail(w, r, err, "measurement")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *handlers) listMeasurements(w http.ResponseWriter, r *http.Request) {
	userID, ok := ownUserID(w, r)
	if !ok {
		return
	}
	q, ok := queryValues(w, r, "limit", "offset")
	if !ok {
		return
	}
	page, ok := pagination(w, q)
	if !ok {
		return
	}
	items, err := h.profiles.ListMeasurements(r.Context(), userID, page)
	if err != nil {
		h.fail(w, r, err, "user")
		return
	}
	writePage(w, items, page)
}
