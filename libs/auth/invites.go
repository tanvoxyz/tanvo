package auth

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// DefaultInviteValidity is how long an invite can be accepted. Lago's
// invites never expire; Tanvo's carry an expiry (domain §5.4).
const DefaultInviteValidity = 7 * 24 * time.Hour

// InvitesConfig configures [Invites].
type InvitesConfig struct {
	// Validity is the invite lifetime; 0 uses [DefaultInviteValidity].
	Validity time.Duration
	// FrontURL is the web console's base URL; invite links are
	// FrontURL "/invitation/" token.
	FrontURL string
}

// Invites creates, changes and accepts invites (lago Invites::*Service).
type Invites struct {
	d     Deps
	users *Users
	cfg   InvitesConfig
}

// NewInvites returns the invite service; users opens sessions and hashes
// passwords for accepted invites.
func NewInvites(d Deps, users *Users, cfg InvitesConfig) *Invites {
	if cfg.Validity <= 0 {
		cfg.Validity = DefaultInviteValidity
	}
	cfg.FrontURL = strings.TrimRight(cfg.FrontURL, "/")
	return &Invites{d: d, users: users, cfg: cfg}
}

// InviteRequest is the input of [Invites.Create].
type InviteRequest struct {
	// OrganizationID is the inviting organization.
	OrganizationID core.OrganizationID
	// Actor is the inviting user; zero for system callers, which skip the
	// admin check (lago skip_admin_check).
	Actor core.UserID
	// Email is the invitee.
	Email string
	// RoleID is the role granted on acceptance.
	RoleID core.RoleID
}

// CreatedInvite is a new invite with its token, shown once.
type CreatedInvite struct {
	// Invite is the stored invite.
	Invite *access.Invite
	// Token is the invite secret.
	Token string
	// URL is the invitation link.
	URL string
}

// checkGrant refuses granting role (admin) unless actor is an active admin
// of org (lago cannot_grant_admin).
func checkGrant(ctx context.Context, r port.Repos, org core.OrganizationID, actor core.UserID, role *access.Role) error {
	if actor.IsZero() || !role.Admin() {
		return nil
	}
	m, err := r.Memberships.GetActiveByUser(ctx, org, actor)
	if errors.Is(err, core.ErrNotFound) {
		return rule(CodeCannotGrantAdmin)
	}
	if err != nil {
		return fmt.Errorf("get membership: %w", err)
	}
	if access.BuiltInRoleID(permission.BuiltInRoleAdmin) != m.RoleID {
		return rule(CodeCannotGrantAdmin)
	}
	return nil
}

// role returns the role id of org, or nil when it does not exist.
func role(ctx context.Context, r port.Repos, org core.OrganizationID, id core.RoleID) (*access.Role, error) {
	if id.IsZero() {
		return nil, nil
	}
	ro, err := r.Roles.Get(ctx, org, id)
	if errors.Is(err, core.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get role: %w", err)
	}
	return ro, nil
}

// Create invites an email with a role. Granting admin needs an admin
// actor; a pending invite or an active membership of the email, an
// invalid email and an unknown role are field errors.
func (s *Invites) Create(ctx context.Context, req InviteRequest) (*CreatedInvite, error) {
	var out *CreatedInvite
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		ro, err := role(ctx, r, req.OrganizationID, req.RoleID)
		if err != nil {
			return err
		}
		if ro != nil {
			if err := checkGrant(ctx, r, req.OrganizationID, req.Actor, ro); err != nil {
				return err
			}
		}
		email := access.NormalizeEmail(req.Email)
		var v core.Validator
		if v.Required("email", email) {
			v.Email("email", email)
		}
		if _, err := r.Invites.GetPendingByEmail(ctx, req.OrganizationID, email); err == nil {
			v.Add("invite", CodeInviteAlreadyExists)
		} else if !errors.Is(err, core.ErrNotFound) {
			return fmt.Errorf("get invite: %w", err)
		}
		member, err := activeMember(ctx, r, req.OrganizationID, email)
		if err != nil {
			return err
		}
		v.Check(!member, "email", CodeEmailAlreadyUsed)
		v.Check(ro != nil, "role", CodeInvalidRole)
		if err := v.Err(); err != nil {
			return err
		}
		token, err := NewSecret(s.d.Rand, secretBytes)
		if err != nil {
			return err
		}
		now := s.d.now()
		inv := access.NewInvite(core.NewID[core.InviteID](s.d.IDs), req.OrganizationID, email, ro.ID, HashSecret(token), now.Add(s.cfg.Validity), now)
		if err := inv.Validate(); err != nil {
			return err
		}
		if err := r.Invites.Create(ctx, inv); err != nil {
			if errors.Is(err, core.ErrConflict) {
				return fieldError("invite", CodeInviteAlreadyExists)
			}
			return fmt.Errorf("create invite: %w", err)
		}
		out = &CreatedInvite{Invite: inv, Token: token, URL: s.URL(token)}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.d.record(ctx, SecurityEvent{OrganizationID: req.OrganizationID, UserID: req.Actor, Type: SecurityLogUser, Event: EventUserInvited, Resources: map[string]any{"invitee_email": out.Invite.Email}})
	return out, nil
}

