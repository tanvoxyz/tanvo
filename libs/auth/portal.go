package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// DefaultPortalTokenTTL is the validity of a customer portal token (domain
// §5.6).
const DefaultPortalTokenTTL = 12 * time.Hour

// portalTokenVersion is the payload format version.
const portalTokenVersion = 1

// minPortalKeyLength is the minimum HMAC key size in bytes.
const minPortalKeyLength = 32

// PortalClaims is what a verified portal token grants: one customer of
// one organization until ExpiresAt.
type PortalClaims struct {
	// OrganizationID is the customer's organization.
	OrganizationID core.OrganizationID `json:"org"`
	// CustomerID is the only customer the token gives access to.
	CustomerID core.CustomerID `json:"cus"`
	// IssuedAt is the issue instant.
	IssuedAt time.Time `json:"iat"`
	// ExpiresAt is the end of validity.
	ExpiresAt time.Time `json:"exp"`
}

type portalPayload struct {
	Version int    `json:"v"`
	KeyID   string `json:"kid"`
	PortalClaims
}

// PortalTokens issues and verifies stateless customer portal tokens:
// base64url(payload) "." base64url(HMAC-SHA256(payload)). Several keys may
// be configured for rotation; tokens are signed with the current one and
// verified with the key their kid names.
type PortalTokens struct {
	keys    map[string][]byte
	current string
	ttl     time.Duration
	clock   core.Clock
	front   string
}

// PortalConfig configures [PortalTokens].
type PortalConfig struct {
	// Keys are the HMAC keys by key ID, each at least 32 bytes.
	Keys map[string][]byte
	// CurrentKeyID names the signing key.
	CurrentKeyID string
	// TTL is the token validity; 0 uses [DefaultPortalTokenTTL].
	TTL time.Duration
	// FrontURL is the web console's base URL for [PortalTokens.URL].
	FrontURL string
}

// NewPortalTokens validates cfg and returns the token service.
func NewPortalTokens(cfg PortalConfig, clock core.Clock) (*PortalTokens, error) {
	if _, ok := cfg.Keys[cfg.CurrentKeyID]; !ok {
		return nil, fmt.Errorf("portal tokens: current key %q is not configured", cfg.CurrentKeyID)
	}
	for id, k := range cfg.Keys {
		if id == "" || strings.ContainsAny(id, ".") {
			return nil, fmt.Errorf("portal tokens: invalid key id %q", id)
		}
		if len(k) < minPortalKeyLength {
			return nil, fmt.Errorf("portal tokens: key %q is shorter than %d bytes", id, minPortalKeyLength)
		}
	}
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = DefaultPortalTokenTTL
	}
	return &PortalTokens{keys: maps.Clone(cfg.Keys), current: cfg.CurrentKeyID, ttl: ttl, clock: clock, front: strings.TrimRight(cfg.FrontURL, "/")}, nil
}

func (t *PortalTokens) sign(kid string, payload []byte) []byte {
	mac := hmac.New(sha256.New, t.keys[kid])
	mac.Write([]byte("tanvo.portal.v1."))
	mac.Write(payload)
	return mac.Sum(nil)
}

// Issue returns a token for customer of org, valid for the configured TTL.
func (t *PortalTokens) Issue(org core.OrganizationID, customer core.CustomerID) (string, PortalClaims, error) {
	if org.IsZero() || customer.IsZero() {
		return "", PortalClaims{}, errors.New("portal token: organization and customer are required")
	}
	now := t.clock.Now().UTC().Truncate(time.Second)
	c := PortalClaims{OrganizationID: org, CustomerID: customer, IssuedAt: now, ExpiresAt: now.Add(t.ttl)}
	payload, err := json.Marshal(portalPayload{Version: portalTokenVersion, KeyID: t.current, PortalClaims: c})
	if err != nil {
		return "", PortalClaims{}, fmt.Errorf("portal token: %w", err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(t.sign(t.current, payload)), c, nil
}

// Verify checks the signature in constant time and the expiry of token and
// returns its claims; any failure is [ErrUnauthenticated].
func (t *PortalTokens) Verify(token string) (PortalClaims, error) {
	enc := base64.RawURLEncoding
	p, s, ok := strings.Cut(token, ".")
	if !ok {
		return PortalClaims{}, ErrUnauthenticated
	}
	payload, err1 := enc.DecodeString(p)
	sig, err2 := enc.DecodeString(s)
	if err1 != nil || err2 != nil {
		return PortalClaims{}, ErrUnauthenticated
	}
	var pl portalPayload
	if err := json.Unmarshal(payload, &pl); err != nil || pl.Version != portalTokenVersion {
		return PortalClaims{}, ErrUnauthenticated
	}
	if _, ok := t.keys[pl.KeyID]; !ok || !hmac.Equal(sig, t.sign(pl.KeyID, payload)) {
		return PortalClaims{}, ErrUnauthenticated
	}
	if !t.clock.Now().Before(pl.ExpiresAt) || pl.OrganizationID.IsZero() || pl.CustomerID.IsZero() {
		return PortalClaims{}, ErrUnauthenticated
	}
	return pl.PortalClaims, nil
}

// GenerateURL returns the portal URL of customer (lago
// CustomerPortal::GenerateUrlService): FrontURL "/customer-portal/" token.
// An unknown customer fails with "customer_not_found".
func (t *PortalTokens) GenerateURL(ctx context.Context, tx port.Transactor, org core.OrganizationID, customer core.CustomerID) (string, error) {
	err := tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		_, err := r.Customers.Get(ctx, org, customer)
		if errors.Is(err, core.ErrNotFound) {
			return notFound("customer")
		}
		return err
	})
	if err != nil {
		return "", err
	}
	tok, _, err := t.Issue(org, customer)
	if err != nil {
		return "", err
	}
	return t.front + "/customer-portal/" + url.PathEscape(tok), nil
}
