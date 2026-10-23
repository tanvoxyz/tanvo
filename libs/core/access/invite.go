package access

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Rule codes of invites and password resets (domain §5.4).
const (
	// CodeEmailAlreadyMember: a pending invite is refused for an email that
	// already has an active membership.
	CodeEmailAlreadyMember = "email_already_used"
	// CodeInviteExpired: the invite expired before acceptance.
	CodeInviteExpired = "invite_expired"
	// CodePasswordResetExpired: the reset token expired.
	CodePasswordResetExpired = "password_reset_expired"
	// CodePasswordResetUsed: the reset token was already used.
	CodePasswordResetUsed = "password_reset_used"
)

// PasswordResetValidity is how long a password reset token is valid.
const PasswordResetValidity = 30 * time.Minute

// Invite invites an email address to an organization with a role (domain
// §5.4).
type Invite struct {
	// ID identifies the invite.
	ID core.InviteID
	// OrganizationID is the inviting organization.
	OrganizationID core.OrganizationID
	// Email is the invitee, normalized by [NormalizeEmail].
	Email string
	// RoleID is the role of the membership created on acceptance.
	RoleID core.RoleID
	// TokenHash is the SHA-256 of the random invite token.
	TokenHash []byte
	// Status is pending, accepted or revoked.
	Status InviteStatus
	// ExpiresAt is the end of validity.
	ExpiresAt time.Time
	// AcceptedAt is set on acceptance.
	AcceptedAt *time.Time
	// RevokedAt is set on revocation.
	RevokedAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// NewInvite returns a pending invite.
func NewInvite(id core.InviteID, org core.OrganizationID, email string, role core.RoleID, tokenHash []byte, expiresAt, now time.Time) *Invite {
	return &Invite{
		ID: id, OrganizationID: org, Email: NormalizeEmail(email), RoleID: role, TokenHash: tokenHash,
		Status: InviteStatusPending, ExpiresAt: expiresAt, CreatedAt: now, UpdatedAt: now,
	}
}

// Validate checks the invariants of domain §5.4.
func (i *Invite) Validate() error {
	var v core.Validator
	v.Check(!i.OrganizationID.IsZero(), "organization_id", core.CodeRequired)
	if v.Required("email", i.Email) {
		v.Email("email", i.Email)
	}
	v.Check(!i.RoleID.IsZero(), "role_id", core.CodeRequired)
	v.Check(len(i.TokenHash) > 0, "token", core.CodeRequired)
	v.Enum("status", i.Status)
	v.Check(!i.ExpiresAt.IsZero(), "expires_at", core.CodeRequired)
	return v.Err()
}

// Expired reports whether the invite can no longer be accepted at now.
func (i *Invite) Expired(now time.Time) bool { return !now.Before(i.ExpiresAt) }

// CanTransitionInvite reports whether an invite may move between
// statuses: pending → accepted and pending → revoked.
func CanTransitionInvite(from, to InviteStatus) bool {
	return from == InviteStatusPending && (to == InviteStatusAccepted || to == InviteStatusRevoked)
}

// Accept accepts the invite at now; an expired invite fails with
// [CodeInviteExpired].
func (i *Invite) Accept(now time.Time) error {
	if !CanTransitionInvite(i.Status, InviteStatusAccepted) {
		return &core.TransitionError{Entity: "invite", From: string(i.Status), To: string(InviteStatusAccepted)}
	}
	if i.Expired(now) {
		return core.NewRuleError(CodeInviteExpired, "the invite has expired")
	}
	i.Status = InviteStatusAccepted
	i.AcceptedAt = &now
	i.UpdatedAt = now
	return nil
}

// Revoke revokes the invite at now.
func (i *Invite) Revoke(now time.Time) error {
	if !CanTransitionInvite(i.Status, InviteStatusRevoked) {
		return &core.TransitionError{Entity: "invite", From: string(i.Status), To: string(InviteStatusRevoked)}
	}
	i.Status = InviteStatusRevoked
	i.RevokedAt = &now
	i.UpdatedAt = now
	return nil
}

// CheckInvite refuses a pending invite for an email that already has an
// active membership in the organization.
func CheckInvite(emailHasActiveMembership bool) error {
	if emailHasActiveMembership {
		return core.NewRuleError(CodeEmailAlreadyMember, "the email already has an active membership")
	}
	return nil
}

// PasswordReset is a single-use password reset token of a user, valid
// [PasswordResetValidity] (domain §5.4). It belongs to a user, not an
// organization.
type PasswordReset struct {
	// ID identifies the reset.
	ID core.PasswordResetID
	// UserID is the user whose password is reset.
	UserID core.UserID
	// TokenHash is the SHA-256 of the random token.
	TokenHash []byte
	// ExpiresAt is the end of validity.
	ExpiresAt time.Time
	// UsedAt is set when the token is used.
	UsedAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
}

// NewPasswordReset returns a reset valid for [PasswordResetValidity] from
// now.
func NewPasswordReset(id core.PasswordResetID, user core.UserID, tokenHash []byte, now time.Time) *PasswordReset {
	return &PasswordReset{ID: id, UserID: user, TokenHash: tokenHash, ExpiresAt: now.Add(PasswordResetValidity), CreatedAt: now}
}

// Validate checks that the user, token and expiry are set.
func (p *PasswordReset) Validate() error {
	var v core.Validator
	v.Check(!p.UserID.IsZero(), "user_id", core.CodeRequired)
	v.Check(len(p.TokenHash) > 0, "token", core.CodeRequired)
	v.Check(!p.ExpiresAt.IsZero(), "expires_at", core.CodeRequired)
	return v.Err()
}

// Use consumes the token at now; an expired or used token is refused.
func (p *PasswordReset) Use(now time.Time) error {
	if p.UsedAt != nil {
		return core.NewRuleError(CodePasswordResetUsed, "the token was already used")
	}
	if !now.Before(p.ExpiresAt) {
		return core.NewRuleError(CodePasswordResetExpired, "the token has expired")
	}
	p.UsedAt = &now
	return nil
}
