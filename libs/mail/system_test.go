package mail

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core/organization"
)

func TestAPIKeyEmails(t *testing.T) {
	admins := []string{"admin1@hooli.test", "admin2@hooli.test"}
	tests := []struct {
		name    string
		build   func(*Composer, APIKeyEmail) (*Message, error)
		subject string
	}{
		// source: spec/mailers/api_key_mailer_spec.rb:16
		{"rotated", (*Composer).APIKeyRotated, "Your Tanvo API key has been rolled"},
		// source: spec/mailers/api_key_mailer_spec.rb:50
		{"created", (*Composer).APIKeyCreated, "A new Tanvo API key has been created"},
		// source: spec/mailers/api_key_mailer_spec.rb:84
		{"destroyed", (*Composer).APIKeyDestroyed, "A Tanvo API key has been deleted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := tt.build(newComposer(t), APIKeyEmail{OrganizationName: "Hooli & Co", AdminEmails: admins})
			if err != nil {
				t.Fatalf("build error = %v", err)
			}
			if m.Subject != tt.subject {
				t.Errorf("Subject = %q, want = %q", m.Subject, tt.subject)
			}
			// source: spec/mailers/api_key_mailer_spec.rb:19 (admins only, in BCC)
			if got := addresses(m.Bcc); !slices.Equal(got, admins) {
				t.Errorf("Bcc = %v, want = %v", got, admins)
			}
			if len(m.To) != 0 {
				t.Errorf("To = %v, want = none", m.To)
			}
			// source: spec/mailers/api_key_mailer_spec.rb:34 (CGI.escapeHTML of the name)
			if !strings.Contains(m.HTML, "Hooli &amp; Co") {
				t.Errorf("HTML does not contain the escaped organization name")
			}
			if m.From.Address != testFrom {
				t.Errorf("From = %q, want = %q", m.From.Address, testFrom)
			}
		})
	}
	t.Run("no admins", func(t *testing.T) {
		_, err := newComposer(t).APIKeyCreated(APIKeyEmail{OrganizationName: "Hooli"})
		wantSkip(t, err, ReasonNoRecipient)
	})
}

func TestDataExportCompleted(t *testing.T) {
	export := func(resource string) DataExportEmail {
		return DataExportEmail{
			UserEmail: "user@hooli.test", ResourceType: resource, Completed: true,
			FileURL: "https://api.tanvo.test/files/eyJf", Filename: "export.csv",
		}
	}
	subjects := []struct{ resource, want string }{
		// source: spec/mailers/data_export_mailer_spec.rb:17
		{"invoices", "Your Tanvo invoices export is ready!"},
		// source: spec/mailers/data_export_mailer_spec.rb:23
		{"invoice_fees", "Your Tanvo invoice fees export is ready!"},
	}
	for _, tt := range subjects {
		m, err := newComposer(t).DataExportCompleted(export(tt.resource))
		if err != nil {
			t.Fatalf("DataExportCompleted(%s) error = %v", tt.resource, err)
		}
		if m.Subject != tt.want {
			t.Errorf("Subject(%s) = %q, want = %q", tt.resource, m.Subject, tt.want)
		}
	}

	m, err := newComposer(t).DataExportCompleted(export("invoices"))
	if err != nil {
		t.Fatalf("DataExportCompleted() error = %v", err)
	}
	// source: spec/mailers/data_export_mailer_spec.rb:33
	if got, want := addresses(m.To), []string{"user@hooli.test"}; !slices.Equal(got, want) {
		t.Errorf("To = %v, want = %v", got, want)
	}
	// source: spec/mailers/data_export_mailer_spec.rb:39
	for _, s := range []string{"will be available for 7 days", "https://api.tanvo.test/files/eyJf"} {
		if !strings.Contains(m.HTML, s) {
			t.Errorf("HTML does not contain %q", s)
		}
	}

	// source: spec/mailers/data_export_mailer_spec.rb:50
	t.Run("not completed", func(t *testing.T) {
		in := export("invoices")
		in.Completed = false
		_, err := newComposer(t).DataExportCompleted(in)
		wantSkip(t, err, ReasonExportNotReady)
	})
	t.Run("expired", func(t *testing.T) {
		in := export("invoices")
		in.Expired = true
		_, err := newComposer(t).DataExportCompleted(in)
		wantSkip(t, err, ReasonExportNotReady)
	})
}

