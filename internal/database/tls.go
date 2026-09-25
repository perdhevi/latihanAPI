package database

import (
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// TransportWarning explains why the connection described by url is not
// protected by verified TLS, or returns "" when it is, or when the database is
// on this host or a private container network where traffic never crosses a
// shared link. Use sslmode=verify-full (with sslrootcert for a private CA) for
// any other database: "require" encrypts but accepts any certificate, so a
// machine in the middle can still read everything.
func TransportWarning(url string) string {
	cfg, err := pgconn.ParseConfig(url)
	if err != nil {
		return ""
	}
	if isLocal(cfg.Host) {
		return ""
	}
	verified := cfg.TLSConfig != nil && !cfg.TLSConfig.InsecureSkipVerify
	for _, fallback := range cfg.Fallbacks {
		if fallback.TLSConfig == nil || fallback.TLSConfig.InsecureSkipVerify {
			verified = false
		}
	}
	if verified {
		return ""
	}
	return "database connection to " + cfg.Host + " is not certificate-verified; use sslmode=verify-full"
}

// isLocal is true for Unix sockets, loopback addresses and single-label host
// names such as a Compose service ("postgres").
func isLocal(host string) bool {
	if strings.HasPrefix(host, "/") || host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return !strings.Contains(host, ".")
}
