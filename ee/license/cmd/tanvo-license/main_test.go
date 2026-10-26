// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cli runs a command line and returns its exit status and output.
func cli(t *testing.T, now time.Time, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr, func(k string) string { return env[k] }, func() time.Time { return now })
	return code, stdout.String(), stderr.String()
}

func TestKeygenSignInspect(t *testing.T) {
	now := time.Date(2026, 10, 27, 15, 30, 0, 0, time.UTC)
	seed := filepath.Join(t.TempDir(), "keys", "test-1.seed")

	code, pubLine, stderr := cli(t, now, nil, "keygen", "test-1", "-out", seed)
	if code != 0 {
		t.Fatalf("keygen exit = %d, want 0; stderr = %s", code, stderr)
	}
	pubLine = strings.TrimSpace(pubLine)
	if !strings.HasPrefix(pubLine, "test-1:") {
		t.Errorf("keygen output = %q, want prefix %q", pubLine, "test-1:")
	}
	info, err := os.Stat(seed)
	if err != nil {
		t.Fatalf("stat seed: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("seed mode = %o, want = 600", got)
	}
	if code, _, _ := cli(t, now, nil, "keygen", "-out", seed, "test-1"); code != 1 {
		t.Errorf("keygen over an existing seed: exit = %d, want 1", code)
	}

	code, key, stderr := cli(t, now, nil, "sign", "-seed-file", seed, "-key-id", "test-1",
		"-licensee", "Acme Inc.", "-services", "analytics, mcp", "-expires", "2027-12-31", "-license-id", "lic-9")
	if code != 0 {
		t.Fatalf("sign exit = %d, want 0; stderr = %s", code, stderr)
	}
	key = strings.TrimSpace(key)

	tests := []struct {
		name      string
		now       time.Time
		env       map[string]string
		args      []string
		wantCode  int
		wantValid bool
		wantErr   string
	}{
		{name: "valid", now: now, args: []string{"inspect", "-public", pubLine, key}, wantValid: true},
		{name: "from environment", now: now, env: map[string]string{"TANVO_LICENSE_KEY": key}, args: []string{"inspect", "-public", pubLine}, wantValid: true},
		{name: "last day", now: time.Date(2027, 12, 31, 23, 59, 59, 0, time.UTC), args: []string{"inspect", key, "-public", pubLine}, wantValid: true},
		{name: "expired", now: time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC), args: []string{"inspect", "-public", pubLine, key}, wantCode: 1, wantErr: "expired"},
		{name: "before issue day", now: time.Date(2026, 10, 26, 23, 0, 0, 0, time.UTC), args: []string{"inspect", "-public", pubLine, key}, wantCode: 1, wantErr: "not yet valid"},
		{name: "built-in keys", now: now, args: []string{"inspect", key}, wantCode: 1, wantErr: "unknown signing key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := cli(t, tt.now, tt.env, tt.args...)
			if code != tt.wantCode {
				t.Fatalf("inspect exit = %d, want %d; stderr = %s", code, tt.wantCode, stderr)
			}
			var got inspection
			if err := json.Unmarshal([]byte(stdout), &got); err != nil {
				t.Fatalf("inspect output %q: %v", stdout, err)
			}
			if got.Valid != tt.wantValid || !strings.Contains(got.Error, tt.wantErr) {
				t.Errorf("inspect = valid %v, error %q, want valid %v, error containing %q", got.Valid, got.Error, tt.wantValid, tt.wantErr)
			}
			want := inspection{
				Valid: got.Valid, Error: got.Error, KeyID: "test-1", LicenseID: "lic-9", Licensee: "Acme Inc.",
				IssuedAt:  time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC),
				ExpiresAt: time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC),
			}
			if got.KeyID != want.KeyID || got.LicenseID != want.LicenseID || got.Licensee != want.Licensee ||
				!got.IssuedAt.Equal(want.IssuedAt) || !got.ExpiresAt.Equal(want.ExpiresAt) ||
				strings.Join(got.Services, ",") != "analytics,mcp" {
				t.Errorf("inspect = %+v, want %+v with services analytics,mcp", got, want)
			}
		})
	}
}

func TestUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "no command", args: nil, want: 2},
		{name: "unknown command", args: []string{"frobnicate"}, want: 2},
		{name: "keygen without out", args: []string{"keygen", "test-1"}, want: 2},
		{name: "sign without flags", args: []string{"sign"}, want: 2},
		{name: "inspect without key", args: []string{"inspect"}, want: 2},
		{name: "inspect bad public key", args: []string{"inspect", "-public", "nope", "x"}, want: 2},
		{name: "inspect malformed key", args: []string{"inspect", "garbage"}, want: 1},
		{name: "sign missing seed", args: []string{"sign", "-seed-file", "/nonexistent", "-key-id", "k", "-licensee", "A", "-services", "mcp", "-expires", "2027-01-01"}, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, _, _ := cli(t, time.Now(), nil, tt.args...); got != tt.want {
				t.Errorf("exit = %d, want %d", got, tt.want)
			}
		})
	}
}
