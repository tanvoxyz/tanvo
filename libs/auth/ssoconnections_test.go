package auth

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

func (e *env) ssoConnections() *SSOConnections {
	e.t.Helper()
	box, err := NewAESSecretBox(bytes.Repeat([]byte{1}, 32), nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return NewSSOConnections(e.deps, box)
}

func (e *env) methods(o *organization.Organization) []organization.AuthenticationMethod {
	e.t.Helper()
	var ms []organization.AuthenticationMethod
	e.tx(func(ctx context.Context, r port.Repos) error {
		got, err := r.Organizations.Get(ctx, o.ID)
		if got != nil {
			ms = got.AuthenticationMethods
		}
		return err
	})
	return ms
}

func (e *env) integration(o *organization.Organization, id core.IntegrationID) *integration.Integration {
	e.t.Helper()
	var i *integration.Integration
	e.tx(func(ctx context.Context, r port.Repos) error {
		var err error
		i, err = r.Integrations.Get(ctx, o.ID, id)
		return err
	})
	return i
}

func oktaInput() SSOConnectionInput {
	return SSOConnectionInput{
		Provider: integration.ProviderOkta, Domain: "Foo.bar", ClientID: "client_id", ClientSecret: "client_secret",
		OrganizationName: "Foobar", DefaultRoleCode: "finance",
	}
}

func entraInput() SSOConnectionInput {
	return SSOConnectionInput{
		Provider: integration.ProviderEntraID, Domain: "foo.bar", ClientID: "client_id", ClientSecret: "client_secret",
		TenantID: "tenant", DefaultRoleCode: "finance",
	}
}

func TestSSOConnectionsCreate(t *testing.T) {
	tests := []struct {
		name  string
		in    func() SSOConnectionInput
		code  string
		field string
	}{
		// source: spec/services/integrations/okta/create_service_spec.rb:55
		{name: "okta", in: oktaInput},
		// source: spec/services/integrations/entra_id/create_service_spec.rb:56
		{name: "entra id", in: entraInput},
		// source: spec/services/integrations/entra_id/create_service_spec.rb:71
		{name: "entra id additional domains", in: func() SSOConnectionInput {
			in := entraInput()
			in.AdditionalDomains = []string{"Baz.com", "baz.com"}
			return in
		}},
		// source: spec/services/integrations/okta/create_service_spec.rb:75
		{name: "okta without domain", in: func() SSOConnectionInput { in := oktaInput(); in.Domain = ""; return in }, code: core.CodeRequired, field: "domain"},
		// source: spec/services/integrations/entra_id/create_service_spec.rb:100
		{name: "entra id without domain", in: func() SSOConnectionInput { in := entraInput(); in.Domain = ""; return in }, code: core.CodeRequired, field: "domain"},
		// source: spec/services/integrations/entra_id/create_service_spec.rb:89
		{name: "invalid additional domain", in: func() SSOConnectionInput {
			in := entraInput()
			in.AdditionalDomains = []string{"localhost"}
			return in
		}, code: core.CodeInvalidFormat, field: "additional_domains"},
		{name: "without secret", in: func() SSOConnectionInput { in := oktaInput(); in.ClientSecret = ""; return in }, code: core.CodeRequired, field: "client_secret"},
		{name: "without role", in: func() SSOConnectionInput { in := oktaInput(); in.DefaultRoleCode = ""; return in }, code: core.CodeRequired, field: "default_role_code"},
		{name: "unknown role", in: func() SSOConnectionInput { in := oktaInput(); in.DefaultRoleCode = "nope"; return in }, code: CodeInvalidRole, field: "default_role_code"},
		{name: "not a single sign-on provider", in: func() SSOConnectionInput { in := oktaInput(); in.Provider = integration.ProviderStripe; return in }, code: core.CodeInvalid, field: "provider"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			o := e.org(organization.AuthenticationMethodEmailPassword)
			s := e.ssoConnections()
			in := tt.in()
			c, err := s.Create(e.ctx, o.ID, in)
			if got := code(err); got != tt.code || fieldOf(err) != tt.field {
				t.Fatalf("Create() error got = %s on %q, want = %s on %q", got, fieldOf(err), tt.code, tt.field)
			}
			if tt.code != "" {
				if got := e.methods(o); len(got) != 1 {
					t.Errorf("methods got = %v, want = [email_password]", got)
				}
				return
			}
			if c.Domain != "foo.bar" || c.ClientID != "client_id" || c.DefaultRoleCode != "finance" {
				t.Errorf("Create() got = %+v", c)
			}
			if in.AdditionalDomains != nil && !slices.Equal(c.AdditionalDomains, []string{"baz.com"}) {
				t.Errorf("additional domains got = %v, want = [baz.com]", c.AdditionalDomains)
			}
			method := ssoProviders[in.Provider].method
			if got := e.methods(o); !slices.Contains(got, method) {
				t.Errorf("methods got = %v, want %s enabled", got, method)
			}
			i := e.integration(o, c.ID)
			if i.Code != ssoProviders[in.Provider].code || i.Name != ssoProviders[in.Provider].name {
				t.Errorf("integration got = %s %q", i.Code, i.Name)
			}
			if secret, err := s.Secret(e.ctx, i); err != nil || secret != "client_secret" {
				t.Errorf("Secret() got = %q, %v, want = client_secret", secret, err)
			}
			if bytes.Contains(i.Secrets, []byte("client_secret")) {
				t.Errorf("stored secrets hold the plaintext")
			}
			if e.log.count(EventIntegrationCreated) != 1 {
				t.Errorf("security log got = %v, want %s", e.log.names(), EventIntegrationCreated)
			}
		})
	}
}

