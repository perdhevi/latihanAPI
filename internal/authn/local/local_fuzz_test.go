package local

import (
	"context"
	"strings"
	"testing"
)

// Accepted emails are lower-case, bounded, free of spaces and brackets, and
// normalizing them again changes nothing.
func FuzzNormalizeEmail(f *testing.F) {
	for _, seed := range []string{"Alex@Example.com", " a@b.c ", "Alex <a@b.c>", "a@b", `"a b"@c.d`, "a@[127.0.0.1]", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		email, ok := normalizeEmail(raw)
		if !ok {
			return
		}
		again, stillOK := normalizeEmail(email)
		if email != strings.ToLower(email) || len(email) > 254 || !stillOK || again != email || strings.ContainsAny(email, "<>\t\r\n") {
			t.Fatalf("%q normalized to %q", raw, email)
		}
	})
}

// Stored hashes come from the database; a corrupted one must fail cleanly and
// never carry parameters outside the bounds that cap memory per login.
func FuzzParsePHC(f *testing.F) {
	h, err := newHasher(testParams, 1)
	if err != nil {
		f.Fatal(err)
	}
	good, err := h.hash(context.Background(), "password")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(good)
	f.Add("$argon2id$v=19$m=4194304,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA")
	f.Add("$argon2id$v=19$m=8192,t=1,p=1$$")
	f.Fuzz(func(t *testing.T, encoded string) {
		p, salt, sum, err := parsePHC(encoded)
		if err != nil {
			return
		}
		if p.memoryKiB > 256*1024 || p.passes > 10 || p.lanes > 16 || len(salt) < 8 || len(sum) < 16 || len(sum) > 64 {
			t.Fatalf("accepted out-of-bounds hash %q: %+v", encoded, p)
		}
	})
}
