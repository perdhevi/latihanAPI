package training

import (
	"context"
	"github.com/google/uuid"
	"latihanApi/internal/validation"
)

type Repository interface {
	SavePlan(context.Context, Plan, bool) (Plan, error)
	GetPlan(context.Context, uuid.UUID) (Plan, error)
	ListPlans(context.Context, uuid.UUID, validation.Page) ([]Plan, error)
	DeletePlan(context.Context, uuid.UUID) error
	SaveSession(context.Context, uuid.UUID, SessionInput, bool) (Session, error)
	GetSession(context.Context, uuid.UUID) (Session, error)
	ListSessions(context.Context, uuid.UUID, validation.Page) ([]Session, error)
	DeleteSession(context.Context, uuid.UUID) error
}
type Service struct{ repo Repository }

func NewService(repo Repository) *Service { return &Service{repo: repo} }
func (s *Service) SavePlan(ctx context.Context, id uuid.UUID, in PlanInput, create bool) (Plan, error) {
	if err := validateHeader(in.UserID, &in.Name, &in.Notes, len(in.Exercises)); err != nil {
		return Plan{}, err
	}
	entries := make([]PlanExercise, 0, len(in.Exercises))
	for _, e := range in.Exercises {
		if err := validateExercise(&e); err != nil {
			return Plan{}, err
		}
		entries = append(entries, PlanExercise{ID: uuid.New(), ExerciseInput: e})
	}
	if create {
		id = uuid.New()
	}
	return s.repo.SavePlan(ctx, Plan{ID: id, UserID: in.UserID, Name: in.Name, Notes: in.Notes, Exercises: entries}, create)
}
func (s *Service) GetPlan(ctx context.Context, id uuid.UUID) (Plan, error) {
	return s.repo.GetPlan(ctx, id)
}
func (s *Service) ListPlans(ctx context.Context, userID uuid.UUID, page validation.Page) ([]Plan, error) {
	if err := page.Validate(); err != nil {
		return nil, err
	}
	return s.repo.ListPlans(ctx, userID, page)
}
func (s *Service) DeletePlan(ctx context.Context, id uuid.UUID) error {
	return s.repo.DeletePlan(ctx, id)
}
func (s *Service) SaveSession(ctx context.Context, id uuid.UUID, in SessionInput, create bool) (Session, error) {
	if err := validateHeader(in.UserID, &in.Name, &in.Notes, len(in.Exercises)); err != nil {
		return Session{}, err
	}
	if !validation.Timestamp(in.PerformedAt) {
		return Session{}, validation.Invalid("performed_at must be a nonzero RFC3339 timestamp")
	}
	if in.PlanID != nil && *in.PlanID == uuid.Nil {
		return Session{}, validation.Invalid("plan_id must not be the nil UUID")
	}
	for i := range in.Exercises {
		if err := validateExercise(&in.Exercises[i].ExerciseInput); err != nil {
			return Session{}, err
		}
	}
	in.PerformedAt = in.PerformedAt.UTC()
	if create {
		id = uuid.New()
	}
	return s.repo.SaveSession(ctx, id, in, create)
}
func (s *Service) GetSession(ctx context.Context, id uuid.UUID) (Session, error) {
	return s.repo.GetSession(ctx, id)
}
func (s *Service) ListSessions(ctx context.Context, userID uuid.UUID, page validation.Page) ([]Session, error) {
	if err := page.Validate(); err != nil {
		return nil, err
	}
	return s.repo.ListSessions(ctx, userID, page)
}
func (s *Service) DeleteSession(ctx context.Context, id uuid.UUID) error {
	return s.repo.DeleteSession(ctx, id)
}
func (s *Service) Compare(ctx context.Context, planID, sessionID uuid.UUID) (Comparison, error) {
	session, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return Comparison{}, err
	}
	if session.PlanID == nil || *session.PlanID != planID || session.PlanSnapshot == nil {
		return Comparison{}, ErrNotFound
	}
	return Compare(session), nil
}
