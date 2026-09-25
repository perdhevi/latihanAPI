//go:build integration

package telemetry

import (
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// Query spans carry the SQL text but never the argument values, which hold user data.
func TestQueryTracerRecordsSQLNotArguments(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	defer otel.SetTracerProvider(previous)

	cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Tracer = QueryTracer{}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, parent := otel.Tracer("test").Start(t.Context(), "request")
	var echoed string
	if err := pool.QueryRow(ctx, "SELECT $1::text", "alex@example.com secret").Scan(&echoed); err != nil {
		t.Fatal(err)
	}
	_, _ = pool.Exec(ctx, "SELECT * FROM no_such_table")
	parent.End()

	var queries []sdktrace.ReadOnlySpan
	for _, s := range recorder.Ended() {
		if s.Name() == "postgresql query" {
			queries = append(queries, s)
		}
	}
	if len(queries) != 2 {
		t.Fatalf("%d query spans", len(queries))
	}
	for _, s := range queries {
		if s.Parent().SpanID() != parent.SpanContext().SpanID() {
			t.Fatal("query span is not a child of the request span")
		}
		for _, a := range s.Attributes() {
			if strings.Contains(a.Value.String(), "secret") {
				t.Fatalf("argument value leaked into span attribute %s", a.Key)
			}
		}
	}
	sql := ""
	for _, a := range queries[0].Attributes() {
		if a.Key == "db.query.text" {
			sql = a.Value.AsString()
		}
	}
	if sql != "SELECT $1::text" {
		t.Fatalf("SQL text not recorded: %v", queries[0].Attributes())
	}
	if queries[1].Status().Code.String() != "Error" {
		t.Fatal("failed query not marked as an error")
	}
}
