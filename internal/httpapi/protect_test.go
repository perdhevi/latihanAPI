package httpapi

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/perdhevi/latihanAPI/internal/profile"
	"github.com/perdhevi/latihanAPI/internal/ratelimit"
	"github.com/perdhevi/latihanAPI/internal/training"
)

func limitedRouter(repo *fakeTraining, opts Options) http.Handler {
	return NewRouter(training.NewService(repo), profile.NewService(fakeProfiles{}), &testPinger{}, registeringAuth{}, slog.New(slog.NewJSONHandler(io.Discard, nil)), opts)
}

func from(h http.Handler, remoteAddr, forwardedFor, authorization, method, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader("{}"))
	r.RemoteAddr = remoteAddr
	r.Header.Set("Content-Type", "application/json")
	if forwardedFor != "" {
		r.Header.Set("X-Forwarded-For", forwardedFor)
	}
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPerIPLimit(t *testing.T) {
	h := limitedRouter(&fakeTraining{}, Options{PerIP: ratelimit.MustParse("1/m:2")})
	session := "/api/v1/sessions/" + uuid.NewString()
	for range 2 {
		if w := from(h, "198.51.100.1:1000", "", "Bearer test|athlete", "GET", session); w.Code != 200 {
			t.Fatalf("within burst: %d", w.Code)
		}
	}
	w := from(h, "198.51.100.1:1000", "", "Bearer test|athlete", "GET", session)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" || !strings.Contains(w.Body.String(), `"rate_limited"`) {
		t.Fatalf("over limit: %d %v %s", w.Code, w.Header(), w.Body)
	}
	if w := from(h, "198.51.100.2:1000", "", "Bearer test|athlete", "GET", session); w.Code != 200 {
		t.Fatal("another address was limited")
	}
	if w := from(h, "198.51.100.1:1000", "", "", "GET", "/health"); w.Code != 200 {
		t.Fatal("health checks must not be rate limited")
	}
	// Rotating addresses inside one IPv6 /64 does not escape the limit.
	for i, addr := range []string{"[2001:db8:1:2::1]:1", "[2001:db8:1:2::2]:1", "[2001:db8:1:2:ffff::3]:1"} {
		want := http.StatusOK
		if i == 2 {
			want = http.StatusTooManyRequests
		}
		if w := from(h, addr, "", "Bearer test|athlete", "GET", session); w.Code != want {
			t.Fatalf("%s: %d, want %d", addr, w.Code, want)
		}
	}
}

func TestForwardedForTrust(t *testing.T) {
	l := newLimits(Options{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}})
	for _, tc := range []struct {
		name, peer, xff, want string
	}{
		{"untrusted peer spoofing the header", "203.0.113.9:1", "1.2.3.4", "203.0.113.9"},
		{"trusted proxy", "10.0.0.2:1", "203.0.113.9", "203.0.113.9"},
		{"client prepends a fake hop", "10.0.0.2:1", "1.2.3.4, 203.0.113.9", "203.0.113.9"},
		{"two trusted proxies", "10.0.0.2:1", "203.0.113.9, 10.0.0.3", "203.0.113.9"},
		{"only trusted hops", "10.0.0.2:1", "10.0.0.3", "10.0.0.3"},
		{"malformed hop", "10.0.0.2:1", "junk", "10.0.0.2"},
		{"trusted proxy without header", "10.0.0.2:1", "", "10.0.0.2"},
		{"IPv4-mapped peer", "[::ffff:203.0.113.9]:1", "", "203.0.113.9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), "GET", "/", nil)
			r.RemoteAddr = tc.peer
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := l.clientIP(r).String(); got != tc.want {
				t.Fatalf("client %s, want %s", got, tc.want)
			}
		})
	}
	// Without trusted proxies the header is ignored entirely.
	none := newLimits(Options{})
	r := httptest.NewRequestWithContext(t.Context(), "GET", "/", nil)
	r.RemoteAddr = "203.0.113.9:1"
	r.Header.Set("X-Forwarded-For", "10.0.0.1")
	if got := none.clientIP(r).String(); got != "203.0.113.9" {
		t.Fatalf("header trusted without a trusted proxy: %s", got)
	}
}

func TestPublicLimitCoversOnlyProviderRoutes(t *testing.T) {
	h := limitedRouter(&fakeTraining{}, Options{Public: ratelimit.MustParse("1/m:1")})
	if w := from(h, "198.51.100.1:1", "", "", "POST", "/api/v1/auth/login"); w.Code != http.StatusNoContent {
		t.Fatalf("first login: %d", w.Code)
	}
	if w := from(h, "198.51.100.1:1", "", "", "POST", "/api/v1/auth/login"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("second login: %d", w.Code)
	}
	for range 3 {
		if w := from(h, "198.51.100.1:1", "", "Bearer test|athlete", "GET", "/api/v1/users/me"); w.Code != 200 {
			t.Fatalf("API route hit the login limit: %d", w.Code)
		}
	}
}

func TestPerUserLimitFollowsTheUser(t *testing.T) {
	h := limitedRouter(&fakeTraining{}, Options{PerUser: ratelimit.MustParse("1/m:1")})
	if w := from(h, "198.51.100.1:1", "", "Bearer test|athlete", "GET", "/api/v1/users/me"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := from(h, "198.51.100.2:1", "", "Bearer test|athlete", "GET", "/api/v1/users/me"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("same user from a new address: %d", w.Code)
	}
	if w := from(h, "198.51.100.1:1", "", "Bearer test|someone-else", "POST", "/api/v1/users"); w.Code == http.StatusTooManyRequests {
		t.Fatal("another user was limited")
	}
}

func TestInFlightCap(t *testing.T) {
	repo := &fakeTraining{entered: make(chan struct{}), release: make(chan struct{})}
	h := limitedRouter(repo, Options{MaxInFlight: 1})
	done := make(chan int)
	go func() {
		done <- from(h, "198.51.100.1:1", "", "Bearer test|athlete", "GET", "/api/v1/sessions/"+uuid.NewString()).Code
	}()
	<-repo.entered // the first request now holds the only slot
	w := from(h, "198.51.100.2:1", "", "Bearer test|athlete", "GET", "/api/v1/users/me")
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("over capacity: %d", w.Code)
	}
	if w := from(h, "198.51.100.2:1", "", "", "GET", "/health"); w.Code != 200 {
		t.Fatal("health check shed under load")
	}
	close(repo.release)
	if code := <-done; code != 200 {
		t.Fatalf("held request: %d", code)
	}
	if w := from(h, "198.51.100.2:1", "", "Bearer test|athlete", "GET", "/api/v1/users/me"); w.Code != 200 {
		t.Fatal("slot not released")
	}
}

func TestResponsesAreNotCacheableAndLogClient(t *testing.T) {
	var logs bytes.Buffer
	h := NewRouter(training.NewService(&fakeTraining{}), profile.NewService(fakeProfiles{}), &testPinger{}, testAuth{}, slog.New(slog.NewJSONHandler(&logs, nil)), Options{})
	w := from(h, "198.51.100.7:1", "", "Bearer test|athlete", "GET", "/api/v1/users/me")
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("personal data may be cached")
	}
	if !strings.Contains(logs.String(), `"client_ip":"198.51.100.7"`) {
		t.Fatalf("client address not logged: %s", logs.String())
	}
}
