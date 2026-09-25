package httpapi

import (
	"errors"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/perdhevi/latihanAPI/internal/ratelimit"
)

// Options holds the abuse limits. The zero value disables them all, which
// suits tests; the service always passes the configured values.
type Options struct {
	// PerIP limits every client address (IPv6 per /64) across all routes but
	// the health checks.
	PerIP ratelimit.Policy
	// PerUser limits each authenticated identity.
	PerUser ratelimit.Policy
	// Public limits each client address on routes the auth provider mounts,
	// such as login, where each request may cost a password hash.
	Public ratelimit.Policy
	// MaxInFlight caps concurrent requests; excess requests get 503 at once
	// instead of queueing until everything times out. Zero means no cap.
	MaxInFlight int
	// TrustedProxies may set X-Forwarded-For. Empty trusts no one.
	TrustedProxies []netip.Prefix
}

type limits struct {
	perIP, perUser, public *ratelimit.Limiter
	inFlight               chan struct{}
	trusted                []netip.Prefix
}

func newLimits(o Options) *limits {
	l := &limits{
		perIP:   ratelimit.New(o.PerIP),
		perUser: ratelimit.New(o.PerUser),
		public:  ratelimit.New(o.Public),
		trusted: o.TrustedProxies,
	}
	if o.MaxInFlight > 0 {
		l.inFlight = make(chan struct{}, o.MaxInFlight)
	}
	return l
}

// clientIP returns the address of the client that sent r. The direct peer is
// the client unless it is a trusted proxy; then X-Forwarded-For is read from
// the right, skipping trusted proxies, and the first other address is the
// client. Entries left of it were written by the client and are ignored.
func (l *limits) clientIP(r *http.Request) netip.Addr {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	addr := peer.Addr().Unmap()
	if !l.isTrusted(addr) {
		return addr
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return addr // a malformed hop: stop at the last address we can vouch for
		}
		hop = hop.Unmap()
		if !l.isTrusted(hop) {
			return hop
		}
		addr = hop
	}
	return addr
}

func (l *limits) isTrusted(a netip.Addr) bool {
	for _, p := range l.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// rateKey groups IPv6 clients by /64, the block a single subscriber usually
// controls, so rotating addresses inside it does not multiply the limit.
func rateKey(a netip.Addr) string {
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p.String()
	}
	return a.String()
}

func isHealthCheck(r *http.Request) bool { return r.URL.Path == "/health" || r.URL.Path == "/ready" }

// protect applies the in-flight cap and the per-IP limit, then routes to the
// provider's routes (with the stricter public limit) or the API.
func (l *limits) protect(api, provider *http.ServeMux, h *handlers) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isHealthCheck(r) {
			api.ServeHTTP(w, r)
			return
		}
		if l.inFlight != nil {
			select {
			case l.inFlight <- struct{}{}:
				defer func() { <-l.inFlight }()
			default:
				w.Header().Set("Retry-After", "1")
				writeError(w, http.StatusServiceUnavailable, "overloaded", "server is busy; retry shortly")
				return
			}
		}
		key := rateKey(clientAddr(r))
		if !h.allow(w, r, l.perIP, "ip:"+key) {
			return
		}
		if _, pattern := provider.Handler(r); pattern != "" {
			if !h.allow(w, r, l.public, "public:"+key) {
				return
			}
			provider.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	})
}

// allow takes a token from limiter for key, answering 429 when there is none.
func (h *handlers) allow(w http.ResponseWriter, r *http.Request, limiter *ratelimit.Limiter, key string) bool {
	ok, retryAfter, err := limiter.Allow(key)
	if ok {
		return true
	}
	if errors.Is(err, ratelimit.ErrTooManyKeys) {
		h.logger.WarnContext(r.Context(), "rate limiter full; refusing new clients", "request_id", r.Context().Value(requestIDKey{}))
		retryAfter = time.Minute
	}
	seconds := int(math.Ceil(retryAfter.Seconds()))
	w.Header().Set("Retry-After", strconv.Itoa(max(seconds, 1)))
	writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests; retry later")
	return false
}

// clientAddr reads the address the outer middleware resolved into RemoteAddr.
func clientAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	a, _ := netip.ParseAddr(host)
	return a
}
