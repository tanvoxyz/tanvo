package access

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
)

var (
	testNow  = time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	testOrg  = core.MustParseID[core.OrganizationID]("0190a0a0-0000-7000-8000-000000000001")
	testUser = core.MustParseID[core.UserID]("0190a0a0-0000-7000-8000-000000000002")
	testRole = core.MustParseID[core.RoleID]("0190a0a0-0000-7000-8000-000000000003")
)

func fieldErrors(err error) []string {
	out := []string{}
	for _, fe := range core.FieldErrors(err) {
		out = append(out, fe.String())
	}
	return out
}

func ruleCode(err error) string {
	var re *core.RuleError
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

func TestEmails(t *testing.T) {
	type tc struct {
		Name      string   `yaml:"name"`
		Source    string   `yaml:"source"`
		Entity    string   `yaml:"entity"`
		Email     string   `yaml:"email"`
		WantEmail string   `yaml:"want_email"`
		Errors    []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "emails.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			var email string
			var err error
			switch c.Entity {
			case "user":
				u := NewUser(testUser, c.Email, nil, testNow)
				email, err = u.Email, u.Validate()
			case "invite":
				i := NewInvite(core.MustParseID[core.InviteID]("0190a0a0-0000-7000-8000-0000000000ff"), testOrg, c.Email, testRole, HashToken("t"), testNow.Add(time.Hour), testNow)
				email, err = i.Email, i.Validate()
			default:
				t.Fatalf("entity = %q", c.Entity)
			}
			if email != c.WantEmail {
				t.Errorf("email got = %q, want = %q (%s)", email, c.WantEmail, c.Source)
			}
			if got, want := fieldErrors(err), append([]string{}, c.Errors...); !slices.Equal(got, want) {
				t.Errorf("Validate() errors got = %v, want = %v (%s)", got, want, c.Source)
			}
		})
	}
}

