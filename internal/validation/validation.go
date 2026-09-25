package validation

import (
	"encoding/base64"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
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

// Page selects up to Limit items of a list ordered newest first by (time, id),
// starting after the position After, or at the start when After is nil.
//
// Keyset pagination replaces offsets: an offset makes the database walk past
// every skipped row, so ?offset=2000000000 was a cheap way to burn CPU, and
// offsets skip or repeat items when rows are inserted between pages.
type Page struct {
	Limit int
	After *Cursor
}

func (p Page) Validate() error {
	if p.Limit < 1 || p.Limit > 100 {
		return Invalid("limit must be 1..100")
	}
	return nil
}

// SQL returns the WHERE continuation and ORDER BY / LIMIT tail for a list
// ordered by (column DESC, id DESC), appending its arguments to args. It asks
// for one row more than Limit: that row's presence means another page exists.
func (p Page) SQL(column string, args []any) (string, []any) {
	clause := ""
	if p.After != nil {
		args = append(args, p.After.Time, p.After.ID)
		clause = fmt.Sprintf(" AND (%s,id) < ($%d,$%d)", column, len(args)-1, len(args))
	}
	args = append(args, p.Limit+1)
	return clause + fmt.Sprintf(" ORDER BY %s DESC,id DESC LIMIT $%d", column, len(args)), args
}

// Cursor is the position after the last item of a page.
type Cursor struct {
	Time time.Time
	ID   uuid.UUID
}

// Encode returns an opaque token. It is not signed: a crafted cursor can only
// choose a position inside the caller's own, owner-scoped list.
func (c Cursor) Encode() string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(c.Time.UnixMicro(), 10) + "." + c.ID.String()))
}

func DecodeCursor(s string) (Cursor, error) {
	bad := Invalid("cursor is invalid; use next_cursor from the previous page")
	if len(s) > 128 {
		return Cursor{}, bad
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, bad
	}
	micros, id, ok := strings.Cut(string(raw), ".")
	t, err := strconv.ParseInt(micros, 10, 64)
	if !ok || err != nil {
		return Cursor{}, bad
	}
	parsed, err := uuid.Parse(id)
	if err != nil || len(id) != 36 {
		return Cursor{}, bad
	}
	return Cursor{Time: time.UnixMicro(t).UTC(), ID: parsed}, nil
}
