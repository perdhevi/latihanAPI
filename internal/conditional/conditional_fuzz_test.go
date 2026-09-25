package conditional

import (
	"testing"
	"time"
)

// Parsing never panics, and any header that contains a version's own ETag
// matches that version.
func FuzzParseIfMatch(f *testing.F) {
	for _, seed := range []string{"", "*", `"abc"`, `W/"abc"`, `"a", "b"`, `"`, `""`, `"-zz"`} {
		f.Add(seed, int64(1_758_000_000_000_000))
	}
	f.Fuzz(func(t *testing.T, header string, micros int64) {
		version := time.UnixMicro(micros).UTC()
		ParseIfMatch(header)
		if !ParseIfMatch(header + ", " + ETag(version)).Matches(version) {
			t.Fatalf("header %q plus the version's own ETag did not match it", header)
		}
	})
}