func TestRoleValidate(t *testing.T) {
	type tc struct {
		Name              string    `yaml:"name"`
		Source            string    `yaml:"source"`
		Code              string    `yaml:"code"`
		CodeLength        int       `yaml:"code_length"`
		RoleName          string    `yaml:"role_name"`
		NameLength        int       `yaml:"name_length"`
		WantName          string    `yaml:"want_name"`
		DescriptionLength int       `yaml:"description_length"`
		Permissions       *[]string `yaml:"permissions"`
		Errors            []string  `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "role_validation.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			code, name := c.Code, c.RoleName
			if c.CodeLength > 0 {
				code = strings.Repeat("a", c.CodeLength)
			}
			if c.NameLength > 0 {
				name = strings.Repeat("a", c.NameLength)
			}
			perms := []permission.Permission{"customers:read"}
			if c.Permissions != nil {
				perms = nil
				for _, p := range *c.Permissions {
					perms = append(perms, permission.Permission(p))
				}
			}
			r := NewCustomRole(testRole, testOrg, code, name, perms, testNow)
			if c.DescriptionLength > 0 {
				d := strings.Repeat("a", c.DescriptionLength)
				r.Description = &d
			}
			if c.WantName != "" && r.Name != c.WantName {
				t.Errorf("Name got = %q, want = %q (%s)", r.Name, c.WantName, c.Source)
			}
			if got, want := fieldErrors(r.Validate()), append([]string{}, c.Errors...); !slices.Equal(got, want) {
				t.Errorf("Validate() errors got = %v, want = %v (%s)", got, want, c.Source)
			}
		})
	}
}

func TestBuiltInRoles(t *testing.T) {
	roles := BuiltInRoles()
	if got, want := len(roles), 3; got != want {
		t.Fatalf("len(BuiltInRoles()) got = %d, want = %d", got, want)
	}
	for _, r := range roles {
		if err := r.Validate(); err != nil {
			t.Errorf("%s Validate() got = %v, want = nil", r.Code, err)
		}
		if got, ok := BuiltInRoleByID(r.ID); !ok || got.Code != r.Code {
			t.Errorf("BuiltInRoleByID(%s) got = %v, want = %s", r.ID, got, r.Code)
		}
		if err := CheckRoleUpdate(r); ruleCode(err) != CodeBuiltInRoleImmutable {
			t.Errorf("CheckRoleUpdate(%s) got = %v, want = %s", r.Code, err, CodeBuiltInRoleImmutable)
		}
	}
	if !roles[0].Admin() || roles[1].Admin() {
		t.Errorf("Admin() got = %v/%v, want = true/false", roles[0].Admin(), roles[1].Admin())
	}
	if got, want := len(roles[0].EffectivePermissions()), len(permission.All()); got != want {
		t.Errorf("admin EffectivePermissions() got = %d, want = %d", got, want)
	}
	custom := NewCustomRole(testRole, testOrg, "dev", "Dev", []permission.Permission{"customers:read"}, testNow)
	if !custom.Grants("customers:read") || custom.Grants("customers:create") {
		t.Errorf("custom Grants got = %v/%v, want = true/false", custom.Grants("customers:read"), custom.Grants("customers:create"))
	}
	if err := CheckRoleDeletion(custom, 1); ruleCode(err) != CodeRoleInUse {
		t.Errorf("CheckRoleDeletion(in use) got = %v, want = %s", err, CodeRoleInUse)
	}
	if err := CheckRoleDeletion(custom, 0); err != nil {
		t.Errorf("CheckRoleDeletion(unused) got = %v, want = nil", err)
	}
}

func TestMembershipRules(t *testing.T) {
	type tc struct {
		Name        string `yaml:"name"`
		Source      string `yaml:"source"`
		Op          string `yaml:"op"`
		Self        bool   `yaml:"self"`
		ActorAdmin  bool   `yaml:"actor_admin"`
		TargetAdmin bool   `yaml:"target_admin"`
		NewAdmin    bool   `yaml:"new_admin"`
		Admins      int    `yaml:"admins"`
		Code        string `yaml:"code"`
	}
	actor := core.MustParseID[core.UserID]("0190a0a0-0000-7000-8000-0000000000aa")
	for _, c := range spectest.Load[tc](t, "membership_rules.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			target := NewMembership(core.MustParseID[core.MembershipID]("0190a0a0-0000-7000-8000-0000000000ff"), testOrg, testUser, testRole, testNow)
			if c.Self {
				target.UserID = actor
			}
			var err error
			switch c.Op {
			case "revoke":
				err = CheckRevoke(actor, target, c.TargetAdmin, c.Admins)
			case "change":
				err = CheckRoleChange(c.ActorAdmin, target, c.TargetAdmin, c.NewAdmin, c.Admins)
			default:
				t.Fatalf("op = %q", c.Op)
			}
			if got := ruleCode(err); got != c.Code {
				t.Errorf("code got = %q, want = %q (%s)", got, c.Code, c.Source)
			}
			if err != nil && !errors.Is(err, core.ErrNotAllowed) {
				t.Errorf("errors.Is(err, ErrNotAllowed) got = false, want = true")
			}
		})
	}
}

func TestMembershipTransitions(t *testing.T) {
	for _, from := range MembershipStatusValues() {
		for _, to := range MembershipStatusValues() {
			want := from == MembershipStatusActive && to == MembershipStatusRevoked
			if got := CanTransitionMembership(from, to); got != want {
				t.Errorf("CanTransitionMembership(%s, %s) got = %v, want = %v", from, to, got, want)
			}
		}
	}
	m := NewMembership(core.MustParseID[core.MembershipID]("0190a0a0-0000-7000-8000-0000000000ff"), testOrg, testUser, testRole, testNow)
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate() got = %v, want = nil", err)
	}
	if err := m.Revoke(testNow); err != nil || m.RevokedAt == nil || m.Active() {
		t.Fatalf("Revoke() got = %v, want = revoked", err)
	}
	if err := m.Validate(); err != nil {
		t.Errorf("Validate() after revoke got = %v, want = nil", err)
	}
	var te *core.TransitionError
	if err := m.Revoke(testNow); !errors.As(err, &te) {
		t.Errorf("second Revoke() got = %v, want = *core.TransitionError", err)
	}
	if err := m.ChangeRole(testRole, testNow); ruleCode(err) != CodeMembershipRevoked {
		t.Errorf("ChangeRole(revoked) got = %v, want = %s", err, CodeMembershipRevoked)
	}
}

func TestInviteTransitions(t *testing.T) {
	for _, from := range InviteStatusValues() {
		for _, to := range InviteStatusValues() {
			want := from == InviteStatusPending && to != InviteStatusPending
			if got := CanTransitionInvite(from, to); got != want {
				t.Errorf("CanTransitionInvite(%s, %s) got = %v, want = %v", from, to, got, want)
			}
		}
	}
	newInvite := func() *Invite {
		return NewInvite(core.MustParseID[core.InviteID]("0190a0a0-0000-7000-8000-0000000000ff"), testOrg, "a@b.co", testRole, HashToken("t"), testNow.Add(time.Hour), testNow)
	}
	// spec/models/invite_spec.rb:11
	i := newInvite()
	if err := i.Revoke(testNow); err != nil || i.Status != InviteStatusRevoked || i.RevokedAt == nil || !i.RevokedAt.Equal(testNow) {
		t.Errorf("Revoke() got = %v %s, want = nil revoked", err, i.Status)
	}
	var te *core.TransitionError
	if err := i.Accept(testNow); !errors.As(err, &te) {
		t.Errorf("Accept(revoked) got = %v, want = *core.TransitionError", err)
	}
	// spec/models/invite_spec.rb:21
	i = newInvite()
	if err := i.Accept(testNow); err != nil || i.Status != InviteStatusAccepted || i.AcceptedAt == nil {
		t.Errorf("Accept() got = %v %s, want = nil accepted", err, i.Status)
	}
	i = newInvite()
	if err := i.Accept(testNow.Add(time.Hour)); ruleCode(err) != CodeInviteExpired || i.Status != InviteStatusPending {
		t.Errorf("Accept(expired) got = %v, want = %s", err, CodeInviteExpired)
	}
	if err := CheckInvite(true); ruleCode(err) != CodeEmailAlreadyMember {
		t.Errorf("CheckInvite(member) got = %v, want = %s", err, CodeEmailAlreadyMember)
	}
	if err := CheckInvite(false); err != nil {
		t.Errorf("CheckInvite(non-member) got = %v, want = nil", err)
	}
}

func TestPasswordReset(t *testing.T) {
	type tc struct {
		Name            string   `yaml:"name"`
		Source          string   `yaml:"source"`
		NoUser          bool     `yaml:"no_user"`
		NoToken         bool     `yaml:"no_token"`
		NoExpiry        bool     `yaml:"no_expiry"`
		UseAfterMinutes []int    `yaml:"use_after_minutes"`
		Errors          []string `yaml:"errors"`
		Code            string   `yaml:"code"`
	}
	for _, c := range spectest.Load[tc](t, "password_resets.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			p := NewPasswordReset(core.MustParseID[core.PasswordResetID]("0190a0a0-0000-7000-8000-0000000000ff"), testUser, HashToken("t"), testNow)
			if c.NoUser {
				p.UserID = core.UserID{}
			}
			if c.NoToken {
				p.TokenHash = nil
			}
			if c.NoExpiry {
				p.ExpiresAt = time.Time{}
			}
			if got, want := fieldErrors(p.Validate()), append([]string{}, c.Errors...); !slices.Equal(got, want) {
				t.Errorf("Validate() errors got = %v, want = %v (%s)", got, want, c.Source)
			}
			var err error
			for _, m := range c.UseAfterMinutes {
				err = p.Use(testNow.Add(time.Duration(m) * time.Minute))
			}
			if got := ruleCode(err); got != c.Code {
				t.Errorf("Use() code got = %q, want = %q (%s)", got, c.Code, c.Source)
			}
		})
	}
}

func TestAPIKeys(t *testing.T) {
	type tc struct {
		Name           string            `yaml:"name"`
		Source         string            `yaml:"source"`
		Permissions    map[string]string `yaml:"permissions"`
		Errors         []string          `yaml:"errors"`
		Resource       string            `yaml:"resource"`
		Level          string            `yaml:"level"`
		Permit         bool              `yaml:"permit"`
		ExpiresInHours *int              `yaml:"expires_in_hours"`
		Expired        bool              `yaml:"expired"`
	}
	for _, c := range spectest.Load[tc](t, "api_keys.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			k, _, err := GenerateAPIKey(core.MustParseID[core.APIKeyID]("0190a0a0-0000-7000-8000-0000000000ff"), testOrg, bytes.NewReader(make([]byte, 32)), testNow)
			if err != nil {
				t.Fatal(err)
			}
			for r, l := range c.Permissions {
				if k.Permissions == nil {
					k.Permissions = map[permission.APIResource]permission.Level{}
				}
				k.Permissions[permission.APIResource(r)] = permission.Level(l)
			}
			if c.ExpiresInHours != nil {
				at := testNow.Add(time.Duration(*c.ExpiresInHours) * time.Hour)
				k.ExpiresAt = &at
			}
			if got, want := fieldErrors(k.Validate()), append([]string{}, c.Errors...); !slices.Equal(got, want) {
				t.Errorf("Validate() errors got = %v, want = %v (%s)", got, want, c.Source)
			}
			if c.Resource != "" {
				if got := k.Permits(permission.APIResource(c.Resource), permission.Level(c.Level)); got != c.Permit {
					t.Errorf("Permits(%s, %s) got = %v, want = %v (%s)", c.Resource, c.Level, got, c.Permit, c.Source)
				}
			}
			if got := k.Expired(testNow); got != c.Expired {
				t.Errorf("Expired() got = %v, want = %v (%s)", got, c.Expired, c.Source)
			}
		})
	}
}

func TestGenerateAPIKey(t *testing.T) {
	k, secret, err := GenerateAPIKey(core.MustParseID[core.APIKeyID]("0190a0a0-0000-7000-8000-0000000000ff"), testOrg, bytes.NewReader(bytes.Repeat([]byte{7}, 32)), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(secret), 43; got != want {
		t.Errorf("len(secret) got = %d, want = %d", got, want)
	}
	if !bytes.Equal(k.TokenHash, HashToken(secret)) || k.Prefix != secret[:APIKeyPrefixLength] {
		t.Errorf("TokenHash/Prefix got = %x/%s, want = hash and prefix of the secret", k.TokenHash, k.Prefix)
	}
	if _, _, err := GenerateAPIKey(core.MustParseID[core.APIKeyID]("0190a0a0-0000-7000-8000-0000000000ff"), testOrg, bytes.NewReader(nil), testNow); err == nil {
		t.Errorf("GenerateAPIKey(short reader) got = nil, want = error")
	}
	k.Permissions = map[permission.APIResource]permission.Level{permission.APIResourceInvoice: permission.LevelRead}
	n, _, err := k.Rotate(core.MustParseID[core.APIKeyID]("0190a0a0-0000-7000-8000-0000000000ff"), bytes.NewReader(bytes.Repeat([]byte{8}, 32)), testNow.Add(-time.Hour), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if k.ExpiresAt == nil || !k.ExpiresAt.Equal(testNow) || !n.NonExpiring() || n.Permissions[permission.APIResourceInvoice] != permission.LevelRead {
		t.Errorf("Rotate() got = old expires %v, new %v, want = old expired now, new non-expiring with permissions", k.ExpiresAt, n)
	}
	n.Permissions[permission.APIResourceInvoice] = permission.LevelWrite
	if k.Permissions[permission.APIResourceInvoice] != permission.LevelRead {
		t.Errorf("Rotate() permissions shared got = true, want = false")
	}
}

func TestAPIKeyRules(t *testing.T) {
	future := testNow.Add(time.Hour)
	tests := []struct {
		name        string
		expiresAt   *time.Time
		nonExpiring int
		want        string
	}{
		{"last non-expiring", nil, 1, CodeLastNonExpiringKey},
		{"another non-expiring", nil, 2, ""},
		{"expiring key", &future, 1, ""},
	}
	for _, tt := range tests {
		k := &APIKey{ExpiresAt: tt.expiresAt}
		if got := ruleCode(CheckAPIKeyDelete(k, tt.nonExpiring)); got != tt.want {
			t.Errorf("%s: CheckAPIKeyDelete got = %q, want = %q", tt.name, got, tt.want)
		}
		if got := ruleCode(CheckAPIKeyExpiry(k, tt.nonExpiring)); got != tt.want {
			t.Errorf("%s: CheckAPIKeyExpiry got = %q, want = %q", tt.name, got, tt.want)
		}
	}
}
