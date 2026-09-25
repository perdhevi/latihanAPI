package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/perdhevi/latihanAPI/internal/telemetry"
)

func TestServerRefusesOversizedHeaders(t *testing.T) {
	ts := httptest.NewUnstartedServer(nil)
	ts.Config = newServer("", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	ts.Start()
	defer ts.Close()
	for size, want := range map[int]int{8 << 10: http.StatusOK, 32 << 10: http.StatusRequestHeaderFieldsTooLarge} {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Big", strings.Repeat("a", size))
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%d-byte header: %d, want %d", size, resp.StatusCode, want)
		}
	}
}

func TestAdminServer(t *testing.T) {
	metrics := telemetry.NewMetrics("v-test")
	for _, withPprof := range []bool{false, true} {
		h := newAdminServer("", metrics, withPprof).Handler
		get := func(path string) *httptest.ResponseRecorder {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
			return w
		}
		if w := get("/metrics"); w.Code != 200 || !strings.Contains(w.Body.String(), `latihan_build_info{go_version=`) || !strings.Contains(w.Body.String(), `version="v-test"`) {
			t.Fatalf("metrics: %d", w.Code)
		}
		wantPprof := http.StatusNotFound
		if withPprof {
			wantPprof = http.StatusOK
		}
		if w := get("/debug/pprof/"); w.Code != wantPprof {
			t.Fatalf("pprof enabled=%v: %d", withPprof, w.Code)
		}
	}
}

func TestHealthcheck(t *testing.T) {
	ready := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ready.Close()
	_, port, _ := strings.Cut(strings.TrimPrefix(ready.URL, "http://"), ":")
	if err := healthcheck(":" + port); err != nil {
		t.Fatalf("ready server: %v", err)
	}
	notReady := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer notReady.Close()
	_, port, _ = strings.Cut(strings.TrimPrefix(notReady.URL, "http://"), ":")
	if err := healthcheck(":" + port); err == nil {
		t.Fatal("503 reported healthy")
	}
	if err := healthcheck("127.0.0.1:1"); err == nil {
		t.Fatal("closed port reported healthy")
	}
}
