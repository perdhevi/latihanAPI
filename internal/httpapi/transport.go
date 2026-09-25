package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"

	"github.com/google/uuid"

	"github.com/perdhevi/latihanAPI/internal/profile"
	"github.com/perdhevi/latihanAPI/internal/training"
	"github.com/perdhevi/latihanAPI/internal/validation"
)

const maxBodyBytes = 1 << 20

type errorResponse struct {
	Error errorDetail `json:"error"`
}
type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: errorDetail{Code: code, Message: message}})
}

type handlers struct {
	training *training.Service
	profiles *profile.Service
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
	var zero T
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "invalid_request", "Content-Type must be application/json")
		return zero, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input *T
	err = decoder.Decode(&input)
	if err == nil {
		var extra any
		if e := decoder.Decode(&extra); e != io.EOF {
			err = e
			if err == nil {
				err = errors.New("multiple JSON values")
			}
		}
	}
	if err != nil || input == nil {
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			writeError(w, http.StatusRequestEntityTooLarge, "invalid_request", "request body exceeds 1 MiB")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_request", "body must be one JSON object with valid writable fields")
		}
		return zero, false
	}
	return *input, true
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
