package auth

import (
	"cmp"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// DefaultStateTTL is how long a sign-in may take at the provider. Lago
// keeps the state 90 seconds.
const DefaultStateTTL = 10 * time.Minute

// Intent is what an OpenID Connect flow does once the identity is known.
type Intent = access.OIDCIntent

// Intents.
const (
	// IntentLogin signs in; organization connections auto-provision the
	// user and membership, Google requires an existing member.
	IntentLogin = access.OIDCIntentLogin
	// IntentSignUp registers a user and an organization (Google only, as
	// in lago).
	IntentSignUp = access.OIDCIntentSignUp
	// IntentAcceptInvite accepts an invite for the identity's email.
	IntentAcceptInvite = access.OIDCIntentAcceptInvite
)

// OIDCState is the server-side state of a flow between [OIDC.Begin] and
// [OIDC.Complete], stored in the access repositories
// ([port.Repos.OIDCStates]) under the SHA-256 of the state parameter, so
// that any replica can finish a flow.
type OIDCState = access.OIDCState

// OIDCConfig configures [OIDC].
type OIDCConfig struct {
	// Google is the installation's Google connection; nil disables Google.
	Google *Connection
	// Connections resolves the organizations' Okta, Entra ID and generic
	// connections.
	Connections ConnectionResolver
	// StateTTL bounds a flow; 0 uses [DefaultStateTTL].
	StateTTL time.Duration
	// HTTPClient reaches the providers. apps/server passes a client that
	// refuses private addresses unless allowed (lago
	// block_private_addresses); nil uses http.DefaultClient. Responses
	// are bounded to [MaxProviderResponseBytes] and requests to
	// [DefaultProviderTimeout] when the client sets no timeout.
	HTTPClient *http.Client
	// AllowInsecure accepts plain http issuers and endpoints, for
	// development; https is required otherwise.
	AllowInsecure bool
	// DiscoveryTTL, JWKSTTL and JWKSMinRefresh tune the provider caches;
	// zero uses [DefaultDiscoveryTTL], [DefaultJWKSTTL] and
	// [DefaultJWKSMinRefresh].
	DiscoveryTTL time.Duration
	// JWKSTTL is how long a key set is reused.
	JWKSTTL time.Duration
	// JWKSMinRefresh is the least time between unknown-key refetches.
	JWKSMinRefresh time.Duration
}

// OIDC signs users in with OpenID Connect providers (lago
// Auth::GoogleService, Auth::Okta::*, Auth::EntraId::*): one standard
// client of the authorization code flow with state, nonce and PKCE
// (S256), over discovered and cached provider metadata and keys. Google,
// Okta and Entra ID are presets of a [Connection]; a generic connection
// names its issuer and may tune scopes, claims and parameters.
type OIDC struct {
	d       Deps
	users   *Users
	invites *Invites
	cfg     OIDCConfig
	client  *http.Client

	mu        sync.Mutex
	providers map[string]*discovered
}

// NewOIDC returns the OpenID Connect service.
func NewOIDC(d Deps, users *Users, invites *Invites, cfg OIDCConfig) *OIDC {
	if cfg.StateTTL <= 0 {
		cfg.StateTTL = DefaultStateTTL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.DiscoveryTTL <= 0 {
		cfg.DiscoveryTTL = DefaultDiscoveryTTL
	}
	if cfg.JWKSTTL <= 0 {
		cfg.JWKSTTL = DefaultJWKSTTL
	}
	if cfg.JWKSMinRefresh <= 0 {
		cfg.JWKSMinRefresh = DefaultJWKSMinRefresh
	}
	return &OIDC{
		d: d, users: users, invites: invites, cfg: cfg, client: providerClient(cfg.HTTPClient),
		providers: map[string]*discovered{},
	}
}

// BeginRequest starts a flow.
type BeginRequest struct {
	// Method selects the provider; email_password is refused.
	Method organization.AuthenticationMethod
	// Intent is the flow's purpose; "" is [IntentLogin].
	Intent Intent
	// Email routes organization connections by domain; required except
	// for Google.
	Email string
	// InviteToken is the invite to accept ([IntentAcceptInvite]).
	InviteToken string
	// OrganizationName names the organization of a sign-up.
	OrganizationName string
}

// providerName is the provider's prefix in lago's error codes.
func providerName(m organization.AuthenticationMethod) string {
	if m == organization.AuthenticationMethodGoogleOAuth {
		return "google"
	}
	return string(m)
}

func emailDomain(email string) string {
	_, d, _ := strings.Cut(email, "@")
	return d
}

// connection resolves the connection of method for email.
func (s *OIDC) connection(ctx context.Context, method organization.AuthenticationMethod, email string) (*Connection, error) {
	if method == organization.AuthenticationMethodGoogleOAuth {
		if s.cfg.Google == nil {
			return nil, rule(CodeOIDCMissingSetup)
		}
		return s.cfg.Google, nil
	}
	if s.cfg.Connections == nil {
		return nil, rule(CodeDomainNotConfigured)
	}
	domain := emailDomain(email)
	cands, err := s.cfg.Connections.ByDomain(ctx, method, domain)
	if err != nil {
		return nil, fmt.Errorf("resolve connection: %w", err)
	}
	c := SelectConnection(cands, domain)
	if c == nil {
		return nil, rule(CodeDomainNotConfigured)
	}
	return c, nil
}

// oauth returns the OAuth client of c over its provider's metadata: the
// token endpoint authentication is the connection's or, without one, the
// provider's first of client_secret_basic and client_secret_post.
func (s *OIDC) oauth(ctx context.Context, c *Connection) (*oauth2.Config, *discovered, error) {
	d, err := s.provider(ctx, c.Issuer)
	if err != nil {
		s.d.logger().WarnContext(ctx, "oidc discovery", "issuer", c.Issuer, "error", err)
		return nil, nil, rule(CodeOIDCRequestError)
	}
	scopes := c.Scopes
	if scopes == nil {
		scopes = []string{"email", "profile"}
	}
	ep := d.provider.Endpoint()
	ep.AuthStyle = oauth2.AuthStyleInHeader
	if cmp.Or(c.TokenAuthMethod, d.tokenStyle) == integration.TokenAuthClientSecretPost {
		ep.AuthStyle = oauth2.AuthStyleInParams
	}
	return &oauth2.Config{
		ClientID: c.ClientID, ClientSecret: c.ClientSecret, RedirectURL: c.RedirectURL, Endpoint: ep,
		Scopes: append([]string{oidc.ScopeOpenID}, slices.DeleteFunc(slices.Clone(scopes), func(x string) bool { return x == oidc.ScopeOpenID })...),
	}, d, nil
}

// Begin validates the request, stores the flow's state and returns the
// provider's authorization URL. Organization connections are routed by
// the email's domain; an invite must be pending and, when the email is
// known, addressed to it.
func (s *OIDC) Begin(ctx context.Context, req BeginRequest) (string, error) {
	if req.Intent == "" {
		req.Intent = IntentLogin
	}
	if !req.Method.Valid() || req.Method == organization.AuthenticationMethodEmailPassword {
		return "", fieldError("method", core.CodeInvalid)
	}
	google := req.Method == organization.AuthenticationMethodGoogleOAuth
	email := access.NormalizeEmail(req.Email)
	if !google && email == "" {
		return "", fieldError("email", core.CodeRequired)
	}
	switch req.Intent {
	case IntentLogin:
	case IntentSignUp:
		if !google {
			return "", fieldError("intent", core.CodeInvalid)
		}
	case IntentAcceptInvite:
		if err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			inv, err := pendingInvite(ctx, r, req.InviteToken)
			if err != nil {
				return err
			}
			if email != "" && inv.Email != email {
				return rule(CodeInviteEmailMismatch)
			}
			return nil
		}); err != nil {
			return "", err
		}
	default:
		return "", fieldError("intent", core.CodeInvalid)
	}
	c, err := s.connection(ctx, req.Method, email)
	if err != nil {
		return "", err
	}
	oc, _, err := s.oauth(ctx, c)
	if err != nil {
		return "", err
	}
	state, err1 := NewSecret(s.d.Rand, secretBytes)
	nonce, err2 := NewSecret(s.d.Rand, secretBytes)
	verifier, err3 := NewSecret(s.d.Rand, secretBytes)
	if err := errors.Join(err1, err2, err3); err != nil {
		return "", err
	}
	now := s.d.now()
	st := &OIDCState{
		KeyHash: HashSecret(state), Method: req.Method, Intent: req.Intent, ConnectionID: c.ID, Email: email,
		OrganizationName: req.OrganizationName, Nonce: nonce, Verifier: verifier, ExpiresAt: now.Add(s.cfg.StateTTL), CreatedAt: now,
	}
	if req.Intent == IntentAcceptInvite {
		st.InviteTokenHash = HashSecret(req.InviteToken)
	}
	if err := st.Validate(); err != nil {
		return "", fmt.Errorf("oidc state: %w", err)
	}
	if err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error { return r.OIDCStates.Create(ctx, st) }); err != nil {
		return "", fmt.Errorf("store oidc state: %w", err)
	}
	opts := make([]oauth2.AuthCodeOption, 0, len(c.AuthParams)+3)
	for _, k := range slices.Sorted(maps.Keys(c.AuthParams)) {
		opts = append(opts, oauth2.SetAuthURLParam(k, c.AuthParams[k]))
	}
	opts = append(opts, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
	if email != "" {
		opts = append(opts, oauth2.SetAuthURLParam("login_hint", email))
	}
	return oc.AuthCodeURL(state, opts...), nil
}

