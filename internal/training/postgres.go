package training

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/perdhevi/latihanAPI/internal/conditional"
	"github.com/perdhevi/latihanAPI/internal/validation"
)

type PostgresRepository struct{ pool *pgxpool.Pool }

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}
func mapError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return ErrReference
	}
	return err
}

const planColumns = `id,user_id,name,notes,exercises,created_at,updated_at`

func scanPlan(row pgx.Row) (Plan, error) {
	var p Plan
	var entries []byte
	if err := row.Scan(&p.ID, &p.UserID, &p.Name, &p.Notes, &entries, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return Plan{}, mapError(err)
	}
	if err := json.Unmarshal(entries, &p.Exercises); err != nil {
		return Plan{}, err
	}
	p.CreatedAt = p.CreatedAt.UTC()
	p.UpdatedAt = p.UpdatedAt.UTC()
	return p, nil
}
func (r *PostgresRepository) SavePlan(ctx context.Context, p Plan, create bool, match conditional.Match) (Plan, error) {
	entries, err := json.Marshal(p.Exercises)
	if err != nil {
		return Plan{}, err
	}
	if create {
		return scanPlan(r.pool.QueryRow(ctx, `INSERT INTO plans (id,user_id,name,notes,exercises) VALUES ($1,$2,$3,$4,$5) RETURNING `+planColumns, p.ID, p.UserID, p.Name, p.Notes, entries))
	}
	// Including the owner in the predicate prevents transfer to another user.
	condition, args := match.SQL([]any{p.ID, p.UserID, p.Name, p.Notes, entries})
	saved, err := scanPlan(r.pool.QueryRow(ctx, `UPDATE plans SET name=$3,notes=$4,exercises=$5,updated_at=GREATEST(clock_timestamp(),updated_at+INTERVAL '1 microsecond') WHERE id=$1 AND user_id=$2`+condition+` RETURNING `+planColumns, args...))
	return saved, r.explainMiss(ctx, err, "plans", p.ID, p.UserID, match)
}

// explainMiss turns "no row written" into ErrPreconditionFailed when the row
// exists and only its version failed to match; otherwise err stands.
func (r *PostgresRepository) explainMiss(ctx context.Context, err error, table string, id, owner uuid.UUID, match conditional.Match) error {
	if !errors.Is(err, ErrNotFound) || !match.Conditional() {
		return err
	}
	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id=$1 AND user_id=$2)`, id, owner).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return conditional.ErrPreconditionFailed
	}
	return ErrNotFound
}
func (r *PostgresRepository) GetPlan(ctx context.Context, owner, id uuid.UUID) (Plan, error) {
	return scanPlan(r.pool.QueryRow(ctx, `SELECT `+planColumns+` FROM plans WHERE id=$1 AND user_id=$2`, id, owner))
}
func (r *PostgresRepository) ListPlans(ctx context.Context, owner uuid.UUID, page validation.Page) ([]Plan, error) {
	tail, args := page.SQL("created_at", []any{owner})
	rows, err := r.pool.Query(ctx, `SELECT `+planColumns+` FROM plans WHERE user_id=$1`+tail, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Plan, 0)
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}
func (r *PostgresRepository) DeletePlan(ctx context.Context, owner, id uuid.UUID, match conditional.Match) error {
	condition, args := match.SQL([]any{id, owner})
	tag, err := r.pool.Exec(ctx, `DELETE FROM plans WHERE id=$1 AND user_id=$2`+condition, args...)
	if errors.Is(mapError(err), ErrReference) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return r.explainMiss(ctx, ErrNotFound, "plans", id, owner, match)
	}
	return nil
}

const sessionColumns = `id,user_id,name,notes,performed_at,plan_id,plan_snapshot,exercises,created_at,updated_at`

func scanSession(row pgx.Row) (Session, error) {
	var s Session
	var snapshot, entries []byte
	if err := row.Scan(&s.ID, &s.UserID, &s.Name, &s.Notes, &s.PerformedAt, &s.PlanID, &snapshot, &entries, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return Session{}, mapError(err)
	}
	if snapshot != nil {
		if err := json.Unmarshal(snapshot, &s.PlanSnapshot); err != nil {
			return Session{}, err
		}
	}
	if err := json.Unmarshal(entries, &s.Exercises); err != nil {
		return Session{}, err
	}
	s.PerformedAt = s.PerformedAt.UTC()
	s.CreatedAt = s.CreatedAt.UTC()
	s.UpdatedAt = s.UpdatedAt.UTC()
	return s, nil
}
func (r *PostgresRepository) SaveSession(ctx context.Context, owner, id uuid.UUID, in SessionInput, create bool, match conditional.Match) (Session, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Session{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit
	var snapshot *Plan
	if !create {
		previous, err := scanSession(tx.QueryRow(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, owner))
		if err != nil {
			return Session{}, err
		}
		// The row is locked, so no other write can slip in between check and update.
		if !match.Matches(previous.UpdatedAt) {
			return Session{}, conditional.ErrPreconditionFailed
		}
		if in.PlanID != nil && previous.PlanID != nil && *in.PlanID == *previous.PlanID {
			snapshot = previous.PlanSnapshot
		}
	}
	if in.PlanID != nil && snapshot == nil {
		p, err := scanPlan(tx.QueryRow(ctx, `SELECT `+planColumns+` FROM plans WHERE id=$1 AND user_id=$2 FOR SHARE`, *in.PlanID, owner))
		if errors.Is(err, ErrNotFound) {
			return Session{}, ErrReference
		}
		if err != nil {
			return Session{}, err
		}
		snapshot = &p
	}
	if err := validatePlanLinks(in, snapshot); err != nil {
		return Session{}, err
	}
	var snapshotJSON []byte
	if snapshot != nil {
		snapshotJSON, err = json.Marshal(snapshot)
		if err != nil {
			return Session{}, err
		}
	}
	entries, err := json.Marshal(in.Exercises)
	if err != nil {
		return Session{}, err
	}
	query := `INSERT INTO sessions (id,user_id,name,notes,performed_at,plan_id,plan_snapshot,exercises) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING ` + sessionColumns
	if !create {
		query = `UPDATE sessions SET name=$3,notes=$4,performed_at=$5,plan_id=$6,plan_snapshot=$7,exercises=$8,updated_at=GREATEST(clock_timestamp(),updated_at+INTERVAL '1 microsecond') WHERE id=$1 AND user_id=$2 RETURNING ` + sessionColumns
	}
	saved, err := scanSession(tx.QueryRow(ctx, query, id, owner, in.Name, in.Notes, in.PerformedAt, in.PlanID, snapshotJSON, entries))
	if err != nil {
		return Session{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, mapError(err)
	}
	return saved, nil
}
func (r *PostgresRepository) GetSession(ctx context.Context, owner, id uuid.UUID) (Session, error) {
	return scanSession(r.pool.QueryRow(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE id=$1 AND user_id=$2`, id, owner))
}
func (r *PostgresRepository) ListSessions(ctx context.Context, owner uuid.UUID, page validation.Page) ([]Session, error) {
	tail, args := page.SQL("performed_at", []any{owner})
	rows, err := r.pool.Query(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE user_id=$1`+tail, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Session, 0)
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}
func (r *PostgresRepository) DeleteSession(ctx context.Context, owner, id uuid.UUID, match conditional.Match) error {
	condition, args := match.SQL([]any{id, owner})
	tag, err := r.pool.Exec(ctx, `DELETE FROM sessions WHERE id=$1 AND user_id=$2`+condition, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return r.explainMiss(ctx, ErrNotFound, "sessions", id, owner, match)
	}
	return nil
}
