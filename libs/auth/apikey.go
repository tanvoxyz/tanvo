package auth

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// APIKeys manages organization API keys (lago ApiKeys::*Service).
type APIKeys struct {
	d     Deps
	usage *UsageTracker
}

// NewAPIKeys returns the API key service; usage collects last-used
// instants of authenticated keys (nil disables tracking).
func NewAPIKeys(d Deps, usage *UsageTracker) *APIKeys { return &APIKeys{d: d, usage: usage} }

// CreatedAPIKey is a new key with its secret, which is never stored and
// cannot be shown again.
type CreatedAPIKey struct {
	// Key is the stored key.
	Key *access.APIKey
	// Secret is the credential to hand to the user once.
	Secret string
}

// prefix identifies a key in logs and emails. Lago logs the last four
// characters of the stored secret; Tanvo stores only the hash and the
// display prefix.
func prefix(k *access.APIKey) string { return k.Prefix }

func keyName(k *access.APIKey) any {
	if k.Name == nil {
		return nil
	}
	return *k.Name
}

func flatPermissions(m map[permission.APIResource]permission.Level) []string {
	out := make([]string, 0, len(m))
	for r, l := range m {
		out = append(out, string(r)+":"+string(l))
	}
	slices.Sort(out)
	return out
}

func apiKeyMail(tpl string, k *access.APIKey, at time.Time) MailJob {
	data := map[string]string{"prefix": k.Prefix, "at": at.Format(time.RFC3339)}
	if k.Name != nil {
		data["name"] = *k.Name
	}
	return MailJob{Template: tpl, OrganizationID: k.OrganizationID, Data: data}
}

// Create creates a key of org with name and permissions (nil or empty is
// full access) and emails the organization's admins.
func (s *APIKeys) Create(ctx context.Context, org core.OrganizationID, name *string, perms map[permission.APIResource]permission.Level) (*CreatedAPIKey, error) {
	var out *CreatedAPIKey
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		now := s.d.now()
		k, secret, err := access.GenerateAPIKey(core.NewID[core.APIKeyID](s.d.IDs), org, s.d.Rand, now)
		if err != nil {
			return err
		}
		k.Name, k.Permissions = name, maps.Clone(perms)
		if err := k.Validate(); err != nil {
			return err
		}
		if err := r.APIKeys.Create(ctx, k); err != nil {
			return fmt.Errorf("create api key: %w", err)
		}
		if err := enqueueMail(ctx, r, apiKeyMail(MailAPIKeyCreated, k, now)); err != nil {
			return err
		}
		out = &CreatedAPIKey{Key: k, Secret: secret}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.d.record(ctx, SecurityEvent{OrganizationID: org, Type: SecurityLogAPIKey, Event: EventAPIKeyCreated, Resources: map[string]any{
		"name": keyName(out.Key), "value_prefix": prefix(out.Key), "permissions": flatPermissions(out.Key.Permissions),
	}})
	return out, nil
}

func getKey(ctx context.Context, r port.Repos, org core.OrganizationID, id core.APIKeyID) (*access.APIKey, error) {
	k, err := r.APIKeys.Get(ctx, org, id)
	if errors.Is(err, core.ErrNotFound) {
		return nil, notFound("api_key")
	}
	if err != nil {
		return nil, fmt.Errorf("get api key: %w", err)
	}
	return k, nil
}

// APIKeyUpdate holds the fields to change; nil fields are kept.
type APIKeyUpdate struct {
	// Name replaces the name when set.
	Name *string
	// Permissions replaces the permissions when set (an empty map is full
	// access).
	Permissions *map[permission.APIResource]permission.Level
}

