# Part 7: Seeing inside

*From CRUD to Hardened, part 7 of 10. Code: `git diff phase-06..phase-07`.*

Six parts of defenses, and no way to see them working. Is the rate limiter
refusing 10 requests a minute or 10,000? Is the login endpoint under attack right
now? Why was yesterday's p99 latency two seconds? A service you can't observe is
one you can only hope about.

This part adds metrics, traces and correlated logs, with one constraint that makes
it interesting: **observability must not become a leak.** It must not create a
second copy of health data, and it must not give attackers new endpoints.

## Metrics, with bounded labels

Prometheus metrics are served by [`client_golang`](https://github.com/prometheus/client_golang).
Each metric answers an operational question:

| Metric | Question |
| --- | --- |
| `latihan_http_requests_total{route,method,code}` | What is the traffic, and what fails? |
| `latihan_http_request_duration_seconds` | How slow is each route? |
| `latihan_http_rejected_total{reason}` | Are the limits from part 5 firing (`ip`, `user`, `public`, `overloaded`)? |
| `latihan_auth_failures_total{reason}` | Are we under a token-guessing attack? |
| `latihan_idempotent_replays_total` | How often do mobile retries happen? |
| `latihan_db_pool_*` | Is the connection pool the bottleneck? (Part 10 shows it is.) |
| `latihan_http_panics_total` | Is anything crashing? |

The most important design decision is small and easy to miss: **the `route` label
is the route pattern, never the raw path.**

```text
route="/api/v1/sessions/{id}"          good: one time series
route="/api/v1/sessions/3f1c9a2e-..."   bad: one time series per session
```

With raw paths, every UUID a client touches creates a new time series. A scan of a
million IDs would create a million series and take down the metrics system: a
denial-of-service attack on your monitoring. Unknown HTTP methods are folded into
`OTHER` for the same reason.

Getting the pattern has a nice wrinkle. The middleware runs *before* routing, but
Go 1.22's `ServeMux` records the matched pattern on the request it routed, so the
middleware reads it afterwards:

```go
// The mux records the matched pattern ("GET /api/v1/sessions/{id}") on the
// request it routed; the method is its own label, so keep only the path.
route := req.Pattern
```

## An admin port nobody else can reach

`/metrics` doesn't belong on the public API. It reveals routes, error rates and
internal names. So it's served by a **separate listener**, `ADMIN_ADDR` (`:9090`),
which Compose never publishes. Prometheus scrapes it over the internal network.

The Go profiler (`/debug/pprof`) is even more sensitive. It exposes internals, and a
CPU profile runs for 30 seconds of real CPU. It's available only with
`ADMIN_PPROF=true`, and only on the admin port.

## Traces that don't carry data

Tracing with [OpenTelemetry](https://opentelemetry.io) is **off unless
`OTEL_EXPORTER_OTLP_ENDPOINT` is set**, so it costs nothing until you want it.

When enabled:

- Each request becomes a span named after its route, such as `GET
  /api/v1/sessions/{id}`.
- An incoming W3C `traceparent` header is continued, so a trace started in the
  mobile app flows through the API.
- **Every PostgreSQL query is a child span**, through a small `pgx` tracer.

That last one needs care. A query span that included its arguments would copy
emails, weights and body-fat percentages into the tracing backend. So the tracer
records the SQL text, **never the arguments**:

```go
// QueryTracer turns every pgx query into a child span of the request. The SQL
// text is recorded; query arguments are not, because they carry user data.
```

An integration test runs `SELECT $1::text` with the argument `"alex@example.com
secret"`, then checks that no span attribute contains `secret`.

## Logs you can join

Every log line written during a request carries `trace_id` and `span_id`, through a
small `slog` handler wrapper. That means a log line leads straight to its trace. The
request log also records `user_id`: a pseudonymous UUID, not a name or email.

## Panics: two bugs from the very first review

The original middleware recovered from panics, but:

1. It **logged no stack trace**. A production panic was a mystery.
2. It **swallowed `http.ErrAbortHandler`**. That's a special panic Go's HTTP
   server uses to abort a response on purpose, and it must be re-raised.

Both are fixed:

```go
if recovered == http.ErrAbortHandler {
	// The handler deliberately aborted the response; net/http expects
	// to see this panic and closes the connection quietly.
	panic(recovered)
}
if recovered != nil {
	logger.ErrorContext(ctx, "request panic", "request_id", id,
		"panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
}
```

Part 10's export endpoint relies on exactly this behaviour to abort a half-written
download.

## Trying it locally

```sh
docker compose --profile observability up --build
```

That also starts Prometheus (localhost:9091) and Jaeger (localhost:16686). Set
`OTEL_EXPORTER_OTLP_ENDPOINT=http://jaeger:4318` in `.env` to send traces.

## How we prove it

- **Unit tests:**
  - 50 different session IDs produce **one** metric series;
  - rejection, auth-failure and panic metrics are counted;
  - panics log a stack trace, and `ErrAbortHandler` is re-panicked;
  - an incoming `traceparent` is continued, and the log line carries the same
    `trace_id` plus the `user_id`;
  - pprof is off by default.
- **Break-it checks:** labelling with raw paths produced **54 series instead of
  one**, and swallowing `ErrAbortHandler` failed its test.
- **Live, with the observability profile:**
  - the admin port was unreachable from the host;
  - Prometheus held per-route counts such as `POST /api/v1/sessions` code 201 = 5;
  - Jaeger held a trace started with a hand-made `traceparent`: one request span
    and two query spans;
  - the matching log line carried the same trace ID.

## Trade-offs

- **Tracing is sampled by the standard `OTEL_TRACES_SAMPLER` settings.** Tracing
  every request in production is expensive; `parentbased_traceidratio` at 10% is a
  common start.
- **Logs still contain client IPs and paths.** They're rotated (part 9), and part 10
  adds an option to truncate IPs.
- **No Grafana dashboard ships with it.** Prometheus's own UI is enough to explore;
  a dashboard is a nice follow-up.

**Next: [Part 8, Secrets, TLS and least privilege](08-secrets-tls-least-privilege.md).**
Assume something *does* get compromised. How much should that give the attacker?
