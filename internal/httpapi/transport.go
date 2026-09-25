package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/google/uuid"

	"github.com/perdhevi/latihanAPI/auth"
	"github.com/perdhevi/latihanAPI/internal/httpjson"
	"github.com/perdhevi/latihanAPI/internal/profile"
	"github.com/perdhevi/latihanAPI/internal/training"
	"github.com/perdhevi/latihanAPI/internal/validation"
)

const maxBodyBytes = httpjson.MaxBodyBytes

type errorResponse = httpjson.ErrorResponse

func writeJSON(w http.ResponseWriter, status int, value any) { httpjson.Write(w, status, value) }
func writeError(w http.ResponseWriter, status int, code, message string) {
	httpjson.Error(w, status, code, message)
}

type handlers struct {
	training *training.Service
	profiles *profile.Service
	auth     auth.Authenticator
	logger   *slog.Logger
}

func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error, resource string) {
	var invalid *validation.Error
	switch {
	case errors.As(err, &invalid):
		writeError(w, http.StatusBadRequest, "validation_failed", invalid.Message)
	case errors.Is(err, profile.ErrNotFound), errors.Is(err, training.ErrNotFound):
		writeError(w, http.StatusNotFound, resource+"_not_found", resource+" not found")
	case errors.Is(err, profile.ErrUserMissing), errors.Is(err, training.ErrReference):
		writeError(w, http.StatusBadRequest, "invalid_reference", "referenced user or plan does not exist for this user")
	case errors.Is(err, profile.ErrConflict), errors.Is(err, training.ErrConflict):
		writeError(w, http.StatusConflict, "resource_in_use", "remove dependent records before deleting this resource")
	case errors.Is(err, profile.ErrIdentityLinked):
		writeError(w, http.StatusConflict, "profile_exists", "this identity already has a profile")
	default:
		h.logger.ErrorContext(r.Context(), "request failed", "request_id", r.Context().Value(requestIDKey{}), "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func parseUUID(w http.ResponseWriter, value string) (uuid.UUID, bool) {
	id, err := uuid.Parse(value)
	if err != nil || len(value) != 36 || id == uuid.Nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "ID must be a nonzero canonical UUID")
		return uuid.Nil, false
	}
	return id, true
}
func pathID(w http.ResponseWriter, r *http.Request, key string) (uuid.UUID, bool) {
	return parseUUID(w, r.PathValue(key))
}
func decodeBody[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	return httpjson.Decode[T](w, r)
}

func queryValues(w http.ResponseWriter, r *http.Request, allowed ...string) (url.Values, bool) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid query string")
		return nil, false
	}
	for key, values := range q {
		found := false
		for _, name := range allowed {
			if name == key {
				found = true
			}
		}
		if !found || len(values) != 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "unknown or repeated query parameter")
			return nil, false
		}
	}
	return q, true
}
func pagination(w http.ResponseWriter, q url.Values) (validation.Page, bool) {
	p := validation.Page{Limit: 20}
	var err error
	if q.Has("limit") {
		p.Limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be an integer")
			return p, false
		}
	}
	if q.Has("offset") {
		p.Offset, err = strconv.Atoi(q.Get("offset"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "offset must be an integer")
			return p, false
		}
	}
	if err := p.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return p, false
	}
	return p, true
}
func writePage[T any](w http.ResponseWriter, items []T, page validation.Page) {
	if items == nil {
		items = make([]T, 0)
	}
	writeJSON(w, http.StatusOK, struct {
		Items  []T `json:"items"`
		Limit  int `json:"limit"`
		Offset int `json:"offset"`
	}{items, page.Limit, page.Offset})
}
