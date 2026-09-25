package profile

import (
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/perdhevi/latihanAPI/internal/validation"
)

var ErrNotFound = errors.New("profile or measurement not found")
var ErrConflict = errors.New("user has dependent records")
var ErrUserMissing = errors.New("user does not exist")
var ErrIdentityLinked = errors.New("identity already has a profile")

// Identity is an authenticated caller as named by its authentication provider.
type Identity struct {
	Issuer  string
	Subject string
}

type User struct {
	ID          uuid.UUID `json:"id"`
	DisplayName string    `json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
type UserInput struct {
	DisplayName string `json:"display_name"`
}

type MeasurementInput struct {
	MeasuredAt     time.Time `json:"measured_at"`
	WeightKG       *float64  `json:"weight_kg"`
	HeightCM       *float64  `json:"height_cm"`
	BodyFatPercent *float64  `json:"body_fat_percent"`
	WaistCM        *float64  `json:"waist_cm"`
	ChestCM        *float64  `json:"chest_cm"`
	HipCM          *float64  `json:"hip_cm"`
}
type Measurement struct {
	ID     uuid.UUID `json:"id"`
	UserID uuid.UUID `json:"user_id"`
	MeasurementInput
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func validateMeasurement(in MeasurementInput) error {
	if !validation.Timestamp(in.MeasuredAt) {
		return validation.Invalid("measured_at must be a nonzero RFC3339 timestamp")
	}
	count := 0
	for _, field := range []struct {
		name        string
		value       *float64
		max         float64
		zeroAllowed bool
	}{
		{"weight_kg", in.WeightKG, 1000, false}, {"height_cm", in.HeightCM, 300, false},
		{"body_fat_percent", in.BodyFatPercent, 100, true}, {"waist_cm", in.WaistCM, 500, false},
		{"chest_cm", in.ChestCM, 500, false}, {"hip_cm", in.HipCM, 500, false},
	} {
		if field.value == nil {
			continue
		}
		count++
		if !validation.Number(*field.value, 0, field.max) || (!field.zeroAllowed && *field.value == 0) {
			return validation.Invalid("invalid " + field.name)
		}
	}
	if count == 0 {
		return validation.Invalid("at least one measurement is required")
	}
	return nil
}
