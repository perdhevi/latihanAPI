package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/perdhevi/latihanAPI/auth"
	"github.com/perdhevi/latihanAPI/internal/profile"
	"github.com/perdhevi/latihanAPI/internal/training"
)

type Pinger interface{ Ping(context.Context) error }

// NewRouter serves the API. Every /api/v1 route requires credentials that
// authenticator accepts; health checks and any routes the provider
// registers itself (such as login) are public.
func NewRouter(sessions *training.Service, profiles *profile.Service, db Pinger, authenticator auth.Authenticator, logger *slog.Logger, opts Options) http.Handler {
	l := newLimits(opts)
	h := &handlers{training: sessions, profiles: profiles, auth: authenticator, logger: logger, limits: l, requireIfMatch: opts.RequireIfMatch, idempotency: opts.Idempotency, metrics: opts.Metrics}
	mux := http.NewServeMux()
	// Provider routes get their own mux so the stricter public limit can be
	// applied to exactly the routes the provider mounted.
	providerMux := http.NewServeMux()
	if provider, ok := authenticator.(auth.RouteRegistrar); ok {
		provider.RegisterRoutes(providerMux)
	}
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			writeError(w, http.StatusServiceUnavailable, "not_ready", "database unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/v1/sessions", h.withUser(h.listSessions))
	mux.HandleFunc("POST /api/v1/sessions", h.withUser(h.idempotent(h.createSession)))
	mux.HandleFunc("GET /api/v1/sessions/{id}", h.withUser(h.getSession))
	mux.HandleFunc("PUT /api/v1/sessions/{id}", h.withUser(h.updateSession))
	mux.HandleFunc("DELETE /api/v1/sessions/{id}", h.withUser(h.deleteSession))
	mux.HandleFunc("GET /api/v1/plans", h.withUser(h.listPlans))
	mux.HandleFunc("POST /api/v1/plans", h.withUser(h.idempotent(h.createPlan)))
	mux.HandleFunc("GET /api/v1/plans/{id}", h.withUser(h.getPlan))
	mux.HandleFunc("PUT /api/v1/plans/{id}", h.withUser(h.updatePlan))
	mux.HandleFunc("DELETE /api/v1/plans/{id}", h.withUser(h.deletePlan))
	mux.HandleFunc("GET /api/v1/plans/{id}/comparison", h.withUser(h.comparison))
	mux.HandleFunc("POST /api/v1/users", h.authenticated(h.idempotent(h.createUser)))
	mux.HandleFunc("GET /api/v1/users/{userID}", h.withUser(h.getUser))
	mux.HandleFunc("PUT /api/v1/users/{userID}", h.withUser(h.updateUser))
	mux.HandleFunc("DELETE /api/v1/users/{userID}", h.withUser(h.deleteUser))
	mux.HandleFunc("GET /api/v1/users/{userID}/measurements", h.withUser(h.listMeasurements))
	mux.HandleFunc("POST /api/v1/users/{userID}/measurements", h.withUser(h.idempotent(h.createMeasurement)))
	mux.HandleFunc("GET /api/v1/users/{userID}/measurements/{id}", h.withUser(h.getMeasurement))
	mux.HandleFunc("PUT /api/v1/users/{userID}/measurements/{id}", h.withUser(h.updateMeasurement))
	mux.HandleFunc("DELETE /api/v1/users/{userID}/measurements/{id}", h.withUser(h.deleteMeasurement))
	// Path-only fallbacks keep method and route errors in the JSON error format.
	for path, allow := range map[string]string{
		"/health": "GET, HEAD", "/ready": "GET, HEAD",
		"/api/v1/sessions": "GET, HEAD, POST", "/api/v1/sessions/{id}": "GET, HEAD, PUT, DELETE",
		"/api/v1/plans": "GET, HEAD, POST", "/api/v1/plans/{id}": "GET, HEAD, PUT, DELETE",
		"/api/v1/plans/{id}/comparison": "GET, HEAD",
		"/api/v1/users":                 "POST", "/api/v1/users/{userID}": "GET, HEAD, PUT, DELETE",
		"/api/v1/users/{userID}/measurements":      "GET, HEAD, POST",
		"/api/v1/users/{userID}/measurements/{id}": "GET, HEAD, PUT, DELETE",
	} {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Allow", allow)
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
	})
	return middleware(l.protect(mux, providerMux, h), logger, l, opts.Metrics)
}
