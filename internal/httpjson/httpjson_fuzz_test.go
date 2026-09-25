package httpjson

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type sample struct {
	Name  string   `json:"name"`
	Count int      `json:"count"`
	Tags  []string `json:"tags"`
}

// Decode must never panic, must answer every rejection with a JSON error, and
// must accept only bodies that are exactly one object of known fields.
func FuzzDecode(f *testing.F) {
	for _, seed := range []string{`{"name":"a","count":1}`, `{}`, `null`, `[]`, `{"x":1}`, `{"name":"a"} {}`, `{"count":1e400}`, "\xff", ``} {
		f.Add([]byte(seed), "application/json")
	}
	f.Add([]byte(`{}`), "text/plain")
	f.Add([]byte(`{}`), "application/json; charset=utf-8")
	f.Fuzz(func(t *testing.T, body []byte, contentType string) {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", bytes.NewReader(body))
		r.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		got, ok := Decode[sample](w, r)
		if !ok {
			var e ErrorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil || e.Error.Code == "" || w.Code < 400 {
				t.Fatalf("rejection without a JSON error: %d %q", w.Code, w.Body)
			}
			return
		}
		// Accepted: a strict standard decoder must agree.
		var want sample
		strict := json.NewDecoder(bytes.NewReader(body))
		strict.DisallowUnknownFields()
		if err := strict.Decode(&want); err != nil {
			t.Fatalf("accepted a body a strict decoder rejects: %v", err)
		}
		if strict.More() {
			t.Fatal("accepted trailing data")
		}
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(want)
		if !bytes.Equal(a, b) {
			t.Fatalf("decoded %s, strict decoder %s", a, b)
		}
	})
}
