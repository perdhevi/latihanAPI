package training

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/perdhevi/latihanAPI/internal/validation"
)

// ProgressQuery selects the sessions to total up and how to group them.
// ExerciseID, when set, restricts the totals to that catalog exercise.
type ProgressQuery struct {
	ExerciseID *uuid.UUID
	From, To   time.Time // From inclusive, To exclusive
	Bucket     string    // day, week or month
}

// ProgressPoint compares, for the sessions of one bucket that followed a plan,
// the planned work with the work done. Planned is everything the plans asked
// for, Actual everything performed (including extras), and Unplanned the part
// of Actual that no plan asked for. A completion percentage is nil when the
// plan asked for none of that metric.
type ProgressPoint struct {
	Bucket         time.Time `json:"bucket"`
	Sessions       int       `json:"sessions"`
	Planned        Totals    `json:"planned"`
	Actual         Totals    `json:"actual"`
	Unplanned      Totals    `json:"unplanned"`
	Delta          Totals    `json:"delta"`
	VolumeDonePct  *float64  `json:"volume_completion_pct"`
	MinutesDonePct *float64  `json:"minutes_completion_pct"`
}

const maxProgressRange = 366 * 24 * time.Hour

func (q *ProgressQuery) validate(now time.Time) error {
	switch q.Bucket {
	case "":
		q.Bucket = "week"
	case "day", "week", "month":
	default:
		return validation.Invalid("bucket must be day, week or month")
	}
	if q.To.IsZero() {
		q.To = now
	}
	if q.From.IsZero() {
		q.From = q.To.Add(-90 * 24 * time.Hour)
	}
	if !validation.Timestamp(q.From) || !validation.Timestamp(q.To) || !q.From.Before(q.To) {
		return validation.Invalid("from must be before to, both RFC3339 timestamps")
	}
	if q.To.Sub(q.From) > maxProgressRange {
		return validation.Invalid("the range from..to may span at most 366 days")
	}
	if q.ExerciseID != nil && *q.ExerciseID == uuid.Nil {
		return validation.Invalid("exercise_id must not be the nil UUID")
	}
	q.From, q.To = q.From.UTC(), q.To.UTC()
	return nil
}

// Progress totals planned against performed work per bucket, oldest first.
func (s *Service) Progress(ctx context.Context, owner uuid.UUID, q ProgressQuery) ([]ProgressPoint, error) {
	if owner == uuid.Nil {
		return nil, errNoOwner
	}
	if err := q.validate(time.Now()); err != nil {
		return nil, err
	}
	points, err := s.repo.Progress(ctx, owner, q)
	if err != nil {
		return nil, err
	}
	for i := range points {
		p := &points[i]
		p.Delta = subtract(p.Actual, p.Planned)
		if p.Planned.VolumeKG > 0 {
			v := (p.Actual.VolumeKG - p.Unplanned.VolumeKG) / p.Planned.VolumeKG * 100
			p.VolumeDonePct = &v
		}
		if p.Planned.Minutes > 0 {
			v := (p.Actual.Minutes - p.Unplanned.Minutes) / p.Planned.Minutes * 100
			p.MinutesDonePct = &v
		}
	}
	return points, nil
}
