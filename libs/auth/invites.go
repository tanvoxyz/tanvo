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
	// RoleIDs are the roles granted on acceptance: at least one, each a
	// built-in role or a custom role of the organization.
	RoleIDs []core.RoleID
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

// checkGrant refuses granting roles that include admin unless actor is an
// active admin of org (lago cannot_grant_admin,
// app/services/invites/create_service.rb:56-62, update_service.rb:34-39).
// A zero actor is a system caller and skips the check (lago
// skip_admin_check).
func checkGrant(ctx context.Context, r port.Repos, org core.OrganizationID, actor core.UserID, roles []core.RoleID) error {
	if actor.IsZero() || !access.HasAdmin(roles) {
		return nil
	}
	m, err := r.Memberships.GetActiveByUser(ctx, org, actor)
	if errors.Is(err, core.ErrNotFound) {
		return rule(CodeCannotGrantAdmin)
	}
	if err != nil {
		return fmt.Errorf("get membership: %w", err)
	}
	if !m.Admin() {
		return rule(CodeCannotGrantAdmin)
	}
	return nil
}

// validRoles reports whether ids is not empty and every role exists in
// org (lago invalid_role, app/services/invites/validate_service.rb:36-46).
func validRoles(ctx context.Context, r port.Repos, org core.OrganizationID, ids []core.RoleID) (bool, error) {
	if len(ids) == 0 {
		return false, nil
	}
	for _, id := range ids {
		if id.IsZero() {
			return false, nil
		}
		_, err := r.Roles.Get(ctx, org, id)
		if errors.Is(err, core.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("get role: %w", err)
		}
	}
	return true, nil
}

// Create invites an email with roles. Granting admin needs an admin
// actor; a pending invite or an active membership of the email, an
// invalid email, no roles and an unknown role are field errors.
func (s *Invites) Create(ctx context.Context, req InviteRequest) (*CreatedInvite, error) {
	var out *CreatedInvite
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := checkGrant(ctx, r, req.OrganizationID, req.Actor, req.RoleIDs); err != nil {
			return err
		}
		rolesOK, err := validRoles(ctx, r, req.OrganizationID, req.RoleIDs)
		if err != nil {
			return err
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
		v.Check(rolesOK, "roles", CodeInvalidRole)
		if err := v.Err(); err != nil {
			return err
		}
		token, err := NewSecret(s.d.Rand, secretBytes)
		if err != nil {
			return err
		}
		now := s.d.now()
		inv := access.NewInvite(core.NewID[core.InviteID](s.d.IDs), req.OrganizationID, email, req.RoleIDs, HashSecret(token), now.Add(s.cfg.Validity), now)
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

// Update replaces the roles of a pending invite (lago
// Invites::UpdateService).
func (s *Invites) Update(ctx context.Context, org core.OrganizationID, actor core.UserID, id core.InviteID, roleIDs []core.RoleID) (*access.Invite, error) {
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
		if err := checkGrant(ctx, r, org, actor, roleIDs); err != nil {
			return err
		}
		ok, err := validRoles(ctx, r, org, roleIDs)
		if err != nil {
			return err
		}
		if !ok {
			return fieldError("roles", CodeInvalidRole)
		}
		inv.RoleIDs, inv.UpdatedAt = access.NormalizeRoleIDs(roleIDs), s.d.now()
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
	return pendingInviteByHash(ctx, r, HashSecret(token))
}

// pendingInviteByHash returns the pending invite whose token has the
// SHA-256 tokenHash, or "invite_not_found".
func pendingInviteByHash(ctx context.Context, r port.Repos, tokenHash []byte) (*access.Invite, error) {
	if len(tokenHash) == 0 {
		return nil, notFound("invite")
	}
	inv, err := r.Invites.GetByTokenHash(ctx, tokenHash)
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
// invite's roles, and opens a session.
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
	created := errors.Is(err, core.ErrNotFound)
	switch {
	case created:
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
	// lago register_from_invite resolves every invite role with first!
	// (app/services/users_service.rb:108-109): a role deleted since the
	// invite fails the acceptance.
	if ok, err := validRoles(ctx, r, inv.OrganizationID, inv.RoleIDs); err != nil {
		return nil, err
	} else if !ok {
		return nil, notFound("role")
	}
	m := access.NewMembership(core.NewID[core.MembershipID](s.d.IDs), inv.OrganizationID, user.ID, inv.RoleIDs, now)
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
	return &SignedIn{User: user, Organization: org, Membership: m, newUser: created}, nil
}
