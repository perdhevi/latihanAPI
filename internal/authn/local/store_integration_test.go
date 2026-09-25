//go:build integration

package local

import (
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/perdhevi/latihanAPI/internal/testdb"
)

func TestSQLStore(t *testing.T) {
	db := stdlib.OpenDBFromPool(testdb.Open(t))
	t.Cleanup(func() { _ = db.Close() })
	s := &sqlStore{db: db}
	ctx := t.Context()
	now := time.Now()

	id := uuid.New()
	hash := "$argon2id$v=19$m=8192,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA"
	if err := s.createCredential(ctx, id, "alex@example.com", hash); err != nil {
		t.Fatal(err)
	}
	if err := s.createCredential(ctx, uuid.New(), "alex@example.com", hash); !errors.Is(err, errEmailTaken) {
		t.Fatalf("duplicate email: %v", err)
	}
	if _, err := s.credentialByEmail(ctx, "nobody@example.com"); !errors.Is(err, errNoCredential) {
		t.Fatalf("unknown email: %v", err)
	}

	// Lockout: the fifth failure locks and resets the count; success clears the lock.
	lockUntil := now.Add(time.Hour).Truncate(time.Microsecond)
	for range 4 {
		if err := s.recordFailure(ctx, id, 5, lockUntil); err != nil {
			t.Fatal(err)
		}
	}
	if c, _ := s.credentialByEmail(ctx, "alex@example.com"); c.lockedUntil != nil {
		t.Fatal("locked too early")
	}
	_ = s.recordFailure(ctx, id, 5, lockUntil)
	if c, _ := s.credentialByEmail(ctx, "alex@example.com"); c.lockedUntil == nil || !c.lockedUntil.Equal(lockUntil) {
		t.Fatalf("not locked after five failures: %v", c.lockedUntil)
	}
	newHash := "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA"
	if err := s.recordSuccess(ctx, id, newHash); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.credentialByEmail(ctx, "alex@example.com"); c.lockedUntil != nil || c.passwordHash != newHash {
		t.Fatalf("success did not clear the lock or store the rehash: %+v", c)
	}

	token := func(plain string, expires time.Time) refreshToken {
		sum := sha256.Sum256([]byte(plain))
		return refreshToken{id: uuid.New(), credentialID: id, familyID: uuid.New(), hash: sum[:], expiresAt: expires}
	}
	first := token("first", now.Add(time.Hour))
	if err := s.createRefreshToken(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := token("second", now.Add(time.Hour))
	if got, err := s.rotateRefreshToken(ctx, first.hash, now, second); err != nil || got != id {
		t.Fatalf("rotate: %v %v", got, err)
	}
	if _, err := s.rotateRefreshToken(ctx, first.hash, now, token("third", now.Add(time.Hour))); !errors.Is(err, errRefreshReuse) {
		t.Fatalf("reuse not detected: %v", err)
	}
	if _, err := s.rotateRefreshToken(ctx, second.hash, now, token("fourth", now.Add(time.Hour))); !errors.Is(err, errInvalidRefresh) {
		t.Fatalf("family not revoked on reuse: %v", err)
	}

	// Two concurrent uses of one token: exactly one wins, the other is reuse.
	racer := token("racer", now.Add(time.Hour))
	_ = s.createRefreshToken(ctx, racer)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := range 2 {
		wg.Go(func() {
			_, err := s.rotateRefreshToken(ctx, racer.hash, now, token("racer-next-"+string(rune('a'+i)), now.Add(time.Hour)))
			results <- err
		})
	}
	wg.Wait()
	close(results)
	var ok, reuse int
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, errRefreshReuse):
			reuse++
		default:
			t.Fatalf("concurrent rotation: %v", err)
		}
	}
	if ok != 1 || reuse != 1 {
		t.Fatalf("concurrent rotation: %d succeeded, %d reuse", ok, reuse)
	}

	expired := token("expired", now.Add(-time.Minute))
	_ = s.createRefreshToken(ctx, expired)
	if _, err := s.rotateRefreshToken(ctx, expired.hash, now, token("x", now.Add(time.Hour))); !errors.Is(err, errInvalidRefresh) {
		t.Fatalf("expired token accepted: %v", err)
	}
	live := token("live", now.Add(time.Hour))
	_ = s.createRefreshToken(ctx, live)
	if err := s.revokeFamily(ctx, live.hash, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.rotateRefreshToken(ctx, live.hash, now, token("y", now.Add(time.Hour))); !errors.Is(err, errInvalidRefresh) {
		t.Fatalf("revoked token accepted: %v", err)
	}
}
