package app

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/billing/billingentities"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/store"
)

// organizationCreator creates the organization of a sign-up or a bootstrap
// with its default billing entity, whose code is the name parameterized
// with sep ("_" when empty).
//
// source: app/services/organizations/create_service.rb:22-35
type organizationCreator struct {
	base auth.BasicOrganizationCreator
	// entities creates the billing entity; it is set once the billing
	// services are built.
	entities *billingentities.Service
	// sep joins the words of the entity code.
	sep string
}

// CreateOrganization implements [auth.OrganizationCreator].
func (c *organizationCreator) CreateOrganization(ctx context.Context, r port.Repos, name string) (*organization.Organization, error) {
	o, err := c.base.CreateOrganization(ctx, r, name)
	if err != nil {
		return nil, err
	}
	if _, err := c.defaultEntity(ctx, r, o); err != nil {
		return nil, err
	}
	return o, nil
}

// defaultEntity creates the default billing entity of o unless it has one
// (lago signup.rake:18-20 finds or creates it). It runs in the transaction
// of r, which the billing entity service joins.
func (c *organizationCreator) defaultEntity(ctx context.Context, r port.Repos, o *organization.Organization) (bool, error) {
	_, err := r.BillingEntities.GetDefault(ctx, o.ID)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, core.ErrNotFound) {
		return false, fmt.Errorf("get default billing entity: %w", err)
	}
	sep := cmp.Or(c.sep, "_")
	code := entityCode(o.Name, sep)
	if code == "" {
		code = strings.ReplaceAll(o.Slug, "-", sep)
	}
	if _, err := c.entities.Create(ctx, o.ID, code, billingentities.Input{Name: core.Some(o.Name)}); err != nil {
		return false, fmt.Errorf("create default billing entity: %w", err)
	}
	return true, nil
}

// entityCode is name parameterized with sep, as lago's parameterize
// (create_service.rb:33 with "_", signup.rake:19 with "-"): lower-cased
// ASCII letters and digits, '-' and '_' kept, other runs joined by sep.
func entityCode(name, sep string) string {
	words := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return r >= unicode.MaxASCII || !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_'
	})
	return strings.Trim(strings.Join(words, sep), sep)
}

// BootstrapInput is what `tanvo admin bootstrap` ensures exists.
type BootstrapInput struct {
	// OrganizationName names the organization; an organization with the
	// slug of that name is reused.
	OrganizationName string
	// Email is the admin user's email; an existing user is reused, and
	// keeps its password.
	Email string
	// Password is the password of a created user.
	Password string
	// APIKey, when set, is the secret of an API key the organization must
	// have; otherwise the organization gets a generated key if it has none.
	APIKey string
}

// BootstrapResult reports what [Bootstrap] did.
type BootstrapResult struct {
	// Organization is the bootstrapped organization.
	Organization *organization.Organization
	// User is the admin user.
	User *access.User
	// Created lists what was created: organization, billing_entity, user,
	// membership, admin_role, api_key.
	Created []string
	// APIKey is the secret of a generated key, shown once; empty when no
	// key was generated.
	APIKey string
}

// Bootstrap ensures, over the database of cfg, an organization, its
// default billing entity, an admin user with a password, its admin
// membership and an API key. Every step finds before it creates, so it may
// run at each start. It is `tanvo admin bootstrap`; the schemas must be
// migrated.
//
// source: lib/tasks/signup.rake:5-35
func Bootstrap(ctx context.Context, cfg config.Config, in BootstrapInput, tp trace.TracerProvider, mp metric.MeterProvider) (*BootstrapResult, error) {
	pool, err := openPool(ctx, cfg.DatabaseURL, poolOptions{MaxConns: 2, Name: "bootstrap"}, tp, mp)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	if err := store.CheckSchema(ctx, pool); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	var opts []store.Option
	if cfg.DatabaseRowLevelSecurity {
		opts = append(opts, store.WithRowLevelSecurity())
	}
	return bootstrap(ctx, store.New(pool, opts...), core.SystemClock{}, auth.NewPasswordHasher(auth.DefaultArgon2Params(), rand.Reader), in)
}

