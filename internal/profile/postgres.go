package profile

import (
	"context"
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
		return ErrUserMissing
	}
	return err
}
func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.DisplayName, &u.CreatedAt, &u.UpdatedAt)
	u.CreatedAt = u.CreatedAt.UTC()
	u.UpdatedAt = u.UpdatedAt.UTC()
	return u, mapError(err)
}
func (r *PostgresRepository) CreateUser(ctx context.Context, id uuid.UUID, in UserInput, owner Identity) (User, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit
	user, err := scanUser(tx.QueryRow(ctx, `INSERT INTO user_profiles (id,display_name) VALUES ($1,$2) RETURNING id,display_name,created_at,updated_at`, id, in.DisplayName))
	if err != nil {
		return User{}, err
	}
	// The identity primary key makes a second profile for the same caller fail here.
	_, err = tx.Exec(ctx, `INSERT INTO user_identities (issuer,subject,user_id) VALUES ($1,$2,$3)`, owner.Issuer, owner.Subject, id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return User{}, ErrIdentityLinked
	}
	if err != nil {
		return User{}, err
	}
	return user, tx.Commit(ctx)
}
func (r *PostgresRepository) ResolveIdentity(ctx context.Context, id Identity) (uuid.UUID, error) {
	var userID uuid.UUID
	err := r.pool.QueryRow(ctx, `SELECT user_id FROM user_identities WHERE issuer=$1 AND subject=$2`, id.Issuer, id.Subject).Scan(&userID)
	return userID, mapError(err)
}
func (r *PostgresRepository) GetUser(ctx context.Context, id uuid.UUID) (User, error) {
	return scanUser(r.pool.QueryRow(ctx, `SELECT id,display_name,created_at,updated_at FROM user_profiles WHERE id=$1`, id))
}
func (r *PostgresRepository) UpdateUser(ctx context.Context, id uuid.UUID, in UserInput, match conditional.Match) (User, error) {
	condition, args := match.SQL([]any{id, in.DisplayName})
	user, err := scanUser(r.pool.QueryRow(ctx, `UPDATE user_profiles SET display_name=$2,updated_at=GREATEST(clock_timestamp(),updated_at+INTERVAL '1 microsecond') WHERE id=$1`+condition+` RETURNING id,display_name,created_at,updated_at`, args...))
	return user, r.explainMiss(ctx, err, match, `SELECT EXISTS (SELECT 1 FROM user_profiles WHERE id=$1)`, id)
}

// explainMiss turns "no row written" into ErrPreconditionFailed when the row
// exists (per existsQuery) and only its version failed to match.
func (r *PostgresRepository) explainMiss(ctx context.Context, err error, match conditional.Match, existsQuery string, args ...any) error {
	if !errors.Is(err, ErrNotFound) || !match.Conditional() {
		return err
	}
	var exists bool
	if err := r.pool.QueryRow(ctx, existsQuery, args...).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return conditional.ErrPreconditionFailed
	}
	return ErrNotFound
}
func (r *PostgresRepository) DeleteUser(ctx context.Context, id uuid.UUID, match conditional.Match) error {
	condition, args := match.SQL([]any{id})
	tag, err := r.pool.Exec(ctx, `DELETE FROM user_profiles WHERE id=$1`+condition, args...)
	if errors.Is(mapError(err), ErrUserMissing) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return r.explainMiss(ctx, ErrNotFound, match, `SELECT EXISTS (SELECT 1 FROM user_profiles WHERE id=$1)`, id)
	}
	return nil
}

const measurementColumns = `id,user_id,measured_at,weight_kg,height_cm,body_fat_percent,waist_cm,chest_cm,hip_cm,created_at,updated_at`

func scanMeasurement(row pgx.Row) (Measurement, error) {
	var m Measurement
	err := row.Scan(&m.ID, &m.UserID, &m.MeasuredAt, &m.WeightKG, &m.HeightCM, &m.BodyFatPercent, &m.WaistCM, &m.ChestCM, &m.HipCM, &m.CreatedAt, &m.UpdatedAt)
	m.MeasuredAt = m.MeasuredAt.UTC()
	m.CreatedAt = m.CreatedAt.UTC()
	m.UpdatedAt = m.UpdatedAt.UTC()
	return m, mapError(err)
}
func (r *PostgresRepository) CreateMeasurement(ctx context.Context, userID, id uuid.UUID, in MeasurementInput) (Measurement, error) {
	return scanMeasurement(r.pool.QueryRow(ctx, `INSERT INTO measurements (id,user_id,measured_at,weight_kg,height_cm,body_fat_percent,waist_cm,chest_cm,hip_cm) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING `+measurementColumns, id, userID, in.MeasuredAt, in.WeightKG, in.HeightCM, in.BodyFatPercent, in.WaistCM, in.ChestCM, in.HipCM))
}
func (r *PostgresRepository) GetMeasurement(ctx context.Context, userID, id uuid.UUID) (Measurement, error) {
	return scanMeasurement(r.pool.QueryRow(ctx, `SELECT `+measurementColumns+` FROM measurements WHERE user_id=$1 AND id=$2`, userID, id))
}
func (r *PostgresRepository) ListMeasurements(ctx context.Context, userID uuid.UUID, page validation.Page) ([]Measurement, error) {
	tail, args := page.SQL("measured_at", []any{userID})
	rows, err := r.pool.Query(ctx, `SELECT `+measurementColumns+` FROM measurements WHERE user_id=$1`+tail, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Measurement, 0)
	for rows.Next() {
		m, err := scanMeasurement(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}
func (r *PostgresRepository) UpdateMeasurement(ctx context.Context, userID, id uuid.UUID, in MeasurementInput, match conditional.Match) (Measurement, error) {
	condition, args := match.SQL([]any{userID, id, in.MeasuredAt, in.WeightKG, in.HeightCM, in.BodyFatPercent, in.WaistCM, in.ChestCM, in.HipCM})
	m, err := scanMeasurement(r.pool.QueryRow(ctx, `UPDATE measurements SET measured_at=$3,weight_kg=$4,height_cm=$5,body_fat_percent=$6,waist_cm=$7,chest_cm=$8,hip_cm=$9,updated_at=GREATEST(clock_timestamp(),updated_at+INTERVAL '1 microsecond') WHERE user_id=$1 AND id=$2`+condition+` RETURNING `+measurementColumns, args...))
	return m, r.explainMiss(ctx, err, match, `SELECT EXISTS (SELECT 1 FROM measurements WHERE user_id=$1 AND id=$2)`, userID, id)
}
func (r *PostgresRepository) DeleteMeasurement(ctx context.Context, userID, id uuid.UUID, match conditional.Match) error {
	condition, args := match.SQL([]any{userID, id})
	tag, err := r.pool.Exec(ctx, `DELETE FROM measurements WHERE user_id=$1 AND id=$2`+condition, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return r.explainMiss(ctx, ErrNotFound, match, `SELECT EXISTS (SELECT 1 FROM measurements WHERE user_id=$1 AND id=$2)`, userID, id)
	}
	return nil
}
