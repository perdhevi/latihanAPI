package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/perdhevi/latihanAPI/auth"
	"github.com/perdhevi/latihanAPI/internal/profile"
	"github.com/perdhevi/latihanAPI/internal/training"
	"github.com/perdhevi/latihanAPI/internal/validation"
)

// eraseAccount deletes everything the service holds about the caller, then
// asks the auth provider to delete the login account if it keeps one. It needs
// only a valid identity, not a profile, so a caller whose earlier erasure
// stopped half-way can finish it. Always 204 on success.
func (h *handlers) eraseAccount(w http.ResponseWriter, r *http.Request) {
	caller := principalFrom(r.Context())
	if caller.userID != uuid.Nil {
		if err := h.account.Erase(r.Context(), caller.userID); err != nil {
			h.fail(w, r, err, "user")
			return
		}
	}
	if eraser, ok := h.auth.(auth.AccountEraser); ok {
		if err := eraser.EraseAccount(r.Context(), caller.identity); err != nil {
			h.fail(w, r, err, "user")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

const exportPageSize = 100

// exportAccount streams every record of the caller as one JSON document.
// Streaming keeps memory flat however much history there is; if a read fails
// half-way the connection is aborted, so a client never mistakes a truncated
// export for a complete one.
func (h *handlers) exportAccount(w http.ResponseWriter, r *http.Request) {
	userID := owner(r)
	if !h.allow(w, r, h.limits.export, "export", "export:"+userID.String()) {
		return
	}
	ctx := r.Context()
	user, err := h.profiles.GetUser(ctx, userID)
	if err != nil {
		h.fail(w, r, err, "user")
		return
	}
	identities, err := h.account.Identities(ctx, userID)
	if err != nil {
		h.fail(w, r, err, "user")
		return
	}
	if err := h.account.RecordExport(ctx, userID); err != nil {
		h.fail(w, r, err, "user")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="latihan-export.json"`)
	w.WriteHeader(http.StatusOK)
	abort := func(err error) {
		h.logger.ErrorContext(ctx, "export aborted", "request_id", ctx.Value(requestIDKey{}), "error", err)
		panic(http.ErrAbortHandler)
	}
	write := func(s string) {
		if _, err := io.WriteString(w, s); err != nil {
			abort(err)
		}
	}
	enc := json.NewEncoder(w)
	value := func(v any) {
		if err := enc.Encode(v); err != nil {
			abort(err)
		}
	}

	write(`{"format":"latihan-export/1","exported_at":`)
	value(time.Now().UTC())
	write(`,"user":`)
	value(user)
	write(`,"identities":`)
	value(identities)
	write(`,"measurements":`)
	streamAll(write, value, abort, func(p validation.Page) ([]profile.Measurement, error) {
		return h.profiles.ListMeasurements(ctx, userID, p)
	}, func(m profile.Measurement) validation.Cursor { return validation.Cursor{Time: m.MeasuredAt, ID: m.ID} })
	write(`,"plans":`)
	streamAll(write, value, abort, func(p validation.Page) ([]training.Plan, error) {
		return h.training.ListPlans(ctx, userID, p)
	}, func(p training.Plan) validation.Cursor { return validation.Cursor{Time: p.CreatedAt, ID: p.ID} })
	write(`,"sessions":`)
	streamAll(write, value, abort, func(p validation.Page) ([]training.Session, error) {
		return h.training.ListSessions(ctx, userID, p)
	}, func(s training.Session) validation.Cursor { return validation.Cursor{Time: s.PerformedAt, ID: s.ID} })
	write("}\n")
}

// streamAll writes a JSON array of every item list returns, page by page.
func streamAll[T any](write func(string), value func(any), abort func(error), list func(validation.Page) ([]T, error), position func(T) validation.Cursor) {
	write("[")
	page := validation.Page{Limit: exportPageSize}
	first := true
	for {
		items, err := list(page)
		if err != nil {
			abort(err)
		}
		more := len(items) > page.Limit
		if more {
			items = items[:page.Limit]
		}
		for _, item := range items {
			if !first {
				write(",")
			}
			first = false
			value(item)
		}
		if !more {
			break
		}
		next := position(items[len(items)-1])
		page.After = &next
	}
	write("]")
}
