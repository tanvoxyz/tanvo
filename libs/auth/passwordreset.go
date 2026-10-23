package auth

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// PasswordResets requests and applies password resets (lago
// PasswordResets::*Service). Tokens are single-use and valid for
// [access.PasswordResetValidity].
type PasswordResets struct {
	d        Deps
	users    *Users
	sessions *Sessions
	front    string
}

// NewPasswordResets returns the service; reset links are frontURL
// "/reset-password/" token.
func NewPasswordResets(d Deps, users *Users, sessions *Sessions, frontURL string) *PasswordResets {
	return &PasswordResets{d: d, users: users, sessions: sessions, front: strings.TrimRight(frontURL, "/")}
}

// Request creates a reset token for the user with email and enqueues the
// reset email. An unknown email, or a user without active memberships,
// fails with "user_not_found"; the API layer
// may hide it to avoid disclosing accounts.
func (s *PasswordResets) Request(ctx context.Context, email string) (*access.PasswordReset, error) {
	var (
		pr   *access.PasswordReset
		user *access.User
		ms   []*access.Membership
	)
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		user, err = r.Users.GetByEmail(ctx, access.NormalizeEmail(email))
		if errors.Is(err, core.ErrNotFound) {
			return notFound("user")
		}
		if err != nil {
			return fmt.Errorf("get user: %w", err)
		}
		// A user without active memberships cannot sign in after the
		// reset; the job also needs an organization as its tenant.
		if ms, err = r.Memberships.ListActiveByUser(ctx, user.ID); err != nil {
			return fmt.Errorf("list memberships: %w", err)
		}
		if len(ms) == 0 {
			return notFound("user")
		}
		token, err := NewSecret(s.d.Rand, secretBytes)
		if err != nil {
			return err
		}
		pr = access.NewPasswordReset(core.NewID[core.PasswordResetID](s.d.IDs), user.ID, HashSecret(token), s.d.now())
		if err := r.PasswordResets.Create(ctx, pr); err != nil {
			return fmt.Errorf("create password reset: %w", err)
		}
		return enqueueMail(ctx, r, MailJob{Template: MailPasswordReset, OrganizationID: ms[0].OrganizationID, To: user.Email, Data: map[string]string{
			"reset_url": s.front + "/reset-password/" + url.PathEscape(token),
		}})
	})
	if err != nil {
		return nil, err
	}
	for _, m := range ms {
		s.d.record(ctx, SecurityEvent{OrganizationID: m.OrganizationID, UserID: user.ID, Type: SecurityLogUser, Event: EventPasswordResetRequested, Resources: map[string]any{"email": user.Email}})
	}
	return pr, nil
}

// Reset sets the password of the token's user, consumes the token, ends
// the user's other sessions and signs the user in. A missing, unknown,
// used or expired token fails with "password_reset_not_found".
func (s *PasswordResets) Reset(ctx context.Context, token, newPassword string, meta SessionMeta) (*SignedIn, error) {
	if newPassword == "" {
		return nil, fieldError("new_password", CodeMissingPassword)
	}
	if token == "" {
		return nil, fieldError("token", CodeMissingToken)
	}
	var user *access.User
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		pr, err := r.PasswordResets.GetByTokenHash(ctx, HashSecret(token))
		if errors.Is(err, core.ErrNotFound) {
			return notFound("password_reset")
		}
		if err != nil {
			return fmt.Errorf("get password reset: %w", err)
		}
		if err := pr.Use(s.d.now()); err != nil {
			return notFound("password_reset")
		}
		if err := r.PasswordResets.Update(ctx, pr); err != nil {
			return fmt.Errorf("use password reset: %w", err)
		}
		if user, err = r.Users.Get(ctx, pr.UserID); err != nil {
			return fmt.Errorf("get user: %w", err)
		}
		if err := s.users.setPassword(user, newPassword); err != nil {
			return err
		}
		return r.Users.Update(ctx, user)
	})
	if err != nil {
		return nil, err
	}
	if err := s.sessions.RevokeUser(ctx, user.ID, SessionID{}); err != nil {
		return nil, err
	}
	out, err := s.users.Login(ctx, user.Email, newPassword, meta)
	if err != nil {
		return nil, err
	}
	var ms []*access.Membership
	if err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		ms, err = r.Memberships.ListActiveByUser(ctx, user.ID)
		return err
	}); err != nil {
		return nil, fmt.Errorf("list memberships: %w", err)
	}
	for _, m := range ms {
		s.d.record(ctx, SecurityEvent{OrganizationID: m.OrganizationID, UserID: user.ID, Type: SecurityLogUser, Event: EventPasswordEdited, Resources: map[string]any{"email": user.Email}})
	}
	return out, nil
}
