package training

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/perdhevi/latihanAPI/internal/conditional"
	"github.com/perdhevi/latihanAPI/internal/ids"
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

// querier is what pool and transaction have in common.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// tableSet names the tables that hold the exercises of one kind of parent:
// plans (planned exercises) or sessions (performed exercises).
type tableSet struct {
	exercises, sets, parentColumn, setColumn string
	// link selects the plan_exercise_id column; plan exercises have none.
	link string
}

var (
	planTables    = tableSet{"plan_exercises", "plan_sets", "plan_id", "plan_exercise_id", "NULL::uuid"}
	sessionTables = tableSet{"session_exercises", "session_sets", "session_id", "session_exercise_id", "plan_exercise_id"}
)

// entry is an exercise row with its sets, before it becomes a PlanExercise or
// SessionExercise.
type entry struct {
	id             uuid.UUID
	planExerciseID *uuid.UUID
	ExerciseInput
}

// loadEntries reads the exercises and sets of the given parents in two queries
// however many parents there are, in the order they were saved.
func loadEntries(ctx context.Context, q querier, t tableSet, parents []uuid.UUID) (map[uuid.UUID][]entry, error) {
	result := make(map[uuid.UUID][]entry, len(parents))
	if len(parents) == 0 {
		return result, nil
	}
	rows, err := q.Query(ctx, `SELECT id, `+t.parentColumn+`, `+t.link+`, exercise_id, name, kind, notes, minutes, avg_bpm FROM `+t.exercises+` WHERE `+t.parentColumn+` = ANY($1) ORDER BY `+t.parentColumn+`, position`, parents)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := make(map[uuid.UUID]*entry)
	var order []uuid.UUID
	parentOf := make(map[uuid.UUID]uuid.UUID)
	staged := make(map[uuid.UUID][]*entry)
	for rows.Next() {
		e := new(entry)
		var parent uuid.UUID
		if err := rows.Scan(&e.id, &parent, &e.planExerciseID, &e.ExerciseID, &e.Name, &e.Kind, &e.Notes, &e.Minutes, &e.AvgBPM); err != nil {
			return nil, err
		}
		byID[e.id] = e
		order = append(order, e.id)
		parentOf[e.id] = parent
		staged[parent] = append(staged[parent], e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if len(order) > 0 {
		setRows, err := q.Query(ctx, `SELECT `+t.setColumn+`, repetitions, weight_kg FROM `+t.sets+` WHERE `+t.setColumn+` = ANY($1) ORDER BY `+t.setColumn+`, set_number`, order)
		if err != nil {
			return nil, err
		}
		defer setRows.Close()
		for setRows.Next() {
			var owner uuid.UUID
			var set Set
			var weight float64
			if err := setRows.Scan(&owner, &set.Repetitions, &weight); err != nil {
				return nil, err
			}
			set.WeightKG = &weight
			byID[owner].Sets = append(byID[owner].Sets, set)
		}
		if err := setRows.Err(); err != nil {
			return nil, err
		}
	}
	for parent, list := range staged {
		for _, e := range list {
			result[parent] = append(result[parent], *e)
		}
	}
	return result, nil
}

// insertEntries writes exercises and their sets in one round trip.
func insertEntries(ctx context.Context, tx pgx.Tx, t tableSet, parent uuid.UUID, entries []entry) error {
	if len(entries) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for i, e := range entries {
		if t.link == "NULL::uuid" {
			batch.Queue(`INSERT INTO `+t.exercises+` (id, `+t.parentColumn+`, position, exercise_id, name, kind, notes, minutes, avg_bpm) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
				e.id, parent, i+1, e.ExerciseID, e.Name, e.Kind, e.Notes, e.Minutes, e.AvgBPM)
		} else {
			batch.Queue(`INSERT INTO `+t.exercises+` (id, `+t.parentColumn+`, position, exercise_id, plan_exercise_id, name, kind, notes, minutes, avg_bpm) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
				e.id, parent, i+1, e.ExerciseID, e.planExerciseID, e.Name, e.Kind, e.Notes, e.Minutes, e.AvgBPM)
		}
		if len(e.Sets) == 0 {
			continue
		}
		reps := make([]int32, len(e.Sets))
		weights := make([]float64, len(e.Sets))
		for j, s := range e.Sets {
			reps[j], weights[j] = int32(s.Repetitions), *s.WeightKG //nolint:gosec // G115: validated to 1..1000
		}
		batch.Queue(`INSERT INTO `+t.sets+` (`+t.setColumn+`, set_number, repetitions, weight_kg) SELECT $1, n, r, w FROM unnest($2::int[], $3::float8[]) WITH ORDINALITY AS u(r, w, n)`, e.id, reps, weights)
	}
	results := tx.SendBatch(ctx, batch)
	for range batch.Len() {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return err
		}
	}
	return results.Close()
}

func sameExercises(a, b []ExerciseInput) bool {
	left, errLeft := json.Marshal(a)
	right, errRight := json.Marshal(b)
	return errLeft == nil && errRight == nil && bytes.Equal(left, right)
}

func planEntries(exercises []PlanExercise) []entry {
	result := make([]entry, len(exercises))
	for i, e := range exercises {
		result[i] = entry{id: e.ID, ExerciseInput: e.ExerciseInput}
	}
	return result
}
func toPlanExercises(entries []entry) []PlanExercise {
	result := make([]PlanExercise, len(entries))
	for i, e := range entries {
		result[i] = PlanExercise{ID: e.id, ExerciseInput: e.ExerciseInput}
	}
	return result
}
func toSessionExercises(entries []entry) []SessionExercise {
	result := make([]SessionExercise, len(entries))
	for i, e := range entries {
		result[i] = SessionExercise{PlanExerciseID: e.planExerciseID, ExerciseInput: e.ExerciseInput}
	}
	return result
}

const planColumns = `id,user_id,name,notes,created_at,updated_at`

func scanPlanRow(row pgx.Row) (Plan, error) {
	var p Plan
	if err := row.Scan(&p.ID, &p.UserID, &p.Name, &p.Notes, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return Plan{}, mapError(err)
	}
	p.CreatedAt = p.CreatedAt.UTC()
	p.UpdatedAt = p.UpdatedAt.UTC()
	return p, nil
}

// attachPlanExercises fills in the exercises of plans read without them.
func attachPlanExercises(ctx context.Context, q querier, plans []Plan) error {
	parents := make([]uuid.UUID, len(plans))
	for i, p := range plans {
		parents[i] = p.ID
	}
	loaded, err := loadEntries(ctx, q, planTables, parents)
	if err != nil {
		return err
	}
	for i := range plans {
		plans[i].Exercises = toPlanExercises(loaded[plans[i].ID])
	}
	return nil
}

func getPlan(ctx context.Context, q querier, owner, id uuid.UUID, lock string) (Plan, error) {
	p, err := scanPlanRow(q.QueryRow(ctx, `SELECT `+planColumns+` FROM plans WHERE id=$1 AND user_id=$2`+lock, id, owner))
	if err != nil {
		return Plan{}, err
	}
	plans := []Plan{p}
	if err := attachPlanExercises(ctx, q, plans); err != nil {
		return Plan{}, err
	}
	return plans[0], nil
}

func (r *PostgresRepository) SavePlan(ctx context.Context, p Plan, create bool, match conditional.Match) (Plan, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Plan{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit
	exercises := planEntries(p.Exercises)
	if create {
		err = tx.QueryRow(ctx, `INSERT INTO plans (id,user_id,name,notes) VALUES ($1,$2,$3,$4) RETURNING created_at,updated_at`, p.ID, p.UserID, p.Name, p.Notes).Scan(&p.CreatedAt, &p.UpdatedAt)
		if err != nil {
			return Plan{}, mapError(err)
		}
	} else {
		// The row is locked, so no session can start using the plan, nor another
		// write slip in, between the checks below and the update.
		current, err := getPlan(ctx, tx, p.UserID, p.ID, ` FOR UPDATE`)
		if err != nil {
			return Plan{}, err
		}
		if !match.Matches(current.UpdatedAt) {
			return Plan{}, conditional.ErrPreconditionFailed
		}
		var inUse bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sessions WHERE plan_id=$1)`, p.ID).Scan(&inUse); err != nil {
			return Plan{}, err
		}
		if inUse {
			// Unchanged exercises keep their ids, which sessions refer to.
			if !sameExercises(inputs(current.Exercises), inputs(p.Exercises)) {
				return Plan{}, ErrPlanInUse
			}
			exercises = nil
			p.Exercises = current.Exercises
		} else if _, err := tx.Exec(ctx, `DELETE FROM plan_exercises WHERE plan_id=$1`, p.ID); err != nil {
			return Plan{}, err
		}
		err = tx.QueryRow(ctx, `UPDATE plans SET name=$3,notes=$4,updated_at=GREATEST(clock_timestamp(),updated_at+INTERVAL '1 microsecond') WHERE id=$1 AND user_id=$2 RETURNING created_at,updated_at`, p.ID, p.UserID, p.Name, p.Notes).Scan(&p.CreatedAt, &p.UpdatedAt)
		if err != nil {
			return Plan{}, mapError(err)
		}
	}
	if err := insertEntries(ctx, tx, planTables, p.ID, exercises); err != nil {
		return Plan{}, mapError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Plan{}, mapError(err)
	}
	p.CreatedAt = p.CreatedAt.UTC()
	p.UpdatedAt = p.UpdatedAt.UTC()
	return p, nil
}

func inputs(exercises []PlanExercise) []ExerciseInput {
	result := make([]ExerciseInput, len(exercises))
	for i, e := range exercises {
		result[i] = e.ExerciseInput
	}
	return result
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
	return getPlan(ctx, r.pool, owner, id, ``)
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
		p, err := scanPlanRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	return result, attachPlanExercises(ctx, r.pool, result)
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

const sessionColumns = `id,user_id,name,notes,performed_at,plan_id,created_at,updated_at`

func scanSessionRow(row pgx.Row) (Session, error) {
	var s Session
	if err := row.Scan(&s.ID, &s.UserID, &s.Name, &s.Notes, &s.PerformedAt, &s.PlanID, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return Session{}, mapError(err)
	}
	s.PerformedAt = s.PerformedAt.UTC()
	s.CreatedAt = s.CreatedAt.UTC()
	s.UpdatedAt = s.UpdatedAt.UTC()
	return s, nil
}

func attachSessionExercises(ctx context.Context, q querier, sessions []Session) error {
	parents := make([]uuid.UUID, len(sessions))
	for i, s := range sessions {
		parents[i] = s.ID
	}
	loaded, err := loadEntries(ctx, q, sessionTables, parents)
	if err != nil {
		return err
	}
	for i := range sessions {
		sessions[i].Exercises = toSessionExercises(loaded[sessions[i].ID])
	}
	return nil
}

func getSession(ctx context.Context, q querier, owner, id uuid.UUID, lock string) (Session, error) {
	s, err := scanSessionRow(q.QueryRow(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE id=$1 AND user_id=$2`+lock, id, owner))
	if err != nil {
		return Session{}, err
	}
	sessions := []Session{s}
	if err := attachSessionExercises(ctx, q, sessions); err != nil {
		return Session{}, err
	}
	return sessions[0], nil
}

