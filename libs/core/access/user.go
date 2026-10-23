package access

import (
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// User is a person who signs in (domain §5.1). A user is not owned by an
// organization: memberships link it to organizations.
type User struct {
	// ID identifies the user.
	ID core.UserID
	// Email is unique case-insensitively; stored normalized by
	// [NormalizeEmail].
	Email string
	// PasswordHash is the argon2id hash of the password; nil for SSO-only
	// users.
	PasswordHash []byte
	// Name is the display name.
	Name *string
	// LastSignInAt is the instant of the last sign-in.
	LastSignInAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// NewUser returns a user with a normalized email.
func NewUser(id core.UserID, email string, passwordHash []byte, now time.Time) *User {
	return &User{ID: id, Email: NormalizeEmail(email), PasswordHash: passwordHash, CreatedAt: now, UpdatedAt: now}
}

// Validate checks the invariants of domain §5.1.
func (u *User) Validate() error {
	var v core.Validator
	if v.Required("email", u.Email) {
		v.Email("email", u.Email)
	}
	v.OptionalString("name", u.Name, 255)
	return v.Err()
}

// HasPassword reports whether the user can sign in with a password.
func (u *User) HasPassword() bool { return len(u.PasswordHash) > 0 }

// SignedIn records a sign-in at now.
func (u *User) SignedIn(now time.Time) {
	u.LastSignInAt = &now
	u.UpdatedAt = now
}

// NormalizeEmail sanitizes an email as it is stored and compared: dash
// look-alikes become "-", invisible characters are removed, surrounding
// whitespace is trimmed and the address is lower-cased (uniqueness is
// case-insensitive, domain §5.1).
func NormalizeEmail(email string) string {
	var b strings.Builder
	for _, r := range email {
		switch r {
		case '\u2012', '\u2013', '\u2014', '\u2015', '\u2043', '\u2212':
			b.WriteByte('-')
		case '\u200B', '\u200C', '\u200D', '\u00A0', '\u200E', '\u200F', '\uFEFF':
		default:
			b.WriteRune(r)
		}
	}
	return strings.ToLower(strings.TrimSpace(b.String()))
}
