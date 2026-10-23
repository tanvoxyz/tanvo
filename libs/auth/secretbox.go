package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// SecretBox encrypts the secrets kept at rest, such as the credentials of
// integrations (integration.Integration.Secrets, architecture §15). A
// sealed value is bound to its organization: it opens only for the
// organization it was sealed for.
type SecretBox interface {
	// Seal encrypts plaintext for org.
	Seal(ctx context.Context, org core.OrganizationID, plaintext []byte) ([]byte, error)
	// Open decrypts a value sealed for org; a value sealed for another
	// organization, with another key or altered fails with
	// [ErrSecretUndecryptable].
	Open(ctx context.Context, org core.OrganizationID, sealed []byte) ([]byte, error)
}

// ErrSecretUndecryptable reports a sealed secret that cannot be opened.
var ErrSecretUndecryptable = errors.New("auth: secret cannot be decrypted")

// sealedVersion is the first byte of a value sealed by [AESSecretBox].
const sealedVersion = 1

// AESSecretBox is the [SecretBox] over AES-256-GCM: a sealed value is
// the version byte, a random 12-byte nonce and the ciphertext with its
// tag; the organization ID is the additional data.
type AESSecretBox struct {
	aead cipher.AEAD
	rand io.Reader
}

var _ SecretBox = (*AESSecretBox)(nil)

// NewAESSecretBox returns the box sealing with the 32-byte key, drawing
// nonces from r (crypto/rand.Reader when nil).
func NewAESSecretBox(key []byte, r io.Reader) (*AESSecretBox, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("auth: secret box key: got %d bytes, want 32", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("auth: secret box key: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("auth: secret box: %w", err)
	}
	if r == nil {
		r = rand.Reader
	}
	return &AESSecretBox{aead: aead, rand: r}, nil
}

// Seal implements [SecretBox].
func (b *AESSecretBox) Seal(_ context.Context, org core.OrganizationID, plaintext []byte) ([]byte, error) {
	out := make([]byte, 1+b.aead.NonceSize(), 1+b.aead.NonceSize()+len(plaintext)+b.aead.Overhead())
	out[0] = sealedVersion
	if _, err := io.ReadFull(b.rand, out[1:]); err != nil {
		return nil, fmt.Errorf("auth: seal secret: %w", err)
	}
	return b.aead.Seal(out, out[1:], plaintext, []byte(org.String())), nil
}

// Open implements [SecretBox].
func (b *AESSecretBox) Open(_ context.Context, org core.OrganizationID, sealed []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < 1+n+b.aead.Overhead() || sealed[0] != sealedVersion {
		return nil, ErrSecretUndecryptable
	}
	out, err := b.aead.Open(nil, sealed[1:1+n], sealed[1+n:], []byte(org.String()))
	if err != nil {
		return nil, ErrSecretUndecryptable
	}
	return out, nil
}
