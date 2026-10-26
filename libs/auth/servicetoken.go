package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	accessv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/access/v1"
)

// DefaultServiceTokenTTL is the validity of a service token.
const DefaultServiceTokenTTL = 15 * time.Minute

// ServiceTokenAudienceDataAPI is the audience of the Data API's tokens.
const ServiceTokenAudienceDataAPI = "data_api"

// serviceTokenVersion is the claims format version.
const serviceTokenVersion = 1

// serviceTokenContext prefixes the signed bytes, so that a signature made
// for another purpose with the same key never verifies as a token.
const serviceTokenContext = "tanvo.service_token.v1."

// maxServiceTokenKeyIDLength bounds the key IDs.
const maxServiceTokenKeyIDLength = 64

// ServiceTokenKey is an Ed25519 signing key of service tokens.
type ServiceTokenKey struct {
	// ID names the key in the tokens it signs.
	ID string
	// PrivateKey signs the tokens.
	PrivateKey ed25519.PrivateKey
}

// ServiceTokenPublicKey is the public half of a [ServiceTokenKey], with
// which an enterprise service verifies the tokens offline.
type ServiceTokenPublicKey struct {
	// ID names the key.
	ID string
	// PublicKey verifies the signatures.
	PublicKey ed25519.PublicKey
}

