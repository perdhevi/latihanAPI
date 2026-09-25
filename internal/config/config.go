package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/perdhevi/latihanAPI/internal/ratelimit"
)

type Config struct {
	HTTPAddr    string
	DatabaseURL string
	LogLevel    slog.Level
	// AuthProvider names the registered authentication provider. There is no
	// default: a missing setting must never leave the API open.
	AuthProvider string

	// Abuse limits; every one has a default so a fresh deployment is protected.
	RateLimitIP    ratelimit.Policy
	RateLimitUser  ratelimit.Policy
	RateLimitAuth  ratelimit.Policy
	MaxInFlight    int
	TrustedProxies []netip.Prefix

	DBMaxConns         int32
	DBStatementTimeout time.Duration

	// RequireIfMatch makes clients prove which version they are changing.
	RequireIfMatch bool
}

func Load() (Config, error) {
	c := Config{HTTPAddr: os.Getenv("HTTP_ADDR"), DatabaseURL: os.Getenv("DATABASE_URL"), AuthProvider: os.Getenv("AUTH_PROVIDER")}
	if c.HTTPAddr == "" {
		c.HTTPAddr = ":8080"
	}
	if _, _, err := net.SplitHostPort(c.HTTPAddr); err != nil {
		return Config{}, fmt.Errorf("HTTP_ADDR must be host:port: %w", err)
	}
	if c.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if c.AuthProvider == "" {
		return Config{}, errors.New("AUTH_PROVIDER is required (for example jwt, firebase, cognito or oidc)")
	}
	if level := os.Getenv("LOG_LEVEL"); level != "" {
		if err := c.LogLevel.UnmarshalText([]byte(level)); err != nil {
			return Config{}, errors.New("invalid LOG_LEVEL")
		}
	}
	var err error
	for _, p := range []struct {
		name, fallback string
		dst            *ratelimit.Policy
	}{
		{"RATE_LIMIT_IP", "50/s:100", &c.RateLimitIP},
		{"RATE_LIMIT_USER", "10/s:30", &c.RateLimitUser},
		{"RATE_LIMIT_AUTH", "10/m:10", &c.RateLimitAuth},
	} {
		if *p.dst, err = ratelimit.ParsePolicy(getenv(p.name, p.fallback)); err != nil {
			return Config{}, fmt.Errorf("%s: %w", p.name, err)
		}
	}
	if c.MaxInFlight, err = intSetting("MAX_IN_FLIGHT", 256, 1, 100_000); err != nil {
		return Config{}, err
	}
	maxConns, err := intSetting("DB_MAX_CONNS", 10, 1, 1000)
	if err != nil {
		return Config{}, err
	}
	c.DBMaxConns = int32(maxConns) //nolint:gosec // G115: bounded to 1..1000 above
	if c.DBStatementTimeout, err = time.ParseDuration(getenv("DB_STATEMENT_TIMEOUT", "5s")); err != nil || c.DBStatementTimeout < 100*time.Millisecond || c.DBStatementTimeout > time.Minute {
		return Config{}, errors.New("DB_STATEMENT_TIMEOUT must be a duration between 100ms and 1m")
	}
	if raw := os.Getenv("REQUIRE_IF_MATCH"); raw != "" {
		if c.RequireIfMatch, err = strconv.ParseBool(raw); err != nil {
			return Config{}, errors.New("REQUIRE_IF_MATCH must be true or false")
		}
	}
	for _, raw := range strings.Split(os.Getenv("TRUSTED_PROXIES"), ",") {
		if raw = strings.TrimSpace(raw); raw == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			addr, addrErr := netip.ParseAddr(raw)
			if addrErr != nil {
				return Config{}, fmt.Errorf("TRUSTED_PROXIES: %q is not an IP address or CIDR", raw)
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		c.TrustedProxies = append(c.TrustedProxies, prefix.Masked())
	}
	return c, nil
}

func getenv(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func intSetting(name string, fallback, lowest, highest int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < lowest || n > highest {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", name, lowest, highest)
	}
	return n, nil
}
