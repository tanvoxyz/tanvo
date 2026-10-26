// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package license_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/ee/license"
)

var (
	issued  = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expires = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
)

// testKey returns a deterministic key pair derived from b.
func testKey(b byte) (ed25519.PublicKey, ed25519.PrivateKey) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{b}, ed25519.SeedSize))
	return priv.Public().(ed25519.PublicKey), priv
}

// testLicense returns a complete license signed by "test-1".
func testLicense() license.License {
	return license.License{
		KeyID:     "test-1",
		ID:        "lic-1",
		Licensee:  "Acme Inc.",
		Services:  []string{license.ServiceAnalytics, license.ServiceMCP},
		IssuedAt:  issued,
		ExpiresAt: expires,
	}
}

func mustSign(t *testing.T, priv ed25519.PrivateKey, l license.License) string {
	t.Helper()
	key, err := license.Sign(priv, l)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	return key
}

// resign returns key with its payload replaced by body and signed by priv.
func resign(priv ed25519.PrivateKey, body string) string {
	sig := ed25519.Sign(priv, append([]byte(license.SigningContext), body...))
	enc := base64.RawURLEncoding
	return license.Prefix + enc.EncodeToString([]byte(body)) + "." + enc.EncodeToString(sig)
}

func at(t time.Time) func() time.Time { return func() time.Time { return t } }

func TestVerify(t *testing.T) {
	pub, priv := testKey(1)
	otherPub, otherPriv := testKey(2)
	good := mustSign(t, priv, testLicense())
	body, sig, _ := strings.Cut(strings.TrimPrefix(good, license.Prefix), ".")
	enc := base64.RawURLEncoding
	raw, _ := enc.DecodeString(body)

	unknown := testLicense()
	unknown.KeyID = "test-9"
	wildcard := testLicense()
	wildcard.Services = []string{license.AllServices}

	const validBody = `{"version":1,"key_id":"test-1","license_id":"x","licensee":"A","services":["mcp"],"issued_at":"2026-01-01T00:00:00Z","expires_at":"2027-01-01T00:00:00Z"}`

	tests := []struct {
		name    string
		key     string
		keys    map[string]ed25519.PublicKey
		now     time.Time
		wantErr error
		wantID  string
	}{
		{name: "valid", key: good, now: issued.Add(time.Hour), wantID: "lic-1"},
		{name: "valid at issue time", key: good, now: issued, wantID: "lic-1"},
		{name: "surrounding white space", key: "  " + good + "\n", now: issued, wantID: "lic-1"},
		{name: "wildcard", key: mustSign(t, priv, wildcard), now: issued, wantID: "lic-1"},
		{name: "expired at expiry", key: good, now: expires, wantErr: license.ErrExpired, wantID: "lic-1"},
		{name: "expired after expiry", key: good, now: expires.Add(time.Hour), wantErr: license.ErrExpired, wantID: "lic-1"},
		{name: "not yet valid", key: good, now: issued.Add(-time.Second), wantErr: license.ErrNotYetValid, wantID: "lic-1"},
		{name: "wrong key", key: mustSign(t, otherPriv, testLicense()), now: issued, wantErr: license.ErrSignature},
		{name: "key ID trusted for another key", key: good, keys: map[string]ed25519.PublicKey{"test-1": otherPub}, now: issued, wantErr: license.ErrSignature},
		{name: "unknown key ID", key: mustSign(t, priv, unknown), now: issued, wantErr: license.ErrUnknownKey},
		{name: "no trusted keys", key: good, keys: map[string]ed25519.PublicKey{}, now: issued, wantErr: license.ErrUnknownKey},
		{
			name:    "tampered payload",
			key:     license.Prefix + enc.EncodeToString(bytes.Replace(raw, []byte("Acme"), []byte("Evil"), 1)) + "." + sig,
			now:     issued,
			wantErr: license.ErrSignature,
		},
		{name: "tampered signature", key: license.Prefix + body + "." + enc.EncodeToString(make([]byte, ed25519.SignatureSize)), now: issued, wantErr: license.ErrSignature},
		{name: "signature without context", key: license.Prefix + body + "." + enc.EncodeToString(ed25519.Sign(priv, raw)), now: issued, wantErr: license.ErrSignature},
		{name: "empty", key: "", now: issued, wantErr: license.ErrMalformed},
		{name: "no prefix", key: strings.TrimPrefix(good, license.Prefix), now: issued, wantErr: license.ErrMalformed},
		{name: "other version prefix", key: "tanvo2." + body + "." + sig, now: issued, wantErr: license.ErrUnsupportedVersion},
		{name: "one part", key: license.Prefix + body, now: issued, wantErr: license.ErrMalformed},
		{name: "three parts", key: good + ".x", now: issued, wantErr: license.ErrMalformed},
		{name: "padded payload", key: license.Prefix + body + "==." + sig, now: issued, wantErr: license.ErrMalformed},
		{name: "standard base64", key: license.Prefix + strings.ReplaceAll(body, "_", "/") + "/." + sig, now: issued, wantErr: license.ErrMalformed},
		{name: "short signature", key: license.Prefix + body + "." + enc.EncodeToString([]byte("short")), now: issued, wantErr: license.ErrMalformed},
		{name: "too long", key: license.Prefix + strings.Repeat("A", 9000) + "." + sig, now: issued, wantErr: license.ErrMalformed},
		{name: "payload not JSON", key: resign(priv, "not json"), now: issued, wantErr: license.ErrMalformed},
		{name: "payload trailing data", key: resign(priv, validBody+"{}"), now: issued, wantErr: license.ErrMalformed},
		{name: "payload unknown field", key: resign(priv, strings.Replace(validBody, `"version":1`, `"version":1,"extra":true`, 1)), now: issued, wantErr: license.ErrMalformed},
		{name: "payload version 2", key: resign(priv, strings.Replace(validBody, `"version":1`, `"version":2`, 1)), now: issued, wantErr: license.ErrUnsupportedVersion},
		{name: "payload bad time", key: resign(priv, strings.Replace(validBody, "2027-01-01T00:00:00Z", "2027-01-01", 1)), now: issued, wantErr: license.ErrMalformed},
		{name: "payload no services", key: resign(priv, strings.Replace(validBody, `["mcp"]`, `[]`, 1)), now: issued, wantErr: license.ErrMalformed},
		{name: "payload empty licensee", key: resign(priv, strings.Replace(validBody, `"licensee":"A"`, `"licensee":""`, 1)), now: issued, wantErr: license.ErrMalformed},
		{name: "payload valid", key: resign(priv, validBody), now: issued, wantID: "x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys := tt.keys
			if keys == nil {
				keys = map[string]ed25519.PublicKey{"test-1": pub}
			}
			got, err := license.NewVerifier(keys, at(tt.now)).Verify(tt.key)
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil) != (err == nil) {
				t.Fatalf("Verify() error = %v, want %v", err, tt.wantErr)
			}
			if got.ID != tt.wantID {
				t.Errorf("Verify().ID = %q, want %q", got.ID, tt.wantID)
			}
		})
	}
}

