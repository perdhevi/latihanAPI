package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/perdhevi/latihanAPI/internal/profile"
	"github.com/perdhevi/latihanAPI/internal/ratelimit"
	"github.com/perdhevi/latihanAPI/internal/telemetry"
	"github.com/perdhevi/latihanAPI/internal/training"
)

func observedRouter(repo *fakeTraining, logs io.Writer, opts Options) (http.Handler, *telemetry.Metrics) {
	m := telemetry.NewMetrics("test")
	opts.Metrics = m
	logger := slog.New(telemetry.LogHandler{Handler: slog.NewJSONHandler(logs, nil)})
	return NewRouter(training.NewService(repo), profile.NewService(fakeProfiles{}), &testPinger{}, testAuth{}, logger, opts), m
}

func TestMetricsUseRoutePatterns(t *testing.T) {
	h, m := observedRouter(&fakeTraining{}, io.Discard, Options{})
	for range 50 {
		request(h, "GET", "/api/v1/sessions/"+uuid.NewString(), "")
	}
	requestAs(h, "", "GET", "/api/v1/plans", "")                     // 401
	requestAs(h, "Bearer test|newcomer", "GET", "/api/v1/plans", "") // 403
	request(h, "BREW", "/api/v1/sessions/"+uuid.NewString(), "")     // unknown method
	request(h, "GET", "/no/such/"+uuid.NewString(), "")

	// 50 different IDs are one series, labelled by pattern, not by path.
	if n := testutil.CollectAndCount(m.Registry(), "latihan_http_requests_total"); n > 8 {
		t.Fatalf("%d request series: raw paths leaked into labels", n)
	}
	expect := func(name, labels string, want float64) {
		t.Helper()
		body := scrape(t, m)
		line := name + "{" + labels + "} "
		if !strings.Contains(body, line+formatFloat(want)) {
			t.Fatalf("want %s%v in:\n%s", line, want, grep(body, name))
		}
	}
	expect("latihan_http_requests_total", `code="200",method="GET",route="/api/v1/sessions/{id}"`, 50)
	expect("latihan_http_requests_total", `code="405",method="OTHER",route="/api/v1/sessions/{id}"`, 1)
	expect("latihan_http_requests_total", `code="404",method="GET",route="/"`, 1)
	expect("latihan_auth_failures_total", `reason="unauthenticated"`, 1)
	expect("latihan_auth_failures_total", `reason="profile_required"`, 1)
}

func TestRejectionMetrics(t *testing.T) {
	h, m := observedRouter(&fakeTraining{}, io.Discard, Options{PerIP: ratelimit.MustParse("1/m:1")})
	request(h, "GET", "/api/v1/users/me", "")
	request(h, "GET", "/api/v1/users/me", "")
	if body := scrape(t, m); !strings.Contains(body, `latihan_http_rejected_total{reason="ip"} 1`) {
		t.Fatalf("rate-limit rejection not counted:\n%s", grep(body, "rejected"))
	}
}

func TestPanicsAreLoggedWithStack(t *testing.T) {
	var logs bytes.Buffer
	h, m := observedRouter(&fakeTraining{panicOnGet: true}, &logs, Options{})
	if w := request(h, "GET", "/api/v1/sessions/"+uuid.NewString(), ""); w.Code != 500 {
		t.Fatal(w.Code)
	}
	if !strings.Contains(logs.String(), `"stack":"goroutine`) || !strings.Contains(logs.String(), "fakeTraining") {
		t.Fatalf("panic logged without a stack trace:\n%s", logs.String())
	}
	if !strings.Contains(scrape(t, m), "latihan_http_panics_total 1") {
		t.Fatal("panic not counted")
	}
}

type abortingTraining struct{ fakeTraining }

func (abortingTraining) GetSession(context.Context, uuid.UUID, uuid.UUID) (training.Session, error) {
	panic(http.ErrAbortHandler)
}

// http.ErrAbortHandler must reach net/http, which aborts the connection quietly.
func TestAbortHandlerIsRepanicked(t *testing.T) {
	h := NewRouter(training.NewService(&abortingTraining{}), profile.NewService(fakeProfiles{}), &testPinger{}, testAuth{}, slog.New(slog.NewJSONHandler(io.Discard, nil)), Options{})
	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler { //nolint:errorlint // comparing a panic value
			t.Fatalf("recovered %v, want http.ErrAbortHandler", recovered)
		}
	}()
	request(h, "GET", "/api/v1/sessions/"+uuid.NewString(), "")
	t.Fatal("ErrAbortHandler was swallowed")
}

func TestTracingAndLogCorrelation(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer otel.SetTracerProvider(previous)

	var logs bytes.Buffer
	h, _ := observedRouter(&fakeTraining{}, &logs, Options{})
	r := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/sessions/"+uuid.NewString(), nil)
	r.Header.Set("Authorization", "Bearer test|athlete")
	// The caller's trace: our span must join it.
	r.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	h.ServeHTTP(httptest.NewRecorder(), r)

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("%d spans", len(spans))
	}
	span := spans[0]
	if span.Name() != "GET /api/v1/sessions/{id}" || span.SpanContext().TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" ||
		span.Parent().SpanID().String() != "00f067aa0ba902b7" {
		t.Fatalf("span %q trace %s parent %s", span.Name(), span.SpanContext().TraceID(), span.Parent().SpanID())
	}
	var entry map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		_ = json.Unmarshal([]byte(line), &entry)
	}
	if entry["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" || entry["user_id"] != linkedUser.String() {
		t.Fatalf("request log not correlated: %v", entry)
	}
}

func scrape(t *testing.T, m *telemetry.Metrics) string {
	t.Helper()
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), "GET", "/metrics", nil))
	return w.Body.String()
}

func grep(body, needle string) string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, needle) && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func formatFloat(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}
