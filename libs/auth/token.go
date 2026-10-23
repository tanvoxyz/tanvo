package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
)

// secretBytes is the entropy of every opaque token (sessions, invites,
// password resets, OIDC state and nonces): 256 bits.
const secretBytes = 32

// NewSecret reads n random bytes from rand and returns them as unpadded
// URL-safe base64.
func NewSecret(rand io.Reader, n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand, b); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashSecret returns the SHA-256 of an opaque token, the form in which
// tokens are stored and looked up.
func HashSecret(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}
