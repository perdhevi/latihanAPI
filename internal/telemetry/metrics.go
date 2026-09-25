// Package telemetry holds the service's metrics, tracing and log correlation.
package telemetry

import (
	"net/http"
	"runtime"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics are the service's Prometheus metrics. Every label has a small, fixed
// set of values: routes are mux patterns such as /api/v1/sessions/{id}, never
// raw paths, so one client walking many IDs cannot create unbounded series.
// A nil *Metrics records nothing.
type Metrics struct {
	registry   *prometheus.Registry
	requests   *prometheus.CounterVec
	duration   *prometheus.HistogramVec
	inFlight   prometheus.Gauge
	rejected   *prometheus.CounterVec
	authFailed *prometheus.CounterVec
	replays    prometheus.Counter
	panics     prometheus.Counter
}

func NewMetrics(version string) *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "latihan_http_requests_total", Help: "HTTP requests by route pattern, method and status code.",
		}, []string{"route", "method", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "latihan_http_request_duration_seconds", Help: "HTTP request latency by route pattern and method.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}, []string{"route", "method"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "latihan_http_requests_in_flight", Help: "HTTP requests currently being served.",
		}),
		rejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "latihan_http_rejected_total", Help: "Requests refused before reaching a handler, by reason (ip, user, public, overloaded, too_many_clients).",
		}, []string{"reason"}),
		authFailed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "latihan_auth_failures_total", Help: "Authentication and authorization failures by reason (unauthenticated, unavailable, profile_required).",
		}, []string{"reason"}),
		replays: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "latihan_idempotent_replays_total", Help: "Responses replayed for a repeated Idempotency-Key.",
		}),
		panics: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "latihan_http_panics_total", Help: "Handler panics recovered by the server.",
		}),
	}
	build := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "latihan_build_info", Help: "Always 1; labels identify the running build.",
		ConstLabels: prometheus.Labels{"version": version, "go_version": runtime.Version()},
	})
	build.Set(1)
	m.registry.MustRegister(m.requests, m.duration, m.inFlight, m.rejected, m.authFailed, m.replays, m.panics, build,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

// Handler serves the metrics in Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError})
}

// Registry exposes the registry, for registering further collectors and tests.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

var knownMethods = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodPost: true, http.MethodPut: true,
	http.MethodPatch: true, http.MethodDelete: true, http.MethodOptions: true,
}

// ObserveRequest records one finished request. route is the matched mux
// pattern; unknown methods are folded into OTHER to keep labels bounded.
func (m *Metrics) ObserveRequest(route, method string, status int, seconds float64) {
	if m == nil {
		return
	}
	if !knownMethods[method] {
		method = "OTHER"
	}
	if route == "" {
		route = "unmatched"
	}
	m.requests.WithLabelValues(route, method, strconv.Itoa(status)).Inc()
	m.duration.WithLabelValues(route, method).Observe(seconds)
}

func (m *Metrics) RequestStarted() {
	if m != nil {
		m.inFlight.Inc()
	}
}

func (m *Metrics) RequestFinished() {
	if m != nil {
		m.inFlight.Dec()
	}
}

func (m *Metrics) Rejected(reason string) {
	if m != nil {
		m.rejected.WithLabelValues(reason).Inc()
	}
}

func (m *Metrics) AuthFailed(reason string) {
	if m != nil {
		m.authFailed.WithLabelValues(reason).Inc()
	}
}

func (m *Metrics) Replayed() {
	if m != nil {
		m.replays.Inc()
	}
}

func (m *Metrics) Panicked() {
	if m != nil {
		m.panics.Inc()
	}
}

// RegisterPool exports connection pool statistics, read at scrape time.
func (m *Metrics) RegisterPool(pool *pgxpool.Pool) {
	m.registry.MustRegister(&poolCollector{pool: pool})
}

var (
	poolConns = prometheus.NewDesc("latihan_db_pool_connections", "Pool connections by state.", []string{"state"}, nil)
	poolMax   = prometheus.NewDesc("latihan_db_pool_max_connections", "Configured maximum pool size.", nil, nil)
	poolWaits = prometheus.NewDesc("latihan_db_pool_empty_acquires_total", "Acquires that had to wait because every connection was busy.", nil, nil)
	poolWait  = prometheus.NewDesc("latihan_db_pool_acquire_wait_seconds_total", "Total time spent waiting for a connection.", nil, nil)
)

type poolCollector struct{ pool *pgxpool.Pool }

func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- poolConns
	ch <- poolMax
	ch <- poolWaits
	ch <- poolWait
}

func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.pool.Stat()
	ch <- prometheus.MustNewConstMetric(poolConns, prometheus.GaugeValue, float64(s.AcquiredConns()), "acquired")
	ch <- prometheus.MustNewConstMetric(poolConns, prometheus.GaugeValue, float64(s.IdleConns()), "idle")
	ch <- prometheus.MustNewConstMetric(poolConns, prometheus.GaugeValue, float64(s.ConstructingConns()), "constructing")
	ch <- prometheus.MustNewConstMetric(poolMax, prometheus.GaugeValue, float64(s.MaxConns()))
	ch <- prometheus.MustNewConstMetric(poolWaits, prometheus.CounterValue, float64(s.EmptyAcquireCount()))
	ch <- prometheus.MustNewConstMetric(poolWait, prometheus.CounterValue, s.EmptyAcquireWaitTime().Seconds())
}
