// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package license

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	// Prefix starts every license key; the 1 is the format version.
	Prefix = "tanvo1."
	// SigningContext is prepended to the payload bytes before signing, so
	// that a license signature cannot be confused with any other Ed25519
	// signature made with the same key.
	SigningContext = "tanvo.license.v1."
	// payloadVersion is the version field of the payload.
	payloadVersion = 1
	// maxKeyLen bounds the input, so that decoding a hostile value stays
	// cheap.
	maxKeyLen = 8 << 10
)

var (
	// b64 is unpadded base64url that rejects non-canonical encodings, so
	// that every license has exactly one key string.
	b64 = base64.RawURLEncoding.Strict()
	// namePattern constrains key IDs and service names.
	namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	// versionPattern matches the prefix of a key of another format version.
	versionPattern = regexp.MustCompile(`^tanvo[0-9]+\.`)
)

// payload is the signed JSON of a key.
type payload struct {
	Version   int      `json:"version"`
	KeyID     string   `json:"key_id"`
	LicenseID string   `json:"license_id"`
	Licensee  string   `json:"licensee"`
	Services  []string `json:"services"`
	IssuedAt  string   `json:"issued_at"`
	ExpiresAt string   `json:"expires_at"`
}

// Sign issues a license key for l, signed with priv under l.KeyID. The times
// are stored in UTC to the second. It returns [ErrMalformed] when the
// license is incomplete: a key ID or service name that is not a lowercase
// identifier, an empty ID or licensee, no services, or an expiry not after
// the issue time.
func Sign(priv ed25519.PrivateKey, l License) (string, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("%w: private key has %d bytes, want %d", ErrMalformed, len(priv), ed25519.PrivateKeySize)
	}
	l.IssuedAt = l.IssuedAt.UTC().Truncate(time.Second)
	l.ExpiresAt = l.ExpiresAt.UTC().Truncate(time.Second)
	if err := validate(l); err != nil {
		return "", err
	}
	p := payload{
		Version:   payloadVersion,
		KeyID:     l.KeyID,
		LicenseID: l.ID,
		Licensee:  l.Licensee,
		Services:  l.Services,
		IssuedAt:  l.IssuedAt.Format(time.RFC3339),
		ExpiresAt: l.ExpiresAt.Format(time.RFC3339),
	}
	body, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("license: encode payload: %w", err)
	}
	sig := ed25519.Sign(priv, signedMessage(body))
	return Prefix + b64.EncodeToString(body) + "." + b64.EncodeToString(sig), nil
}

// ParseUnverified decodes key without checking its signature or validity
// period. It is for diagnostics, such as showing what a rejected key holds;
// never grant anything on its result, use [Verifier.Verify].
func ParseUnverified(key string) (License, error) {
	l, _, _, err := decode(key)
	return l, err
}

