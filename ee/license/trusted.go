// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package license

import "crypto/ed25519"

// The vendor's production public keys, in the form of [FormatPublicKey]. A
// new signing key is added here, and to TrustedKeys, before keys signed with
// it are issued; a retired key stays until every key it signed has expired.
const (
	// keyTanvo2026_1 is the first production signing key.
	keyTanvo2026_1 = "tanvo-2026-1:YCjLypiG8HwkwFAEqgxtJwkeJtJ0iDEkJNcmhX6W5EY"
)

// TrustedKeys returns a new map of the vendor's production public keys, by
// key ID, for [NewVerifier].
func TrustedKeys() map[string]ed25519.PublicKey {
	lines := []string{keyTanvo2026_1}
	keys := make(map[string]ed25519.PublicKey, len(lines))
	for _, line := range lines {
		id, pub, err := ParsePublicKey(line)
		if err != nil {
			panic("license: invalid built-in public key: " + err.Error())
		}
		keys[id] = pub
	}
	return keys
}
