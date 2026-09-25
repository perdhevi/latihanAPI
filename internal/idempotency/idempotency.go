// Package idempotency remembers the responses to POST requests that carried an
// Idempotency-Key header, so a client can retry after a lost response without
// creating the record twice.
//
// The stored response and the record the request created are saved in
// separate transactions. If the process dies between the two, a retry runs the
// request again; closing that window would need every handler to write the key
// in its own transaction.
package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// TTL is how long a key is remembered.
	TTL = 24 * time.Hour
	// abandonedAfter is when an unfinished claim is presumed dead: far longer
	// than any request may run (the request deadline is 10s).
	abandonedAfter = time.Minute
)

// Scope identifies a key: keys are private to the identity that sent them.
type Scope struct {
	Issuer, Subject, Key string
}

// Response is what gets replayed.
type Response struct {
	Status int
	Header map[string]string
	Body   []byte
}

// Outcome is the result of claiming a key.
type Outcome int

const (
	// Claimed: this request owns the key and must Complete or Release it.
	Claimed Outcome = iota
	// Replay: the request already ran; send the stored Response.
	Replay
	// InProgress: the first request with this key is still running.
	InProgress
	// Mismatch: the key was used for a different request.
	Mismatch
)

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Claim reserves the key for a request with the given hash, or reports what
// happened to an earlier request with the same key.
func (s *Store) Claim(ctx context.Context, sc Scope, hash []byte) (Outcome, Response, error) {
	tag, err := s.pool.Exec(ctx, `INSERT INTO idempotency_keys (issuer,subject,key,request_hash) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
		sc.Issuer, sc.Subject, sc.Key, hash)
	if err != nil {
		return 0, Response{}, err
	}
	if tag.RowsAffected() == 1 {
		return Claimed, Response{}, nil
	}
	// Reuse an expired key, or take over a claim whose request died mid-way.
	tag, err = s.pool.Exec(ctx, `UPDATE idempotency_keys
		SET request_hash=$4, status_code=NULL, response_headers=NULL, response_body=NULL, created_at=clock_timestamp()
		WHERE issuer=$1 AND subject=$2 AND key=$3 AND (
			created_at < clock_timestamp() - make_interval(secs => $5)
			OR (status_code IS NULL AND request_hash=$4 AND created_at < clock_timestamp() - make_interval(secs => $6)))`,
		sc.Issuer, sc.Subject, sc.Key, hash, TTL.Seconds(), abandonedAfter.Seconds())
	if err != nil {
		return 0, Response{}, err
	}
	if tag.RowsAffected() == 1 {
		return Claimed, Response{}, nil
	}
	var stored []byte
	var status *int
	var headers, body []byte
	err = s.pool.QueryRow(ctx, `SELECT request_hash,status_code,response_headers,response_body FROM idempotency_keys WHERE issuer=$1 AND subject=$2 AND key=$3`,
		sc.Issuer, sc.Subject, sc.Key).Scan(&stored, &status, &headers, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		// Purged between our statements; the client can simply retry.
		return InProgress, Response{}, nil
	}
	if err != nil {
		return 0, Response{}, err
	}
	switch {
	case string(stored) != string(hash):
		return Mismatch, Response{}, nil
	case status == nil:
		return InProgress, Response{}, nil
	}
	resp := Response{Status: *status, Body: body}
	if err := json.Unmarshal(headers, &resp.Header); err != nil {
		return 0, Response{}, err
	}
	return Replay, resp, nil
}

// Complete stores the response for replay.
func (s *Store) Complete(ctx context.Context, sc Scope, resp Response) error {
	headers, err := json.Marshal(resp.Header)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `UPDATE idempotency_keys SET status_code=$4, response_headers=$5, response_body=$6
		WHERE issuer=$1 AND subject=$2 AND key=$3`, sc.Issuer, sc.Subject, sc.Key, resp.Status, headers, resp.Body)
	return err
}

// Release forgets an unfinished claim so the request can be retried for real,
// used when the outcome was not final (a server error or a rate limit).
func (s *Store) Release(ctx context.Context, sc Scope) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE issuer=$1 AND subject=$2 AND key=$3 AND status_code IS NULL`, sc.Issuer, sc.Subject, sc.Key)
	return err
}

// Purge deletes keys older than TTL and reports how many.
func (s *Store) Purge(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE created_at < clock_timestamp() - make_interval(secs => $1)`, TTL.Seconds())
	return tag.RowsAffected(), err
}
