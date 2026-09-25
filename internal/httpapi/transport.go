package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/perdhevi/latihanAPI/auth"
	"github.com/perdhevi/latihanAPI/internal/conditional"
	"github.com/perdhevi/latihanAPI/internal/httpjson"
	"github.com/perdhevi/latihanAPI/internal/idempotency"
	"github.com/perdhevi/latihanAPI/internal/profile"
	"github.com/perdhevi/latihanAPI/internal/telemetry"
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
	limits   *limits
	// requireIfMatch makes If-Match mandatory on updates and deletes.
	requireIfMatch bool
	// idempotency stores responses for Idempotency-Key retries; nil disables it.
	idempotency *idempotency.Store
	metrics     *telemetry.Metrics
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
	case errors.Is(err, conditional.ErrPreconditionFailed):
		writeError(w, http.StatusPreconditionFailed, "precondition_failed", "the record changed since you read it; fetch it again and retry")
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
	if q.Has("cursor") {
		cursor, err := validation.DecodeCursor(q.Get("cursor"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
			return p, false
		}
		p.After = &cursor
	}
	if err := p.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
		return p, false
	}
	return p, true
}

// writePage writes one page. Repositories return up to Limit+1 items; the extra
// one only signals that next_cursor is needed. position gives an item's cursor.
func writePage[T any](w http.ResponseWriter, items []T, page validation.Page, position func(T) validation.Cursor) {
	if items == nil {
		items = make([]T, 0)
	}
	var next *string
	if len(items) > page.Limit {
		items = items[:page.Limit]
		cursor := position(items[len(items)-1]).Encode()
		next = &cursor
	}
	writeJSON(w, http.StatusOK, struct {
		Items      []T     `json:"items"`
		Limit      int     `json:"limit"`
		NextCursor *string `json:"next_cursor"`
	}{items, page.Limit, next})
}

// ifMatch reads If-Match for a write to an existing record. Without the
// header the write is unconditional, unless the service requires it (428).
func (h *handlers) ifMatch(w http.ResponseWriter, r *http.Request) (conditional.Match, bool) {
	header := r.Header.Get("If-Match")
	if header == "" && h.requireIfMatch {
		writeError(w, http.StatusPreconditionRequired, "precondition_required", "send If-Match with the ETag from your last read")
		return conditional.Any, false
	}
	return conditional.ParseIfMatch(header), true
}

// setETag labels a single-record response with its version for If-Match.
func setETag(w http.ResponseWriter, updatedAt time.Time) {
	w.Header().Set("ETag", conditional.ETag(updatedAt))
}
