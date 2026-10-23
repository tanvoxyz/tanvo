package auth

import (
	"bytes"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
)

func TestPasswordHasher(t *testing.T) {
	h := testHasher()
	hash, err := h.Hash("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(hash), "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Errorf("hash = %s, want an argon2id PHC string", hash)
	}
	other, _ := h.Hash("s3cret")
	if bytes.Equal(hash, other) {
		t.Error("two hashes of one password are equal; salt not random")
	}
	stronger := NewPasswordHasher(Argon2Params{Memory: 128, Time: 2, Threads: 1, SaltLength: 16, KeyLength: 32}, rand.Reader)
	tests := []struct {
		name       string
		hasher     *PasswordHasher
		hash       []byte
		password   string
		ok, rehash bool
		err        error
	}{
		{name: "correct password", hasher: h, hash: hash, password: "s3cret", ok: true},
		{name: "wrong password", hasher: h, hash: hash, password: "s3cre", ok: false},
		{name: "empty password", hasher: h, hash: hash, password: "", ok: false},
		{name: "outdated parameters", hasher: stronger, hash: hash, password: "s3cret", ok: true, rehash: true},
		{name: "bcrypt hash", hasher: h, hash: []byte("$2a$12$R9h/cIPz0gi.URNNX3kh2OPST9/PgBkqquzi.Ss7KIUgO2t0jWMUW"), err: ErrMalformedHash},
		{name: "garbage", hasher: h, hash: []byte("$argon2id$v=19$m=x$a$b"), err: ErrMalformedHash},
		{name: "wrong version", hasher: h, hash: []byte(strings.Replace(string(hash), "v=19", "v=16", 1)), err: ErrMalformedHash},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, rehash, err := tt.hasher.Verify(tt.hash, tt.password)
			if !errors.Is(err, tt.err) {
				t.Fatalf("err = %v, want %v", err, tt.err)
			}
			if ok != tt.ok || rehash != tt.rehash {
				t.Errorf("got = (%v, %v), want = (%v, %v)", ok, rehash, tt.ok, tt.rehash)
			}
		})
	}
}

func TestDefaultArgon2Params(t *testing.T) {
	p := DefaultArgon2Params()
	if p.Memory < 19*1024 || p.Time < 2 || p.SaltLength < 16 || p.KeyLength < 32 {
		t.Errorf("got = %+v, want at least the OWASP minimum", p)
	}
}

func TestNewSecret(t *testing.T) {
	a, err := NewSecret(rand.Reader, secretBytes)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewSecret(rand.Reader, secretBytes)
	if len(a) != 43 || a == b {
		t.Errorf("got = %q, %q, want two distinct 43-character secrets", a, b)
	}
	if len(HashSecret(a)) != 32 {
		t.Errorf("hash length = %d, want = 32", len(HashSecret(a)))
	}
	if _, err := NewSecret(strings.NewReader("short"), secretBytes); err == nil {
		t.Error("err = nil, want a read error")
	}
}
