package validation

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A cursor that decodes must stand for exactly one position: re-encoding it
// gives a token that decodes to the same place.
func FuzzDecodeCursor(f *testing.F) {
	f.Add("MTc1ODYxMDgwMDAwMDAwMC4wMGQ0NjJhOC1mMjI1LTQ2ZGItYjk4Ny0wMjFkNWU0NjUxZDA")
	f.Add("")
	f.Add("!!!")
	f.Add("LTEuMDAwMDAwMDAtMDAwMC0wMDAwLTAwMDAtMDAwMDAwMDAwMDAw")
	f.Fuzz(func(t *testing.T, token string) {
		c, err := DecodeCursor(token)
		if err != nil {
			return
		}
		again, err := DecodeCursor(c.Encode())
		if err != nil || !again.Time.Equal(c.Time) || again.ID != c.ID {
			t.Fatalf("cursor %q does not round-trip: %+v vs %+v (%v)", token, c, again, err)
		}
	})
}

// Accepted text is trimmed, valid UTF-8, free of NUL and within the limit.
func FuzzText(f *testing.F) {
	for _, seed := range []string{"  Leg day ", "", "\x00", "\xff\xfe", strings.Repeat("é", 201), " \t\n"} {
		f.Add(seed, 200, true)
	}
	f.Fuzz(func(t *testing.T, input string, limit int, required bool) {
		if limit < 0 || limit > 10_000 {
			return
		}
		value := input
		if err := Text("field", &value, limit, required); err != nil {
			return
		}
		if value != strings.TrimSpace(value) || !utf8.ValidString(value) || strings.ContainsRune(value, 0) ||
			utf8.RuneCountInString(value) > limit || (required && value == "") {
			t.Fatalf("accepted %q from %q", value, input)
		}
	})
}
