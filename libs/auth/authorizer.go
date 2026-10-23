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

// PrincipalKind tells what authenticated a request.
type PrincipalKind int

// Principal kinds.
const (
	// PrincipalNone is the zero value: nothing authenticated.
	PrincipalNone PrincipalKind = iota
	// PrincipalAPIKey is an organization API key.
	PrincipalAPIKey
	// PrincipalUser is a user session acting in one organization.
	PrincipalUser
	// PrincipalPortal is a customer portal token.
	PrincipalPortal
)

// Principal is who performs a request, scoped to one organization.
type Principal struct {
	// Kind tells which of the fields below are set.
	Kind PrincipalKind
	// OrganizationID is the organization the request acts in.
	OrganizationID core.OrganizationID
	// APIKey is the key of a [PrincipalAPIKey].
	APIKey *access.APIKey
	// User is the user of a [PrincipalUser].
	User *access.User
	// Membership is the user's active membership in OrganizationID.
	Membership *access.Membership
	// Roles are the membership's roles; the user holds the union of their
	// permissions.
	Roles []*access.Role
	// Session is the user's session, when authenticated by one.
	Session *Session
	// CustomerID is the customer of a [PrincipalPortal].
	CustomerID core.CustomerID
}

// APIKeyPrincipal returns the principal of an authenticated key.
func APIKeyPrincipal(k *access.APIKey) Principal {
	return Principal{Kind: PrincipalAPIKey, OrganizationID: k.OrganizationID, APIKey: k}
}

// PortalPrincipal returns the principal of verified portal claims.
func PortalPrincipal(c PortalClaims) Principal {
	return Principal{Kind: PrincipalPortal, OrganizationID: c.OrganizationID, CustomerID: c.CustomerID}
}

// apiResourceOverride maps permission resources whose API key resource is
// not their singular form.
func apiResourceOverride(res string) (permission.APIResource, bool) {
	switch res {
	case "addons":
		return permission.APIResourceAddOn, true
	case "analytics":
		return permission.APIResourceAnalytic, true
	case "audit_logs":
		return permission.APIResourceActivityLog, true
	case "billing_entities":
		return permission.APIResourceBillingEntity, true
	case "charges":
		return permission.APIResourcePlan, true
	case "organization:taxes":
		return permission.APIResourceTax, true
	case "organization", "organization:invoices", "organization:emails":
		return permission.APIResourceOrganization, true
	case "developers":
		return permission.APIResourceWebhookEndpoint, true
	default:
		return "", false
	}
}

// APIResourceFor returns the API key resource and level that grant the
// role permission p ("customers:read" → customer, read; "coupons:attach" →
// applied_coupon, write). Permissions with no API counterpart (members,
// roles, integrations, …) return false: only a full-access key holds them.
func APIResourceFor(p permission.Permission) (permission.APIResource, permission.Level, bool) {
	res, action := p.Resource(), p.Action()
	level := permission.LevelForAction(action)
	if res == "coupons" && (action == "attach" || action == "detach") {
		return permission.APIResourceAppliedCoupon, level, true
	}
	if r, ok := apiResourceOverride(res); ok {
		return r, level, true
	}
	var r permission.APIResource
	switch {
	case strings.HasSuffix(res, "ies"):
		r = permission.APIResource(strings.TrimSuffix(res, "ies") + "y")
	default:
		r = permission.APIResource(strings.TrimSuffix(res, "s"))
	}
	if !r.Valid() {
		return "", "", false
	}
	return r, level, true
}

// Authorizer checks a principal's permissions and resolves user
// principals. The zero value can check; [Authorizer.UserPrincipal] needs a
// transactor.
type Authorizer struct {
	tx port.Transactor
}

// NewAuthorizer returns an authorizer reading memberships through tx.
func NewAuthorizer(tx port.Transactor) *Authorizer { return &Authorizer{tx: tx} }

// Check returns nil when p holds permission perm, and
// [ErrPermissionDenied] otherwise. A user holds the union of its roles'
// permissions (lago Membership#permissions_hash); an API key holds perm when its resource and level allow
// [APIResourceFor] perm (an empty permission map is full access); a portal
// principal holds no role permission (see [Authorizer.CheckCustomer]).
func (a *Authorizer) Check(p Principal, perm permission.Permission) error {
	switch p.Kind {
	case PrincipalUser:
		if p.Membership != nil && p.Membership.Active() && access.AnyGrants(p.Roles, perm) {
			return nil
		}
	case PrincipalAPIKey:
		if p.APIKey == nil {
			break
		}
		if len(p.APIKey.Permissions) == 0 {
			return nil
		}
		if res, level, ok := APIResourceFor(perm); ok && p.APIKey.Permits(res, level) {
			return nil
		}
	case PrincipalNone:
		return ErrUnauthenticated
	}
	return fmt.Errorf("%w: %s", ErrPermissionDenied, perm)
}

