package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// issuerAuth accepts any token and reports which issuer's verifier ran.
type issuerAuth struct{ issuer string }

func (a issuerAuth) Issuer() string { return a.issuer }
func (a issuerAuth) Authenticate(*http.Request) (Identity, error) {
	return Identity{Issuer: a.issuer, Subject: "s"}, nil
}

type unboundAuth struct{}

func (unboundAuth) Authenticate(*http.Request) (Identity, error) { return Identity{}, nil }

func token(payload string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256"}`)) + "." + enc([]byte(payload)) + ".sig"
}

func TestMultiProvider(t *testing.T) {
	Register("multi-a", func(context.Context, Deps) (Authenticator, error) { return issuerAuth{"https://a.example"}, nil })
	Register("multi-b", func(context.Context, Deps) (Authenticator, error) { return issuerAuth{"https://b.example"}, nil })
	Register("multi-a-again", func(context.Context, Deps) (Authenticator, error) { return issuerAuth{"https://a.example"}, nil })
	Register("multi-unbound", func(context.Context, Deps) (Authenticator, error) { return unboundAuth{}, nil })

	a, err := New(t.Context(), "multi-a, multi-b", Deps{})
	if err != nil {
		t.Fatal(err)
	}
	for authorization, want := range map[string]string{
		"Bearer " + token(`{"iss":"https://a.example"}`): "https://a.example",
		"Bearer " + token(`{"iss":"https://b.example"}`): "https://b.example",
		"Bearer " + token(`{"iss":"https://c.example"}`): "",
		"Bearer " + token(`not json`):                    "",
		"Bearer not-a-jwt":                               "",
		"Bearer " + strings.Repeat("a", 9000):            "",
		"":                                               "",
	} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		if authorization != "" {
			r.Header.Set("Authorization", authorization)
		}
		id, err := a.Authenticate(r)
		if want == "" {
			if !errors.Is(err, ErrUnauthenticated) {
				t.Errorf("%.40q: want ErrUnauthenticated, got %v", authorization, err)
			}
			continue
		}
		if err != nil || id.Issuer != want {
			t.Errorf("%.40q routed to %q (%v), want %q", authorization, id.Issuer, err, want)
		}
	}
	for _, names := range []string{"multi-a,multi-a-again", "multi-a,multi-unbound", "multi-a,missing"} {
		if _, err := New(t.Context(), names, Deps{}); err == nil {
			t.Errorf("%s: expected error", names)
		}
	}
}

type erasingAuth struct {
	issuerAuth
	erased *[]string
}

func (a erasingAuth) EraseAccount(_ context.Context, id Identity) error {
	*a.erased = append(*a.erased, a.issuer+" "+id.Subject)
	return nil
}

func TestMultiProviderErasure(t *testing.T) {
	var erased []string
	Register("erase-a", func(context.Context, Deps) (Authenticator, error) {
		return erasingAuth{issuerAuth{"https://erase-a.example"}, &erased}, nil
	})
	Register("erase-b", func(context.Context, Deps) (Authenticator, error) { return issuerAuth{"https://erase-b.example"}, nil })
	m, err := New(t.Context(), "erase-a,erase-b", Deps{})
	if err != nil {
		t.Fatal(err)
	}
	eraser := m.(AccountEraser)
	_ = eraser.EraseAccount(t.Context(), Identity{Issuer: "https://erase-b.example", Subject: "x"}) // keeps no accounts
	_ = eraser.EraseAccount(t.Context(), Identity{Issuer: "https://erase-a.example", Subject: "y"})
	if len(erased) != 1 || erased[0] != "https://erase-a.example y" {
		t.Fatalf("erasure routed to %v", erased)
	}
}
