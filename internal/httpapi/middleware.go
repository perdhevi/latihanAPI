package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

type requestIDKey struct{}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func middleware(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		// Generate our own correlation ID instead of trusting caller-controlled log data.
		id := uuid.NewString()
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		sw := &statusWriter{ResponseWriter: w}
		ctx, cancel := context.WithTimeout(context.WithValue(r.Context(), requestIDKey{}, id), 10*time.Second)
		defer cancel()
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.ErrorContext(ctx, "request panic", "request_id", id)
				if sw.status == 0 {
					writeError(sw, http.StatusInternalServerError, "internal_error", "internal server error")
				}
			}
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			logger.InfoContext(ctx, "http request", "request_id", id, "method", r.Method, "path", r.URL.Path, "status", status, "duration", time.Since(start))
		}()
		next.ServeHTTP(sw, r.WithContext(ctx))
	})
}
