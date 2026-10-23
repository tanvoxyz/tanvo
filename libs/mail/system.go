package mail

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
)

// PasswordResetEmail is the data of the password reset email.
type PasswordResetEmail struct {
	// Email is the user's address.
	Email string
	// Token is the reset token, shown once in the link.
	Token string
}

type passwordResetView struct{ ResetURL, ForgotURL string }

// PasswordResetRequested builds PasswordResetMailer#requested: sent in
// English from the instance sender, with links to
// <AppURL>/reset-password/<token> and <AppURL>/forgot-password.
func (c *Composer) PasswordResetRequested(in PasswordResetEmail) (*Message, error) {
	const k = KindPasswordResetRequested
	if in.Token == "" {
		return nil, skip(k, ReasonTokenMissing)
	}
	m, err := c.systemMessage(k, []string{in.Email}, nil)
	if err != nil {
		return nil, err
	}
	var v passwordResetView
	if v.ResetURL, err = c.link("/reset-password/" + url.PathEscape(in.Token)); err != nil {
		return nil, err
	}
	if v.ForgotURL, err = c.link("/forgot-password"); err != nil {
		return nil, err
	}
	l := c.Localizer(DefaultLocale)
	if m.Subject, err = l.T("email.password_reset.subject"); err != nil {
		return nil, err
	}
	return m, c.render(m, l, layoutData{Wordmark: true}, v)
}

// systemMessage returns a message from the instance sender to to (or, when
// to is nil, to bcc), skipping when there is no recipient.
func (c *Composer) systemMessage(k Kind, to, bcc []string) (*Message, error) {
	m := &Message{Kind: k}
	var err error
	if m.To, err = parseAddresses(to); err != nil {
		return nil, fmt.Errorf("%s email: %w", k, err)
	}
	if m.Bcc, err = parseAddresses(bcc); err != nil {
		return nil, fmt.Errorf("%s email: %w", k, err)
	}
	if len(m.To)+len(m.Bcc) == 0 {
		return nil, skip(k, ReasonNoRecipient)
	}
	if m.From, err = c.systemFrom(); err != nil {
		return nil, fmt.Errorf("%s email: %w", k, err)
	}
	return m, nil
}

// APIKeyEmail is the data of the API key created, rotated and destroyed
// emails.
type APIKeyEmail struct {
	// OrganizationName is the key's organization.
	OrganizationName string
	// AdminEmails are the organization's active admins, all in BCC.
	AdminEmails []string
}

type apiKeyView struct{ OrganizationName, Action string }

// APIKeyCreated builds ApiKeyMailer#created.
func (c *Composer) APIKeyCreated(in APIKeyEmail) (*Message, error) {
	return c.apiKey(KindAPIKeyCreated, "created", in)
}

// APIKeyRotated builds ApiKeyMailer#rotated.
func (c *Composer) APIKeyRotated(in APIKeyEmail) (*Message, error) {
	return c.apiKey(KindAPIKeyRotated, "rotated", in)
}

// APIKeyDestroyed builds ApiKeyMailer#destroyed.
func (c *Composer) APIKeyDestroyed(in APIKeyEmail) (*Message, error) {
	return c.apiKey(KindAPIKeyDestroyed, "destroyed", in)
}

// apiKey sends in English to every admin in BCC.
func (c *Composer) apiKey(k Kind, action string, in APIKeyEmail) (*Message, error) {
	m, err := c.systemMessage(k, nil, in.AdminEmails)
	if err != nil {
		return nil, err
	}
	l := c.Localizer(DefaultLocale)
	if m.Subject, err = l.T("email.api_key." + action + ".subject"); err != nil {
		return nil, err
	}
	return m, c.render(m, l, layoutData{Wordmark: true}, apiKeyView{OrganizationName: in.OrganizationName, Action: action})
}