func TestSSOConnectionsCreateConflicts(t *testing.T) {
	e := newEnv(t)
	s := e.ssoConnections()
	o, other := e.org(), e.org()
	if _, err := s.Create(e.ctx, o.ID, oktaInput()); err != nil {
		t.Fatal(err)
	}
	in := oktaInput()
	in.Domain = "other.com"
	if _, err := s.Create(e.ctx, o.ID, in); code(err) != core.CodeAlreadyExists || fieldOf(err) != "code" {
		t.Errorf("second okta got = %v, want code value_already_exist", err)
	}
	if _, err := s.Create(e.ctx, other.ID, oktaInput()); code(err) != CodeDomainNotUnique || fieldOf(err) != "domain" {
		t.Errorf("claimed domain got = %v, want domain domain_not_unique", err)
	}
	en := entraInput()
	if _, err := s.Create(e.ctx, other.ID, en); err != nil {
		t.Errorf("same domain on another provider got = %v, want nil", err)
	}
}

func TestSSOConnectionsUpdate(t *testing.T) {
	tests := []struct {
		name   string
		in     SSOConnectionUpdate
		secret string
		code   string
	}{
		// source: spec/services/integrations/okta/update_service_spec.rb:54
		{name: "updates", in: SSOConnectionUpdate{Domain: core.Some("New.com"), ClientID: core.Some("new_id")}, secret: "client_secret"},
		// source: spec/services/integrations/okta/update_service_spec.rb:68
		{name: "without a client secret", in: SSOConnectionUpdate{}, secret: "client_secret"},
		// source: spec/services/integrations/okta/update_service_spec.rb:77
		{name: "blank client secret", in: SSOConnectionUpdate{ClientSecret: core.Some("  ")}, secret: "client_secret"},
		// source: spec/services/integrations/okta/update_service_spec.rb:86
		{name: "masked client secret", in: SSOConnectionUpdate{ClientSecret: core.Some("••••••••…cret")}, secret: "client_secret"},
		// source: spec/services/integrations/okta/update_service_spec.rb:96
		{name: "new client secret", in: SSOConnectionUpdate{ClientSecret: core.Some("rotated")}, secret: "rotated"},
		// source: spec/services/integrations/okta/update_service_spec.rb:106
		{name: "blank domain", in: SSOConnectionUpdate{Domain: core.Some("")}, code: core.CodeRequired},
		{name: "unknown role", in: SSOConnectionUpdate{DefaultRoleCode: core.Some("nope")}, code: CodeInvalidRole},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			o := e.org()
			s := e.ssoConnections()
			c, err := s.Create(e.ctx, o.ID, oktaInput())
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.Update(e.ctx, o.ID, c.ID, tt.in)
			if code(err) != tt.code {
				t.Fatalf("Update() error got = %v, want = %s", err, tt.code)
			}
			if tt.code != "" {
				return
			}
			if tt.in.Domain.Set && got.Domain != "new.com" {
				t.Errorf("domain got = %s, want = new.com", got.Domain)
			}
			if secret, err := s.Secret(e.ctx, e.integration(o, c.ID)); err != nil || secret != tt.secret {
				t.Errorf("Secret() got = %q, %v, want = %q", secret, err, tt.secret)
			}
		})
	}
}

