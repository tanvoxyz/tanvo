// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package license

import (
	"crypto/ed25519"
	"fmt"
	"maps"
	"time"
)

// Verifier checks license keys against a set of trusted public keys. It is
// immutable and safe for concurrent use.
type Verifier struct {
	keys map[string]ed25519.PublicKey
	now  func() time.Time
}

// NewVerifier returns a verifier that trusts keys, by key ID, and reads the
// time from now (time.Now when nil). The map is copied. Production services
// pass [TrustedKeys]().
func NewVerifier(keys map[string]ed25519.PublicKey, now func() time.Time) *Verifier {
	if now == nil {
		now = time.Now
	}
	return &Verifier{keys: maps.Clone(keys), now: now}
}

// Now returns the verifier's current time in UTC.
func (v *Verifier) Now() time.Time {
	return v.now().UTC()
}

// Verify decodes key, checks its signature against the trusted key it
// names and checks that it is in force now. Surrounding white space is
// ignored.
//
// It returns [ErrMalformed], [ErrUnsupportedVersion], [ErrUnknownKey] or
// [ErrSignature] with a zero License when the key cannot be trusted. When
// the signature verifies but the license is out of its validity period, it
// returns the license together with [ErrExpired] or [ErrNotYetValid].
func (v *Verifier) Verify(key string) (License, error) {
	l, body, sig, err := decode(key)
	if err != nil {
		return License{}, err
	}
	pub, ok := v.keys[l.KeyID]
	if !ok {
		return License{}, fmt.Errorf("%w: %q", ErrUnknownKey, l.KeyID)
	}
	if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, signedMessage(body), sig) {
		return License{}, fmt.Errorf("%w: key %q", ErrSignature, l.KeyID)
	}
	now := v.Now()
	switch {
	case now.Before(l.IssuedAt):
		return l, fmt.Errorf("%w: license %s starts at %s", ErrNotYetValid, l.ID, l.IssuedAt.Format(time.RFC3339))
	case !now.Before(l.ExpiresAt):
		return l, fmt.Errorf("%w: license %s expired at %s", ErrExpired, l.ID, l.ExpiresAt.Format(time.RFC3339))
	}
	return l, nil
}