func TestPasswordResetRequested(t *testing.T) {
	// source: spec/mailers/password_reset_mailer_spec.rb:11
	m, err := newComposer(t).PasswordResetRequested(PasswordResetEmail{Email: "user@hooli.test", Token: "abc/123"})
	if err != nil {
		t.Fatalf("PasswordResetRequested() error = %v", err)
	}
	if got, want := addresses(m.To), []string{"user@hooli.test"}; !slices.Equal(got, want) {
		t.Errorf("To = %v, want = %v", got, want)
	}
	if want := testAppURL + "/reset-password/abc%2F123"; !strings.Contains(m.HTML, want) {
		t.Errorf("HTML has no reset link %s", want)
	}
	if want := testAppURL + "/forgot-password"; !strings.Contains(m.Text, want) {
		t.Errorf("Text has no forgot link %s", want)
	}
	if m.Locale != "en" || m.Subject != "Reset your Tanvo password" {
		t.Errorf("Locale, Subject = %q, %q, want = en, %q", m.Locale, m.Subject, "Reset your Tanvo password")
	}

	// source: spec/mailers/password_reset_mailer_spec.rb:17
	t.Run("user email nil", func(t *testing.T) {
		_, err := newComposer(t).PasswordResetRequested(PasswordResetEmail{Token: "abc"})
		wantSkip(t, err, ReasonNoRecipient)
	})
	t.Run("no token", func(t *testing.T) {
		_, err := newComposer(t).PasswordResetRequested(PasswordResetEmail{Email: "user@hooli.test"})
		wantSkip(t, err, ReasonTokenMissing)
	})
	t.Run("no app url", func(t *testing.T) {
		c := newComposer(t, func(c *Config) { c.AppURL = "" })
		if _, err := c.PasswordResetRequested(PasswordResetEmail{Email: "user@hooli.test", Token: "abc"}); err == nil {
			t.Errorf("PasswordResetRequested() error = nil, want an AppURL error")
		}
	})
}

func TestAuthenticationMethodsUpdated(t *testing.T) {
	// source: spec/mailers/organization_mailer_spec.rb:25
	t.Run("changes", func(t *testing.T) {
		in := authMethodsEmail("en")
		in.OrganizationEmail = ""
		m, err := newComposer(t).AuthenticationMethodsUpdated(in)
		if err != nil {
			t.Fatalf("AuthenticationMethodsUpdated() error = %v", err)
		}
		if want := "Login method updated in your Tanvo workspace"; m.Subject != want {
			t.Errorf("Subject = %q, want = %q", m.Subject, want)
		}
		if got := addresses(m.Bcc); !slices.Equal(got, in.AdminEmails) {
			t.Errorf("Bcc = %v, want = %v", got, in.AdminEmails)
		}
		if m.From.Address != testFrom {
			t.Errorf("From = %q, want = %q", m.From.Address, testFrom)
		}
		if got, want := addresses(m.ReplyTo), []string{testFrom}; !slices.Equal(got, want) {
			t.Errorf("ReplyTo = %v, want = %v", got, want)
		}
		for _, s := range []string{"admin0@hooli.test", "Enabled Okta", "Disabled Google Oauth"} {
			if !strings.Contains(m.HTML, s) {
				t.Errorf("HTML does not contain %q", s)
			}
		}
	})

	t.Run("organization sends from its own email", func(t *testing.T) {
		in := authMethodsEmail("en")
		in.FromEmailEnabled = true
		m, err := newComposer(t).AuthenticationMethodsUpdated(in)
		if err != nil {
			t.Fatalf("AuthenticationMethodsUpdated() error = %v", err)
		}
		if got, want := addresses(m.ReplyTo), []string{"contact@hooli.test"}; !slices.Equal(got, want) {
			t.Errorf("ReplyTo = %v, want = %v", got, want)
		}
		if m.From.Address != testFrom {
			t.Errorf("From = %q, want = %q", m.From.Address, testFrom)
		}
	})

	// source: spec/mailers/organization_mailer_spec.rb:37
	t.Run("without changes", func(t *testing.T) {
		in := authMethodsEmail("en")
		in.Additions, in.Deletions = nil, nil
		_, err := newComposer(t).AuthenticationMethodsUpdated(in)
		wantSkip(t, err, ReasonNoChanges)
	})
}

