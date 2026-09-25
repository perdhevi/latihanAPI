package database

import "testing"

func TestTransportWarning(t *testing.T) {
	for url, wantWarning := range map[string]bool{
		"postgres://u:p@postgres:5432/db?sslmode=disable":      false, // Compose network
		"postgres://u:p@localhost/db?sslmode=disable":          false,
		"postgres://u:p@127.0.0.1/db":                          false,
		"postgres://u:p@db.example.com/db?sslmode=disable":     true,
		"postgres://u:p@db.example.com/db?sslmode=require":     true, // encrypted, not verified
		"postgres://u:p@db.example.com/db?sslmode=prefer":      true,
		"postgres://u:p@db.example.com/db":                     true, // pgx defaults to prefer
		"postgres://u:p@db.example.com/db?sslmode=verify-full": false,
		"postgres://u:p@10.0.0.5/db?sslmode=disable":           true,
	} {
		if got := TransportWarning(url) != ""; got != wantWarning {
			t.Errorf("%s: warning=%v", url, got)
		}
	}
}
