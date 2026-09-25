package database

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Open bounds startup retries; request queries use the caller's context.
// Options bound what one request can cost the database.
type Options struct {
	MaxConns int32
	// StatementTimeout is enforced by PostgreSQL itself, so a runaway query
	// stops even if the client never cancels it.
	StatementTimeout time.Duration
}

func Open(ctx context.Context, url string, opts Options) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("invalid DATABASE_URL")
	}
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.MaxConns = opts.MaxConns
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 5 * time.Minute
	params := cfg.ConnConfig.RuntimeParams
	params["statement_timeout"] = strconv.FormatInt(opts.StatementTimeout.Milliseconds(), 10)
	// Row locks (FOR UPDATE) wait at most this long instead of piling up.
	params["lock_timeout"] = "3000"
	// A transaction abandoned mid-way releases its locks and connection.
	params["idle_in_transaction_session_timeout"] = "10000"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("could not initialize database pool")
	}
	startupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		if err := pool.Ping(startupCtx); err == nil {
			return pool, nil
		}
		select {
		case <-startupCtx.Done():
			pool.Close()
			return nil, errors.New("database unavailable during startup")
		case <-time.After(time.Second):
		}
	}
}
