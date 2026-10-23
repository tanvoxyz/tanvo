package auth

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/argon2"
)

// ErrMalformedHash is returned for a stored password hash that is not an
// argon2id PHC string.
var ErrMalformedHash = errors.New("malformed password hash")

// Argon2Params are the argon2id cost parameters (RFC 9106).
type Argon2Params struct {
	// Memory is the memory cost in KiB.
	Memory uint32
	// Time is the number of passes.
	Time uint32
	// Threads is the degree of parallelism.
	Threads uint8
	// SaltLength is the salt size in bytes.
	SaltLength int
	// KeyLength is the derived key size in bytes.
	KeyLength uint32
}

// DefaultArgon2Params are the production parameters: 64 MiB, 3 passes, 4
// lanes (RFC 9106 §4, second recommended option), 16-byte salt and 32-byte
// key.
func DefaultArgon2Params() Argon2Params {
	return Argon2Params{Memory: 64 * 1024, Time: 3, Threads: 4, SaltLength: 16, KeyLength: 32}
}

// PasswordHasher hashes passwords with argon2id and encodes them as PHC
// strings ("$argon2id$v=19$m=65536,t=3,p=4$<salt>$<key>").
type PasswordHasher struct {
	params Argon2Params
	rand   io.Reader
}

// NewPasswordHasher returns a hasher with params, reading salts from rand
// (crypto/rand.Reader in production).
func NewPasswordHasher(params Argon2Params, rand io.Reader) *PasswordHasher {
	return &PasswordHasher{params: params, rand: rand}
}

// Hash returns the PHC string of password.
func (h *PasswordHasher) Hash(password string) ([]byte, error) {
	salt := make([]byte, h.params.SaltLength)
	if _, err := io.ReadFull(h.rand, salt); err != nil {
		return nil, fmt.Errorf("read salt: %w", err)
	}
	p := h.params
	key := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLength)
	enc := base64.RawStdEncoding
	return fmt.Appendf(nil, "$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// Verify reports whether password matches the PHC string encoded, in
// constant time, and whether the hash should be recomputed because its
// parameters differ from the hasher's.
func (h *PasswordHasher) Verify(encoded []byte, password string) (ok, needsRehash bool, err error) {
	p, salt, key, err := decodePHC(string(encoded))
	if err != nil {
		return false, false, err
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, uint32(len(key)))
	if subtle.ConstantTimeCompare(got, key) != 1 {
		return false, false, nil
	}
	cur := h.params
	needsRehash = p.Memory != cur.Memory || p.Time != cur.Time || p.Threads != cur.Threads ||
		len(salt) != cur.SaltLength || uint32(len(key)) != cur.KeyLength
	return true, needsRehash, nil
}

func decodePHC(s string) (Argon2Params, []byte, []byte, error) {
	var p Argon2Params
	parts := strings.Split(s, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, nil, nil, ErrMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return p, nil, nil, ErrMalformedHash
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil || p.Time == 0 || p.Threads == 0 {
		return p, nil, nil, ErrMalformedHash
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return p, nil, nil, ErrMalformedHash
	}
	key, err := enc.DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return p, nil, nil, ErrMalformedHash
	}
	p.SaltLength, p.KeyLength = len(salt), uint32(len(key))
	return p, salt, key, nil
}
