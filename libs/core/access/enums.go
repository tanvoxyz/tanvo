package access

import (
	"github.com/tanvoxyz/tanvo/libs/core"
)

// MembershipStatus is the status of a membership (domain §5.2); revoked is terminal.
type MembershipStatus string

// MembershipStatus values.
const (
	// MembershipStatusActive is "active": the user can act in the organization.
	MembershipStatusActive MembershipStatus = "active"
	// MembershipStatusRevoked is "revoked": the membership was revoked.
	MembershipStatusRevoked MembershipStatus = "revoked"
)

// MembershipStatusValues returns every MembershipStatus, in declaration order.
func MembershipStatusValues() []MembershipStatus {
	return []MembershipStatus{MembershipStatusActive, MembershipStatusRevoked}
}

// Valid reports whether v is one of the declared MembershipStatus values.
func (v MembershipStatus) Valid() bool {
	switch v {
	case MembershipStatusActive, MembershipStatusRevoked:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v MembershipStatus) String() string { return string(v) }

// ParseMembershipStatus parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseMembershipStatus(s string) (MembershipStatus, error) {
	if v := MembershipStatus(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("access.MembershipStatus", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v MembershipStatus) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *MembershipStatus) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseMembershipStatus(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// InviteStatus is the status of an invite (domain §5.4); accepted and revoked are terminal.
type InviteStatus string

// InviteStatus values.
const (
	// InviteStatusPending is "pending": waiting for the invitee.
	InviteStatusPending InviteStatus = "pending"
	// InviteStatusAccepted is "accepted": accepted; a membership was created.
	InviteStatusAccepted InviteStatus = "accepted"
	// InviteStatusRevoked is "revoked": revoked before acceptance.
	InviteStatusRevoked InviteStatus = "revoked"
)

// InviteStatusValues returns every InviteStatus, in declaration order.
func InviteStatusValues() []InviteStatus {
	return []InviteStatus{InviteStatusPending, InviteStatusAccepted, InviteStatusRevoked}
}

// Valid reports whether v is one of the declared InviteStatus values.
func (v InviteStatus) Valid() bool {
	switch v {
	case InviteStatusPending, InviteStatusAccepted, InviteStatusRevoked:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v InviteStatus) String() string { return string(v) }

// ParseInviteStatus parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseInviteStatus(s string) (InviteStatus, error) {
	if v := InviteStatus(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("access.InviteStatus", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v InviteStatus) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *InviteStatus) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseInviteStatus(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}