func TestSignRoundTrip(t *testing.T) {
	pub, priv := testKey(1)
	in := testLicense()
	in.IssuedAt = time.Date(2026, 3, 4, 5, 6, 7, 890, time.FixedZone("CET", 3600))
	key := mustSign(t, priv, in)
	if !strings.HasPrefix(key, license.Prefix) {
		t.Errorf("key = %q, want prefix %q", key, license.Prefix)
	}
	got, err := license.NewVerifier(map[string]ed25519.PublicKey{"test-1": pub}, at(expires.Add(-time.Second))).Verify(key)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	wantIssued := time.Date(2026, 3, 4, 4, 6, 7, 0, time.UTC)
	if !got.IssuedAt.Equal(wantIssued) || got.IssuedAt.Location() != time.UTC {
		t.Errorf("IssuedAt = %v, want %v", got.IssuedAt, wantIssued)
	}
	if got.KeyID != in.KeyID || got.ID != in.ID || got.Licensee != in.Licensee ||
		!slices.Equal(got.Services, in.Services) || !got.ExpiresAt.Equal(in.ExpiresAt) {
		t.Errorf("Verify() = %+v, want %+v", got, in)
	}
	unverified, err := license.ParseUnverified(key)
	if err != nil || unverified.ID != in.ID {
		t.Errorf("ParseUnverified() = %+v, %v, want ID %q", unverified, err, in.ID)
	}
	if again := mustSign(t, priv, in); again != key {
		t.Errorf("Sign() is not deterministic: got = %q, want = %q", again, key)
	}
}

