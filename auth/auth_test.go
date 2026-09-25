package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type staticAuth struct{}

func (staticAuth) Authenticate(*http.Request) (Identity, error) { return Identity{}, nil }

func TestRegistry(t *testing.T) {
	Register("registry-ok", func(context.Context, Deps) (Authenticator, error) { return staticAuth{}, nil })
	Register("registry-err", func(context.Context, Deps) (Authenticator, error) { return nil, errors.New("bad config") })
	Register("registry-nil", func(context.Context, Deps) (Authenticator, error) { return nil, nil })

	if _, err := New(t.Context(), "registry-ok", Deps{}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(t.Context(), "registry-err", Deps{}); err == nil || !strings.Contains(err.Error(), "bad config") {
		t.Fatalf("factory error not reported: %v", err)
	}
	if _, err := New(t.Context(), "registry-nil", Deps{}); err == nil {
		t.Fatal("nil authenticator accepted")
	}
	_, err := New(t.Context(), "missing", Deps{})
	if err == nil || !strings.Contains(err.Error(), "registry-ok") {
		t.Fatalf("unknown provider error should list available names: %v", err)
	}
	for name, register := range map[string]func(){
		"duplicate": func() {
			Register("registry-ok", func(context.Context, Deps) (Authenticator, error) { return nil, nil })
		},
		"empty name": func() { Register("", func(context.Context, Deps) (Authenticator, error) { return nil, nil }) },
		"comma":      func() { Register("a,b", func(context.Context, Deps) (Authenticator, error) { return nil, nil }) },
		"nil":        func() { Register("registry-nil-factory", nil) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic")
				}
			}()
			register()
		})
	}
}

func TestBearerToken(t *testing.T) {
	for header, want := range map[string]string{
		"Bearer abc.def.ghi": "abc.def.ghi",
		"bearer abc":         "abc",
		"":                   "",
		"Bearer":             "",
		"Bearer ":            "",
		"Basic abc":          "",
		"Bearer a b":         "",
		"Bearer  abc":        "",
	} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		got, ok := BearerToken(r)
		if got != want || ok != (want != "") {
			t.Errorf("%q: got %q, %v", header, got, ok)
		}
	}
}
