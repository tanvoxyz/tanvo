package auth

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
)

func TestAESSecretBox(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	box, err := NewAESSecretBox(key, nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := core.UUIDv7Generator{}
	org, other := core.NewID[core.OrganizationID](ids), core.NewID[core.OrganizationID](ids)
	sealed, err := box.Seal(t.Context(), org, []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("hunter2")) {
		t.Errorf("sealed value holds the plaintext")
	}
	got, err := box.Open(t.Context(), org, sealed)
	if err != nil || string(got) != "hunter2" {
		t.Errorf("Open() got = %q, %v, want = %q", got, err, "hunter2")
	}
	again, err := box.Seal(t.Context(), org, []byte("hunter2"))
	if err != nil || bytes.Equal(again, sealed) {
		t.Errorf("Seal() twice got equal values, want distinct nonces")
	}
	otherKey, err := NewAESSecretBox(bytes.Repeat([]byte{8}, 32), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 1
	tests := []struct {
		name   string
		box    *AESSecretBox
		org    core.OrganizationID
		sealed []byte
	}{
		{name: "other organization", box: box, org: other, sealed: sealed},
		{name: "other key", box: otherKey, org: org, sealed: sealed},
		{name: "tampered", box: box, org: org, sealed: tampered},
		{name: "truncated", box: box, org: org, sealed: sealed[:10]},
		{name: "unknown version", box: box, org: org, sealed: append([]byte{9}, sealed[1:]...)},
		{name: "empty", box: box, org: org},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.box.Open(t.Context(), tt.org, tt.sealed); !errors.Is(err, ErrSecretUndecryptable) {
				t.Errorf("Open() error got = %v, want = %v", err, ErrSecretUndecryptable)
			}
		})
	}
	if _, err := NewAESSecretBox(key[:16], nil); err == nil {
		t.Errorf("NewAESSecretBox(16 bytes) error got = nil, want an error")
	}
}
