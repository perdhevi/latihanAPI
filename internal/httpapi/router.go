package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/perdhevi/latihanAPI/internal/profile"
	"github.com/perdhevi/latihanAPI/internal/training"
)

type Pinger interface{ Ping(context.Context) error }

func NewRouter(sessions *training.Service, profiles *profile.Service, db Pinger, logger *slog.Logger) http.Handler {
	h := &handlers{training: sessions, profiles: profiles, logger: logger}
	mux := http.NewServeMux()
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
	mux.HandleFunc("GET /api/v1/sessions", h.listSessions)
	mux.HandleFunc("POST /api/v1/sessions", h.createSession)
	mux.HandleFunc("GET /api/v1/sessions/{id}", h.getSession)
	mux.HandleFunc("PUT /api/v1/sessions/{id}", h.updateSession)
	mux.HandleFunc("DELETE /api/v1/sessions/{id}", h.deleteSession)
	mux.HandleFunc("GET /api/v1/plans", h.listPlans)
	mux.HandleFunc("POST /api/v1/plans", h.createPlan)
	mux.HandleFunc("GET /api/v1/plans/{id}", h.getPlan)
	mux.HandleFunc("PUT /api/v1/plans/{id}", h.updatePlan)
	mux.HandleFunc("DELETE /api/v1/plans/{id}", h.deletePlan)
	mux.HandleFunc("GET /api/v1/plans/{id}/comparison", h.comparison)
	mux.HandleFunc("POST /api/v1/users", h.createUser)
	mux.HandleFunc("GET /api/v1/users/{userID}", h.getUser)
	mux.HandleFunc("PUT /api/v1/users/{userID}", h.updateUser)
	mux.HandleFunc("DELETE /api/v1/users/{userID}", h.deleteUser)
	mux.HandleFunc("GET /api/v1/users/{userID}/measurements", h.listMeasurements)
	mux.HandleFunc("POST /api/v1/users/{userID}/measurements", h.createMeasurement)
	mux.HandleFunc("GET /api/v1/users/{userID}/measurements/{id}", h.getMeasurement)
	mux.HandleFunc("PUT /api/v1/users/{userID}/measurements/{id}", h.updateMeasurement)
	mux.HandleFunc("DELETE /api/v1/users/{userID}/measurements/{id}", h.deleteMeasurement)
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
	return middleware(mux, logger)
}