func TestSignInvalid(t *testing.T) {
	_, priv := testKey(1)
	tests := []struct {
		name   string
		priv   ed25519.PrivateKey
		modify func(*license.License)
	}{
		{name: "short private key", priv: priv[:10], modify: func(*license.License) {}},
		{name: "empty key ID", modify: func(l *license.License) { l.KeyID = "" }},
		{name: "upper-case key ID", modify: func(l *license.License) { l.KeyID = "Test" }},
		{name: "empty license ID", modify: func(l *license.License) { l.ID = " " }},
		{name: "empty licensee", modify: func(l *license.License) { l.Licensee = "" }},
		{name: "no services", modify: func(l *license.License) { l.Services = nil }},
		{name: "bad service", modify: func(l *license.License) { l.Services = []string{"Analytics"} }},
		{name: "duplicate service", modify: func(l *license.License) { l.Services = []string{"mcp", "mcp"} }},
		{name: "expiry at issue", modify: func(l *license.License) { l.ExpiresAt = l.IssuedAt }},
		{name: "expiry before issue", modify: func(l *license.License) { l.ExpiresAt = l.IssuedAt.Add(-time.Hour) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := testLicense()
			tt.modify(&l)
			p := tt.priv
			if p == nil {
				p = priv
			}
			if _, err := license.Sign(p, l); !errors.Is(err, license.ErrMalformed) {
				t.Errorf("Sign() error = %v, want %v", err, license.ErrMalformed)
			}
		})
	}
}

func TestAllows(t *testing.T) {
	tests := []struct {
		services []string
		service  string
		want     bool
	}{
		{[]string{"analytics", "mcp"}, "analytics", true},
		{[]string{"analytics", "mcp"}, "mcp", true},
		{[]string{"analytics", "mcp"}, "assistant", false},
		{[]string{"analytics"}, "", false},
		{[]string{"*"}, "pipeline", true},
		{[]string{"*"}, "future-service", true},
		{[]string{"*"}, "", false},
		{nil, "analytics", false},
	}
	for _, tt := range tests {
		l := license.License{Services: tt.services}
		if got := l.Allows(tt.service); got != tt.want {
			t.Errorf("License{Services: %v}.Allows(%q) = %v, want %v", tt.services, tt.service, got, tt.want)
		}
	}
}

func TestValidAt(t *testing.T) {
	l := testLicense()
	tests := []struct {
		t    time.Time
		want bool
	}{
		{issued.Add(-time.Nanosecond), false},
		{issued, true},
		{expires.Add(-time.Nanosecond), true},
		{expires, false},
	}
	for _, tt := range tests {
		if got := l.ValidAt(tt.t); got != tt.want {
			t.Errorf("ValidAt(%v) = %v, want %v", tt.t, got, tt.want)
		}
	}
}

func TestPublicKeyFormat(t *testing.T) {
	pub, _ := testKey(3)
	line := license.FormatPublicKey("test-3", pub)
	id, got, err := license.ParsePublicKey(line)
	if err != nil || id != "test-3" || !got.Equal(pub) {
		t.Errorf("ParsePublicKey(%q) = %q, %x, %v, want test-3, %x, nil", line, id, got, err, pub)
	}
	for _, bad := range []string{"", "test-3", ":" + line[7:], "Test:" + line[7:], "test-3:!!", "test-3:" + base64.RawURLEncoding.EncodeToString([]byte("short"))} {
		if _, _, err := license.ParsePublicKey(bad); !errors.Is(err, license.ErrMalformed) {
			t.Errorf("ParsePublicKey(%q) error = %v, want %v", bad, err, license.ErrMalformed)
		}
	}
}

func TestTrustedKeys(t *testing.T) {
	keys := license.TrustedKeys()
	pub, ok := keys["tanvo-2026-1"]
	if !ok || len(pub) != ed25519.PublicKeySize {
		t.Fatalf("TrustedKeys()[tanvo-2026-1] = %x, %v, want a public key", pub, ok)
	}
	delete(keys, "tanvo-2026-1")
	if _, ok := license.TrustedKeys()["tanvo-2026-1"]; !ok {
		t.Errorf("TrustedKeys() returned a shared map, want a fresh one")
	}
}

func TestNewVerifierCopiesKeys(t *testing.T) {
	pub, priv := testKey(1)
	keys := map[string]ed25519.PublicKey{"test-1": pub}
	v := license.NewVerifier(keys, at(issued))
	delete(keys, "test-1")
	if _, err := v.Verify(mustSign(t, priv, testLicense())); err != nil {
		t.Errorf("Verify() after the caller's map changed: error = %v, want nil", err)
	}
}
