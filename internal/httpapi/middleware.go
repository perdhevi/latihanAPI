package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/perdhevi/latihanAPI/internal/telemetry"
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

// requestInfo collects facts learned deep in the handler chain (who the caller
// turned out to be) for the request log written by the outer middleware.
type requestInfo struct{ userID uuid.UUID }

type requestInfoKey struct{}

func infoFrom(ctx context.Context) *requestInfo {
	info, _ := ctx.Value(requestInfoKey{}).(*requestInfo)
	return info
}

func middleware(next http.Handler, logger *slog.Logger, l *limits, metrics *telemetry.Metrics) http.Handler {
	tracer := otel.Tracer(telemetry.TracerName)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		// Everything downstream, including auth providers, sees the real client
		// address in RemoteAddr, resolved through trusted proxies only.
		client := l.clientIP(r)
		r.RemoteAddr = netip.AddrPortFrom(client, 0).String()
		// Generate our own correlation ID instead of trusting caller-controlled log data.
		id := uuid.NewString()
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Responses hold personal data; no shared cache may keep them. Handlers
		// serving public data (such as signing keys) override this.
		w.Header().Set("Cache-Control", "no-store")
		sw := &statusWriter{ResponseWriter: w}

		// Continue the caller's trace if it sent traceparent; the span is renamed
		// to its route once routing has happened.
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := tracer.Start(ctx, r.Method, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()
		info := &requestInfo{}
		ctx = context.WithValue(context.WithValue(ctx, requestIDKey{}, id), requestInfoKey{}, info)
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		req := r.WithContext(ctx)
		metrics.RequestStarted()
		defer metrics.RequestFinished()

		defer func() {
			recovered := recover()
			if recovered == http.ErrAbortHandler { //nolint:errorlint // panic values are compared, not unwrapped
				// The handler deliberately aborted the response; net/http expects
				// to see this panic and closes the connection quietly.
				panic(recovered)
			}
			if recovered != nil {
				metrics.Panicked()
				logger.ErrorContext(ctx, "request panic", "request_id", id, "panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
				if sw.status == 0 {
					writeError(sw, http.StatusInternalServerError, "internal_error", "internal server error")
				}
			}
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			// The mux records the matched pattern ("GET /api/v1/sessions/{id}") on the
			// request it routed; the method is its own label, so keep only the path.
			route := req.Pattern
			if _, path, hasMethod := strings.Cut(route, " "); hasMethod {
				route = path
			}
			if route != "" {
				span.SetName(r.Method + " " + route)
			}
			span.SetAttributes(semconv.HTTPRequestMethodKey.String(r.Method), semconv.HTTPRoute(route), semconv.HTTPResponseStatusCode(status))
			if status >= http.StatusInternalServerError {
				span.SetStatus(codes.Error, http.StatusText(status))
			}
			duration := time.Since(start)
			metrics.ObserveRequest(route, r.Method, status, duration.Seconds())
			attrs := []any{"request_id", id, "client_ip", client.String(), "method", r.Method, "path", r.URL.Path, "status", status, "duration", duration}
			if info.userID != uuid.Nil {
				attrs = append(attrs, "user_id", info.userID.String())
			}
			logger.InfoContext(ctx, "http request", attrs...)
		}()
		next.ServeHTTP(sw, req)
	})
}