// DataExportEmail is the data of the data export email.
type DataExportEmail struct {
	// UserEmail is the requesting user.
	UserEmail string
	// ResourceType is the exported resource ("invoices", "invoice_fees").
	ResourceType string
	// Completed tells whether the export finished.
	Completed bool
	// Expired tells whether the file link expired.
	Expired bool
	// FileURL is the download link.
	FileURL string
	// Filename is the downloaded file's name.
	Filename string
}

type dataExportView struct{ ResourceType, FileURL, Filename string }

// DataExportCompleted builds DataExportMailer#completed, in English; an
// export that is not completed or expired is skipped.
func (c *Composer) DataExportCompleted(in DataExportEmail) (*Message, error) {
	const k = KindDataExportCompleted
	if in.Expired || !in.Completed {
		return nil, skip(k, ReasonExportNotReady)
	}
	m, err := c.systemMessage(k, []string{in.UserEmail}, nil)
	if err != nil {
		return nil, err
	}
	v := dataExportView{ResourceType: humanize(in.ResourceType), FileURL: in.FileURL, Filename: in.Filename}
	l := c.Localizer(DefaultLocale)
	if m.Subject, err = l.T("email.data_export.completed.subject", "resource_type", v.ResourceType); err != nil {
		return nil, err
	}
	return m, c.render(m, l, layoutData{Wordmark: true}, v)
}

// humanize is Rails' humanize.downcase: underscores become spaces and a
// trailing "_id" is dropped.
func humanize(s string) string {
	s = strings.TrimSuffix(s, "_id")
	return strings.ToLower(strings.TrimSpace(strings.ReplaceAll(s, "_", " ")))
}

// AuthenticationMethodsEmail is the data of the email telling admins the
// sign-in methods changed.
type AuthenticationMethodsEmail struct {
	// OrganizationName is the organization.
	OrganizationName string
	// OrganizationEmail is the organization's contact email.
	OrganizationEmail string
	// OrganizationLogoURL is the organization logo shown in the header.
	OrganizationLogoURL string
	// FromEmailEnabled makes the organization email the reply-to.
	FromEmailEnabled bool
	// Locale is the organization's document locale.
	Locale string
	// ActorEmail is the user who made the change.
	ActorEmail string
	// AdminEmails are the organization's admins, all in BCC.
	AdminEmails []string
	// Additions are the enabled methods.
	Additions []organization.AuthenticationMethod
	// Deletions are the disabled methods.
	Deletions []organization.AuthenticationMethod
}

type authMethodsView struct{ ActorEmail, Enabled, Disabled string }

// AuthenticationMethodsUpdated builds
// OrganizationMailer#authentication_methods_updated: from the instance
// sender, reply-to the organization's from address, to every admin in
// BCC; skipped without changes.
func (c *Composer) AuthenticationMethodsUpdated(in AuthenticationMethodsEmail) (*Message, error) {
	const k = KindAuthenticationMethodsUpdated
	if len(in.Additions) == 0 && len(in.Deletions) == 0 {
		return nil, skip(k, ReasonNoChanges)
	}
	m, err := c.systemMessage(k, nil, in.AdminEmails)
	if err != nil {
		return nil, err
	}
	rt, err := c.fromAddress(strings.TrimSpace(in.OrganizationEmail), in.FromEmailEnabled, in.OrganizationName)
	if err != nil {
		return nil, fmt.Errorf("%s email: %w", k, err)
	}
	m.ReplyTo = []Address{rt}
	v := authMethodsView{ActorEmail: in.ActorEmail, Enabled: methodSentence(in.Additions), Disabled: methodSentence(in.Deletions)}
	l := c.Localizer(in.Locale)
	if m.Subject, err = l.T("email.organization.authentication_methods_updated.subject"); err != nil {
		return nil, err
	}
	lay := layoutData{OrganizationName: in.OrganizationName, LogoURL: in.OrganizationLogoURL, ContactEmail: in.OrganizationEmail, Wordmark: true}
	return m, c.render(m, l, lay, v)
}

