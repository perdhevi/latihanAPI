// Package httpjson holds the JSON request and response conventions shared by
// the API and by providers that serve their own endpoints.
package httpjson

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
)

const MaxBodyBytes = 1 << 20

// ErrorResponse is the body of every error: {"error":{"code":"...","message":"..."}}.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func Write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func Error(w http.ResponseWriter, status int, code, message string) {
	Write(w, status, ErrorResponse{Error: ErrorDetail{Code: code, Message: message}})
}

// Decode reads exactly one JSON object of type T, rejecting other media types,
// bodies over MaxBodyBytes, unknown fields and trailing values. On failure it
// has already written the error response.
func Decode[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var zero T
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		Error(w, http.StatusUnsupportedMediaType, "invalid_request", "Content-Type must be application/json")
		return zero, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input *T
	err = decoder.Decode(&input)
	if err == nil {
		var extra any
		if e := decoder.Decode(&extra); e != io.EOF {
			err = e
			if err == nil {
				err = errors.New("multiple JSON values")
			}
		}
	}
	if err != nil || input == nil {
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			Error(w, http.StatusRequestEntityTooLarge, "invalid_request", "request body exceeds 1 MiB")
		} else {
			Error(w, http.StatusBadRequest, "invalid_request", "body must be one JSON object with valid writable fields")
		}
		return zero, false
	}
	return *input, true
}
