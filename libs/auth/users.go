package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// OrganizationCreator creates the organization of a self sign-up inside
// the registration transaction. apps/server provides one that also creates
// the default billing entity (lago Organizations::CreateService).
type OrganizationCreator interface {
	// CreateOrganization creates and returns an organization named name.
	CreateOrganization(ctx context.Context, r port.Repos, name string) (*organization.Organization, error)
}

// BasicOrganizationCreator creates the organization alone, suffixing the
// slug when it is taken.
type BasicOrganizationCreator struct {
	// Clock and IDs default to the service's.
	Clock core.Clock
	// IDs generates the organization's identifier.
	IDs core.IDGenerator
}

// CreateOrganization implements [OrganizationCreator].
func (c BasicOrganizationCreator) CreateOrganization(ctx context.Context, r port.Repos, name string) (*organization.Organization, error) {
	o := organization.New(core.NewID[core.OrganizationID](c.IDs), name, c.Clock.Now().UTC())
	if err := o.Validate(); err != nil {
		return nil, err
	}
	base := o.Slug
	for i := 2; ; i++ {
		err := r.Organizations.Create(ctx, o)
		if err == nil {
			return o, nil
		}
		if !errors.Is(err, core.ErrConflict) || i > 100 {
			return nil, fmt.Errorf("create organization: %w", err)
		}
		suffix := fmt.Sprintf("-%d", i)
		o.Slug = strings.TrimRight(base[:min(len(base), 63-len(suffix))], "-") + suffix
	}
}

// UsersConfig configures [Users].
type UsersConfig struct {
	// SignupDisabled refuses self sign-up (lago LAGO_DISABLE_SIGNUP).
	SignupDisabled bool
	// Organizations creates the organization of a sign-up.
	Organizations OrganizationCreator
}

// SignedIn is the outcome of a sign-in or sign-up: the user, the session
// and its token.
type SignedIn struct {
	// User is the signed-in user.
	User *access.User
	// Organization is the organization created by a sign-up or joined by
	// an invite; nil for a plain sign-in.
	Organization *organization.Organization
	// Membership is the membership created by a sign-up or an invite.
	Membership *access.Membership
	// Session is the opened session.
	Session *Session
	// Token is the session token, shown once.
	Token string
}

// Users registers users and signs them in with a password (lago
// UsersService).
type Users struct {
	d        Deps
	sessions *Sessions
	hasher   *PasswordHasher
	cfg      UsersConfig
}

// NewUsers returns the user service.
func NewUsers(d Deps, sessions *Sessions, hasher *PasswordHasher, cfg UsersConfig) *Users {
	if cfg.Organizations == nil {
		cfg.Organizations = BasicOrganizationCreator{Clock: d.Clock, IDs: d.IDs}
	}
	return &Users{d: d, sessions: sessions, hasher: hasher, cfg: cfg}
}

// Login signs in with email and password. Unknown emails, wrong
// passwords, SSO-only users and users without an active membership all
// fail with [CodeIncorrectLoginOrPassword]; a user none of whose
// organizations allow passwords fails with [CodeLoginMethodNotAuthorized].
// A hash with outdated parameters is upgraded.
func (u *Users) Login(ctx context.Context, email, password string, meta SessionMeta) (*SignedIn, error) {
	if strings.ContainsRune(email, 0) || strings.ContainsRune(password, 0) {
		return nil, rule(CodeIncorrectLoginOrPassword)
	}
	var (
		user *access.User
		ms   []*access.Membership
	)
	err := u.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		user, err = r.Users.GetByEmail(ctx, access.NormalizeEmail(email))
		if errors.Is(err, core.ErrNotFound) {
			return rule(CodeIncorrectLoginOrPassword)
		}
		if err != nil {
			return fmt.Errorf("get user: %w", err)
		}
		if !user.HasPassword() {
			return rule(CodeIncorrectLoginOrPassword)
		}
		ok, rehash, err := u.hasher.Verify(user.PasswordHash, password)
		if err != nil {
			return fmt.Errorf("verify password: %w", err)
		}
		if !ok {
			return rule(CodeIncorrectLoginOrPassword)
		}
		ms, err = r.Memberships.ListActiveByUser(ctx, user.ID)
		if err != nil {
			return fmt.Errorf("list memberships: %w", err)
		}
		if len(ms) == 0 {
			return rule(CodeIncorrectLoginOrPassword)
		}
		if err := checkLoginMethod(ctx, r, user.ID, organization.AuthenticationMethodEmailPassword); err != nil {
			return err
		}
		if rehash {
			if user.PasswordHash, err = u.hasher.Hash(password); err != nil {
				return err
			}
		}
		user.SignedIn(u.d.now())
		return r.Users.Update(ctx, user)
	})
	if err != nil {
		return nil, err
	}
	u.recordSignIn(ctx, user, ms, organization.AuthenticationMethodEmailPassword)
	return u.open(ctx, &SignedIn{User: user}, organization.AuthenticationMethodEmailPassword, meta)
}