// Update changes a key's name or permissions.
func (s *APIKeys) Update(ctx context.Context, org core.OrganizationID, id core.APIKeyID, u APIKeyUpdate) (*access.APIKey, error) {
	var k *access.APIKey
	diff := map[string]any{}
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if k, err = getKey(ctx, r, org, id); err != nil {
			return err
		}
		if u.Name != nil && (k.Name == nil || *k.Name != *u.Name) {
			diff["name"] = map[string]any{"deleted": keyName(k), "added": *u.Name}
			k.Name = u.Name
		}
		if u.Permissions != nil {
			before, after := flatPermissions(k.Permissions), flatPermissions(*u.Permissions)
			added := slices.DeleteFunc(slices.Clone(after), func(p string) bool { return slices.Contains(before, p) })
			deleted := slices.DeleteFunc(slices.Clone(before), func(p string) bool { return slices.Contains(after, p) })
			if len(added)+len(deleted) > 0 {
				diff["permissions"] = map[string]any{"added": added, "deleted": deleted}
			}
			k.Permissions = maps.Clone(*u.Permissions)
		}
		if err := k.Validate(); err != nil {
			return err
		}
		k.UpdatedAt = s.d.now()
		if err := r.APIKeys.Update(ctx, k); err != nil {
			return fmt.Errorf("update api key: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	res := map[string]any{"name": keyName(k), "value_prefix": prefix(k)}
	maps.Copy(res, diff)
	s.d.record(ctx, SecurityEvent{OrganizationID: org, Type: SecurityLogAPIKey, Event: EventAPIKeyUpdated, Resources: res})
	return k, nil
}

// APIKeyRotation configures [APIKeys.Rotate].
type APIKeyRotation struct {
	// Name names the new key; nil keeps the old key's name.
	Name *string
	// ExpiresAt is when the old key stops working: the overlap window
	// during which both keys authenticate. Nil or a past instant expires
	// it immediately.
	ExpiresAt *time.Time
}

// Rotate creates a key replacing id, with the same permissions, and
// expires the old key at the end of the overlap window.
func (s *APIKeys) Rotate(ctx context.Context, org core.OrganizationID, id core.APIKeyID, rot APIKeyRotation) (*CreatedAPIKey, error) {
	var (
		old *access.APIKey
		out *CreatedAPIKey
	)
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if old, err = getKey(ctx, r, org, id); err != nil {
			return err
		}
		now := s.d.now()
		expireAt := now
		if rot.ExpiresAt != nil {
			expireAt = rot.ExpiresAt.UTC()
		}
		n, secret, err := old.Rotate(core.NewID[core.APIKeyID](s.d.IDs), s.d.Rand, expireAt, now)
		if err != nil {
			return err
		}
		if rot.Name != nil {
			n.Name = rot.Name
		}
		if err := n.Validate(); err != nil {
			return err
		}
		if err := r.APIKeys.Create(ctx, n); err != nil {
			return fmt.Errorf("create api key: %w", err)
		}
		if err := r.APIKeys.Update(ctx, old); err != nil {
			return fmt.Errorf("expire api key: %w", err)
		}
		if err := enqueueMail(ctx, r, apiKeyMail(MailAPIKeyRotated, old, now)); err != nil {
			return err
		}
		out = &CreatedAPIKey{Key: n, Secret: secret}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.d.record(ctx, SecurityEvent{OrganizationID: org, Type: SecurityLogAPIKey, Event: EventAPIKeyRotated, Resources: map[string]any{
		"name": keyName(out.Key), "value_prefix": map[string]string{"deleted": prefix(old), "added": prefix(out.Key)},
	}})
	return out, nil
}

// Destroy expires the key id now, as lago does, keeping the row for the
// logs. The organization's last non-expiring key is refused unless force
// (system callers); force also skips the email.
func (s *APIKeys) Destroy(ctx context.Context, org core.OrganizationID, id core.APIKeyID, force bool) (*access.APIKey, error) {
	var k *access.APIKey
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if k, err = getKey(ctx, r, org, id); err != nil {
			return err
		}
		if !force {
			n, err := r.APIKeys.CountNonExpiring(ctx, org)
			if err != nil {
				return fmt.Errorf("count api keys: %w", err)
			}
			if err := access.CheckAPIKeyDelete(k, n); err != nil {
				return err
			}
		}
		now := s.d.now()
		k.ExpiresAt, k.UpdatedAt = &now, now
		if err := r.APIKeys.Update(ctx, k); err != nil {
			return fmt.Errorf("expire api key: %w", err)
		}
		if !force {
			return enqueueMail(ctx, r, apiKeyMail(MailAPIKeyDestroyed, k, now))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.d.record(ctx, SecurityEvent{OrganizationID: org, Type: SecurityLogAPIKey, Event: EventAPIKeyDeleted, Resources: map[string]any{
		"name": keyName(k), "value_prefix": prefix(k),
	}})
	return k, nil
}

// Authenticate returns the key whose secret is secret; an unknown or
// expired key fails with [ErrUnauthenticated]. The use is recorded in the
// usage tracker, not written synchronously.
func (s *APIKeys) Authenticate(ctx context.Context, secret string) (*access.APIKey, error) {
	if secret == "" {
		return nil, ErrUnauthenticated
	}
	var k *access.APIKey
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		k, err = r.APIKeys.GetByTokenHash(ctx, access.HashToken(secret))
		return err
	})
	if errors.Is(err, core.ErrNotFound) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, fmt.Errorf("authenticate api key: %w", err)
	}
	now := s.d.now()
	if !k.Active(now) {
		return nil, ErrUnauthenticated
	}
	if s.usage != nil {
		s.usage.Record(k.OrganizationID, k.ID, now)
	}
	return k, nil
}

type usageKey struct {
	org core.OrganizationID
	id  core.APIKeyID
}

// UsageTracker collects the last use of each API key in memory and writes
// them in batches (lago ApiKeys::TrackUsageService), so authentication
// never writes to the database.
type UsageTracker struct {
	mu   sync.Mutex
	last map[usageKey]time.Time
}

// NewUsageTracker returns an empty tracker.
func NewUsageTracker() *UsageTracker { return &UsageTracker{last: map[usageKey]time.Time{}} }

// Record notes that key id of org was used at at.
func (t *UsageTracker) Record(org core.OrganizationID, id core.APIKeyID, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := usageKey{org, id}
	if at.After(t.last[k]) {
		t.last[k] = at
	}
}

// Pending returns the number of keys with an unwritten use.
func (t *UsageTracker) Pending() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.last)
}

// Flush writes every recorded use with TouchLastUsed and clears them; a
// failed write is kept for the next flush.
func (t *UsageTracker) Flush(ctx context.Context, tx port.Transactor) error {
	t.mu.Lock()
	batch := t.last
	t.last = map[usageKey]time.Time{}
	t.mu.Unlock()
	err := tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		for k, at := range batch {
			if err := r.APIKeys.TouchLastUsed(ctx, k.org, k.id, at); err != nil && !errors.Is(err, core.ErrNotFound) {
				return fmt.Errorf("track api key usage: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		for k, at := range batch {
			t.Record(k.org, k.id, at)
		}
	}
	return err
}
