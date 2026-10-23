package access

import (
	"crypto/sha256"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
)

// Session is a signed-in user's opaque session (domain §5.7). The token
// is shown once; only its SHA-256 is stored. Sessions belong to users, not
// organizations: the organization is chosen per request, and an
// organization that does not allow Method is not accessible with the
// session.
type Session struct {
	// ID identifies the session.
	ID core.SessionID
	// UserID is the signed-in user.
	UserID core.UserID
	// TokenHash is the SHA-256 of the token.
	TokenHash []byte
	// Method is the sign-in method.
	Method organization.AuthenticationMethod
	// UserAgent is the client's user agent at sign-in.
	UserAgent string
	// IP is the client's address at sign-in.
	IP string
	// CreatedAt is the sign-in instant.
	CreatedAt time.Time
	// LastSeenAt is the instant of the last authenticated request.
	LastSeenAt time.Time
	// ExpiresAt is the absolute end of validity.
	ExpiresAt time.Time
	// RevokedAt is set on sign-out or revocation.
	RevokedAt *time.Time
}

// Validate checks the invariants of domain §5.7.
func (s *Session) Validate() error {
	var v core.Validator
	v.Check(!s.UserID.IsZero(), "user_id", core.CodeRequired)
	v.Check(len(s.TokenHash) == sha256.Size, "token_hash", core.CodeInvalid)
	v.Enum("method", s.Method)
	v.Check(!s.ExpiresAt.IsZero(), "expires_at", core.CodeRequired)
	return v.Err()
}

// Valid reports whether the session authenticates requests at now under
// the idle timeout idle (0 disables it).
func (s *Session) Valid(now time.Time, idle time.Duration) bool {
	if s.RevokedAt != nil || !now.Before(s.ExpiresAt) {
		return false
	}
	return idle <= 0 || now.Sub(s.LastSeenAt) < idle
}

// OIDCIntent is what an OpenID Connect sign-in does once the identity is
// known (domain §5.7).
type OIDCIntent string

// OIDCIntent values.
const (
	// OIDCIntentLogin is "login": sign in.
	OIDCIntentLogin OIDCIntent = "login"
	// OIDCIntentSignUp is "sign_up": register a user and an organization.
	OIDCIntentSignUp OIDCIntent = "sign_up"
	// OIDCIntentAcceptInvite is "accept_invite": accept an invite.
	OIDCIntentAcceptInvite OIDCIntent = "accept_invite"
)

// OIDCIntentValues returns every OIDCIntent, in declaration order.
func OIDCIntentValues() []OIDCIntent {
	return []OIDCIntent{OIDCIntentLogin, OIDCIntentSignUp, OIDCIntentAcceptInvite}
}

// Valid reports whether v is one of the declared OIDCIntent values.
func (v OIDCIntent) Valid() bool {
	switch v {
	case OIDCIntentLogin, OIDCIntentSignUp, OIDCIntentAcceptInvite:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v OIDCIntent) String() string { return string(v) }

// ParseOIDCIntent parses the stored form s, failing with
// [core.ErrInvalidValue] for anything but a declared value.
func ParseOIDCIntent(s string) (OIDCIntent, error) {
	if v := OIDCIntent(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("access.OIDCIntent", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v OIDCIntent) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *OIDCIntent) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseOIDCIntent(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// OIDCState is the server-side state of an OpenID Connect sign-in between
// the redirect to the provider and its callback (domain §5.7), stored
// under the SHA-256 of the state parameter and used once. It belongs to no
// user or organization yet.
type OIDCState struct {
	// KeyHash is the SHA-256 of the state parameter.
	KeyHash []byte
	// Method is the sign-in method.
	Method organization.AuthenticationMethod
	// Intent is the flow's purpose.
	Intent OIDCIntent
	// ConnectionID is the connection chosen at the start.
	ConnectionID string
	// Email is the address typed at the start (empty for Google).
	Email string
	// InviteTokenHash is the SHA-256 of the token of the invite being
	// accepted; the token itself is not stored.
	InviteTokenHash []byte
	// OrganizationName names the organization of a sign-up.
	OrganizationName string
	// Nonce binds the ID token to the flow.
	Nonce string
	// Verifier is the PKCE code verifier.
	Verifier string
	// ExpiresAt ends the flow's validity.
	ExpiresAt time.Time
	// CreatedAt is the start of the flow.
	CreatedAt time.Time
}

// Validate checks the invariants of domain §5.7.
func (s *OIDCState) Validate() error {
	var v core.Validator
	v.Check(len(s.KeyHash) == sha256.Size, "key_hash", core.CodeInvalid)
	if v.Enum("method", s.Method) {
		v.Check(s.Method != organization.AuthenticationMethodEmailPassword, "method", core.CodeInvalid)
	}
	v.Enum("intent", s.Intent)
	v.Required("connection_id", s.ConnectionID)
	v.Check(s.Intent != OIDCIntentAcceptInvite || len(s.InviteTokenHash) > 0, "invite_token", core.CodeRequired)
	v.Required("nonce", s.Nonce)
	v.Required("verifier", s.Verifier)
	v.Check(!s.ExpiresAt.IsZero(), "expires_at", core.CodeRequired)
	return v.Err()
}

// Expired reports whether the flow can no longer complete at now.
func (s *OIDCState) Expired(now time.Time) bool { return !now.Before(s.ExpiresAt) }
