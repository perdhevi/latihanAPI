//go:build integration

package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/perdhevi/latihanAPI/auth"
	"github.com/perdhevi/latihanAPI/internal/account"
	"github.com/perdhevi/latihanAPI/internal/authn/local"
	"github.com/perdhevi/latihanAPI/internal/profile"
	"github.com/perdhevi/latihanAPI/internal/testdb"
	"github.com/perdhevi/latihanAPI/internal/training"
)

// TestBuiltInProviderEndToEnd runs the jwt provider through the real router
// and database: register, create a profile, use the API, rotate, detect reuse.
func TestBuiltInProviderEndToEnd(t *testing.T) {
	pool := testdb.Open(t)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	keyFile := filepath.Join(t.TempDir(), "signing.pem")
	if _, err := local.GenerateKeyFile(keyFile); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"AUTH_JWT_ISSUER": "http://localhost:8080", "AUTH_JWT_KEY_FILE": keyFile}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	provider, err := local.New(t.Context(), auth.Deps{Getenv: func(k string) string { return env[k] }, Logger: logger, DB: db})
	if err != nil {
		t.Fatal(err)
	}
	h := contract(t, NewRouter(training.NewService(training.NewPostgresRepository(pool)), profile.NewService(profile.NewPostgresRepository(pool)), pool, provider, logger, Options{Account: account.NewStore(pool)}))

	type tokens struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	post := func(path string, body any, status int, out any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		w := requestAs(h, "", http.MethodPost, path, string(raw))
		if w.Code != status {
			t.Fatalf("POST %s: got %d want %d: %s", path, w.Code, status, w.Body)
		}
		if out != nil {
			if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
	}
	withToken := func(token, method, path string, body any, status int) {
		t.Helper()
		raw := ""
		if body != nil {
			encoded, _ := json.Marshal(body)
			raw = string(encoded)
		}
		if w := requestAs(h, "Bearer "+token, method, path, raw); w.Code != status {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, status, w.Body)
		}
	}

	var first tokens
	post("/api/v1/auth/register", map[string]string{"email": "alex@example.com", "password": "correct horse battery staple"}, 201, &first)
	withToken(first.AccessToken, "GET", "/api/v1/sessions", nil, 403) // no profile yet
	withToken(first.AccessToken, "POST", "/api/v1/users", profile.UserInput{DisplayName: "Alex"}, 201)
	withToken(first.AccessToken, "POST", "/api/v1/plans", training.PlanInput{Name: "Leg day", Exercises: plannedExercises()}, 201)
	withToken("forged."+first.AccessToken, "GET", "/api/v1/plans", nil, 401)

	var second tokens
	post("/api/v1/auth/refresh", map[string]string{"refresh_token": first.RefreshToken}, 200, &second)
	withToken(second.AccessToken, "GET", "/api/v1/users/me", nil, 200)
	post("/api/v1/auth/refresh", map[string]string{"refresh_token": first.RefreshToken}, 401, nil)
	post("/api/v1/auth/refresh", map[string]string{"refresh_token": second.RefreshToken}, 401, nil)

	var third tokens
	post("/api/v1/auth/login", map[string]string{"email": "alex@example.com", "password": "correct horse battery staple"}, 200, &third)
	withToken(third.AccessToken, "GET", "/api/v1/plans", nil, 200) // same account, same profile
	post("/api/v1/auth/logout", map[string]string{"refresh_token": third.RefreshToken}, 204, nil)
	post("/api/v1/auth/refresh", map[string]string{"refresh_token": third.RefreshToken}, 401, nil)

	if w := requestAs(h, "", http.MethodGet, "/.well-known/jwks.json", ""); w.Code != 200 {
		t.Fatalf("jwks: %d", w.Code)
	}
	if w := requestAs(h, "", http.MethodGet, "/api/v1/auth/login", ""); w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "POST" {
		t.Fatalf("wrong method on auth route: %d", w.Code)
	}

	// Erasure removes the login too: the password stops working and the email is free again.
	var last tokens
	post("/api/v1/auth/login", map[string]string{"email": "alex@example.com", "password": "correct horse battery staple"}, 200, &last)
	withToken(last.AccessToken, "DELETE", "/api/v1/account", nil, 204)
	post("/api/v1/auth/login", map[string]string{"email": "alex@example.com", "password": "correct horse battery staple"}, 401, nil)
	post("/api/v1/auth/refresh", map[string]string{"refresh_token": last.RefreshToken}, 401, nil)
	post("/api/v1/auth/register", map[string]string{"email": "alex@example.com", "password": "correct horse battery staple"}, 201, nil)
}
