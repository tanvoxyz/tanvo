// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

// Package license verifies the offline license keys of the Tanvo enterprise
// services (ee/*).
//
// A key is an Ed25519-signed statement of who the licensee is, until when
// the license runs and which services it enables. It is verified offline
// against the vendor's public keys ([TrustedKeys]); nothing is ever sent to a
// license server. A key has the form
//
//	tanvo1.<base64url(payload)>.<base64url(signature)>
//
// without padding, where the payload is a JSON object and the signature is
// Ed25519 over [SigningContext] followed by the payload bytes.
//
// A service reads its key from the [EnvKey] environment variable, builds a
// [Checker] with [NewVerifier]([TrustedKeys](), nil) and calls
// [Checker.Start] at start-up. The checker re-verifies the key once a day and
// at its expiry; once [Checker.Valid] reports false the service refuses its
// requests.
//
// [Sign] issues keys; the tanvo-license command (cmd/tanvo-license) wraps it
// for key generation, signing and inspection.
package license
