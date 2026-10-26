// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package servicetoken

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	_, other, _ := ed25519.GenerateKey(nil)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	v := NewVerifier(Keys{"k1": pub}, func() time.Time { return now })
	claims := Claims{OrganizationID: "org", Subject: "user:u", Permissions: []string{"data_api:read"}, ExpiresAt: now.Add(15 * time.Minute)}
	sign := func(priv ed25519.PrivateKey, kid string, c Claims, iat time.Time) string {
		tok, err := Sign(priv, kid, c, iat)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	good := sign(priv, "k1", claims, now)
	payload, sig, _ := strings.Cut(good, ".")
	expired := claims
	expired.ExpiresAt = now.Add(-2 * time.Minute)
	for _, tt := range []struct {
		name  string
		token string
		want  error
	}{
		{name: "valid", token: good},
		{name: "malformed", token: "nodot", want: ErrMalformed},
		{name: "bad base64", token: "!!." + sig, want: ErrMalformed},
		{name: "unknown key", token: sign(priv, "k2", claims, now), want: ErrUnknownKey},
		{name: "wrong signer", token: sign(other, "k1", claims, now), want: ErrSignature},
		{name: "tampered", token: payload + "A." + sig, want: ErrMalformed},
		{name: "expired", token: sign(priv, "k1", expired, now.Add(-time.Hour)), want: ErrExpired},
		{name: "issued in the future", token: sign(priv, "k1", claims, now.Add(time.Hour)), want: ErrExpired},
		{name: "signature swapped", token: payload + "." + base64.RawURLEncoding.EncodeToString(make([]byte, 64)), want: ErrSignature},
	} {
		got, err := v.Verify(tt.token)
		if !errors.Is(err, tt.want) {
			t.Errorf("%s: Verify() error got = %v, want = %v", tt.name, err, tt.want)
			continue
		}
		if err == nil && (got.OrganizationID != "org" || got.Subject != "user:u" || !got.Has("data_api:read") || !got.ExpiresAt.Equal(claims.ExpiresAt)) {
			t.Errorf("%s: Verify() got = %+v, want = %+v", tt.name, got, claims)
		}
	}
}

func TestParseKeys(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	enc := base64.RawURLEncoding.EncodeToString(pub)
	keys, err := ParseKeys(" k1:" + enc + ", k2:" + enc + ",")
	if err != nil || len(keys) != 2 {
		t.Fatalf("ParseKeys() got = %v, %v, want = 2 keys", keys, err)
	}
	for _, bad := range []string{"", "k1", ":" + enc, "k1:short", "k1:" + enc + ",k1:" + enc} {
		if _, err := ParseKeys(bad); err == nil {
			t.Errorf("ParseKeys(%q) error got = nil, want = an error", bad)
		}
	}
}