// MethodName returns the display name of an authentication method.
func MethodName(m organization.AuthenticationMethod) string {
	switch m {
	case organization.AuthenticationMethodEmailPassword:
		return "Email Password"
	case organization.AuthenticationMethodGoogleOAuth:
		return "Google Oauth"
	case organization.AuthenticationMethodOkta:
		return "Okta"
	case organization.AuthenticationMethodEntraID:
		return "Entra ID"
	case organization.AuthenticationMethodOIDC:
		return "OIDC"
	default:
		return string(m)
	}
}

// methodSentence joins method names as Rails' to_sentence does in
// English: "A", "A and B", "A, B, and C".
func methodSentence(ms []organization.AuthenticationMethod) string {
	names := make([]string, len(ms))
	for i, m := range ms {
		names[i] = MethodName(m)
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
	}
}

// FeatureToggledEmail is the data of the email telling the organization's
// owner that support toggled a feature.
type FeatureToggledEmail struct {
	// OrganizationName is the organization.
	OrganizationName string
	// OwnerEmail is the earliest admin; empty skips the email.
	OwnerEmail string
	// FeatureKey is the toggled feature ("okta").
	FeatureKey string
	// Enabled tells whether it was turned on.
	Enabled bool
	// ActorEmail is who toggled it.
	ActorEmail string
}

type featureView struct{ OrganizationName, FeatureKey, Action, ActorEmail string }

// FeatureToggled builds AdminMailer#feature_toggled.
func (c *Composer) FeatureToggled(in FeatureToggledEmail) (*Message, error) {
	const k = KindFeatureToggled
	m, err := c.systemMessage(k, []string{in.OwnerEmail}, nil)
	if err != nil {
		return nil, err
	}
	l := c.Localizer(DefaultLocale)
	key := "email.admin.feature_toggled.action_disabled"
	if in.Enabled {
		key = "email.admin.feature_toggled.action_enabled"
	}
	v := featureView{OrganizationName: in.OrganizationName, FeatureKey: in.FeatureKey, ActorEmail: in.ActorEmail}
	if v.Action, err = l.T(key); err != nil {
		return nil, err
	}
	if m.Subject, err = l.T("email.admin.feature_toggled.subject", "feature_key", in.FeatureKey, "action", v.Action); err != nil {
		return nil, err
	}
	return m, c.render(m, l, layoutData{OrganizationName: in.OrganizationName}, v)
}

// InvitationEmail is the data of the invitation email (new in Tanvo).
type InvitationEmail struct {
	// Email is the invitee.
	Email string
	// OrganizationName is the inviting organization.
	OrganizationName string
	// InviterEmail is the user who sent the invite.
	InviterEmail string
	// RoleName is the role granted on acceptance.
	RoleName string
	// Token is the invite token, shown once in the link
	// <AppURL>/invitation/<token> (lago's invite URL).
	Token string
	// ExpiresAt ends the invite's validity.
	ExpiresAt time.Time
}

type invitationView struct{ OrganizationName, InviterEmail, RoleName, URL, ExpiresOn string }

// Invitation builds the invitation email, in English from the instance
// sender.
func (c *Composer) Invitation(in InvitationEmail) (*Message, error) {
	const k = KindInvitation
	if in.Token == "" {
		return nil, skip(k, ReasonTokenMissing)
	}
	m, err := c.systemMessage(k, []string{in.Email}, nil)
	if err != nil {
		return nil, err
	}
	l := c.Localizer(DefaultLocale)
	v := invitationView{OrganizationName: in.OrganizationName, InviterEmail: in.InviterEmail, RoleName: in.RoleName}
	if v.URL, err = c.link("/invitation/" + url.PathEscape(in.Token)); err != nil {
		return nil, err
	}
	if !in.ExpiresAt.IsZero() {
		if v.ExpiresOn, err = l.Date(core.DateOf(in.ExpiresAt, time.UTC)); err != nil {
			return nil, err
		}
	}
	if m.Subject, err = l.T("email.invitation.subject", "organization_name", in.OrganizationName); err != nil {
		return nil, err
	}
	return m, c.render(m, l, layoutData{Wordmark: true}, v)
}