// CallbackRequest finishes a flow with the provider's redirect.
type CallbackRequest struct {
	// State is the "state" query parameter.
	State string
	// Code is the "code" query parameter.
	Code string
	// Meta describes the client for the session.
	Meta SessionMeta
}

// Identity is the verified identity a provider returned.
type Identity struct {
	// Subject is the provider's stable user identifier.
	Subject string
	// Email is the normalized email.
	Email string
	// Name is the display name, if given.
	Name string
}

// Complete exchanges the code, verifies the ID token (signature, issuer,
// audience, expiry, nonce) and applies the flow's intent, then opens a
// session.
func (s *OIDC) Complete(ctx context.Context, req CallbackRequest) (*SignedIn, error) {
	if req.State == "" {
		return nil, rule(CodeStateNotFound)
	}
	var st *OIDCState
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		st, err = r.OIDCStates.Take(ctx, HashSecret(req.State), s.d.now())
		return err
	})
	if errors.Is(err, core.ErrNotFound) {
		return nil, rule(CodeStateNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("take oidc state: %w", err)
	}
	if req.Code == "" {
		return nil, rule(CodeCodeNotFound)
	}
	c, err := s.connection(ctx, st.Method, st.Email)
	if err != nil {
		return nil, err
	}
	if c.ID != st.ConnectionID {
		return nil, rule(CodeDomainNotConfigured)
	}
	if st.Intent == IntentAcceptInvite {
		if err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			_, err := pendingInviteByHash(ctx, r, st.InviteTokenHash)
			return err
		}); err != nil {
			return nil, err
		}
	}
	id, err := s.identity(ctx, c, st, req.Code)
	if err != nil {
		return nil, err
	}
	if st.Email != "" && id.Email != st.Email {
		return nil, rule(UserinfoErrorCode(providerName(st.Method)))
	}
	switch st.Intent {
	case IntentSignUp:
		out, err := s.users.register(ctx, id.Email, nil, st.OrganizationName)
		if err != nil {
			return nil, err
		}
		return s.users.open(ctx, out, st.Method, req.Meta)
	case IntentAcceptInvite:
		return s.acceptInvite(ctx, st, id, req.Meta)
	default:
		return s.login(ctx, c, st.Method, id, req.Meta)
	}
}

