package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"

	"github.com/perdhevi/latihanAPI/internal/idempotency"
)

// replayedHeaders are the response headers stored with a key and replayed.
var replayedHeaders = []string{"Content-Type", "Location", "ETag"}

// idempotent makes a creating POST safe to retry. Requests without an
// Idempotency-Key header run normally. With one, the first request's response
// is stored and every retry with the same key and body gets it back
// (Idempotent-Replayed: true) instead of creating another record. It must run
// after authentication: keys are private to the caller.
func (h *handlers) idempotent(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" || h.idempotency == nil {
			next(w, r)
			return
		}
		if !validKey(key) {
			writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must be 1..255 visible ASCII characters")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			writeError(w, http.StatusRequestEntityTooLarge, "invalid_request", "request body exceeds 1 MiB")
			return
		} else if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "could not read request body")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		// The same key on another endpoint, or with another body, is a client bug.
		fingerprint := sha256.New()
		for _, part := range [][]byte{[]byte(r.Method), []byte(r.URL.Path), []byte(r.Header.Get("Content-Type")), body} {
			fingerprint.Write(part)
			fingerprint.Write([]byte{0})
		}
		caller := principalFrom(r.Context()).identity
		scope := idempotency.Scope{Issuer: caller.Issuer, Subject: caller.Subject, Key: key}

		outcome, stored, err := h.idempotency.Claim(r.Context(), scope, fingerprint.Sum(nil))
		switch {
		case err != nil:
			h.fail(w, r, err, "request")
			return
		case outcome == idempotency.Mismatch:
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_reused", "this Idempotency-Key was used for a different request")
			return
		case outcome == idempotency.InProgress:
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusConflict, "idempotency_key_in_progress", "a request with this Idempotency-Key is still running")
			return
		case outcome == idempotency.Replay:
			h.metrics.Replayed()
			for name, value := range stored.Header {
				w.Header().Set(name, value)
			}
			w.Header().Set("Idempotent-Replayed", "true")
			w.WriteHeader(stored.Status)
			_, _ = w.Write(stored.Body)
			return
		}

		// Claimed: run the request, then keep its response or give the key back.
		// Bookkeeping must finish even if the client has gone.
		cleanup := context.WithoutCancel(r.Context())
		finished := false
		defer func() {
			if !finished { // the handler panicked
				_ = h.idempotency.Release(cleanup, scope)
			}
		}()
		rec := &recorder{header: http.Header{}}
		next(rec, r)
		finished = true
		resp := rec.response()
		// Server errors and rate limits are not the request's answer; the
		// client should be able to try again for real.
		if resp.Status >= http.StatusInternalServerError || resp.Status == http.StatusTooManyRequests {
			err = h.idempotency.Release(cleanup, scope)
		} else {
			err = h.idempotency.Complete(cleanup, scope, resp)
		}
		if err != nil {
			h.logger.ErrorContext(r.Context(), "storing idempotent response failed", "request_id", r.Context().Value(requestIDKey{}), "error", err)
		}
		for name, values := range rec.header {
			w.Header()[name] = values
		}
		w.WriteHeader(resp.Status)
		_, _ = w.Write(resp.Body)
	}
}

func validKey(key string) bool {
	if len(key) > 255 {
		return false
	}
	for i := range len(key) {
		if key[i] < 0x21 || key[i] > 0x7e {
			return false
		}
	}
	return true
}

// recorder buffers a handler's response so it can be stored before sending.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (rec *recorder) Header() http.Header { return rec.header }
func (rec *recorder) WriteHeader(status int) {
	if rec.status == 0 {
		rec.status = status
	}
}
func (rec *recorder) Write(p []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return rec.body.Write(p)
}

func (rec *recorder) response() idempotency.Response {
	status := rec.status
	if status == 0 {
		status = http.StatusOK
	}
	header := map[string]string{}
	for _, name := range replayedHeaders {
		if v := rec.header.Get(name); v != "" {
			header[name] = v
		}
	}
	return idempotency.Response{Status: status, Header: header, Body: rec.body.Bytes()}
}
