package training

import (
	"context"

	"github.com/google/uuid"

	"github.com/perdhevi/latihanAPI/internal/validation"
)

// Repository methods take the owner explicitly and must scope every query by
// it: a record that belongs to someone else is indistinguishable from one that
// does not exist.
type Repository interface {
	SavePlan(context.Context, Plan, bool) (Plan, error)
	GetPlan(ctx context.Context, owner, id uuid.UUID) (Plan, error)
	ListPlans(ctx context.Context, owner uuid.UUID, page validation.Page) ([]Plan, error)
	DeletePlan(ctx context.Context, owner, id uuid.UUID) error
	SaveSession(ctx context.Context, owner, id uuid.UUID, in SessionInput, create bool) (Session, error)
	GetSession(ctx context.Context, owner, id uuid.UUID) (Session, error)
	ListSessions(ctx context.Context, owner uuid.UUID, page validation.Page) ([]Session, error)
	DeleteSession(ctx context.Context, owner, id uuid.UUID) error
}
type Service struct{ repo Repository }

func NewService(repo Repository) *Service { return &Service{repo: repo} }
func (s *Service) SavePlan(ctx context.Context, owner, id uuid.UUID, in PlanInput, create bool) (Plan, error) {
	if owner == uuid.Nil {
		return Plan{}, errNoOwner
	}
	if err := validateHeader(&in.Name, &in.Notes, len(in.Exercises)); err != nil {
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
	return s.repo.SavePlan(ctx, Plan{ID: id, UserID: owner, Name: in.Name, Notes: in.Notes, Exercises: entries}, create)
}
func (s *Service) GetPlan(ctx context.Context, owner, id uuid.UUID) (Plan, error) {
	if owner == uuid.Nil {
		return Plan{}, errNoOwner
	}
	return s.repo.GetPlan(ctx, owner, id)
}
func (s *Service) ListPlans(ctx context.Context, owner uuid.UUID, page validation.Page) ([]Plan, error) {
	if owner == uuid.Nil {
		return nil, errNoOwner
	}
	if err := page.Validate(); err != nil {
		return nil, err
	}
	return s.repo.ListPlans(ctx, owner, page)
}
func (s *Service) DeletePlan(ctx context.Context, owner, id uuid.UUID) error {
	if owner == uuid.Nil {
		return errNoOwner
	}
	return s.repo.DeletePlan(ctx, owner, id)
}
func (s *Service) SaveSession(ctx context.Context, owner, id uuid.UUID, in SessionInput, create bool) (Session, error) {
	if owner == uuid.Nil {
		return Session{}, errNoOwner
	}
	if err := validateHeader(&in.Name, &in.Notes, len(in.Exercises)); err != nil {
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
	return s.repo.SaveSession(ctx, owner, id, in, create)
}
func (s *Service) GetSession(ctx context.Context, owner, id uuid.UUID) (Session, error) {
	if owner == uuid.Nil {
		return Session{}, errNoOwner
	}
	return s.repo.GetSession(ctx, owner, id)
}
func (s *Service) ListSessions(ctx context.Context, owner uuid.UUID, page validation.Page) ([]Session, error) {
	if owner == uuid.Nil {
		return nil, errNoOwner
	}
	if err := page.Validate(); err != nil {
		return nil, err
	}
	return s.repo.ListSessions(ctx, owner, page)
}
func (s *Service) DeleteSession(ctx context.Context, owner, id uuid.UUID) error {
	if owner == uuid.Nil {
		return errNoOwner
	}
	return s.repo.DeleteSession(ctx, owner, id)
}
func (s *Service) Compare(ctx context.Context, owner, planID, sessionID uuid.UUID) (Comparison, error) {
	session, err := s.GetSession(ctx, owner, sessionID)
	if err != nil {
		return Comparison{}, err
	}
	if session.PlanID == nil || *session.PlanID != planID || session.PlanSnapshot == nil {
		return Comparison{}, ErrNotFound
	}
	return Compare(session), nil
}