func (u *Users) open(ctx context.Context, s *SignedIn, method organization.AuthenticationMethod, meta SessionMeta) (*SignedIn, error) {
	tok, sess, err := u.sessions.Issue(ctx, s.User.ID, method, meta)
	if err != nil {
		return nil, err
	}
	s.Token, s.Session = tok, sess
	return s, nil
}

// recordSignIn logs a sign-in in every organization of the user.
func (u *Users) recordSignIn(ctx context.Context, user *access.User, ms []*access.Membership, method organization.AuthenticationMethod) {
	for _, m := range ms {
		u.d.record(ctx, SecurityEvent{OrganizationID: m.OrganizationID, UserID: user.ID, Type: SecurityLogUser, Event: EventUserSignedIn, Resources: map[string]any{
			"email": user.Email, "login_method": string(method),
		}})
	}
}

// Register creates a user with password, an organization named orgName
// and an admin membership, and signs the user in.
func (u *Users) Register(ctx context.Context, email, password, orgName string, meta SessionMeta) (*SignedIn, error) {
	if password == "" {
		return nil, fieldError("password", core.CodeRequired)
	}
	hash, err := u.hasher.Hash(password)
	if err != nil {
		return nil, err
	}
	s, err := u.register(ctx, email, hash, orgName)
	if err != nil {
		return nil, err
	}
	return u.open(ctx, s, organization.AuthenticationMethodEmailPassword, meta)
}

// register creates the user (passwordHash nil for SSO-only users), the
// organization and the admin membership.
func (u *Users) register(ctx context.Context, email string, passwordHash []byte, orgName string) (*SignedIn, error) {
	if u.cfg.SignupDisabled {
		return nil, rule(CodeSignupDisabled)
	}
	out := &SignedIn{}
	err := u.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		email = access.NormalizeEmail(email)
		if _, err := r.Users.GetByEmail(ctx, email); err == nil {
			return fieldError("email", CodeUserAlreadyExists)
		} else if !errors.Is(err, core.ErrNotFound) {
			return fmt.Errorf("get user: %w", err)
		}
		now := u.d.now()
		user := access.NewUser(core.NewID[core.UserID](u.d.IDs), email, passwordHash, now)
		if err := user.Validate(); err != nil {
			return err
		}
		user.SignedIn(now)
		if err := r.Users.Create(ctx, user); err != nil {
			if errors.Is(err, core.ErrConflict) {
				return fieldError("email", CodeUserAlreadyExists)
			}
			return fmt.Errorf("create user: %w", err)
		}
		org, err := u.cfg.Organizations.CreateOrganization(ctx, r, orgName)
		if err != nil {
			return err
		}
		m := access.NewMembership(core.NewID[core.MembershipID](u.d.IDs), org.ID, user.ID, access.BuiltInRoleID(permission.BuiltInRoleAdmin), now)
		if err := r.Memberships.Create(ctx, m); err != nil {
			return fmt.Errorf("create membership: %w", err)
		}
		out.User, out.Organization, out.Membership = user, org, m
		return nil
	})
	if err != nil {
		return nil, err
	}
	u.d.record(ctx, SecurityEvent{OrganizationID: out.Organization.ID, UserID: out.User.ID, Type: SecurityLogUser, Event: EventUserSignedUp, Resources: map[string]any{
		"email": out.User.Email, "roles": []string{string(permission.BuiltInRoleAdmin)},
	}})
	return out, nil
}

// setPassword hashes password into user.
func (u *Users) setPassword(user *access.User, password string) error {
	hash, err := u.hasher.Hash(password)
	if err != nil {
		return err
	}
	user.PasswordHash = hash
	user.UpdatedAt = u.d.now()
	return nil
}
