// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

// Package servicetoken verifies the service tokens the core issues to the
// callers of the Data API (tanvo.access.v1.ServiceTokenClaims): offline,
// with the core's Ed25519 public keys.
package servicetoken

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	accessv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/access/v1"
)

// Audience is the audience of the Data API's tokens.
const Audience = "data_api"

// Version is the claims version this package reads.
const Version = 1

// signingPrefix precedes the claims in the signed bytes.
const signingPrefix = "tanvo.service_token.v1."

// Errors of [Verifier.Verify].
var (
	// ErrMalformed reports a token that is not one.
	ErrMalformed = errors.New("servicetoken: malformed token")
	// ErrUnknownKey reports a token signed by a key not configured.
	ErrUnknownKey = errors.New("servicetoken: unknown signing key")
	// ErrSignature reports a token whose signature does not verify.
	ErrSignature = errors.New("servicetoken: invalid signature")
	// ErrExpired reports a token past its expiry, or issued in the future.
	ErrExpired = errors.New("servicetoken: token expired")
	// ErrAudience reports a token for another service or claims version.
	ErrAudience = errors.New("servicetoken: token not for this service")
)

// Claims are the verified claims of a token.
type Claims struct {
	// OrganizationID is the organization the caller acts in.
	OrganizationID string
	// Subject is the caller: "user:<id>" or "api_key:<id>".
	Subject string
	// Permissions are the permissions the caller holds.
	Permissions []string
	// ExpiresAt is the end of the token's validity.
	ExpiresAt time.Time
}

// Has reports whether c grants permission.
func (c Claims) Has(permission string) bool {
	return slices.Contains(c.Permissions, permission)
}

// Keys maps key IDs to the core's public keys.
type Keys map[string]ed25519.PublicKey

// ParseKeys parses a comma-separated list of "<key_id>:<public key>"
// entries, the public keys in unpadded base64url, as
// `tanvo admin service-token-key public` prints them.
func ParseKeys(s string) (Keys, error) {
	keys := Keys{}
	for entry := range strings.SplitSeq(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		id, enc, ok := strings.Cut(entry, ":")
		if !ok || id == "" {
			return nil, fmt.Errorf("servicetoken: key %q: want <key_id>:<public key>", entry)
		}
		raw, err := base64.RawURLEncoding.DecodeString(enc)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("servicetoken: key %q: want an unpadded base64url Ed25519 public key", id)
		}
		if _, dup := keys[id]; dup {
			return nil, fmt.Errorf("servicetoken: key %q listed twice", id)
		}
		keys[id] = ed25519.PublicKey(raw)
	}
	if len(keys) == 0 {
		return nil, errors.New("servicetoken: no keys")
	}
	return keys, nil
}

// Verifier verifies tokens.
type Verifier struct {
	keys   Keys
	now    func() time.Time
	leeway time.Duration
}

// NewVerifier returns a verifier trusting keys; now is the clock. Times
// are compared with a leeway of a minute for clock skew.
func NewVerifier(keys Keys, now func() time.Time) *Verifier {
	return &Verifier{keys: keys, now: now, leeway: time.Minute}
}

// Verify checks token and returns its claims.
func (v *Verifier) Verify(token string) (Claims, error) {
	enc, sigEnc, ok := strings.Cut(token, ".")
	if !ok || enc == "" || sigEnc == "" {
		return Claims{}, ErrMalformed
	}
	payload, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return Claims{}, ErrMalformed
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigEnc)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return Claims{}, ErrMalformed
	}
	var c accessv1.ServiceTokenClaims
	if err := proto.Unmarshal(payload, &c); err != nil {
		return Claims{}, ErrMalformed
	}
	key, ok := v.keys[c.GetKeyId()]
	if !ok {
		return Claims{}, ErrUnknownKey
	}
	if !ed25519.Verify(key, signed(payload), sig) {
		return Claims{}, ErrSignature
	}
	if c.GetVersion() != Version || c.GetAudience() != Audience {
		return Claims{}, ErrAudience
	}
	now := v.now()
	exp, iat := c.GetExpiresAt().AsTime(), c.GetIssuedAt().AsTime()
	if !c.HasExpiresAt() || !now.Before(exp.Add(v.leeway)) || now.Add(v.leeway).Before(iat) {
		return Claims{}, ErrExpired
	}
	if c.GetOrganizationId() == "" || c.GetSubject() == "" {
		return Claims{}, ErrMalformed
	}
	return Claims{
		OrganizationID: c.GetOrganizationId(),
		Subject:        c.GetSubject(),
		Permissions:    c.GetPermissions(),
		ExpiresAt:      exp,
	}, nil
}

func signed(payload []byte) []byte {
	return bytes.Join([][]byte{[]byte(signingPrefix), payload}, nil)
}

// Sign returns the token of claims signed with priv under keyID, for
// tests and tools; the core issues the real ones.
func Sign(priv ed25519.PrivateKey, keyID string, c Claims, issuedAt time.Time) (string, error) {
	var m accessv1.ServiceTokenClaims
	m.SetVersion(Version)
	m.SetKeyId(keyID)
	m.SetAudience(Audience)
	m.SetOrganizationId(c.OrganizationID)
	m.SetSubject(c.Subject)
	m.SetPermissions(c.Permissions)
	m.SetIssuedAt(timestamppb.New(issuedAt))
	m.SetExpiresAt(timestamppb.New(c.ExpiresAt))
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(&m)
	if err != nil {
		return "", fmt.Errorf("servicetoken: sign: %w", err)
	}
	sig := ed25519.Sign(priv, signed(payload))
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
