// Package ids generates identifiers for database primary keys.
package ids

import "github.com/google/uuid"

// New returns a time-ordered (version 7) UUID. Unlike a random version 4 UUID,
// consecutive values land next to each other in a B-tree index, so inserts
// append instead of splitting pages at random.
func New() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		// Only possible if the system's random source fails.
		panic(err)
	}
	return id
}
