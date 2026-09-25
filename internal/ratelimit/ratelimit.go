// Package ratelimit keeps one token bucket per key (client IP, user) in memory.
// That suits a single-instance deployment; several instances would each
// enforce the limit separately, so a shared store would be needed there.
package ratelimit

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Policy is a sustained rate and the burst allowed on top of it.
type Policy struct {
	Rate  rate.Limit
	Burst int
}

// Off disables limiting.
var Off = Policy{}

// Enabled reports whether the policy limits anything.
func (p Policy) Enabled() bool { return p.Burst > 0 }

// ParsePolicy reads "N/unit:burst" (unit s, m or h), for example "50/s:100" or
// "10/m:10", or "off".
func ParsePolicy(s string) (Policy, error) {
	if s == "off" {
		return Off, nil
	}
	ratePart, burstPart, ok := strings.Cut(s, ":")
	count, unit, ok2 := strings.Cut(ratePart, "/")
	if !ok || !ok2 {
		return Policy{}, fmt.Errorf("rate limit %q: want N/unit:burst, such as 50/s:100, or off", s)
	}
	n, err := strconv.ParseFloat(count, 64)
	if err != nil || n <= 0 || math.IsInf(n, 0) {
		return Policy{}, fmt.Errorf("rate limit %q: rate must be a positive number", s)
	}
	per := map[string]time.Duration{"s": time.Second, "m": time.Minute, "h": time.Hour}[unit]
	if per == 0 {
		return Policy{}, fmt.Errorf("rate limit %q: unit must be s, m or h", s)
	}
	burst, err := strconv.Atoi(burstPart)
	if err != nil || burst < 1 || burst > 1_000_000 {
		return Policy{}, fmt.Errorf("rate limit %q: burst must be 1..1000000", s)
	}
	return Policy{Rate: rate.Limit(n / per.Seconds()), Burst: burst}, nil
}

// MustParse is ParsePolicy for constants.
func MustParse(s string) Policy {
	p, err := ParsePolicy(s)
	if err != nil {
		panic(err)
	}
	return p
}

// ErrTooManyKeys is reported when a new key arrives while the table is full.
var ErrTooManyKeys = errors.New("ratelimit: too many distinct clients")

// DefaultMaxKeys bounds memory at roughly 100 bytes per tracked key.
const DefaultMaxKeys = 100_000

// Limiter tracks one bucket per key.
type Limiter struct {
	policy  Policy
	maxKeys int
	// idleAfter is how long an unused bucket takes to refill completely.
	// Dropping it then is indistinguishable from keeping it, so eviction never
	// hands a client extra tokens.
	idleAfter time.Duration
	now       func() time.Time

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func New(p Policy) *Limiter {
	idle := time.Minute
	if p.Enabled() {
		if refill := time.Duration(float64(p.Burst) / float64(p.Rate) * float64(time.Second)); refill > idle {
			idle = refill
		}
	}
	return &Limiter{policy: p, maxKeys: DefaultMaxKeys, idleAfter: idle, now: time.Now, buckets: map[string]*bucket{}}
}

// Allow takes one token for key. When it refuses, retryAfter says when a
// token will be available (zero for ErrTooManyKeys).
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration, err error) {
	if !l.policy.Enabled() {
		return true, 0, nil
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) >= time.Minute {
		l.sweep(now)
	}
	b, found := l.buckets[key]
	if !found {
		if len(l.buckets) >= l.maxKeys {
			// Refusing unknown keys, rather than evicting live buckets, keeps
			// an attacker with many addresses from resetting others' limits.
			return false, 0, ErrTooManyKeys
		}
		b = &bucket{limiter: rate.NewLimiter(l.policy.Rate, l.policy.Burst)}
		l.buckets[key] = b
	}
	b.lastSeen = now
	r := b.limiter.ReserveN(now, 1)
	if delay := r.DelayFrom(now); delay > 0 {
		r.CancelAt(now)
		return false, delay, nil
	}
	return true, 0, nil
}

func (l *Limiter) sweep(now time.Time) {
	for key, b := range l.buckets {
		if now.Sub(b.lastSeen) >= l.idleAfter {
			delete(l.buckets, key)
		}
	}
	l.lastSweep = now
}
