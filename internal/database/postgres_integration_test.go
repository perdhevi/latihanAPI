//go:build integration

package database

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// The database, not the client, stops a runaway statement.
func TestStatementTimeoutIsEnforcedByPostgres(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("TEST_DATABASE_URL is required with -tags=integration")
	}
	pool, err := Open(t.Context(), url, Options{MaxConns: 2, StatementTimeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	_, err = pool.Exec(t.Context(), "SELECT pg_sleep(2)") // no client deadline at all
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "57014" { // query_canceled
		t.Fatalf("want statement timeout, got %v", err)
	}
	for setting, want := range map[string]string{"statement_timeout": "200ms", "lock_timeout": "3s", "idle_in_transaction_session_timeout": "10s"} {
		var got string
		if err := pool.QueryRow(t.Context(), "SHOW "+setting).Scan(&got); err != nil || got != want {
			t.Errorf("%s = %q, want %q (%v)", setting, got, want, err)
		}
	}
}

// Keyset pages are served from the history index, however deep the page.
func TestKeysetQueryUsesIndex(t *testing.T) {
	pool, err := Open(t.Context(), os.Getenv("TEST_DATABASE_URL"), Options{MaxConns: 2, StatementTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	// A throwaway table shaped like sessions keeps this independent of test schemas.
	for _, stmt := range []string{
		`CREATE TEMP TABLE s (id uuid, user_id uuid, performed_at timestamptz)`,
		`CREATE INDEX ON s (user_id, performed_at DESC, id DESC)`,
		`INSERT INTO s SELECT gen_random_uuid(), '00000000-0000-4000-8000-000000000001', now() - g * interval '1 minute' FROM generate_series(1, 20000) g`,
		`ANALYZE s`,
	} {
		if _, err := tx.Exec(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := tx.Query(t.Context(), `EXPLAIN SELECT * FROM s WHERE user_id='00000000-0000-4000-8000-000000000001'
		AND (performed_at,id) < (now() - interval '10000 minutes', '00000000-0000-4000-8000-000000000000') ORDER BY performed_at DESC,id DESC LIMIT 21`)
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for rows.Next() {
		var line string
		_ = rows.Scan(&line)
		plan.WriteString(line + "\n")
	}
	if !strings.Contains(plan.String(), "Index") || strings.Contains(plan.String(), "Sort") {
		t.Fatalf("keyset page is not an index walk:\n%s", plan.String())
	}
}
