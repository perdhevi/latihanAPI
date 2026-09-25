package ratelimit

import "testing"

// Every accepted policy either is Off or limits with a positive rate and a
// bounded burst.
func FuzzParsePolicy(f *testing.F) {
	for _, seed := range []string{"50/s:100", "10/m:10", "off", "", "1e308/s:1", "NaN/s:1", "0.0000001/h:1", "5/s:-1"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		p, err := ParsePolicy(s)
		if err != nil || p == Off {
			return
		}
		if !(p.Rate > 0) || p.Burst < 1 || p.Burst > 1_000_000 {
			t.Fatalf("%q parsed to %+v", s, p)
		}
	})
}
