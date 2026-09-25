package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// maxSecretBytes bounds a secret file; real secrets are far smaller.
const maxSecretBytes = 64 << 10

// Env reads settings from the process environment. Any setting X may instead
// be given as X_FILE, the path of a file holding the value: that is how Docker
// and Compose secrets are mounted, and it keeps secrets out of `docker inspect`
// and process listings.
//
// X_FILE is only consulted when X itself is requested, so settings that are
// paths by nature (AUTH_JWT_KEY_FILE) are never mistaken for indirections.
// Problems reading a file are collected and reported by Err, because Get is
// also handed to auth providers, whose signature has no error.
type Env struct {
	lookup func(string) (string, bool)
	mu     sync.Mutex
	errs   []error
}

// NewEnv reads the process environment.
func NewEnv() *Env { return &Env{lookup: os.LookupEnv} }

// Get returns setting name, from name or name_FILE, or "" when neither is set.
func (e *Env) Get(name string) string {
	value, _ := e.lookup(name)
	path, _ := e.lookup(name + "_FILE")
	switch {
	case path == "":
		return value
	case value != "":
		e.fail(fmt.Errorf("%s and %s_FILE are both set; use one", name, name))
		return ""
	}
	f, err := os.Open(path) //nolint:gosec // G304: the operator names the secret file
	if err != nil {
		e.fail(fmt.Errorf("%s_FILE: %w", name, err))
		return ""
	}
	defer func() { _ = f.Close() }()
	content, err := io.ReadAll(io.LimitReader(f, maxSecretBytes+1))
	if err != nil || len(content) > maxSecretBytes {
		e.fail(fmt.Errorf("%s_FILE: unreadable or larger than 64 KiB", name))
		return ""
	}
	// Editors and `echo` add a trailing newline that is never part of the secret.
	return strings.TrimRight(string(content), "\r\n")
}

// Err reports every problem met while reading settings so far.
func (e *Env) Err() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return errors.Join(e.errs...)
}

func (e *Env) fail(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.errs = append(e.errs, err)
}
