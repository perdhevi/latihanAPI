package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// The resolved client is the direct peer or an address listed in
// X-Forwarded-For, and X-Forwarded-For only matters when the peer is trusted.
func FuzzClientIP(f *testing.F) {
	f.Add("10.0.0.2:1", "203.0.113.9")
	f.Add("203.0.113.9:1", "1.2.3.4")
	f.Add("10.0.0.2:1", "1.2.3.4, 10.0.0.3, junk")
	f.Add("[::ffff:10.0.0.2]:1", "::1,,")
	f.Add("garbage", "")
	l := newLimits(Options{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}})
	f.Fuzz(func(t *testing.T, remoteAddr, forwardedFor string) {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		r.RemoteAddr = remoteAddr
		r.Header.Set("X-Forwarded-For", forwardedFor)
		got := l.clientIP(r)
		peer, err := netip.ParseAddrPort(remoteAddr)
		if err != nil {
			if got.IsValid() {
				t.Fatalf("unparseable peer %q resolved to %s", remoteAddr, got)
			}
			return
		}
		if !l.isTrusted(peer.Addr().Unmap()) {
			if got != peer.Addr().Unmap() {
				t.Fatalf("untrusted peer %s, but client resolved to %s from %q", peer, got, forwardedFor)
			}
			return
		}
		if got == peer.Addr().Unmap() {
			return
		}
		for _, hop := range strings.Split(forwardedFor, ",") {
			if a, err := netip.ParseAddr(strings.TrimSpace(hop)); err == nil && a.Unmap() == got {
				return
			}
		}
		t.Fatalf("client %s appears nowhere in peer %s or %q", got, peer, forwardedFor)
	})
}
