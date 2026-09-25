package local

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// keyRing holds the key that signs new tokens and every key whose tokens are
// still accepted. Rotation: generate a new key, make it AUTH_JWT_KEY_FILE, move
// the old file to AUTH_JWT_PREVIOUS_KEY_FILES, restart, and remove the old key
// once the access-token lifetime has passed.
type keyRing struct {
	signer   ed25519.PrivateKey
	signerID string
	public   map[string]crypto.PublicKey
}

func loadKeyRing(activePath string, previousPaths []string) (*keyRing, error) {
	active, err := readPrivateKey(activePath)
	if err != nil {
		return nil, err
	}
	pub := active.Public().(ed25519.PublicKey)
	ring := &keyRing{signer: active, signerID: keyID(pub), public: map[string]crypto.PublicKey{}}
	ring.public[ring.signerID] = pub
	for _, path := range previousPaths {
		if path = strings.TrimSpace(path); path == "" {
			continue
		}
		old, err := readPrivateKey(path)
		if err != nil {
			return nil, err
		}
		oldPub := old.Public().(ed25519.PublicKey)
		ring.public[keyID(oldPub)] = oldPub
	}
	return ring, nil
}

func readPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the operator chooses the key file
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("signing key %s does not exist; create one with: api keygen -out %s", path, path)
	}
	if err != nil {
		return nil, fmt.Errorf("reading signing key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("signing key %s must be a PEM \"PRIVATE KEY\" (PKCS #8)", path)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing signing key %s: %w", path, err)
	}
	ed, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("signing key %s must be Ed25519, got %T", path, key)
	}
	return ed, nil
}

// keyID is the RFC 7638 JWK thumbprint: derived from the key itself, so it
// needs no configuration and cannot collide between different keys.
func keyID(pub ed25519.PublicKey) string {
	canonical := `{"crv":"Ed25519","kty":"OKP","x":"` + base64.RawURLEncoding.EncodeToString(pub) + `"}`
	sum := sha256.Sum256([]byte(canonical))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// jwks is the public half of the ring in JSON Web Key Set form.
func (k *keyRing) jwks() map[string]any {
	keys := make([]map[string]string, 0, len(k.public))
	for kid, pub := range k.public {
		keys = append(keys, map[string]string{
			"kty": "OKP", "crv": "Ed25519", "use": "sig", "alg": "EdDSA", "kid": kid,
			"x": base64.RawURLEncoding.EncodeToString(pub.(ed25519.PublicKey)),
		})
	}
	return map[string]any{"keys": keys}
}

// GenerateKeyFile writes a new Ed25519 key to path, readable only by its
// owner. It never overwrites: created is false when path already exists.
func GenerateKeyFile(path string) (created bool, err error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return false, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return false, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: the operator chooses the key file
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
		_ = f.Close()
		return false, err
	}
	return true, f.Close()
}
