package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretsFromFiles(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "database_url")
	if err := os.WriteFile(secret, []byte("postgres://app:s3cret@db/latihan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DATABASE_URL_FILE", secret)
	t.Setenv("AUTH_PROVIDER", "jwt")
	cfg, err := Load()
	if err != nil || cfg.DatabaseURL != "postgres://app:s3cret@db/latihan" {
		t.Fatalf("%q %v", cfg.DatabaseURL, err)
	}
	// Providers read their settings through the same indirection.
	clientSecret := filepath.Join(dir, "client_secret")
	_ = os.WriteFile(clientSecret, []byte("abc"), 0o600)
	t.Setenv("AUTH_EXAMPLE_SECRET_FILE", clientSecret)
	if got := cfg.Env.Get("AUTH_EXAMPLE_SECRET"); got != "abc" {
		t.Fatalf("provider setting: %q", got)
	}
	// Settings that are paths by nature stay paths.
	t.Setenv("AUTH_JWT_KEY_FILE", filepath.Join(dir, "missing.pem"))
	if got := cfg.Env.Get("AUTH_JWT_KEY_FILE"); !strings.HasSuffix(got, "missing.pem") {
		t.Fatalf("path setting resolved as a secret: %q", got)
	}
}

func TestSecretFileErrors(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big")
	_ = os.WriteFile(big, make([]byte, maxSecretBytes+1), 0o600)
	for name, setup := range map[string]func(t *testing.T){
		"both set": func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://x")
			t.Setenv("DATABASE_URL_FILE", big)
		},
		"missing file": func(t *testing.T) { t.Setenv("DATABASE_URL_FILE", filepath.Join(dir, "nope")) },
		"oversized":    func(t *testing.T) { t.Setenv("DATABASE_URL_FILE", big) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "")
			t.Setenv("AUTH_PROVIDER", "jwt")
			setup(t)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
				t.Fatalf("want an error naming DATABASE_URL, got %v", err)
			}
			if strings.Contains(err.Error(), "postgres://") {
				t.Fatal("error message leaks the secret")
			}
		})
	}
}
