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