// bootstrap is [Bootstrap] over tx, in one transaction.
func bootstrap(ctx context.Context, tx port.Transactor, clock core.Clock, hasher *auth.PasswordHasher, in BootstrapInput) (*BootstrapResult, error) {
	var v core.Validator
	v.Required("organization_name", strings.TrimSpace(in.OrganizationName))
	v.Required("email", in.Email)
	v.Required("password", in.Password)
	if in.APIKey != "" {
		v.Check(len(in.APIKey) >= access.APIKeyPrefixLength, "api_key", core.CodeInvalid)
	}
	if err := v.Err(); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	ids := core.UUIDv7Generator{}
	entities, err := billingentities.New(&kernel.Deps{Tx: tx, Clock: clock, IDs: ids}, nil)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	orgs := &organizationCreator{base: auth.BasicOrganizationCreator{Clock: clock, IDs: ids}, entities: entities, sep: "-"}
	out := &BootstrapResult{}
	err = tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		out.Created, out.APIKey = nil, ""
		now := clock.Now().UTC()
		user, err := r.Users.GetByEmail(ctx, access.NormalizeEmail(in.Email))
		if errors.Is(err, core.ErrNotFound) {
			hash, herr := hasher.Hash(in.Password)
			if herr != nil {
				return herr
			}
			user = access.NewUser(core.NewID[core.UserID](ids), in.Email, hash, now)
			if err := user.Validate(); err != nil {
				return err
			}
			if err := r.Users.Create(ctx, user); err != nil {
				return fmt.Errorf("create user: %w", err)
			}
			out.Created = append(out.Created, "user")
		} else if err != nil {
			return fmt.Errorf("get user: %w", err)
		}
		out.User = user

		// Organization names are not unique; the slug derived from the name
		// is (domain §4.1).
		o, err := r.Organizations.GetBySlug(ctx, organization.Slugify(in.OrganizationName))
		switch {
		case errors.Is(err, core.ErrNotFound):
			if o, err = orgs.base.CreateOrganization(ctx, r, in.OrganizationName); err != nil {
				return err
			}
			out.Created = append(out.Created, "organization")
		case err != nil:
			return fmt.Errorf("get organization: %w", err)
		}
		out.Organization = o
		switch made, err := orgs.defaultEntity(ctx, r, o); {
		case err != nil:
			return err
		case made:
			out.Created = append(out.Created, "billing_entity")
		}

		admin := access.BuiltInRoleID(permission.BuiltInRoleAdmin)
		m, err := r.Memberships.GetActiveByUser(ctx, o.ID, user.ID)
		switch {
		case errors.Is(err, core.ErrNotFound):
			m = access.NewMembership(core.NewID[core.MembershipID](ids), o.ID, user.ID, []core.RoleID{admin}, now)
			if err := r.Memberships.Create(ctx, m); err != nil {
				return fmt.Errorf("create membership: %w", err)
			}
			out.Created = append(out.Created, "membership")
		case err != nil:
			return fmt.Errorf("get membership: %w", err)
		case !slices.Contains(m.RoleIDs, admin):
			m.RoleIDs = access.NormalizeRoleIDs(append(m.RoleIDs, admin))
			m.UpdatedAt = now
			if err := r.Memberships.Update(ctx, m); err != nil {
				return fmt.Errorf("update membership: %w", err)
			}
			out.Created = append(out.Created, "admin_role")
		}

		created, secret, err := ensureAPIKey(ctx, r, o.ID, in.APIKey, ids, now)
		if err != nil {
			return err
		}
		if created {
			out.Created = append(out.Created, "api_key")
		}
		out.APIKey = secret
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	return out, nil
}

// ensureAPIKey makes org have the key secret, or, when secret is empty, any
// key, generating one; it returns whether a key was created and the
// secret of a generated one. A secret of another organization's key is
// a conflict.
func ensureAPIKey(ctx context.Context, r port.Repos, org core.OrganizationID, secret string, ids core.IDGenerator, now time.Time) (created bool, generated string, err error) {
	if secret == "" {
		page, err := r.APIKeys.List(ctx, org, access.APIKeyFilter{}, core.PageRequest{Size: 1})
		if err != nil {
			return false, "", fmt.Errorf("list api keys: %w", err)
		}
		if len(page.Items) > 0 {
			return false, "", nil
		}
		k, s, err := access.GenerateAPIKey(core.NewID[core.APIKeyID](ids), org, rand.Reader, now)
		if err != nil {
			return false, "", err
		}
		if err := r.APIKeys.Create(ctx, k); err != nil {
			return false, "", fmt.Errorf("create api key: %w", err)
		}
		return true, s, nil
	}
	switch k, err := r.APIKeys.GetByTokenHash(ctx, access.HashToken(secret)); {
	case err == nil && k.OrganizationID == org:
		return false, "", nil
	case err == nil:
		return false, "", fmt.Errorf("api key: %w", core.ErrConflict)
	case !errors.Is(err, core.ErrNotFound):
		return false, "", fmt.Errorf("get api key: %w", err)
	}
	k := &access.APIKey{
		ID: core.NewID[core.APIKeyID](ids), OrganizationID: org, TokenHash: access.HashToken(secret),
		Prefix: secret[:access.APIKeyPrefixLength], CreatedAt: now, UpdatedAt: now,
	}
	if err := k.Validate(); err != nil {
		return false, "", err
	}
	if err := r.APIKeys.Create(ctx, k); err != nil {
		return false, "", fmt.Errorf("create api key: %w", err)
	}
	return true, "", nil
}
