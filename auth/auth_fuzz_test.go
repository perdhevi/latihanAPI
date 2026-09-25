package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A token is never empty and never contains whitespace.
func FuzzBearerToken(f *testing.F) {
	for _, seed := range []string{"Bearer abc", "bearer  x", "Basic x", "Bearer", "Bearer a\tb", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, header string) {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		r.Header.Set("Authorization", header)
		token, ok := BearerToken(r)
		if ok && (token == "" || strings.ContainsAny(token, " \t\r\n\x00") || !isB64Token(token)) {
			t.Fatalf("%q gave token %q", header, token)
		}
	})
}

// Routing an arbitrary token reaches at most one verifier, the one whose
// issuer the token names; everything else is ErrUnauthenticated.
func FuzzMultiRouting(f *testing.F) {
	Register("fuzz-a", func(context.Context, Deps) (Authenticator, error) { return issuerAuth{"https://a.example"}, nil })
	Register("fuzz-b", func(context.Context, Deps) (Authenticator, error) { return issuerAuth{"https://b.example"}, nil })
	m, err := New(context.Background(), "fuzz-a,fuzz-b", Deps{})
	if err != nil {
		f.Fatal(err)
	}
	f.Add("Bearer " + token(`{"iss":"https://a.example"}`))
	f.Add("Bearer " + token(`{"iss":["https://a.example"]}`))
	f.Add("Bearer a.b.c")
	f.Add("Bearer ...")
	f.Fuzz(func(t *testing.T, header string) {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		r.Header.Set("Authorization", header)
		id, err := m.Authenticate(r)
		if err != nil {
			if !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("unexpected error %v", err)
			}
			return
		}
		if id.Issuer != "https://a.example" && id.Issuer != "https://b.example" {
			t.Fatalf("routed to %q", id.Issuer)
		}
	})
}
