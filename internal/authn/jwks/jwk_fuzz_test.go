package jwks

import (
	"crypto/rsa"
	"encoding/json"
	"testing"
)

// Key sets come from the network: any JSON must parse or fail without
// panicking, and no accepted RSA key may be weaker than 2048 bits.
func FuzzJWK(f *testing.F) {
	for _, seed := range []string{
		`{"kty":"OKP","crv":"Ed25519","x":"11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo"}`,
		`{"kty":"RSA","n":"AQAB","e":"AQAB"}`,
		`{"kty":"EC","crv":"P-256","x":"","y":""}`,
		`{"kty":"RSA","n":"","e":"AQ"}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		var k jwk
		if json.Unmarshal(raw, &k) != nil {
			return
		}
		pub, err := k.publicKey()
		if err != nil {
			return
		}
		if r, ok := pub.(*rsa.PublicKey); ok && (r.N.BitLen() < 2048 || r.E < 3) {
			t.Fatalf("accepted a weak RSA key: %d bits, e=%d", r.N.BitLen(), r.E)
		}
	})
}