// identityClaims are an identity's claims, from the ID token completed
// by userinfo, read through the connection's claim mapping.
type identityClaims map[string]any

func (cl identityClaims) str(name string) string {
	v, _ := cl[name].(string)
	return v
}

// email is the connection's email claim, or its fallback claim when
// empty.
func (cl identityClaims) email(c *Connection) string {
	if e := cl.str(cmp.Or(c.EmailClaim, integration.DefaultEmailClaim)); e != "" || c.EmailFallbackClaim == "" {
		return e
	}
	return cl.str(c.EmailFallbackClaim)
}

// verified is the email_verified claim, a boolean or (as some providers
// send it) the string "true" or "false"; ok is false when it is missing
// or of another type.
func (cl identityClaims) verified() (verified, ok bool) {
	switch v := cl["email_verified"].(type) {
	case bool:
		return v, true
	case string:
		b, err := strconv.ParseBool(v)
		return b, err == nil
	default:
		return false, false
	}
}

// complete reports whether the claims satisfy the connection: an email
// and, when it must be verified, the email_verified claim.
func (cl identityClaims) complete(c *Connection) bool {
	_, hasVerified := cl.verified()
	return cl.email(c) != "" && (c.EmailVerification != EmailVerifiedRequired || hasVerified)
}

// numericDate reads a JWT NumericDate claim.
func (cl identityClaims) numericDate(name string) (time.Time, bool) {
	f, ok := cl[name].(float64)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(f), 0), true
}

