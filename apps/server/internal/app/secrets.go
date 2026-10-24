package app

import (
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
)

// Purposes of the keys derived from the installation secret
// (TANVO_SECRET_KEY): each use gets its own key, so that a key leaked
// from one use opens nothing else.
const (
	purposePortalTokens = "tanvo/portal-tokens/v1"
	purposeSecretBox    = "tanvo/secret-box/v1"
)

// deriveKey returns the 32-byte key for purpose derived from secret with
// HKDF-SHA256.
func deriveKey(secret []byte, purpose string) ([]byte, error) {
	k, err := hkdf.Key(sha256.New, secret, nil, purpose, 32)
	if err != nil {
		return nil, fmt.Errorf("derive %s key: %w", purpose, err)
	}
	return k, nil
}

// NewSecretBox returns the secret box of the installation secret, which
// seals the credentials of integrations and x402 connections.
func NewSecretBox(secret []byte) (*auth.AESSecretBox, error) {
	k, err := deriveKey(secret, purposeSecretBox)
	if err != nil {
		return nil, err
	}
	box, err := auth.NewAESSecretBox(k, rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("secret box: %w", err)
	}
	return box, nil
}

// portalKeyID names the portal token key; a rotation of the installation
// secret would add the next one.
const portalKeyID = "k1"

// NewPortalTokens returns the customer portal tokens of the installation
// secret, whose links open appURL "/customer-portal/<token>" (lago
// CustomerPortal::GenerateUrlService, valid for 12 hours).
func NewPortalTokens(secret []byte, appURL string, clock core.Clock) (*auth.PortalTokens, error) {
	k, err := deriveKey(secret, purposePortalTokens)
	if err != nil {
		return nil, err
	}
	t, err := auth.NewPortalTokens(auth.PortalConfig{Keys: map[string][]byte{portalKeyID: k}, CurrentKeyID: portalKeyID, FrontURL: appURL}, clock)
	if err != nil {
		return nil, fmt.Errorf("portal tokens: %w", err)
	}
	return t, nil
}
