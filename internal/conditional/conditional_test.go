package conditional

import (
	"testing"
	"time"
)

func TestETagRoundTrip(t *testing.T) {
	v := time.Date(2026, 9, 25, 8, 0, 0, 123456000, time.UTC)
	other := v.Add(time.Microsecond)
	m := ParseIfMatch(ETag(v))
	if !m.Conditional() || !m.Matches(v) || m.Matches(other) {
		t.Fatal("tag does not match exactly its own version")
	}
	if list := ParseIfMatch(`"zzz", ` + ETag(other) + `, ` + ETag(v)); !list.Matches(v) || !list.Matches(other) {
		t.Fatal("any tag in a list should match")
	}
}

func TestIfMatchForms(t *testing.T) {
	v := time.Now().UTC().Truncate(time.Microsecond)
	for header, wantConditional := range map[string]bool{"": false, "*": false, " * ": false, ETag(v): true, "W/" + ETag(v): true, "garbage": true, `"not base36!"`: true} {
		m := ParseIfMatch(header)
		if m.Conditional() != wantConditional {
			t.Errorf("%q: conditional=%v", header, m.Conditional())
		}
		if wantConditional && header != ETag(v) && m.Matches(v) {
			t.Errorf("%q must never match (weak or foreign tags fail strong comparison)", header)
		}
	}
}

func TestSQL(t *testing.T) {
	if clause, args := Any.SQL([]any{1, 2}); clause != "" || len(args) != 2 {
		t.Fatal("unconditional write gained a condition")
	}
	clause, args := ParseIfMatch(ETag(time.Now())).SQL([]any{1, 2})
	if clause != " AND updated_at = ANY($3)" || len(args) != 3 {
		t.Fatalf("%q %v", clause, args)
	}
}
