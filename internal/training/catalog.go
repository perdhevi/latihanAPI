package training

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/perdhevi/latihanAPI/internal/validation"
)

// CatalogExercise is an entry of the shared, read-only exercise catalog.
type CatalogExercise struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Category    string    `json:"category"`
	MuscleGroup string    `json:"muscle_group"`
	Equipment   string    `json:"equipment"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func validCategory(c string) bool {
	switch c {
	case "strength", "cardio", "mobility", "stretching":
		return true
	}
	return false
}

// ListCatalog lists catalog entries, optionally of one category.
func (s *Service) ListCatalog(ctx context.Context, category string, page validation.Page) ([]CatalogExercise, error) {
	if err := page.Validate(); err != nil {
		return nil, err
	}
	if category != "" && !validCategory(category) {
		return nil, validation.Invalid("category must be strength, cardio, mobility or stretching")
	}
	return s.repo.ListCatalog(ctx, category, page)
}

func (s *Service) GetCatalogExercise(ctx context.Context, id uuid.UUID) (CatalogExercise, error) {
	found, err := s.repo.CatalogByIDs(ctx, []uuid.UUID{id})
	if err != nil {
		return CatalogExercise{}, err
	}
	e, ok := found[id]
	if !ok {
		return CatalogExercise{}, ErrNotFound
	}
	return e, nil
}

// resolveCatalog fills the name and kind of exercises that link the catalog and
// rejects links to entries that do not exist or contradict the given kind.
func (s *Service) resolveCatalog(ctx context.Context, inputs []*ExerciseInput) error {
	var wanted []uuid.UUID
	for _, in := range inputs {
		if in.ExerciseID == nil {
			continue
		}
		if *in.ExerciseID == uuid.Nil {
			return validation.Invalid("exercise_id must not be the nil UUID")
		}
		wanted = append(wanted, *in.ExerciseID)
	}
	if len(wanted) == 0 {
		return nil
	}
	found, err := s.repo.CatalogByIDs(ctx, wanted)
	if err != nil {
		return err
	}
	for _, in := range inputs {
		if in.ExerciseID == nil {
			continue
		}
		entry, ok := found[*in.ExerciseID]
		if !ok {
			return ErrReference
		}
		if in.Name == "" {
			in.Name = entry.Name
		}
		if in.Kind == "" {
			in.Kind = entry.Category
		} else if in.Kind != entry.Category {
			return validation.Invalid("exercise kind must match the catalog category of its exercise_id")
		}
	}
	return nil
}