func TestSSOConnectionsUpdateAdditionalDomains(t *testing.T) {
	e := newEnv(t)
	o := e.org()
	s := e.ssoConnections()
	in := entraInput()
	in.AdditionalDomains = []string{"one.com"}
	c, err := s.Create(e.ctx, o.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	// source: spec/services/integrations/entra_id/update_service_spec.rb:79
	if got, err := s.Update(e.ctx, o.ID, c.ID, SSOConnectionUpdate{ClientID: core.Some("x")}); err != nil || !slices.Equal(got.AdditionalDomains, []string{"one.com"}) {
		t.Errorf("kept domains got = %v, %v, want = [one.com]", got, err)
	}
	// source: spec/services/integrations/entra_id/update_service_spec.rb:70
	got, err := s.Update(e.ctx, o.ID, c.ID, SSOConnectionUpdate{AdditionalDomains: core.Some([]string{"Two.com"})})
	if err != nil || !slices.Equal(got.AdditionalDomains, []string{"two.com"}) {
		t.Errorf("replaced domains got = %v, %v, want = [two.com]", got, err)
	}
	var ev SecurityEvent
	for _, x := range e.log.events {
		if x.Event == EventIntegrationUpdated {
			ev = x
		}
	}
	want := map[string]any{"deleted": []string{"one.com"}, "added": []string{"two.com"}}
	d, _ := ev.Resources["additional_domains"].(map[string]any)
	if !slices.Equal(d["deleted"].([]string), want["deleted"].([]string)) || !slices.Equal(d["added"].([]string), want["added"].([]string)) {
		t.Errorf("security log diff got = %v, want = %v", ev.Resources, want)
	}
}

func TestSSOConnectionsDelete(t *testing.T) {
	tests := []struct {
		name    string
		methods []organization.AuthenticationMethod
		disable bool
		code    string
	}{
		// source: spec/services/integrations/okta/destroy_service_spec.rb:20
		{name: "destroys", methods: []organization.AuthenticationMethod{organization.AuthenticationMethodEmailPassword}},
		// source: spec/services/integrations/okta/destroy_service_spec.rb:50
		{name: "only method", code: CodeEnabledAuthenticationMethodsRequired},
		// source: spec/services/integrations/okta/destroy_service_spec.rb:63
		{name: "method already disabled", methods: []organization.AuthenticationMethod{organization.AuthenticationMethodEmailPassword}, disable: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			o := e.org(organization.AuthenticationMethodOkta)
			s := e.ssoConnections()
			c, err := s.Create(e.ctx, o.ID, oktaInput())
			if err != nil {
				t.Fatal(err)
			}
			e.tx(func(ctx context.Context, r port.Repos) error {
				got, err := r.Organizations.Get(ctx, o.ID)
				if err != nil {
					return err
				}
				got.AuthenticationMethods = append([]organization.AuthenticationMethod{}, tt.methods...)
				if !tt.disable {
					got.AuthenticationMethods = append(got.AuthenticationMethods, organization.AuthenticationMethodOkta)
				}
				return r.Organizations.Update(ctx, got)
			})
			err = s.Delete(e.ctx, o.ID, c.ID)
			if code(err) != tt.code {
				t.Fatalf("Delete() error got = %v, want = %s", err, tt.code)
			}
			if tt.code != "" {
				return
			}
			if got := e.methods(o); slices.Contains(got, organization.AuthenticationMethodOkta) {
				t.Errorf("methods got = %v, want okta disabled", got)
			}
			if _, err := s.Get(e.ctx, o.ID, c.ID); code(err) != "integration_not_found" {
				t.Errorf("Get() after delete got = %v, want integration_not_found", err)
			}
		})
	}
	// source: spec/services/integrations/okta/destroy_service_spec.rb:37
	e := newEnv(t)
	o := e.org()
	if err := e.ssoConnections().Delete(e.ctx, o.ID, core.NewID[core.IntegrationID](e.deps.IDs)); code(err) != "integration_not_found" {
		t.Errorf("Delete(unknown) got = %v, want integration_not_found", err)
	}
}

func TestSSOConnectionsListAndResolve(t *testing.T) {
	e := newEnv(t)
	o := e.org()
	s := e.ssoConnections()
	okta, err := s.Create(e.ctx, o.ID, oktaInput())
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(1)
	en, err := s.Create(e.ctx, o.ID, entraInput())
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.List(e.ctx, o.ID)
	if err != nil || len(got) != 2 || got[0].ID != okta.ID || got[1].ID != en.ID {
		t.Fatalf("List() got = %v, %v, want = [okta entra]", got, err)
	}
	res := IntegrationConnections{Tx: e.store, Secret: s.Secret, RedirectURL: func(organization.AuthenticationMethod) string { return "https://app.tanvo.test/cb" }}
	cs, err := res.ByDomain(e.ctx, organization.AuthenticationMethodEntraID, "foo.bar")
	if err != nil || len(cs) != 1 || cs[0].ClientSecret != "client_secret" {
		t.Errorf("ByDomain() got = %v, %v, want the entra connection with its secret", cs, err)
	}
	// A secret sealed for another organization cannot be opened.
	i := e.integration(o, en.ID)
	i.OrganizationID = e.org().ID
	if _, err := s.Secret(e.ctx, i); !errors.Is(err, ErrSecretUndecryptable) {
		t.Errorf("Secret(other org) got = %v, want = %v", err, ErrSecretUndecryptable)
	}
}