// URL returns the invitation link of token.
func (s *Invites) URL(token string) string {
	return s.cfg.FrontURL + "/invitation/" + url.PathEscape(token)
}

func activeMember(ctx context.Context, r port.Repos, org core.OrganizationID, email string) (bool, error) {
	u, err := r.Users.GetByEmail(ctx, email)
	if errors.Is(err, core.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get user: %w", err)
	}
	_, err = r.Memberships.GetActiveByUser(ctx, org, u.ID)
	if errors.Is(err, core.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get membership: %w", err)
	}
	return true, nil
}

// Update changes the role of a pending invite.
func (s *Invites) Update(ctx context.Context, org core.OrganizationID, actor core.UserID, id core.InviteID, roleID core.RoleID) (*access.Invite, error) {
	var inv *access.Invite
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		inv, err = r.Invites.Get(ctx, org, id)
		if errors.Is(err, core.ErrNotFound) {
			return notFound("invite")
		}
		if err != nil {
			return fmt.Errorf("get invite: %w", err)
		}
		switch inv.Status {
		case access.InviteStatusAccepted:
			return rule(CodeCannotUpdateAcceptedInvite)
		case access.InviteStatusRevoked:
			return rule(CodeCannotUpdateRevokedInvite)
		case access.InviteStatusPending:
		}
		ro, err := role(ctx, r, org, roleID)
		if err != nil {
			return err
		}
		if ro != nil {
			if err := checkGrant(ctx, r, org, actor, ro); err != nil {
				return err
			}
		}
		if ro == nil {
			return fieldError("role", CodeInvalidRole)
		}
		inv.RoleID, inv.UpdatedAt = ro.ID, s.d.now()
		return r.Invites.Update(ctx, inv)
	})
	if err != nil {
		return nil, err
	}
	return inv, nil
}

// Revoke revokes a pending invite; an accepted or revoked invite is not
// found, as in lago.
func (s *Invites) Revoke(ctx context.Context, org core.OrganizationID, id core.InviteID) (*access.Invite, error) {
	var inv *access.Invite
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		inv, err = r.Invites.Get(ctx, org, id)
		if errors.Is(err, core.ErrNotFound) || (err == nil && inv.Status != access.InviteStatusPending) {
			return notFound("invite")
		}
		if err != nil {
			return fmt.Errorf("get invite: %w", err)
		}
		if err := inv.Revoke(s.d.now()); err != nil {
			return err
		}
		return r.Invites.Update(ctx, inv)
	})
	if err != nil {
		return nil, err
	}
	return inv, nil
}

// pendingInvite returns the pending invite of token, or "invite_not_found".
func pendingInvite(ctx context.Context, r port.Repos, token string) (*access.Invite, error) {
	if token == "" {
		return nil, notFound("invite")
	}
	inv, err := r.Invites.GetByTokenHash(ctx, HashSecret(token))
	if errors.Is(err, core.ErrNotFound) || (err == nil && inv.Status != access.InviteStatusPending) {
		return nil, notFound("invite")
	}
	if err != nil {
		return nil, fmt.Errorf("get invite: %w", err)
	}
	return inv, nil
}

