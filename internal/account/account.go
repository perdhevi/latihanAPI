// Package account acts on everything the service holds about one user at
// once: erasure and the audit record of an export.
package account

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Identity is a login linked to the user.
type Identity struct {
	Issuer    string    `json:"issuer"`
	Subject   string    `json:"subject"`
	CreatedAt time.Time `json:"created_at"`
}

// Erase deletes every record of the user in one transaction: sessions, plans,
// measurements, the profile, its identity links and the idempotency records
// (whose stored responses can contain health data). The audit log keeps the
// fact that an erasure happened, under a UUID that no longer leads to anyone.
// Erasing a user who does not exist does nothing.
func (s *Store) Erase(ctx context.Context, userID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_profiles WHERE id=$1 FOR UPDATE)`, userID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	for _, stmt := range []string{
		`INSERT INTO audit_events (user_id, action, resource_type, resource_id) VALUES ($1, 'erase', 'user_profiles', $1)`,
		`DELETE FROM idempotency_keys WHERE (issuer, subject) IN (SELECT issuer, subject FROM user_identities WHERE user_id=$1)`,
		// Sessions first: they reference plans.
		`DELETE FROM sessions WHERE user_id=$1`,
		`DELETE FROM plans WHERE user_id=$1`,
		`DELETE FROM measurements WHERE user_id=$1`,
		// Identity links go with the profile (ON DELETE CASCADE).
		`DELETE FROM user_profiles WHERE id=$1`,
	} {
		if _, err := tx.Exec(ctx, stmt, userID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// RecordExport notes in the audit log that the user's data was exported.
func (s *Store) RecordExport(ctx context.Context, userID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO audit_events (user_id, action, resource_type, resource_id) VALUES ($1, 'export', 'user_profiles', $1)`, userID)
	return err
}

// Identities lists the logins linked to the user.
func (s *Store) Identities(ctx context.Context, userID uuid.UUID) ([]Identity, error) {
	rows, err := s.pool.Query(ctx, `SELECT issuer, subject, created_at FROM user_identities WHERE user_id=$1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	identities := make([]Identity, 0)
	for rows.Next() {
		var id Identity
		if err := rows.Scan(&id.Issuer, &id.Subject, &id.CreatedAt); err != nil {
			return nil, err
		}
		id.CreatedAt = id.CreatedAt.UTC()
		identities = append(identities, id)
	}
	return identities, rows.Err()
}

// PurgeAudit deletes audit events older than keep, which the database refuses
// to accept if shorter than 30 days.
func (s *Store) PurgeAudit(ctx context.Context, keep time.Duration) (int64, error) {
	var removed int64
	err := s.pool.QueryRow(ctx, `SELECT purge_audit_events(make_interval(secs => $1))`, keep.Seconds()).Scan(&removed)
	return removed, err
}
