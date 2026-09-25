package local

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	errEmailTaken     = errors.New("email already registered")
	errNoCredential   = errors.New("no credential for this email")
	errInvalidRefresh = errors.New("refresh token invalid, expired or revoked")
	// errRefreshReuse means a rotated refresh token came back: either the
	// client or an attacker holds a stolen copy. The whole family is revoked.
	errRefreshReuse = errors.New("refresh token reused; family revoked")
)

type credential struct {
	id           uuid.UUID
	passwordHash string
	lockedUntil  *time.Time
}

type refreshToken struct {
	id           uuid.UUID
	credentialID uuid.UUID
	familyID     uuid.UUID
	hash         []byte
	expiresAt    time.Time
}

// store is the provider's persistence. Every method is atomic.
type store interface {
	createCredential(ctx context.Context, id uuid.UUID, email, passwordHash string) error
	credentialByEmail(ctx context.Context, email string) (credential, error)
	// recordFailure counts a failed login and, on reaching maxFailures, locks
	// the credential until lockUntil.
	recordFailure(ctx context.Context, id uuid.UUID, maxFailures int, lockUntil time.Time) error
	// recordSuccess clears the failure count; a non-empty newHash replaces the stored hash.
	recordSuccess(ctx context.Context, id uuid.UUID, newHash string) error
	createRefreshToken(ctx context.Context, t refreshToken) error
	// rotateRefreshToken consumes the token with oldHash and stores next in the
	// same family, returning the credential. next.credentialID and
	// next.familyID are filled in from the consumed token.
	rotateRefreshToken(ctx context.Context, oldHash []byte, now time.Time, next refreshToken) (uuid.UUID, error)
	revokeFamily(ctx context.Context, hash []byte, now time.Time) error
	// deleteCredential removes an account and its refresh tokens; a missing
	// account is not an error.
	deleteCredential(ctx context.Context, id uuid.UUID) error
}

type sqlStore struct{ db *sql.DB }

func (s *sqlStore) createCredential(ctx context.Context, id uuid.UUID, email, passwordHash string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO local_credentials (id,email,password_hash) VALUES ($1,$2,$3)`, id, email, passwordHash)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return errEmailTaken
	}
	return err
}

func (s *sqlStore) credentialByEmail(ctx context.Context, email string) (credential, error) {
	var c credential
	var locked sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT id,password_hash,locked_until FROM local_credentials WHERE email=$1`, email).Scan(&c.id, &c.passwordHash, &locked)
	if errors.Is(err, sql.ErrNoRows) {
		return credential{}, errNoCredential
	}
	if locked.Valid {
		c.lockedUntil = &locked.Time
	}
	return c, err
}

func (s *sqlStore) recordFailure(ctx context.Context, id uuid.UUID, maxFailures int, lockUntil time.Time) error {
	// Reaching the limit locks the account and restarts the count, so the
	// next lock needs another full run of failures.
	_, err := s.db.ExecContext(ctx, `UPDATE local_credentials SET
		locked_until = CASE WHEN failed_logins + 1 >= $2 THEN $3 ELSE locked_until END,
		failed_logins = CASE WHEN failed_logins + 1 >= $2 THEN 0 ELSE failed_logins + 1 END,
		updated_at = clock_timestamp()
		WHERE id=$1`, id, maxFailures, lockUntil)
	return err
}

func (s *sqlStore) recordSuccess(ctx context.Context, id uuid.UUID, newHash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE local_credentials SET failed_logins=0, locked_until=NULL,
		password_hash=COALESCE(NULLIF($2,''), password_hash), updated_at=clock_timestamp() WHERE id=$1`, id, newHash)
	return err
}

func (s *sqlStore) createRefreshToken(ctx context.Context, t refreshToken) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO refresh_tokens (id,credential_id,family_id,token_hash,expires_at) VALUES ($1,$2,$3,$4,$5)`,
		t.id, t.credentialID, t.familyID, t.hash, t.expiresAt)
	return err
}

func (s *sqlStore) rotateRefreshToken(ctx context.Context, oldHash []byte, now time.Time, next refreshToken) (uuid.UUID, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = tx.Rollback() }() // no-op after a successful Commit
	var id, credentialID, familyID uuid.UUID
	var expiresAt time.Time
	var usedAt, revokedAt sql.NullTime
	// FOR UPDATE serializes concurrent uses of one token: the second waits,
	// then sees used_at set and is treated as reuse.
	err = tx.QueryRowContext(ctx, `SELECT id,credential_id,family_id,expires_at,used_at,revoked_at FROM refresh_tokens WHERE token_hash=$1 FOR UPDATE`, oldHash).
		Scan(&id, &credentialID, &familyID, &expiresAt, &usedAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, errInvalidRefresh
	}
	if err != nil {
		return uuid.Nil, err
	}
	if revokedAt.Valid || !now.Before(expiresAt) {
		return uuid.Nil, errInvalidRefresh
	}
	if usedAt.Valid {
		if _, err := tx.ExecContext(ctx, `UPDATE refresh_tokens SET revoked_at=$2 WHERE family_id=$1 AND revoked_at IS NULL`, familyID, now); err != nil {
			return uuid.Nil, err
		}
		if err := tx.Commit(); err != nil {
			return uuid.Nil, err
		}
		return uuid.Nil, errRefreshReuse
	}
	if _, err := tx.ExecContext(ctx, `UPDATE refresh_tokens SET used_at=$2 WHERE id=$1`, id, now); err != nil {
		return uuid.Nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO refresh_tokens (id,credential_id,family_id,token_hash,expires_at) VALUES ($1,$2,$3,$4,$5)`,
		next.id, credentialID, familyID, next.hash, next.expiresAt); err != nil {
		return uuid.Nil, err
	}
	// Expired rows are useless for reuse detection; prune them while here.
	if _, err := tx.ExecContext(ctx, `DELETE FROM refresh_tokens WHERE credential_id=$1 AND expires_at < $2`, credentialID, now); err != nil {
		return uuid.Nil, err
	}
	return credentialID, tx.Commit()
}

func (s *sqlStore) revokeFamily(ctx context.Context, hash []byte, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE refresh_tokens SET revoked_at=$2
		WHERE family_id=(SELECT family_id FROM refresh_tokens WHERE token_hash=$1) AND revoked_at IS NULL`, hash, now)
	return err
}

func (s *sqlStore) deleteCredential(ctx context.Context, id uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM local_credentials WHERE id=$1`, id)
	return err
}
