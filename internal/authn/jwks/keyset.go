package jwks

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"sync"
	"time"
)

const (
	maxKeySetBytes = 1 << 20
	// refreshAfter bounds how long keys are trusted without re-reading the set,
	// so revoked keys stop working even when no unknown kid forces a refresh.
	refreshAfter = time.Hour
	// minRefetchInterval stops tokens with made-up kids from turning the
	// service into a request amplifier against the key server.
	minRefetchInterval = time.Minute
)

// errKeysUnavailable means no key set has ever been loaded, so the verifier
// cannot tell good tokens from bad ones.
var errKeysUnavailable = errors.New("jwks: signing keys unavailable")

type publicKey struct {
	key any    // *rsa.PublicKey, *ecdsa.PublicKey or ed25519.PublicKey
	alg string // optional "alg" pinned by the JWK
}

type keySet struct {
	url    string
	client *http.Client
	logger *slog.Logger
	now    func() time.Time

	// mu is held during fetches. Fetches are rare (at most once per
	// minRefetchInterval), and waiting requests need the result anyway.
	mu          sync.Mutex
	keys        map[string]publicKey
	fetchedAt   time.Time
	lastAttempt time.Time
}

// key returns the key for kid, refreshing the set when the kid is unknown or
// the set is stale.
func (s *keySet) key(ctx context.Context, kid string) (publicKey, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[kid]
	stale := s.now().Sub(s.fetchedAt) >= refreshAfter
	if (!ok || stale) && s.now().Sub(s.lastAttempt) >= minRefetchInterval {
		if err := s.refreshLocked(ctx); err != nil {
			// Keep serving the previous keys during a key-server outage.
			s.logger.WarnContext(ctx, "refreshing signing keys failed", "url", s.url, "error", err)
		}
		k, ok = s.keys[kid]
	}
	if s.keys == nil {
		return publicKey{}, false, errKeysUnavailable
	}
	return k, ok, nil
}

func (s *keySet) refresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshLocked(ctx)
}

func (s *keySet) refreshLocked(ctx context.Context) error {
	s.lastAttempt = s.now()
	keys, err := fetchKeys(ctx, s.client, s.url)
	if err != nil {
		return err
	}
	s.keys = keys
	s.fetchedAt = s.now()
	return nil
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func fetchKeys(ctx context.Context, client *http.Client, url string) (map[string]publicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("key server returned %s", resp.Status)
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxKeySetBytes)).Decode(&set); err != nil {
		return nil, fmt.Errorf("decoding key set: %w", err)
	}
	keys := make(map[string]publicKey, len(set.Keys))
	for _, k := range set.Keys {
		if k.Kid == "" || (k.Use != "" && k.Use != "sig") {
			continue
		}
		pub, err := k.publicKey()
		if err != nil {
			// One unusable key (say, a new algorithm) must not take the others down.
			continue
		}
		keys[k.Kid] = publicKey{key: pub, alg: k.Alg}
	}
	if len(keys) == 0 {
		return nil, errors.New("key set contains no usable signing keys")
	}
	return keys, nil
}

func (k jwk) publicKey() (any, error) {
	switch k.Kty {
	case "RSA":
		n, err := decode(k.N)
		if err != nil {
			return nil, err
		}
		e, err := decode(k.E)
		if err != nil {
			return nil, err
		}
		exponent := new(big.Int).SetBytes(e)
		if !exponent.IsInt64() || exponent.Int64() < 3 || exponent.Int64() > 1<<31-1 {
			return nil, errors.New("invalid RSA exponent")
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exponent.Int64())}
		if pub.N.BitLen() < 2048 {
			return nil, errors.New("RSA key shorter than 2048 bits")
		}
		return pub, nil
	case "EC":
		curves := map[string]elliptic.Curve{"P-256": elliptic.P256(), "P-384": elliptic.P384(), "P-521": elliptic.P521()}
		curve, ok := curves[k.Crv]
		if !ok {
			return nil, fmt.Errorf("unsupported curve %q", k.Crv)
		}
		x, err := decode(k.X)
		if err != nil {
			return nil, err
		}
		y, err := decode(k.Y)
		if err != nil {
			return nil, err
		}
		size := (curve.Params().BitSize + 7) / 8
		if len(x) != size || len(y) != size {
			return nil, errors.New("invalid EC coordinates")
		}
		// ParseUncompressedPublicKey also checks that the point is on the curve.
		return ecdsa.ParseUncompressedPublicKey(curve, append(append([]byte{4}, x...), y...))
	case "OKP":
		x, err := decode(k.X)
		if err != nil {
			return nil, err
		}
		if k.Crv != "Ed25519" || len(x) != ed25519.PublicKeySize {
			return nil, errors.New("unsupported OKP key")
		}
		return ed25519.PublicKey(x), nil
	}
	return nil, fmt.Errorf("unsupported key type %q", k.Kty)
}

func decode(s string) ([]byte, error) {
	if s == "" {
		return nil, errors.New("missing key parameter")
	}
	return base64.RawURLEncoding.DecodeString(s)
}
