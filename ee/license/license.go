// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package license

import (
	"errors"
	"slices"
	"time"
)

// EnvKey is the environment variable from which an enterprise service reads
// its license key.
const EnvKey = "TANVO_LICENSE_KEY"

// The services a license enables. A license names the services it enables,
// or [AllServices] for every service.
const (
	ServiceAnalytics = "analytics"
	ServiceMCP       = "mcp"
	ServiceAssistant = "assistant"
	ServicePipeline  = "pipeline"

	// AllServices, in a license's services, enables every service, including
	// services added after the key was signed.
	AllServices = "*"
)

// Errors returned by [Verifier.Verify], [ParseUnverified], [Sign] and the
// [Checker]. They are wrapped with details; inspect them with [errors.Is].
var (
	// ErrMalformed reports a key that is not a well-formed license key, or
	// a license that cannot be signed.
	ErrMalformed = errors.New("license: malformed key")
	// ErrUnsupportedVersion reports a key of a format version this package
	// does not know.
	ErrUnsupportedVersion = errors.New("license: unsupported key version")
	// ErrUnknownKey reports a key signed by a key ID that is not trusted.
	ErrUnknownKey = errors.New("license: unknown signing key")
	// ErrSignature reports a key whose signature does not verify.
	ErrSignature = errors.New("license: invalid signature")
	// ErrExpired reports a license whose expiry has passed.
	ErrExpired = errors.New("license: expired")
	// ErrNotYetValid reports a license issued in the future.
	ErrNotYetValid = errors.New("license: not yet valid")
	// ErrServiceNotAllowed reports a valid license that does not enable the
	// service checking it.
	ErrServiceNotAllowed = errors.New("license: service not enabled")
	// ErrNotChecked reports a [Checker] whose key has not been checked yet.
	ErrNotChecked = errors.New("license: not checked")
)

// License is the content of a verified license key.
type License struct {
	// KeyID names the vendor key that signed the license.
	KeyID string
	// ID identifies the license, for support and revocation lists.
	ID string
	// Licensee is the name of the party the license is issued to.
	Licensee string
	// Services lists the services the license enables, or holds
	// [AllServices].
	Services []string
	// IssuedAt is when the license starts, in UTC.
	IssuedAt time.Time
	// ExpiresAt is when the license ends, in UTC; the license is valid
	// strictly before it.
	ExpiresAt time.Time
}

// Allows reports whether the license enables service.
func (l License) Allows(service string) bool {
	return service != "" && (slices.Contains(l.Services, service) || slices.Contains(l.Services, AllServices))
}

// ValidAt reports whether the license is in force at t: not before IssuedAt
// and before ExpiresAt. It does not check the signature, which
// [Verifier.Verify] does.
func (l License) ValidAt(t time.Time) bool {
	return !t.Before(l.IssuedAt) && t.Before(l.ExpiresAt)
}
