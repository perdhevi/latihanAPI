package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
