package telemetry

import (
	"context"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// TracerName identifies this service's instrumentation.
const TracerName = "github.com/perdhevi/latihanAPI"

// SetupTracing exports traces over OTLP/HTTP when OTEL_EXPORTER_OTLP_ENDPOINT
// (or OTEL_EXPORTER_OTLP_TRACES_ENDPOINT) is set, and otherwise leaves the
// no-op tracer in place so tracing costs nothing. The exporter and sampler
// follow the standard OTEL_* variables, such as OTEL_TRACES_SAMPLER.
// shutdown flushes buffered spans.
func SetupTracing(ctx context.Context, version string) (shutdown func(context.Context) error, err error) {
	// W3C trace context is honoured either way, so callers' trace IDs appear in logs.
	otel.SetTextMapPropagator(propagation.TraceContext{})
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	res := resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName("latihan-api"), semconv.ServiceVersion(version))
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}

// QueryTracer turns every pgx query into a child span of the request. The SQL
// text is recorded; query arguments are not, because they carry user data.
type QueryTracer struct{}

type querySpanKey struct{}

func (QueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	ctx, span := otel.Tracer(TracerName).Start(ctx, "postgresql query", trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(semconv.DBSystemPostgreSQL, semconv.DBQueryText(data.SQL)))
	return context.WithValue(ctx, querySpanKey{}, span)
}

func (QueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	span, ok := ctx.Value(querySpanKey{}).(trace.Span)
	if !ok {
		return
	}
	if data.Err != nil {
		span.RecordError(data.Err)
		span.SetStatus(codes.Error, "query failed")
	}
	span.SetAttributes(attribute.Int64("db.rows_affected", data.CommandTag.RowsAffected()))
	span.End()
}

// LogHandler adds trace_id and span_id to every record logged with a context
// that carries a span, so a log line leads straight to its trace.
type LogHandler struct{ slog.Handler }

func (h LogHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}

func (h LogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return LogHandler{h.Handler.WithAttrs(attrs)}
}

func (h LogHandler) WithGroup(name string) slog.Handler { return LogHandler{h.Handler.WithGroup(name)} }
