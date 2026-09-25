// Package conditional implements optimistic concurrency with ETag and
// If-Match (RFC 9110, section 13.1.1). A record's version is its updated_at
// timestamp, which every write moves forward by at least a microsecond.
package conditional

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrPreconditionFailed means the record exists but has changed since the
// version the client based its request on.
var ErrPreconditionFailed = errors.New("record changed since it was read")

// ETag returns the strong entity tag for a record version.
func ETag(updatedAt time.Time) string {
	return `"` + strconv.FormatInt(updatedAt.UnixMicro(), 36) + `"`
}

// Match is a parsed If-Match header. The zero value (no header, or "*")
// places no condition: the write applies to whatever version exists.
type Match struct {
	versions []time.Time
	set      bool
}

// Any matches every existing version.
var Any = Match{}

// ParseIfMatch reads an If-Match header value. Weak tags (W/"...") and tags
// this service did not issue can never match, as strong comparison requires.
func ParseIfMatch(header string) Match {
	header = strings.TrimSpace(header)
	if header == "" || header == "*" {
		return Any
	}
	m := Match{set: true}
	for _, tag := range strings.Split(header, ",") {
		tag = strings.TrimSpace(tag)
		if len(tag) < 3 || tag[0] != '"' || tag[len(tag)-1] != '"' {
			continue
		}
		micros, err := strconv.ParseInt(tag[1:len(tag)-1], 36, 64)
		if err != nil {
			continue
		}
		m.versions = append(m.versions, time.UnixMicro(micros).UTC())
	}
	return m
}

// Conditional reports whether the header named specific versions.
func (m Match) Conditional() bool { return m.set }

// Matches reports whether a record at version updatedAt satisfies m.
func (m Match) Matches(updatedAt time.Time) bool {
	if !m.set {
		return true
	}
	for _, v := range m.versions {
		if v.Equal(updatedAt) {
			return true
		}
	}
	return false
}

// SQL returns a WHERE continuation that restricts a write to the matching
// versions, appending its argument to args. Evaluating the condition inside the
// UPDATE or DELETE makes check and write one atomic step.
func (m Match) SQL(args []any) (string, []any) {
	if !m.set {
		return "", args
	}
	args = append(args, m.versions)
	return fmt.Sprintf(" AND updated_at = ANY($%d)", len(args)), args
}
