package memstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
)

func TestAccessRepos(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	d := newAccessData()
	org := core.MustParseID[core.OrganizationID]("0190a0a0-0000-7000-8000-000000000001")
	other := core.MustParseID[core.OrganizationID]("0190a0a0-0000-7000-8000-000000000002")

	users := userRepo{d}
	u := access.NewUser(core.MustParseID[core.UserID]("0190a0a0-0000-7000-8000-000000000010"), "Gavin@Hooli.com", nil, now)
	if err := users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	dup := access.NewUser(core.MustParseID[core.UserID]("0190a0a0-0000-7000-8000-000000000011"), "gavin@hooli.com", nil, now)
	if err := users.Create(ctx, dup); !errors.Is(err, core.ErrConflict) {
		t.Errorf("Create(duplicate email) got = %v, want = ErrConflict", err)
	}
	if got, err := users.GetByEmail(ctx, " GAVIN@hooli.com"); err != nil || got.ID != u.ID {
		t.Errorf("GetByEmail got = %v, %v, want = %s", got, err, u.ID)
	}

	// spec/models/role_spec.rb:91, :96, :31, :48
	roles := roleRepo{d}
	dev := access.NewCustomRole(core.MustParseID[core.RoleID]("0190a0a0-0000-7000-8000-000000000020"), org, "developer", "Developer", []permission.Permission{"customers:read"}, now)
	if err := roles.Create(ctx, dev); err != nil {
		t.Fatal(err)
	}
	same := *dev
	same.ID = core.MustParseID[core.RoleID]("0190a0a0-0000-7000-8000-000000000021")
	if err := roles.Create(ctx, &same); !errors.Is(err, core.ErrConflict) {
		t.Errorf("Create(same code, same org) got = %v, want = ErrConflict", err)
	}
	same.OrganizationID = other
	if err := roles.Create(ctx, &same); err != nil {
		t.Errorf("Create(same code, other org) got = %v, want = nil", err)
	}
	if got, err := roles.GetByCode(ctx, org, "developer"); err != nil || got.ID != dev.ID {
		t.Errorf("GetByCode got = %v, %v, want = %s", got, err, dev.ID)
	}
	if _, err := roles.GetByCode(ctx, org, "nonexistent"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("GetByCode(nonexistent) got = %v, want = ErrNotFound", err)
	}
	admin := access.BuiltInRoleID(permission.BuiltInRoleAdmin)
	if got, err := roles.Get(ctx, other, admin); err != nil || !got.Admin() {
		t.Errorf("Get(admin) got = %v, %v, want = admin", got, err)
	}
	page, err := roles.List(ctx, org, core.PageRequest{})
	if err != nil || len(page.Items) != 4 || page.Items[3].ID != dev.ID {
		t.Errorf("List got = %d roles, %v, want = 3 built-ins then developer", len(page.Items), err)
	}

	// spec/models/membership_role_spec.rb:20
	members := membershipRepo{d}
	m := access.NewMembership(core.MustParseID[core.MembershipID]("0190a0a0-0000-7000-8000-000000000030"), org, u.ID, []core.RoleID{admin, dev.ID}, now)
	if err := members.Create(ctx, m); err != nil {
		t.Fatal(err)
	}
	m2 := *m
	m2.ID = core.MustParseID[core.MembershipID]("0190a0a0-0000-7000-8000-000000000031")
	if err := members.Create(ctx, &m2); !errors.Is(err, core.ErrConflict) {
		t.Errorf("Create(second active) got = %v, want = ErrConflict", err)
	}
	for _, role := range []core.RoleID{admin, dev.ID} {
		if n, _ := members.CountActiveByRole(ctx, org, role); n != 1 {
			t.Errorf("CountActiveByRole(%s) got = %d, want = 1", role, n)
		}
	}
	if p, err := members.List(ctx, org, access.MembershipFilter{RoleID: dev.ID}, core.PageRequest{}); err != nil || len(p.Items) != 1 || len(p.Items[0].RoleIDs) != 2 {
		t.Errorf("List(role) got = %v, %v, want = the membership with both roles", p.Items, err)
	}
	if err := m.Revoke(now); err != nil {
		t.Fatal(err)
	}
	if err := members.Update(ctx, m); err != nil {
		t.Fatal(err)
	}
	if n, _ := members.CountActiveByRole(ctx, org, admin); n != 0 {
		t.Errorf("CountActiveByRole after revoke got = %d, want = 0", n)
	}
	if err := members.Create(ctx, &m2); err != nil {
		t.Errorf("Create(after revoke) got = %v, want = nil", err)
	}

	invites := inviteRepo{d}
	i := access.NewInvite(core.MustParseID[core.InviteID]("0190a0a0-0000-7000-8000-000000000040"), org, "a@b.co", []core.RoleID{dev.ID}, access.HashToken("i1"), now.Add(time.Hour), now)
	if err := invites.Create(ctx, i); err != nil {
		t.Fatal(err)
	}
	i2 := access.NewInvite(core.MustParseID[core.InviteID]("0190a0a0-0000-7000-8000-000000000041"), org, "A@b.co", []core.RoleID{dev.ID}, access.HashToken("i2"), now.Add(time.Hour), now)
	if err := invites.Create(ctx, i2); !errors.Is(err, core.ErrConflict) {
		t.Errorf("Create(second pending) got = %v, want = ErrConflict", err)
	}
	if got, err := invites.GetPendingByEmail(ctx, org, "A@B.co"); err != nil || got.ID != i.ID {
		t.Errorf("GetPendingByEmail got = %v, %v, want = %s", got, err, i.ID)
	}

	resets := passwordResetRepo{d}
	p := access.NewPasswordReset(core.MustParseID[core.PasswordResetID]("0190a0a0-0000-7000-8000-000000000050"), u.ID, access.HashToken("p"), now)
	if err := resets.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	if n, _ := resets.DeleteExpired(ctx, now.Add(time.Hour)); n != 1 {
		t.Errorf("DeleteExpired got = %d, want = 1", n)
	}

	// spec/models/api_key_spec.rb:117, :134
	keys := apiKeyRepo{d}
	k1 := &access.APIKey{ID: core.MustParseID[core.APIKeyID]("0190a0a0-0000-7000-8000-000000000060"), OrganizationID: org, TokenHash: access.HashToken("k1"), Prefix: "k1"}
	past := now.Add(-time.Hour)
	k2 := &access.APIKey{ID: core.MustParseID[core.APIKeyID]("0190a0a0-0000-7000-8000-000000000061"), OrganizationID: org, TokenHash: access.HashToken("k2"), Prefix: "k2", ExpiresAt: &past}
	for _, k := range []*access.APIKey{k1, k2} {
		if err := keys.Create(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	k3 := *k1
	k3.ID = core.MustParseID[core.APIKeyID]("0190a0a0-0000-7000-8000-000000000062")
	if err := keys.Create(ctx, &k3); !errors.Is(err, core.ErrConflict) {
		t.Errorf("Create(same token) got = %v, want = ErrConflict", err)
	}
	if n, _ := keys.CountNonExpiring(ctx, org); n != 1 {
		t.Errorf("CountNonExpiring got = %d, want = 1", n)
	}
	active, _ := keys.List(ctx, org, access.APIKeyFilter{ActiveAt: &now}, core.PageRequest{})
	if len(active.Items) != 1 || active.Items[0].ID != k1.ID {
		t.Errorf("List(active) got = %d keys, want = 1", len(active.Items))
	}
	if err := keys.TouchLastUsed(ctx, org, k1.ID, now); err != nil {
		t.Fatal(err)
	}
	if got, _ := keys.GetByTokenHash(ctx, access.HashToken("k1")); got.LastUsedAt == nil || !got.LastUsedAt.Equal(now) {
		t.Errorf("LastUsedAt got = %v, want = %v", got.LastUsedAt, now)
	}
	if _, err := keys.Get(ctx, other, k1.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get(other org) got = %v, want = ErrNotFound", err)
	}
}