// decode splits key and decodes its payload, returning the license, the
// payload bytes and the signature.
func decode(key string) (License, []byte, []byte, error) {
	key = strings.TrimSpace(key)
	if len(key) > maxKeyLen {
		return License{}, nil, nil, fmt.Errorf("%w: key longer than %d bytes", ErrMalformed, maxKeyLen)
	}
	rest, ok := strings.CutPrefix(key, Prefix)
	if !ok {
		if v := versionPattern.FindString(key); v != "" {
			return License{}, nil, nil, fmt.Errorf("%w: key prefix %q", ErrUnsupportedVersion, v)
		}
		return License{}, nil, nil, fmt.Errorf("%w: missing %q prefix", ErrMalformed, Prefix)
	}
	encBody, encSig, ok := strings.Cut(rest, ".")
	if !ok || strings.Contains(encSig, ".") {
		return License{}, nil, nil, fmt.Errorf("%w: want two dot-separated parts after the prefix", ErrMalformed)
	}
	body, err := b64.DecodeString(encBody)
	if err != nil {
		return License{}, nil, nil, fmt.Errorf("%w: payload encoding: %w", ErrMalformed, err)
	}
	sig, err := b64.DecodeString(encSig)
	if err != nil {
		return License{}, nil, nil, fmt.Errorf("%w: signature encoding: %w", ErrMalformed, err)
	}
	if len(sig) != ed25519.SignatureSize {
		return License{}, nil, nil, fmt.Errorf("%w: signature has %d bytes, want %d", ErrMalformed, len(sig), ed25519.SignatureSize)
	}

	var p payload
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return License{}, nil, nil, fmt.Errorf("%w: payload: %w", ErrMalformed, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return License{}, nil, nil, fmt.Errorf("%w: payload: trailing data", ErrMalformed)
	}
	if p.Version != payloadVersion {
		return License{}, nil, nil, fmt.Errorf("%w: payload version %d", ErrUnsupportedVersion, p.Version)
	}
	issued, err := time.Parse(time.RFC3339, p.IssuedAt)
	if err != nil {
		return License{}, nil, nil, fmt.Errorf("%w: issued_at: %w", ErrMalformed, err)
	}
	expires, err := time.Parse(time.RFC3339, p.ExpiresAt)
	if err != nil {
		return License{}, nil, nil, fmt.Errorf("%w: expires_at: %w", ErrMalformed, err)
	}
	l := License{
		KeyID:     p.KeyID,
		ID:        p.LicenseID,
		Licensee:  p.Licensee,
		Services:  p.Services,
		IssuedAt:  issued.UTC(),
		ExpiresAt: expires.UTC(),
	}
	if err := validate(l); err != nil {
		return License{}, nil, nil, err
	}
	return l, body, sig, nil
}

// validate checks the fields both [Sign] and decode require.
func validate(l License) error {
	switch {
	case !namePattern.MatchString(l.KeyID):
		return fmt.Errorf("%w: key ID %q is not a lowercase identifier", ErrMalformed, l.KeyID)
	case strings.TrimSpace(l.ID) == "":
		return fmt.Errorf("%w: empty license ID", ErrMalformed)
	case strings.TrimSpace(l.Licensee) == "":
		return fmt.Errorf("%w: empty licensee", ErrMalformed)
	case len(l.Services) == 0:
		return fmt.Errorf("%w: no services", ErrMalformed)
	case !l.ExpiresAt.After(l.IssuedAt):
		return fmt.Errorf("%w: expires_at %s is not after issued_at %s", ErrMalformed,
			l.ExpiresAt.Format(time.RFC3339), l.IssuedAt.Format(time.RFC3339))
	}
	for i, s := range l.Services {
		if s != AllServices && !namePattern.MatchString(s) {
			return fmt.Errorf("%w: service %q is not a lowercase identifier", ErrMalformed, s)
		}
		if slices.Contains(l.Services[:i], s) {
			return fmt.Errorf("%w: service %q listed twice", ErrMalformed, s)
		}
	}
	return nil
}

// signedMessage is the message the signature covers.
func signedMessage(body []byte) []byte {
	return append([]byte(SigningContext), body...)
}

// FormatPublicKey renders a public key as "<key_id>:<base64url key>", the
// form [ParsePublicKey] reads and the tanvo-license command prints.
func FormatPublicKey(keyID string, pub ed25519.PublicKey) string {
	return keyID + ":" + b64.EncodeToString(pub)
}

// ParsePublicKey reads a public key in the "<key_id>:<base64url key>" form
// of [FormatPublicKey].
func ParsePublicKey(s string) (string, ed25519.PublicKey, error) {
	keyID, enc, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok || !namePattern.MatchString(keyID) {
		return "", nil, fmt.Errorf("%w: public key %q: want <key_id>:<base64url key>", ErrMalformed, s)
	}
	raw, err := b64.DecodeString(enc)
	if err != nil {
		return "", nil, fmt.Errorf("%w: public key %s: %w", ErrMalformed, keyID, err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return "", nil, fmt.Errorf("%w: public key %s has %d bytes, want %d", ErrMalformed, keyID, len(raw), ed25519.PublicKeySize)
	}
	return keyID, ed25519.PublicKey(raw), nil
}
