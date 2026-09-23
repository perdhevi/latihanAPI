package config

import (
	"log/slog"
	"testing"
)

func TestLoad(t *testing.T) {
	for _, tc := range []struct {
		name, addr, url, level string
		wantErr                bool
	}{
		{"defaults", "", "postgres://localhost/latihan", "", false},
		{"explicit", "127.0.0.1:9000", "postgres://localhost/latihan", "debug", false},
		{"missing database", "", "", "", true},
		{"bad address", "localhost", "postgres://localhost/latihan", "", true},
		{"bad log level", "", "postgres://localhost/latihan", "verbose", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HTTP_ADDR", tc.addr)
			t.Setenv("DATABASE_URL", tc.url)
			t.Setenv("LOG_LEVEL", tc.level)
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
