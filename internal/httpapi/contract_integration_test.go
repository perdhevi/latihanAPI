//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
)

var (
	specOnce   sync.Once
	specRouter routers.Router
	specErr    error
)

// loadSpec parses api/openapi.yaml once, pointed at the host httptest uses.
func loadSpec() (routers.Router, error) {
	specOnce.Do(func() {
		_, self, _, _ := runtime.Caller(0)
		doc, err := openapi3.NewLoader().LoadFromFile(filepath.Join(filepath.Dir(self), "..", "..", "api", "openapi.yaml"))
		if err != nil {
			specErr = err
			return
		}
		if err := doc.Validate(context.Background()); err != nil {
			specErr = err
			return
		}
		doc.Servers = openapi3.Servers{{URL: "http://example.com"}}
		specRouter, specErr = legacy.NewRouter(doc)
	})
	return specRouter, specErr
}

// contract checks every response h gives against the OpenAPI document: the
// status code must be documented for that operation and the body must match
// its schema, including additionalProperties: false. Routes the document does
// not describe (such as the catch-all 404) are not checked.
func contract(t *testing.T, h http.Handler) http.Handler {
	t.Helper()
	router, err := loadSpec()
	if err != nil {
		t.Fatalf("api/openapi.yaml: %v", err)
	}
	// A contract check that silently matches nothing proves nothing.
	var mu sync.Mutex
	checked := 0
	t.Cleanup(func() {
		if checked == 0 {
			t.Error("contract: no response was matched to an operation in api/openapi.yaml")
		}
		t.Logf("contract: %d responses validated against api/openapi.yaml", checked)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)

		lookup := r.Clone(r.Context())
		lookup.Body = io.NopCloser(bytes.NewReader(body))
		// Server requests carry only a path; route matching needs the full URL.
		lookup.URL.Scheme, lookup.URL.Host = "http", r.Host
		if route, params, err := router.FindRoute(lookup); err == nil {
			input := &openapi3filter.ResponseValidationInput{
				RequestValidationInput: &openapi3filter.RequestValidationInput{Request: lookup, PathParams: params, Route: route},
				Status:                 rec.Code,
				Header:                 rec.Header(),
				Options:                &openapi3filter.Options{IncludeResponseStatus: true, MultiError: true},
			}
			mu.Lock()
			checked++
			mu.Unlock()
			input.SetBodyBytes(rec.Body.Bytes())
			if err := openapi3filter.ValidateResponse(r.Context(), input); err != nil {
				t.Errorf("contract: %s %s -> %d does not match api/openapi.yaml: %v\nbody: %s", r.Method, r.URL.Path, rec.Code, err, rec.Body.String())
			}
		}

		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
}