func TestMethodSentence(t *testing.T) {
	const (
		ep = organization.AuthenticationMethodEmailPassword
		g  = organization.AuthenticationMethodGoogleOAuth
		o  = organization.AuthenticationMethodOkta
		e  = organization.AuthenticationMethodEntraID
	)
	tests := []struct {
		in   []organization.AuthenticationMethod
		want string
	}{
		{nil, ""},
		{[]organization.AuthenticationMethod{o}, "Okta"},
		{[]organization.AuthenticationMethod{ep, g}, "Email Password and Google Oauth"},
		{[]organization.AuthenticationMethod{ep, g, e}, "Email Password, Google Oauth, and Entra ID"},
		{[]organization.AuthenticationMethod{organization.AuthenticationMethodOIDC}, "OIDC"},
	}
	for _, tt := range tests {
		if got := methodSentence(tt.in); got != tt.want {
			t.Errorf("methodSentence(%v) = %q, want = %q", tt.in, got, tt.want)
		}
	}
}

func TestFeatureToggled(t *testing.T) {
	in := FeatureToggledEmail{OrganizationName: "Hooli Inc", OwnerEmail: "owner@hooli.test", FeatureKey: "okta", Enabled: true, ActorEmail: "cs@tanvo.test"}

	// source: spec/mailers/admin_mailer_spec.rb:30
	m, err := newComposer(t).FeatureToggled(in)
	if err != nil {
		t.Fatalf("FeatureToggled() error = %v", err)
	}
	if m.From.Address != testFrom {
		t.Errorf("From = %q, want = %q", m.From.Address, testFrom)
	}
	if got, want := addresses(m.To), []string{"owner@hooli.test"}; !slices.Equal(got, want) {
		t.Errorf("To = %v, want = %v", got, want)
	}
	if want := "Feature okta has been enabled on your organization"; m.Subject != want {
		t.Errorf("Subject = %q, want = %q", m.Subject, want)
	}
	// source: spec/mailers/admin_mailer_spec.rb:36
	for _, s := range []string{"Hooli Inc", "okta", "cs@tanvo.test"} {
		if !strings.Contains(m.HTML, s) {
			t.Errorf("HTML does not contain %q", s)
		}
	}

	// source: spec/mailers/admin_mailer_spec.rb:44
	t.Run("toggled off", func(t *testing.T) {
		off := in
		off.Enabled = false
		m, err := newComposer(t).FeatureToggled(off)
		if err != nil {
			t.Fatalf("FeatureToggled() error = %v", err)
		}
		if want := "Feature okta has been disabled on your organization"; m.Subject != want {
			t.Errorf("Subject = %q, want = %q", m.Subject, want)
		}
	})

	// source: spec/mailers/admin_mailer_spec.rb:72 (the caller passes the
	// oldest admin, spec line 62; none skips the email)
	t.Run("no admin", func(t *testing.T) {
		none := in
		none.OwnerEmail = ""
		_, err := newComposer(t).FeatureToggled(none)
		wantSkip(t, err, ReasonNoRecipient)
	})
}

func TestInvitation(t *testing.T) {
	in := InvitationEmail{
		Email: "new@hooli.test", OrganizationName: "Hooli Inc", InviterEmail: "admin@hooli.test", RoleName: "Finance",
		Token: "tok", ExpiresAt: time.Date(2024, time.October, 17, 23, 30, 0, 0, time.UTC),
	}
	m, err := newComposer(t).Invitation(in)
	if err != nil {
		t.Fatalf("Invitation() error = %v", err)
	}
	if want := "You have been invited to join Hooli Inc on Tanvo"; m.Subject != want {
		t.Errorf("Subject = %q, want = %q", m.Subject, want)
	}
	for _, s := range []string{testAppURL + "/invitation/tok", "admin@hooli.test invited you to join Hooli Inc on Tanvo as Finance.", "Oct 17, 2024"} {
		if !strings.Contains(m.Text, s) {
			t.Errorf("Text does not contain %q", s)
		}
	}
	t.Run("no token", func(t *testing.T) {
		bad := in
		bad.Token = ""
		_, err := newComposer(t).Invitation(bad)
		wantSkip(t, err, ReasonTokenMissing)
	})
}