// CheckAPI returns nil when p may perform a public API request needing
// level on resource. Only API keys pass: users are checked with
// [Authorizer.Check].
func (a *Authorizer) CheckAPI(p Principal, resource permission.APIResource, level permission.Level) error {
	if p.Kind == PrincipalAPIKey && p.APIKey != nil && p.APIKey.Permits(resource, level) {
		return nil
	}
	if p.Kind == PrincipalNone {
		return ErrUnauthenticated
	}
	return fmt.Errorf("%w: %s:%s", ErrPermissionDenied, resource, level)
}

// CheckCustomer returns nil when p may act on customer of org: a portal
// principal only on its own customer.
func (a *Authorizer) CheckCustomer(p Principal, org core.OrganizationID, customer core.CustomerID) error {
	if p.Kind == PrincipalPortal && p.OrganizationID == org && p.CustomerID == customer {
		return nil
	}
	return fmt.Errorf("%w: customer %s", ErrPermissionDenied, customer)
}

// UserPrincipal resolves the principal of a session acting in org: the
// user must have an active membership there and org must allow the
// session's sign-in method (lago CurrentOrganization
// accessible_by_current_session). A missing membership fails with
// [ErrPermissionDenied]; a refused method with
// [CodeLoginMethodNotAuthorized].
func (a *Authorizer) UserPrincipal(ctx context.Context, sess *Session, org core.OrganizationID) (Principal, error) {
	var p Principal
	err := a.tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		u, err := r.Users.Get(ctx, sess.UserID)
		if err != nil {
			return fmt.Errorf("get user: %w", err)
		}
		m, err := r.Memberships.GetActiveByUser(ctx, org, u.ID)
		if errors.Is(err, core.ErrNotFound) {
			return fmt.Errorf("%w: no membership in organization %s", ErrPermissionDenied, org)
		}
		if err != nil {
			return fmt.Errorf("get membership: %w", err)
		}
		o, err := r.Organizations.Get(ctx, org)
		if err != nil {
			return fmt.Errorf("get organization: %w", err)
		}
		if sess.Method != "" && !o.AllowsAuthentication(sess.Method) {
			return fieldError(string(sess.Method), CodeLoginMethodNotAuthorized)
		}
		roles, err := membershipRoles(ctx, r, m)
		if err != nil {
			return err
		}
		p = Principal{Kind: PrincipalUser, OrganizationID: org, User: u, Membership: m, Roles: roles, Session: sess}
		return nil
	})
	return p, err
}

// membershipRoles returns the roles of m. A role deleted since it was
// assigned is skipped: it grants nothing.
func membershipRoles(ctx context.Context, r port.Repos, m *access.Membership) ([]*access.Role, error) {
	out := make([]*access.Role, 0, len(m.RoleIDs))
	for _, id := range m.RoleIDs {
		ro, err := r.Roles.Get(ctx, m.OrganizationID, id)
		if errors.Is(err, core.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get role: %w", err)
		}
		out = append(out, ro)
	}
	return out, nil
}

// allowedMethods returns the union of the sign-in methods of the
// organizations where memberships are active.
func allowedMethods(ctx context.Context, r port.Repos, ms []*access.Membership) (map[organization.AuthenticationMethod]bool, error) {
	out := map[organization.AuthenticationMethod]bool{}
	for _, m := range ms {
		o, err := r.Organizations.Get(ctx, m.OrganizationID)
		if err != nil {
			return nil, fmt.Errorf("get organization: %w", err)
		}
		for _, am := range o.AuthenticationMethods {
			out[am] = true
		}
	}
	return out, nil
}

// checkLoginMethod applies lago's rule: at least one organization where
// the user is an active member allows method.
func checkLoginMethod(ctx context.Context, r port.Repos, user core.UserID, method organization.AuthenticationMethod) error {
	ms, err := r.Memberships.ListActiveByUser(ctx, user)
	if err != nil {
		return fmt.Errorf("list memberships: %w", err)
	}
	allowed, err := allowedMethods(ctx, r, ms)
	if err != nil {
		return err
	}
	if !allowed[method] {
		return fieldError(string(method), CodeLoginMethodNotAuthorized)
	}
	return nil
}
