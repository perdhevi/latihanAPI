package validation

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

type Error struct{ Message string }

func (e *Error) Error() string     { return e.Message }
func Invalid(message string) error { return &Error{Message: message} }

func Text(name string, value *string, max int, required bool) error {
	*value = strings.TrimSpace(*value)
	if !utf8.ValidString(*value) || strings.ContainsRune(*value, '\x00') {
		return Invalid(name + " must be valid text without null characters")
	}
	if required && *value == "" {
		return Invalid(name + " is required")
	}
	if utf8.RuneCountInString(*value) > max {
		return Invalid(fmt.Sprintf("%s must be at most %d characters", name, max))
	}
	return nil
}

func Number(value float64, min, max float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= min && value <= max
}
func Timestamp(value time.Time) bool {
	return !value.IsZero() && value.Year() >= 1 && value.Year() <= 9999
}

type Page struct {
	Limit  int
	Offset int
}

func (p Page) Validate() error {
	if p.Limit < 1 || p.Limit > 100 || p.Offset < 0 || int64(p.Offset) > 2147483647 {
		return Invalid("limit must be 1..100 and offset must be 0..2147483647")
	}
	return nil
}
