package access

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"maps"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
)

// Rule codes of API keys (domain §5.5).
const (
	// CodeLastNonExpiringKey: the last non-expiring key cannot be deleted or
	// given an expiry.
	CodeLastNonExpiringKey = "last_non_expiring_api_key"
	// CodeForbiddenKeys: a permission names an unknown resource.
	CodeForbiddenKeys = "forbidden_keys"
	// CodeForbiddenValues: a permission has an unknown level.
	CodeForbiddenValues = "forbidden_values"
)

// APIKeyPrefixLength is the number of leading secret characters kept for
// display.
const APIKeyPrefixLength = 8

// APIKey is an organization-scoped API credential (domain §5.5). The
// secret is shown once at creation; only its SHA-256 is stored.
type APIKey struct {
	// ID identifies the key.
	ID core.APIKeyID
	// OrganizationID is the organization the key acts for.
	OrganizationID core.OrganizationID
	// Name is a display name, ≤ 255 characters.
	Name *string
	// TokenHash is the SHA-256 of the secret ([HashToken]).
	TokenHash []byte
	// Prefix is the first characters of the secret, for display.
	Prefix string
	// Permissions maps resources to levels; an empty map is full access.
	Permissions map[permission.APIResource]permission.Level
	// ExpiresAt ends the key's validity; nil never expires.
	ExpiresAt *time.Time
	// LastUsedAt is the instant of the last authenticated request.
	LastUsedAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// HashToken returns the SHA-256 of an API key secret, as stored in
// [APIKey.TokenHash].
func HashToken(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}

// GenerateAPIKey returns a new key with full access and its secret, read
// from rand (crypto/rand.Reader in production): 32 random bytes encoded in
// unpadded URL-safe base64.
func GenerateAPIKey(id core.APIKeyID, org core.OrganizationID, rand io.Reader, now time.Time) (*APIKey, string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(rand, b); err != nil {
		return nil, "", fmt.Errorf("generate api key: %w", err)
	}
	secret := base64.RawURLEncoding.EncodeToString(b)
	return &APIKey{
		ID: id, OrganizationID: org, TokenHash: HashToken(secret), Prefix: secret[:APIKeyPrefixLength],
		CreatedAt: now, UpdatedAt: now,
	}, secret, nil
}

// Validate checks the invariants of domain §5.5.
func (k *APIKey) Validate() error {
	var v core.Validator
	v.Check(!k.OrganizationID.IsZero(), "organization_id", core.CodeRequired)
	v.OptionalString("name", k.Name, 255)
	v.Check(len(k.TokenHash) == sha256.Size, "token_hash", core.CodeInvalid)
	v.Required("prefix", k.Prefix)
	for r, l := range k.Permissions {
		if !r.Valid() {
			v.Add("permissions", CodeForbiddenKeys)
			break
		}
		if !l.Valid() {
			v.Add("permissions", CodeForbiddenValues)
			break
		}
	}
	return v.Err()
}

// Expired reports whether the key has expired at now.
func (k *APIKey) Expired(now time.Time) bool { return k.ExpiresAt != nil && !now.Before(*k.ExpiresAt) }

// Active reports whether the key authenticates requests at now.
func (k *APIKey) Active(now time.Time) bool { return !k.Expired(now) }

// NonExpiring reports whether the key has no expiry.
func (k *APIKey) NonExpiring() bool { return k.ExpiresAt == nil }

// Permits reports whether the key may perform a request needing level on
// resource: an empty permission map is full access, otherwise the resource
// must be listed with a level that allows it.
func (k *APIKey) Permits(resource permission.APIResource, level permission.Level) bool {
	if len(k.Permissions) == 0 {
		return true
	}
	l, ok := k.Permissions[resource]
	return ok && l.Allows(level)
}

// ClonePermissions returns a copy of the permission map.
func (k *APIKey) ClonePermissions() map[permission.APIResource]permission.Level {
	return maps.Clone(k.Permissions)
}

// Rotate expires k at expireAt (now for an immediate rotation) and returns
// a new key with the same name and permissions; the caller checks
// [CheckAPIKeyExpiry] counting the new key, which never expires.
func (k *APIKey) Rotate(newID core.APIKeyID, rand io.Reader, expireAt, now time.Time) (*APIKey, string, error) {
	n, secret, err := GenerateAPIKey(newID, k.OrganizationID, rand, now)
	if err != nil {
		return nil, "", err
	}
	n.Name = k.Name
	n.Permissions = k.ClonePermissions()
	if expireAt.Before(now) {
		expireAt = now
	}
	k.ExpiresAt = &expireAt
	k.UpdatedAt = now
	return n, secret, nil
}

// Touch records a use of the key at now.
func (k *APIKey) Touch(now time.Time) { k.LastUsedAt = &now }

// CheckAPIKeyDelete refuses deleting the last non-expiring key;
// nonExpiring is the organization's number of non-expiring keys (k
// included).
func CheckAPIKeyDelete(k *APIKey, nonExpiring int) error {
	if k.NonExpiring() && nonExpiring <= 1 {
		return core.NewRuleError(CodeLastNonExpiringKey, "the last non-expiring API key cannot be deleted")
	}
	return nil
}

// CheckAPIKeyExpiry refuses giving an expiry to the last non-expiring key;
// nonExpiring is the organization's number of non-expiring keys (k
// included).
func CheckAPIKeyExpiry(k *APIKey, nonExpiring int) error {
	if k.NonExpiring() && nonExpiring <= 1 {
		return core.NewRuleError(CodeLastNonExpiringKey, "the last non-expiring API key cannot expire")
	}
	return nil
}