// validServiceTokenKeyID reports whether id is 1 to 64 letters, digits,
// "-" or "_": it never contains the separators of the key lists.
func validServiceTokenKeyID(id string) bool {
	if id == "" || len(id) > maxServiceTokenKeyIDLength {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

// GenerateServiceTokenKey returns a new key named id, or a random 8-digit
// hexadecimal ID when id is empty, from the entropy of rand.
func GenerateServiceTokenKey(rand io.Reader, id string) (ServiceTokenKey, error) {
	if id == "" {
		b := make([]byte, 4)
		if _, err := io.ReadFull(rand, b); err != nil {
			return ServiceTokenKey{}, fmt.Errorf("service token key: %w", err)
		}
		id = hex.EncodeToString(b)
	}
	if !validServiceTokenKeyID(id) {
		return ServiceTokenKey{}, fmt.Errorf("service token key: invalid key id %q: want 1 to %d letters, digits, - or _", id, maxServiceTokenKeyIDLength)
	}
	_, priv, err := ed25519.GenerateKey(rand)
	if err != nil {
		return ServiceTokenKey{}, fmt.Errorf("service token key: %w", err)
	}
	return ServiceTokenKey{ID: id, PrivateKey: priv}, nil
}

// ParseServiceTokenKey parses "<key id>:<seed>", where seed is the
// base64url encoding without padding of a 32-byte Ed25519 seed.
func ParseServiceTokenKey(entry string) (ServiceTokenKey, error) {
	id, seed, ok := strings.Cut(strings.TrimSpace(entry), ":")
	if !ok {
		return ServiceTokenKey{}, errors.New("service token key: want <key id>:<seed>")
	}
	if !validServiceTokenKeyID(id) {
		return ServiceTokenKey{}, fmt.Errorf("service token key: invalid key id %q: want 1 to %d letters, digits, - or _", id, maxServiceTokenKeyIDLength)
	}
	b, err := base64.RawURLEncoding.DecodeString(seed)
	if err != nil {
		return ServiceTokenKey{}, fmt.Errorf("service token key %q: want the seed in base64url without padding: %w", id, err)
	}
	if len(b) != ed25519.SeedSize {
		return ServiceTokenKey{}, fmt.Errorf("service token key %q: got a %d-byte seed, want %d bytes", id, len(b), ed25519.SeedSize)
	}
	return ServiceTokenKey{ID: id, PrivateKey: ed25519.NewKeyFromSeed(b)}, nil
}

// ParseServiceTokenKeys parses the entries of [ParseServiceTokenKey],
// skipping blank ones; key IDs must be distinct.
func ParseServiceTokenKeys(entries []string) ([]ServiceTokenKey, error) {
	var out []ServiceTokenKey
	seen := map[string]bool{}
	for _, e := range entries {
		if strings.TrimSpace(e) == "" {
			continue
		}
		k, err := ParseServiceTokenKey(e)
		if err != nil {
			return nil, err
		}
		if seen[k.ID] {
			return nil, fmt.Errorf("service token key: duplicate key id %q", k.ID)
		}
		seen[k.ID] = true
		out = append(out, k)
	}
	return out, nil
}

// Encode returns "<key id>:<seed>", the form [ParseServiceTokenKey]
// reads. It is a secret.
func (k ServiceTokenKey) Encode() string {
	return k.ID + ":" + base64.RawURLEncoding.EncodeToString(k.PrivateKey.Seed())
}

// Public returns the public half of k.
func (k ServiceTokenKey) Public() ServiceTokenPublicKey {
	pub, _ := k.PrivateKey.Public().(ed25519.PublicKey)
	return ServiceTokenPublicKey{ID: k.ID, PublicKey: pub}
}

// String returns "<key id>:<public key>", the public key in base64url
// without padding.
func (k ServiceTokenPublicKey) String() string {
	return k.ID + ":" + base64.RawURLEncoding.EncodeToString(k.PublicKey)
}

// ServiceClaims are the claims of a service token
// (tanvo.access.v1.ServiceTokenClaims).
type ServiceClaims struct {
	// KeyID names the signing key.
	KeyID string
	// Audience is the service the token is for.
	Audience string
	// OrganizationID is the organization the caller acts in.
	OrganizationID core.OrganizationID
	// Subject is the caller: "user:<id>" or "api_key:<id>".
	Subject string
	// Permissions are the caller's permissions among those the audience
	// needs, sorted.
	Permissions []permission.Permission
	// IssuedAt is the issue instant.
	IssuedAt time.Time
	// ExpiresAt is the end of validity.
	ExpiresAt time.Time
}

// ServiceTokenConfig configures [ServiceTokens].
type ServiceTokenConfig struct {
	// Keys are the configured keys; the first signs, the others still
	// verify during a rotation.
	Keys []ServiceTokenKey
	// TTL is the token validity; 0 uses [DefaultServiceTokenTTL].
	TTL time.Duration
}

// ServiceTokens issues and verifies the service tokens of the enterprise
// services (tanvo.access.v1.ServiceTokenClaims): base64url(claims) "."
// base64url(signature) without padding, where claims is the deterministic
// binary encoding of the claims message and signature the Ed25519
// signature of "tanvo.service_token.v1." followed by claims. Tokens are
// signed with the current key and verified with the key their key_id
// names, so several keys may be configured for rotation.
type ServiceTokens struct {
	keys    map[string]ed25519.PrivateKey
	order   []string
	current string
	ttl     time.Duration
	clock   core.Clock
}

// NewServiceTokens validates cfg and returns the token service.
func NewServiceTokens(cfg ServiceTokenConfig, clock core.Clock) (*ServiceTokens, error) {
	if len(cfg.Keys) == 0 {
		return nil, errors.New("service tokens: no key is configured")
	}
	t := &ServiceTokens{keys: map[string]ed25519.PrivateKey{}, current: cfg.Keys[0].ID, ttl: cfg.TTL, clock: clock}
	for _, k := range cfg.Keys {
		if !validServiceTokenKeyID(k.ID) {
			return nil, fmt.Errorf("service tokens: invalid key id %q", k.ID)
		}
		if len(k.PrivateKey) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("service tokens: key %q is not an Ed25519 private key", k.ID)
		}
		if _, dup := t.keys[k.ID]; dup {
			return nil, fmt.Errorf("service tokens: duplicate key id %q", k.ID)
		}
		t.keys[k.ID] = slices.Clone(k.PrivateKey)
		t.order = append(t.order, k.ID)
	}
	if t.ttl <= 0 {
		t.ttl = DefaultServiceTokenTTL
	}
	return t, nil
}

// PublicKeys returns the public keys of the configured keys, the signing
// key first.
func (t *ServiceTokens) PublicKeys() []ServiceTokenPublicKey {
	out := make([]ServiceTokenPublicKey, len(t.order))
	for i, id := range t.order {
		out[i] = ServiceTokenKey{ID: id, PrivateKey: t.keys[id]}.Public()
	}
	return out
}

// Issue returns a token for subject of org with perms (sorted and
// deduplicated) for audience, signed with the current key and valid for
// the configured TTL.
func (t *ServiceTokens) Issue(audience string, org core.OrganizationID, subject string, perms []permission.Permission) (string, ServiceClaims, error) {
	if audience == "" || org.IsZero() || subject == "" {
		return "", ServiceClaims{}, errors.New("service token: audience, organization and subject are required")
	}
	now := t.clock.Now().UTC().Truncate(time.Second)
	perms = slices.Clone(perms)
	slices.Sort(perms)
	perms = slices.Compact(perms)
	c := ServiceClaims{
		KeyID: t.current, Audience: audience, OrganizationID: org, Subject: subject,
		Permissions: perms, IssuedAt: now, ExpiresAt: now.Add(t.ttl),
	}
	names := make([]string, len(perms))
	for i, p := range perms {
		names[i] = string(p)
	}
	msg := accessv1.ServiceTokenClaims_builder{
		Version: new(int32(serviceTokenVersion)), KeyId: new(c.KeyID), Audience: new(audience),
		OrganizationId: new(org.String()), Subject: new(subject), Permissions: names,
		IssuedAt: timestamppb.New(c.IssuedAt), ExpiresAt: timestamppb.New(c.ExpiresAt),
	}.Build()
	claims, err := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
	if err != nil {
		return "", ServiceClaims{}, fmt.Errorf("service token: %w", err)
	}
	enc := base64.RawURLEncoding
	sig := ed25519.Sign(t.keys[t.current], signedServiceClaims(claims))
	return enc.EncodeToString(claims) + "." + enc.EncodeToString(sig), c, nil
}

// signedServiceClaims returns the bytes a token's signature covers.
func signedServiceClaims(claims []byte) []byte {
	return append([]byte(serviceTokenContext), claims...)
}

// Verify checks the signature of token with the key its key_id names, its
// version, its audience and its expiry, and returns its claims; any
// failure is [ErrUnauthenticated].
func (t *ServiceTokens) Verify(token, audience string) (ServiceClaims, error) {
	enc := base64.RawURLEncoding
	p, s, ok := strings.Cut(token, ".")
	if !ok {
		return ServiceClaims{}, ErrUnauthenticated
	}
	claims, err1 := enc.DecodeString(p)
	sig, err2 := enc.DecodeString(s)
	if err1 != nil || err2 != nil {
		return ServiceClaims{}, ErrUnauthenticated
	}
	var msg accessv1.ServiceTokenClaims
	if err := proto.Unmarshal(claims, &msg); err != nil || msg.GetVersion() != serviceTokenVersion {
		return ServiceClaims{}, ErrUnauthenticated
	}
	key, ok := t.keys[msg.GetKeyId()]
	if !ok {
		return ServiceClaims{}, ErrUnauthenticated
	}
	pub, _ := key.Public().(ed25519.PublicKey)
	if !ed25519.Verify(pub, signedServiceClaims(claims), sig) {
		return ServiceClaims{}, ErrUnauthenticated
	}
	if msg.GetAudience() != audience || msg.GetSubject() == "" || !msg.HasExpiresAt() {
		return ServiceClaims{}, ErrUnauthenticated
	}
	exp := msg.GetExpiresAt().AsTime()
	if !t.clock.Now().Before(exp) {
		return ServiceClaims{}, ErrUnauthenticated
	}
	org, err := core.ParseID[core.OrganizationID](msg.GetOrganizationId())
	if err != nil || org.IsZero() {
		return ServiceClaims{}, ErrUnauthenticated
	}
	perms := make([]permission.Permission, len(msg.GetPermissions()))
	for i, n := range msg.GetPermissions() {
		perms[i] = permission.Permission(n)
	}
	return ServiceClaims{
		KeyID: msg.GetKeyId(), Audience: msg.GetAudience(), OrganizationID: org, Subject: msg.GetSubject(),
		Permissions: perms, IssuedAt: msg.GetIssuedAt().AsTime(), ExpiresAt: exp,
	}, nil
}

// ServiceSubject returns the subject of p in service tokens: "user:<id>"
// for a member, "api_key:<id>" for an API key. Other principals, such as
// customer portal tokens, fail with [ErrPermissionDenied].
func (p Principal) ServiceSubject() (string, error) {
	switch {
	case p.Kind == PrincipalUser && p.User != nil && !p.OrganizationID.IsZero():
		return "user:" + p.User.ID.String(), nil
	case p.Kind == PrincipalAPIKey && p.APIKey != nil:
		return "api_key:" + p.APIKey.ID.String(), nil
	case p.Kind == PrincipalNone:
		return "", ErrUnauthenticated
	default:
		return "", fmt.Errorf("%w: service tokens are issued to members and API keys only", ErrPermissionDenied)
	}
}