// checkTimes checks exp (required), nbf and iat (required) with
// [ClockSkew] of leeway, and the authorized party: with several
// audiences azp must be present, and when present it must be the
// client (OpenID Connect Core §3.1.3.7).
func (cl identityClaims) checkTimes(now time.Time, idt *oidc.IDToken, clientID string) error {
	if idt.Expiry.IsZero() || !now.Before(idt.Expiry.Add(ClockSkew)) {
		return fmt.Errorf("id token expired at %v", idt.Expiry)
	}
	if nbf, ok := cl.numericDate("nbf"); ok && now.Add(ClockSkew).Before(nbf) {
		return fmt.Errorf("id token not valid before %v", nbf)
	}
	if iat, ok := cl.numericDate("iat"); !ok || now.Add(ClockSkew).Before(iat) {
		return fmt.Errorf("id token issued at %v, in the future or missing", iat)
	}
	azp := cl.str("azp")
	if (len(idt.Audience) > 1 && azp == "") || (azp != "" && azp != clientID) {
		return fmt.Errorf("id token authorized party %q is not the client", azp)
	}
	return nil
}

// identity exchanges the code with the PKCE verifier and verifies the ID
// token: the signature against the provider's keys with an accepted
// algorithm, the issuer, the audience and authorized party, the time
// claims and the nonce. Userinfo completes the claims when the email or
// a required email_verified is missing; its subject must be the token's.
func (s *OIDC) identity(ctx context.Context, c *Connection, st *OIDCState, code string) (Identity, error) {
	oc, d, err := s.oauth(ctx, c)
	if err != nil {
		return Identity{}, err
	}
	hctx := oidc.ClientContext(ctx, s.client)
	tok, err := oc.Exchange(hctx, code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		s.d.logger().WarnContext(ctx, "oidc code exchange", "issuer", c.Issuer, "error", err)
		return Identity{}, rule(CodeOIDCRequestError)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return Identity{}, rule(CodeOIDCInvalidToken)
	}
	verifier := oidc.NewVerifier(c.Issuer, d.keys, &oidc.Config{
		ClientID: c.ClientID, SupportedSigningAlgs: d.algs, SkipExpiryCheck: true, Now: s.d.Clock.Now,
	})
	idt, err := verifier.Verify(hctx, raw)
	if err != nil {
		s.d.logger().WarnContext(ctx, "oidc id token", "issuer", c.Issuer, "error", err)
		return Identity{}, rule(CodeOIDCInvalidToken)
	}
	cl := identityClaims{}
	if err := idt.Claims(&cl); err != nil {
		return Identity{}, rule(CodeOIDCInvalidToken)
	}
	if err := cl.checkTimes(s.d.now(), idt, c.ClientID); err != nil {
		s.d.logger().WarnContext(ctx, "oidc id token", "issuer", c.Issuer, "error", err)
		return Identity{}, rule(CodeOIDCInvalidToken)
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(st.Nonce)) != 1 {
		return Identity{}, rule(CodeOIDCInvalidToken)
	}
	if !cl.complete(c) && d.provider.UserInfoEndpoint() != "" {
		ui, err := d.provider.UserInfo(hctx, oauth2.StaticTokenSource(tok))
		if err != nil {
			s.d.logger().WarnContext(ctx, "oidc userinfo", "issuer", c.Issuer, "error", err)
			return Identity{}, rule(UserinfoErrorCode(providerName(c.Method)))
		}
		uc := identityClaims{}
		if err := ui.Claims(&uc); err != nil {
			return Identity{}, rule(UserinfoErrorCode(providerName(c.Method)))
		}
		if ui.Subject != idt.Subject {
			return Identity{}, rule(CodeOIDCInvalidToken)
		}
		for k, v := range uc {
			if cur, ok := cl[k]; !ok || cur == "" {
				cl[k] = v
			}
		}
	}
	email := access.NormalizeEmail(cl.email(c))
	if email == "" {
		return Identity{}, rule(UserinfoErrorCode(providerName(c.Method)))
	}
	switch verified, ok := cl.verified(); c.EmailVerification {
	case EmailVerifiedRequired:
		if !ok || !verified {
			return Identity{}, rule(CodeOIDCEmailNotVerified)
		}
	case EmailVerifiedIgnored:
	default:
		if ok && !verified {
			return Identity{}, rule(CodeOIDCEmailNotVerified)
		}
	}
	return Identity{Subject: idt.Subject, Email: email, Name: cl.str(cmp.Or(c.NameClaim, integration.DefaultNameClaim))}, nil
}

