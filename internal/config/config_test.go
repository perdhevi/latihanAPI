package config

import (
	"log/slog"
	"testing"
)

func TestLoad(t *testing.T) {
	for _, tc := range []struct {
		name, addr, url, level, provider string
		wantErr                          bool
	}{
		{"defaults", "", "postgres://localhost/latihan", "", "oidc", false},
		{"explicit", "127.0.0.1:9000", "postgres://localhost/latihan", "debug", "firebase", false},
		{"missing database", "", "", "", "oidc", true},
		{"missing auth provider", "", "postgres://localhost/latihan", "", "", true},
		{"bad address", "localhost", "postgres://localhost/latihan", "", "oidc", true},
		{"bad log level", "", "postgres://localhost/latihan", "verbose", "oidc", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HTTP_ADDR", tc.addr)
			t.Setenv("DATABASE_URL", tc.url)
			t.Setenv("LOG_LEVEL", tc.level)
			t.Setenv("AUTH_PROVIDER", tc.provider)
			cfg, err := Load()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Load() error=%v", err)
			}
			if !tc.wantErr && tc.addr == "" && cfg.HTTPAddr != ":8080" {
				t.Fatal("wrong default address")
			}
			if !tc.wantErr && tc.level == "debug" && cfg.LogLevel != slog.LevelDebug {
				t.Fatal("wrong log level")
			}
		})
	}
}

func TestAbuseSettings(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/latihan")
	t.Setenv("AUTH_PROVIDER", "jwt")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RateLimitIP.Enabled() || !cfg.RateLimitUser.Enabled() || !cfg.RateLimitAuth.Enabled() || cfg.MaxInFlight != 256 || cfg.DBMaxConns != 10 || len(cfg.TrustedProxies) != 0 {
		t.Fatalf("defaults must protect a fresh deployment: %+v", cfg)
	}
	t.Setenv("TRUSTED_PROXIES", "172.18.0.0/16, 10.0.0.7 ,::1")
	t.Setenv("RATE_LIMIT_AUTH", "off")
	cfg, err = Load()
	if err != nil || len(cfg.TrustedProxies) != 3 || cfg.TrustedProxies[1].String() != "10.0.0.7/32" || cfg.RateLimitAuth.Enabled() {
		t.Fatalf("%+v %v", cfg, err)
	}
	for name, value := range map[string]string{
		"TRUSTED_PROXIES":      "proxy.example",
		"RATE_LIMIT_IP":        "fast",
		"MAX_IN_FLIGHT":        "0",
		"DB_MAX_CONNS":         "5000",
		"DB_STATEMENT_TIMEOUT": "2h",
		"ADMIN_ADDR":           ":8080",
		"ADMIN_PPROF":          "maybe",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, value)
			if _, err := Load(); err == nil {
				t.Fatalf("%s=%s accepted", name, value)
			}
		})
	}
}
