package profile

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

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
func (r *PostgresRepository) CreateUser(ctx context.Context, id uuid.UUID, in UserInput) (User, error) {
	return scanUser(r.pool.QueryRow(ctx, `INSERT INTO user_profiles (id,display_name) VALUES ($1,$2) RETURNING id,display_name,created_at,updated_at`, id, in.DisplayName))
}
func (r *PostgresRepository) GetUser(ctx context.Context, id uuid.UUID) (User, error) {
	return scanUser(r.pool.QueryRow(ctx, `SELECT id,display_name,created_at,updated_at FROM user_profiles WHERE id=$1`, id))
}
func (r *PostgresRepository) UpdateUser(ctx context.Context, id uuid.UUID, in UserInput) (User, error) {
	return scanUser(r.pool.QueryRow(ctx, `UPDATE user_profiles SET display_name=$2,updated_at=GREATEST(clock_timestamp(),updated_at+INTERVAL '1 microsecond') WHERE id=$1 RETURNING id,display_name,created_at,updated_at`, id, in.DisplayName))
}
func (r *PostgresRepository) DeleteUser(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM user_profiles WHERE id=$1`, id)
	if errors.Is(mapError(err), ErrUserMissing) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
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
	rows, err := r.pool.Query(ctx, `SELECT `+measurementColumns+` FROM measurements WHERE user_id=$1 ORDER BY measured_at DESC,id DESC LIMIT $2 OFFSET $3`, userID, page.Limit, page.Offset)
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
func (r *PostgresRepository) UpdateMeasurement(ctx context.Context, userID, id uuid.UUID, in MeasurementInput) (Measurement, error) {
	return scanMeasurement(r.pool.QueryRow(ctx, `UPDATE measurements SET measured_at=$3,weight_kg=$4,height_cm=$5,body_fat_percent=$6,waist_cm=$7,chest_cm=$8,hip_cm=$9,updated_at=GREATEST(clock_timestamp(),updated_at+INTERVAL '1 microsecond') WHERE user_id=$1 AND id=$2 RETURNING `+measurementColumns, userID, id, in.MeasuredAt, in.WeightKG, in.HeightCM, in.BodyFatPercent, in.WaistCM, in.ChestCM, in.HipCM))
}
func (r *PostgresRepository) DeleteMeasurement(ctx context.Context, userID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM measurements WHERE user_id=$1 AND id=$2`, userID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