func (s *OIDC) acceptInvite(ctx context.Context, st *OIDCState, id Identity, meta SessionMeta) (*SignedIn, error) {
	var out *SignedIn
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		inv, err := pendingInviteByHash(ctx, r, st.InviteTokenHash)
		if err != nil {
			return err
		}
		if inv.Email != id.Email {
			return rule(CodeInviteEmailMismatch)
		}
		out, err = s.invites.accept(ctx, r, inv, st.Method, "")
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.invites.finish(ctx, out, st.Method, meta)
}

// login signs the identity in. The installation's Google connection only
// signs in existing members (lago user_does_not_exist); an organization
// connection links the identity to the user with the same email, creating
// an SSO-only user and a membership with the connection's default role
// when missing (lago find_or_create). A revoked membership is not revived.
// Either way one of the user's organizations must allow the method.
func (s *OIDC) login(ctx context.Context, c *Connection, method organization.AuthenticationMethod, id Identity, meta SessionMeta) (*SignedIn, error) {
	var (
		user *access.User
		ms   []*access.Membership
	)
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		now := s.d.now()
		var err error
		user, err = r.Users.GetByEmail(ctx, id.Email)
		switch {
		case errors.Is(err, core.ErrNotFound) && c.OrganizationID.IsZero():
			return rule(CodeUserDoesNotExist)
		case errors.Is(err, core.ErrNotFound):
			user = access.NewUser(core.NewID[core.UserID](s.d.IDs), id.Email, nil, now)
			if id.Name != "" {
				user.Name = &id.Name
			}
			if err := user.Validate(); err != nil {
				return err
			}
			if err := r.Users.Create(ctx, user); err != nil {
				return fmt.Errorf("create user: %w", err)
			}
		case err != nil:
			return fmt.Errorf("get user: %w", err)
		}
		if !c.OrganizationID.IsZero() {
			if err := s.provision(ctx, r, c, user, now); err != nil {
				return err
			}
		}
		if ms, err = r.Memberships.ListActiveByUser(ctx, user.ID); err != nil {
			return fmt.Errorf("list memberships: %w", err)
		}
		if len(ms) == 0 {
			return rule(CodeUserDoesNotExist)
		}
		if err := checkLoginMethod(ctx, r, user.ID, method); err != nil {
			return err
		}
		user.SignedIn(now)
		return r.Users.Update(ctx, user)
	})
	if err != nil {
		return nil, err
	}
	s.users.recordSignIn(ctx, user, ms, method)
	return s.users.open(ctx, &SignedIn{User: user}, method, meta)
}

// provision gives user an active membership in the connection's
// organization unless it already has one or had one revoked.
func (s *OIDC) provision(ctx context.Context, r port.Repos, c *Connection, user *access.User, now time.Time) error {
	_, err := r.Memberships.GetActiveByUser(ctx, c.OrganizationID, user.ID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, core.ErrNotFound) {
		return fmt.Errorf("get membership: %w", err)
	}
	page := core.PageRequest{}
	for {
		p, err := r.Memberships.List(ctx, c.OrganizationID, access.MembershipFilter{Status: access.MembershipStatusRevoked}, page)
		if err != nil {
			return fmt.Errorf("list memberships: %w", err)
		}
		if slices.ContainsFunc(p.Items, func(m *access.Membership) bool { return m.UserID == user.ID }) {
			return rule(CodeMembershipRevoked)
		}
		if p.NextToken == "" {
			break
		}
		page.Token = p.NextToken
	}
	m := access.NewMembership(core.NewID[core.MembershipID](s.d.IDs), c.OrganizationID, user.ID, []core.RoleID{c.DefaultRoleID}, now)
	if err := r.Memberships.Create(ctx, m); err != nil {
		return fmt.Errorf("create membership: %w", err)
	}
	return nil
}
