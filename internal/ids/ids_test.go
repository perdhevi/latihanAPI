package ids

import (
	"bytes"
	"testing"
)

func TestNewIsVersion7AndIncreasing(t *testing.T) {
	prev := New()
	if prev.Version() != 7 {
		t.Fatalf("version = %d, want 7", prev.Version())
	}
	for range 1000 {
		next := New()
		if bytes.Compare(prev[:], next[:]) >= 0 {
			t.Fatalf("%s is not after %s", next, prev)
		}
		prev = next
	}
}
