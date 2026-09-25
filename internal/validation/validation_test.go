package validation

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCursorRoundTrip(t *testing.T) {
	c := Cursor{Time: time.Date(2026, 9, 23, 8, 0, 0, 123456000, time.UTC), ID: uuid.New()}
	got, err := DecodeCursor(c.Encode())
	if err != nil || !got.Time.Equal(c.Time) || got.ID != c.ID {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	for _, bad := range []string{"", "!!!", "bm90LWEtY3Vyc29y", Cursor{ID: uuid.New()}.Encode()[:10]} {
		if _, err := DecodeCursor(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPageSQL(t *testing.T) {
	tail, args := Page{Limit: 20}.SQL("performed_at", []any{"owner"})
	if tail != " ORDER BY performed_at DESC,id DESC LIMIT $2" || len(args) != 2 || args[1] != 21 {
		t.Fatalf("first page: %q %v", tail, args)
	}
	after := Cursor{Time: time.Now(), ID: uuid.New()}
	tail, args = Page{Limit: 5, After: &after}.SQL("created_at", []any{"owner"})
	if tail != " AND (created_at,id) < ($2,$3) ORDER BY created_at DESC,id DESC LIMIT $4" || len(args) != 4 || args[3] != 6 {
		t.Fatalf("next page: %q %v", tail, args)
	}
}
