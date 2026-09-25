package ratelimit

import (
	"errors"
	"testing"
	"time"
)

func TestParsePolicy(t *testing.T) {
	for in, want := range map[string]Policy{
		"50/s:100": {Rate: 50, Burst: 100},
		"10/m:10":  {Rate: 10.0 / 60, Burst: 10},
		"1/h:1":    {Rate: 1.0 / 3600, Burst: 1},
		"off":      Off,
	} {
		got, err := ParsePolicy(in)
		if err != nil || got != want {
			t.Errorf("%s: got %+v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "50", "50/s", "50/d:1", "0/s:1", "-1/s:1", "x/s:1", "5/s:0", "5/s:x", "Inf/s:1"} {
		if _, err := ParsePolicy(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestBurstRefillAndIsolation(t *testing.T) {
	now := time.Now()
	l := New(MustParse("1/s:3"))
	l.now = func() time.Time { return now }
	for i := range 3 {
		if ok, _, _ := l.Allow("a"); !ok {
			t.Fatalf("request %d within burst refused", i)
		}
	}
	ok, retry, err := l.Allow("a")
	if ok || err != nil || retry <= 0 || retry > time.Second {
		t.Fatalf("over burst: ok=%v retry=%v err=%v", ok, retry, err)
	}
	if ok, _, _ := l.Allow("b"); !ok {
		t.Fatal("one key's bucket limited another")
	}
	now = now.Add(time.Second)
	if ok, _, _ := l.Allow("a"); !ok {
		t.Fatal("token not refilled after a second")
	}
	if ok, _, _ := l.Allow("a"); ok {
		t.Fatal("refused request consumed no token: refill too generous")
	}
}

func TestEvictionNeverResetsABusyBucket(t *testing.T) {
	now := time.Now()
	l := New(MustParse("1/m:5")) // five minutes to refill completely
	l.now = func() time.Time { return now }
	for range 5 {
		l.Allow("a")
	}
	now = now.Add(2 * time.Minute) // a sweep runs, but the bucket is not full yet
	l.Allow("other")
	if _, kept := l.buckets["a"]; !kept {
		t.Fatal("bucket evicted before it refilled; its client would get a fresh burst")
	}
	now = now.Add(5 * time.Minute)
	l.Allow("other")
	if _, kept := l.buckets["a"]; kept {
		t.Fatal("idle, refilled bucket was not evicted")
	}
}

func TestKeyTableIsBounded(t *testing.T) {
	l := New(MustParse("1/s:1"))
	l.maxKeys = 2
	l.Allow("a")
	l.Allow("b")
	if ok, _, err := l.Allow("c"); ok || !errors.Is(err, ErrTooManyKeys) {
		t.Fatalf("third key: ok=%v err=%v", ok, err)
	}
	if _, _, err := l.Allow("a"); err != nil {
		t.Fatal("known keys must keep working when the table is full")
	}
}

func TestOff(t *testing.T) {
	l := New(Off)
	for range 1000 {
		if ok, _, _ := l.Allow("a"); !ok {
			t.Fatal("disabled limiter refused")
		}
	}
}
