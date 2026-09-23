package profile

import (
	"math"
	"testing"
	"time"
)

func ptr(v float64) *float64 { return &v }
func TestMeasurements(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name    string
		in      MeasurementInput
		invalid bool
	}{
		{"weight", MeasurementInput{MeasuredAt: now, WeightKG: ptr(80)}, false},
		{"all fields", MeasurementInput{MeasuredAt: now, WeightKG: ptr(80), HeightCM: ptr(180), BodyFatPercent: ptr(15), WaistCM: ptr(80), ChestCM: ptr(100), HipCM: ptr(90)}, false},
		{"zero fat", MeasurementInput{MeasuredAt: now, BodyFatPercent: ptr(0)}, false},
		{"missing time", MeasurementInput{WeightKG: ptr(80)}, true},
		{"empty", MeasurementInput{MeasuredAt: now}, true},
		{"negative", MeasurementInput{MeasuredAt: now, WeightKG: ptr(-1)}, true},
		{"zero height", MeasurementInput{MeasuredAt: now, HeightCM: ptr(0)}, true},
		{"fat out of range", MeasurementInput{MeasuredAt: now, BodyFatPercent: ptr(101)}, true},
		{"NaN", MeasurementInput{MeasuredAt: now, WeightKG: ptr(math.NaN())}, true},
		{"infinity", MeasurementInput{MeasuredAt: now, WeightKG: ptr(math.Inf(1))}, true},
		{"large circumference", MeasurementInput{MeasuredAt: now, HipCM: ptr(501)}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMeasurement(tc.in)
			if (err != nil) != tc.invalid {
				t.Fatalf("error=%v invalid=%v", err, tc.invalid)
			}
		})
	}
}
