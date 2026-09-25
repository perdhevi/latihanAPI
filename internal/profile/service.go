package profile

import (
	"context"
	"github.com/google/uuid"
	"github.com/perdhevi/latihanAPI/internal/validation"
)

type Repository interface {
	CreateUser(context.Context, uuid.UUID, UserInput) (User, error)
	GetUser(context.Context, uuid.UUID) (User, error)
	UpdateUser(context.Context, uuid.UUID, UserInput) (User, error)
	DeleteUser(context.Context, uuid.UUID) error
	CreateMeasurement(context.Context, uuid.UUID, uuid.UUID, MeasurementInput) (Measurement, error)
	GetMeasurement(context.Context, uuid.UUID, uuid.UUID) (Measurement, error)
	ListMeasurements(context.Context, uuid.UUID, validation.Page) ([]Measurement, error)
	UpdateMeasurement(context.Context, uuid.UUID, uuid.UUID, MeasurementInput) (Measurement, error)
	DeleteMeasurement(context.Context, uuid.UUID, uuid.UUID) error
}

type Service struct{ repo Repository }

func NewService(repo Repository) *Service { return &Service{repo: repo} }
func (s *Service) CreateUser(ctx context.Context, in UserInput) (User, error) {
	if err := validation.Text("display_name", &in.DisplayName, 200, true); err != nil {
		return User{}, err
	}
	return s.repo.CreateUser(ctx, uuid.New(), in)
}
func (s *Service) GetUser(ctx context.Context, id uuid.UUID) (User, error) {
	return s.repo.GetUser(ctx, id)
}
func (s *Service) UpdateUser(ctx context.Context, id uuid.UUID, in UserInput) (User, error) {
	if err := validation.Text("display_name", &in.DisplayName, 200, true); err != nil {
		return User{}, err
	}
	return s.repo.UpdateUser(ctx, id, in)
}
func (s *Service) DeleteUser(ctx context.Context, id uuid.UUID) error {
	return s.repo.DeleteUser(ctx, id)
}
func (s *Service) CreateMeasurement(ctx context.Context, userID uuid.UUID, in MeasurementInput) (Measurement, error) {
	if err := validateMeasurement(in); err != nil {
		return Measurement{}, err
	}
	in.MeasuredAt = in.MeasuredAt.UTC()
	return s.repo.CreateMeasurement(ctx, userID, uuid.New(), in)
}
func (s *Service) GetMeasurement(ctx context.Context, userID, id uuid.UUID) (Measurement, error) {
	return s.repo.GetMeasurement(ctx, userID, id)
}
func (s *Service) ListMeasurements(ctx context.Context, userID uuid.UUID, page validation.Page) ([]Measurement, error) {
	if err := page.Validate(); err != nil {
		return nil, err
	}
	if _, err := s.repo.GetUser(ctx, userID); err != nil {
		return nil, err
	}
	return s.repo.ListMeasurements(ctx, userID, page)
}
func (s *Service) UpdateMeasurement(ctx context.Context, userID, id uuid.UUID, in MeasurementInput) (Measurement, error) {
	if err := validateMeasurement(in); err != nil {
		return Measurement{}, err
	}
	in.MeasuredAt = in.MeasuredAt.UTC()
	return s.repo.UpdateMeasurement(ctx, userID, id, in)
}
func (s *Service) DeleteMeasurement(ctx context.Context, userID, id uuid.UUID) error {
	return s.repo.DeleteMeasurement(ctx, userID, id)
}