func (r *PostgresRepository) SaveSession(ctx context.Context, owner, id uuid.UUID, in SessionInput, create bool, match conditional.Match) (Session, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Session{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit
	if !create {
		var updatedAt time.Time
		// The row is locked, so no other write can slip in between check and update.
		err := tx.QueryRow(ctx, `SELECT updated_at FROM sessions WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, owner).Scan(&updatedAt)
		if err != nil {
			return Session{}, mapError(err)
		}
		if !match.Matches(updatedAt) {
			return Session{}, conditional.ErrPreconditionFailed
		}
	}
	// FOR SHARE keeps the plan from changing under the session that uses it.
	var plan *Plan
	if in.PlanID != nil {
		p, err := getPlan(ctx, tx, owner, *in.PlanID, ` FOR SHARE`)
		if errors.Is(err, ErrNotFound) {
			return Session{}, ErrReference
		}
		if err != nil {
			return Session{}, err
		}
		plan = &p
	}
	if err := validatePlanLinks(in, plan); err != nil {
		return Session{}, err
	}
	query := `INSERT INTO sessions (id,user_id,name,notes,performed_at,plan_id) VALUES ($1,$2,$3,$4,$5,$6) RETURNING ` + sessionColumns
	if !create {
		query = `UPDATE sessions SET name=$3,notes=$4,performed_at=$5,plan_id=$6,updated_at=GREATEST(clock_timestamp(),updated_at+INTERVAL '1 microsecond') WHERE id=$1 AND user_id=$2 RETURNING ` + sessionColumns
	}
	saved, err := scanSessionRow(tx.QueryRow(ctx, query, id, owner, in.Name, in.Notes, in.PerformedAt, in.PlanID))
	if err != nil {
		return Session{}, err
	}
	if !create {
		if _, err := tx.Exec(ctx, `DELETE FROM session_exercises WHERE session_id=$1`, id); err != nil {
			return Session{}, err
		}
	}
	entries := make([]entry, len(in.Exercises))
	for i, e := range in.Exercises {
		entries[i] = entry{id: ids.New(), planExerciseID: e.PlanExerciseID, ExerciseInput: e.ExerciseInput}
	}
	if err := insertEntries(ctx, tx, sessionTables, id, entries); err != nil {
		return Session{}, mapError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Session{}, mapError(err)
	}
	saved.Exercises = toSessionExercises(entries)
	return saved, nil
}
func (r *PostgresRepository) GetSession(ctx context.Context, owner, id uuid.UUID) (Session, error) {
	return getSession(ctx, r.pool, owner, id, ``)
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
		s, err := scanSessionRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	return result, attachSessionExercises(ctx, r.pool, result)
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

const catalogColumns = `id,name,description,category,muscle_group,equipment,created_at,updated_at`

func scanCatalog(row pgx.Row) (CatalogExercise, error) {
	var e CatalogExercise
	if err := row.Scan(&e.ID, &e.Name, &e.Description, &e.Category, &e.MuscleGroup, &e.Equipment, &e.CreatedAt, &e.UpdatedAt); err != nil {
		return CatalogExercise{}, mapError(err)
	}
	e.CreatedAt = e.CreatedAt.UTC()
	e.UpdatedAt = e.UpdatedAt.UTC()
	return e, nil
}

// ListCatalog pages through the catalog alphabetically by (name, id), which
// exercises_name_id_idx serves. The cursor's position is its ID alone: the page
// continues after that entry's name, so a cursor needs no copy of the name.
func (r *PostgresRepository) ListCatalog(ctx context.Context, category string, page validation.Page) ([]CatalogExercise, error) {
	args := []any{category, page.Limit + 1}
	after := ""
	if page.After != nil {
		args = append(args, page.After.ID)
		after = ` AND (name, id) > (SELECT name, id FROM exercises WHERE id = $3)`
	}
	rows, err := r.pool.Query(ctx, `SELECT `+catalogColumns+` FROM exercises WHERE ($1 = '' OR category = $1)`+after+` ORDER BY name, id LIMIT $2`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]CatalogExercise, 0)
	for rows.Next() {
		e, err := scanCatalog(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

func (r *PostgresRepository) CatalogByIDs(ctx context.Context, wanted []uuid.UUID) (map[uuid.UUID]CatalogExercise, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+catalogColumns+` FROM exercises WHERE id = ANY($1)`, wanted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[uuid.UUID]CatalogExercise, len(wanted))
	for rows.Next() {
		e, err := scanCatalog(rows)
		if err != nil {
			return nil, err
		}
		result[e.ID] = e
	}
	return result, rows.Err()
}

// Progress totals, per bucket, the work plans asked for and the work done in
// the owner's sessions. Plans count once per session that followed them,
// including exercises the session skipped. All work is summed in SQL, set by
// set, so the Go side does no per-row arithmetic.
func (r *PostgresRepository) Progress(ctx context.Context, owner uuid.UUID, q ProgressQuery) ([]ProgressPoint, error) {
	const query = `
WITH picked AS (
    SELECT id, plan_id, date_trunc($2, performed_at AT TIME ZONE 'UTC') AS bucket
    FROM sessions
    WHERE user_id = $1 AND performed_at >= $3 AND performed_at < $4
), planned AS (
    SELECT pk.bucket,
           COALESCE(SUM(ps.sets), 0) AS sets,
           COALESCE(SUM(ps.reps), 0) AS reps,
           COALESCE(SUM(ps.volume), 0) AS volume,
           COALESCE(SUM(pe.minutes), 0) AS minutes
    FROM picked pk
    JOIN plan_exercises pe ON pe.plan_id = pk.plan_id
    LEFT JOIN LATERAL (
        SELECT COUNT(*) AS sets, SUM(repetitions) AS reps, SUM(repetitions * weight_kg) AS volume
        FROM plan_sets WHERE plan_exercise_id = pe.id
    ) ps ON true
    WHERE $5::uuid IS NULL OR pe.exercise_id = $5
    GROUP BY pk.bucket
), done AS (
    SELECT pk.bucket,
           COALESCE(SUM(ss.sets), 0) AS sets,
           COALESCE(SUM(ss.reps), 0) AS reps,
           COALESCE(SUM(ss.volume), 0) AS volume,
           COALESCE(SUM(se.minutes), 0) AS minutes,
           COALESCE(SUM(ss.sets) FILTER (WHERE se.plan_exercise_id IS NULL), 0) AS x_sets,
           COALESCE(SUM(ss.reps) FILTER (WHERE se.plan_exercise_id IS NULL), 0) AS x_reps,
           COALESCE(SUM(ss.volume) FILTER (WHERE se.plan_exercise_id IS NULL), 0) AS x_volume,
           COALESCE(SUM(se.minutes) FILTER (WHERE se.plan_exercise_id IS NULL), 0) AS x_minutes
    FROM picked pk
    JOIN session_exercises se ON se.session_id = pk.id
    LEFT JOIN LATERAL (
        SELECT COUNT(*) AS sets, SUM(repetitions) AS reps, SUM(repetitions * weight_kg) AS volume
        FROM session_sets WHERE session_exercise_id = se.id
    ) ss ON true
    WHERE $5::uuid IS NULL OR se.exercise_id = $5
    GROUP BY pk.bucket
), buckets AS (
    SELECT bucket, COUNT(*) AS sessions FROM picked GROUP BY bucket
)
SELECT b.bucket, b.sessions,
       COALESCE(p.sets, 0), COALESCE(p.reps, 0), COALESCE(p.volume, 0), COALESCE(p.minutes, 0),
       COALESCE(d.sets, 0), COALESCE(d.reps, 0), COALESCE(d.volume, 0), COALESCE(d.minutes, 0),
       COALESCE(d.x_sets, 0), COALESCE(d.x_reps, 0), COALESCE(d.x_volume, 0), COALESCE(d.x_minutes, 0)
FROM buckets b
LEFT JOIN planned p USING (bucket)
LEFT JOIN done d USING (bucket)
ORDER BY b.bucket`
	rows, err := r.pool.Query(ctx, query, owner, q.Bucket, q.From, q.To, q.ExerciseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	points := make([]ProgressPoint, 0)
	for rows.Next() {
		var p ProgressPoint
		err := rows.Scan(&p.Bucket, &p.Sessions,
			&p.Planned.Sets, &p.Planned.Repetitions, &p.Planned.VolumeKG, &p.Planned.Minutes,
			&p.Actual.Sets, &p.Actual.Repetitions, &p.Actual.VolumeKG, &p.Actual.Minutes,
			&p.Unplanned.Sets, &p.Unplanned.Repetitions, &p.Unplanned.VolumeKG, &p.Unplanned.Minutes)
		if err != nil {
			return nil, err
		}
		p.Bucket = p.Bucket.UTC()
		points = append(points, p)
	}
	return points, rows.Err()
}
