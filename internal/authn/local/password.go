package local

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argonParams follow the OWASP Password Storage Cheat Sheet's argon2id
// minimum (19 MiB, 2 passes, 1 lane): memory-hard enough to slow offline
// guessing, cheap enough to run several at once on a small VM.
type argonParams struct {
	memoryKiB uint32
	passes    uint32
	lanes     uint8
}

var defaultParams = argonParams{memoryKiB: 19 * 1024, passes: 2, lanes: 1}

const (
	saltBytes = 16
	hashBytes = 32
)

// hasher bounds concurrent hashing: each hash holds memoryKiB of RAM, so a
// burst of logins must queue rather than exhaust the container's memory.
type hasher struct {
	params argonParams
	slots  chan struct{}
	// dummy is verified when an account does not exist or is locked, so those
	// answers take as long as a wrong password and do not reveal accounts.
	dummy string
}

func newHasher(params argonParams, concurrency int) (*hasher, error) {
	h := &hasher{params: params, slots: make(chan struct{}, concurrency)}
	dummy, err := h.hash(context.Background(), "dummy password never matches")
	if err != nil {
		return nil, err
	}
	h.dummy = dummy
	return h, nil
}

func (h *hasher) acquire(ctx context.Context) error {
	select {
	case h.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *hasher) release() { <-h.slots }

// hash returns a PHC string: $argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>.
func (h *hasher) hash(ctx context.Context, password string) (string, error) {
	salt := make([]byte, saltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	p := h.params
	sum := argon2.IDKey([]byte(password), salt, p.passes, p.memoryKiB, p.lanes, hashBytes)
	enc := base64.RawStdEncoding.EncodeToString
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.memoryKiB, p.passes, p.lanes, enc(salt), enc(sum)), nil
}

// verify reports whether password matches encoded. rehash is true when the
// stored hash used different parameters and should be replaced.
func (h *hasher) verify(ctx context.Context, password, encoded string) (ok, rehash bool, err error) {
	p, salt, want, err := parsePHC(encoded)
	if err != nil {
		return false, false, err
	}
	if err := h.acquire(ctx); err != nil {
		return false, false, err
	}
	defer h.release()
	got := argon2.IDKey([]byte(password), salt, p.passes, p.memoryKiB, p.lanes, uint32(len(want))) //nolint:gosec // G115: len(want) is bounded by parsePHC
	return subtle.ConstantTimeCompare(got, want) == 1, p != h.params, nil
}

func parsePHC(encoded string) (argonParams, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return argonParams{}, nil, nil, errors.New("unsupported password hash format")
	}
	var p argonParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memoryKiB, &p.passes, &p.lanes); err != nil {
		return argonParams{}, nil, nil, fmt.Errorf("password hash parameters: %w", err)
	}
	// Bounds keep a corrupted row from turning one login into a memory bomb.
	if p.memoryKiB < 8*1024 || p.memoryKiB > 256*1024 || p.passes < 1 || p.passes > 10 || p.lanes < 1 || p.lanes > 16 {
		return argonParams{}, nil, nil, errors.New("password hash parameters out of range")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return argonParams{}, nil, nil, errors.New("invalid password hash salt")
	}
	sum, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(sum) < 16 || len(sum) > 64 {
		return argonParams{}, nil, nil, errors.New("invalid password hash")
	}
	return p, salt, sum, nil
}