// Lookup returns the pending invite of token, for the invitation page.
func (s *Invites) Lookup(ctx context.Context, token string) (*access.Invite, error) {
	var inv *access.Invite
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		inv, err = pendingInvite(ctx, r, token)
		return err
	})
	return inv, err
}

// Accept accepts the invite of token with a password: it creates the user
// (or reuses the one with the invite's email), the membership with the
// invite's role, and opens a session.
func (s *Invites) Accept(ctx context.Context, token, password string, meta SessionMeta) (*SignedIn, error) {
	var out *SignedIn
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		inv, err := pendingInvite(ctx, r, token)
		if err != nil {
			return err
		}
		out, err = s.accept(ctx, r, inv, organization.AuthenticationMethodEmailPassword, password)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.finish(ctx, out, organization.AuthenticationMethodEmailPassword, meta)
}

func (s *Invites) finish(ctx context.Context, out *SignedIn, method organization.AuthenticationMethod, meta SessionMeta) (*SignedIn, error) {
	s.d.record(ctx, SecurityEvent{OrganizationID: out.Membership.OrganizationID, UserID: out.User.ID, Type: SecurityLogUser, Event: EventUserJoined, Resources: map[string]any{
		"email": out.User.Email, "login_method": string(method),
	}})
	return s.users.open(ctx, out, method, meta)
}

// accept applies an invite inside r's transaction (lago
// UsersService#register_from_invite). With the password method a new user
// gets password, and an existing user without active memberships has it
// replaced; a user with active memberships keeps theirs (lago fix
// 8caa0c26d). SSO methods never set a password.
func (s *Invites) accept(ctx context.Context, r port.Repos, inv *access.Invite, method organization.AuthenticationMethod, password string) (*SignedIn, error) {
	org, err := r.Organizations.Get(ctx, inv.OrganizationID)
	if err != nil {
		return nil, fmt.Errorf("get organization: %w", err)
	}
	if !org.AllowsAuthentication(method) {
		return nil, fieldError(string(method), CodeLoginMethodNotAuthorized)
	}
	withPassword := method == organization.AuthenticationMethodEmailPassword
	if withPassword && password == "" {
		return nil, fieldError("password", core.CodeRequired)
	}
	now := s.d.now()
	user, err := r.Users.GetByEmail(ctx, inv.Email)
	switch {
	case errors.Is(err, core.ErrNotFound):
		user = access.NewUser(core.NewID[core.UserID](s.d.IDs), inv.Email, nil, now)
		if withPassword {
			if err := s.users.setPassword(user, password); err != nil {
				return nil, err
			}
		}
		if err := r.Users.Create(ctx, user); err != nil {
			return nil, fmt.Errorf("create user: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("get user: %w", err)
	default:
		ms, err := r.Memberships.ListActiveByUser(ctx, user.ID)
		if err != nil {
			return nil, fmt.Errorf("list memberships: %w", err)
		}
		if withPassword && len(ms) == 0 {
			if err := s.users.setPassword(user, password); err != nil {
				return nil, err
			}
		}
	}
	user.SignedIn(now)
	if err := r.Users.Update(ctx, user); err != nil {
		return nil, fmt.Errorf("update user: %w", err)
	}
	m := access.NewMembership(core.NewID[core.MembershipID](s.d.IDs), inv.OrganizationID, user.ID, inv.RoleID, now)
	if err := r.Memberships.Create(ctx, m); err != nil {
		if errors.Is(err, core.ErrConflict) {
			return nil, fieldError("email", CodeEmailAlreadyUsed)
		}
		return nil, fmt.Errorf("create membership: %w", err)
	}
	if err := inv.Accept(now); err != nil {
		return nil, err
	}
	if err := r.Invites.Update(ctx, inv); err != nil {
		return nil, fmt.Errorf("accept invite: %w", err)
	}
	return &SignedIn{User: user, Organization: org, Membership: m}, nil
}
