package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// MailAuthenticationMethodsUpdated is
// OrganizationMailer#authentication_methods_updated: Data["actor_email"] is
// the user who made the change, Data["additions"] and Data["deletions"]
// the comma-separated methods.
const MailAuthenticationMethodsUpdated = "organization.authentication_methods_updated"

// Field and rule error codes of the organization logo (lago ImageValidator).
const (
	// CodeLogoInvalidSize: the logo is larger than [MaxOrganizationLogoSize].
	CodeLogoInvalidSize = "invalid_size"
	// CodeLogoInvalidContentType: the logo is neither a PNG nor a JPEG.
	CodeLogoInvalidContentType = "invalid_content_type"
	// CodeLogoStorageNotConfigured: a logo was sent to a server without a
	// blob store.
	CodeLogoStorageNotConfigured = "logo_storage_not_configured"
)

// MaxOrganizationLogoSize is the largest logo accepted, in bytes (lago
// app/models/organization.rb validates :logo, image: {max_size: 800.kilobytes}).
const MaxOrganizationLogoSize = 800 << 10

var organizationLogoTypes = []string{"image/png", "image/jpeg"}

// OrganizationUpdate holds the organization settings to change (lago
// app/services/organizations/update_service.rb:16-49); unset fields are
// kept. Billing settings are changed on billing entities.
type OrganizationUpdate struct {
	// Slug is trimmed and lower-cased.
	Slug core.Optional[string]
	// Email is the contact address; "" clears it.
	Email core.Optional[string]
	// Address is the postal address.
	Address core.Optional[core.Address]
	// AuthenticationMethods are the allowed sign-in methods.
	AuthenticationMethods core.Optional[[]organization.AuthenticationMethod]
	// Logo is a PNG or JPEG image; empty removes the logo.
	Logo core.Optional[[]byte]
	// DocumentNumberPrefix is upper-cased (lago Organization#document_number_prefix=).
	DocumentNumberPrefix core.Optional[string]
	// FromEmailEnabled sends document emails from the billing entities'
	// own addresses ([organization.Organization.FromEmailEnabled]).
	FromEmailEnabled core.Optional[bool]
}

// OrganizationSettings reads and changes an organization's identity and
// access settings.
type OrganizationSettings struct {
	d     Deps
	blobs port.BlobStore
}

// NewOrganizationSettings returns the service storing logos in blobs; a
// nil blobs refuses logos with [CodeLogoStorageNotConfigured].
func NewOrganizationSettings(d Deps, blobs port.BlobStore) *OrganizationSettings {
	return &OrganizationSettings{d: d, blobs: blobs}
}

// Get returns the organization org.
func (s *OrganizationSettings) Get(ctx context.Context, org core.OrganizationID) (*organization.Organization, error) {
	var out *organization.Organization
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = r.Organizations.Get(ctx, org)
		if errors.Is(err, core.ErrNotFound) {
			return notFound("organization")
		}
		return err
	})
	return out, err
}

// LogoURL returns a download URL of the organization's logo, or "" when
// it has none or no blob store is configured.
func (s *OrganizationSettings) LogoURL(ctx context.Context, o *organization.Organization) (string, error) {
	if o.Logo.IsZero() || s.blobs == nil {
		return "", nil
	}
	u, err := s.blobs.URL(ctx, o.ID, o.Logo)
	if err != nil {
		return "", fmt.Errorf("logo url of organization %s: %w", o.ID, err)
	}
	return u, nil
}

// Update changes the settings of org (lago Organizations::UpdateService).
// A change of the authentication methods made by actor (non-zero) emails
// the organization's admins with the additions and deletions
// (update_service.rb:34-49). A slug in use is "slug" already_exists.
func (s *OrganizationSettings) Update(ctx context.Context, org core.OrganizationID, actor core.UserID, u OrganizationUpdate) (*organization.Organization, error) {
	if u.Logo.Set && len(u.Logo.Value) > 0 {
		var v core.Validator
		v.Check(len(u.Logo.Value) <= MaxOrganizationLogoSize, "logo", CodeLogoInvalidSize)
		v.Check(slices.Contains(organizationLogoTypes, logoContentType(u.Logo.Value)), "logo", CodeLogoInvalidContentType)
		if err := v.Err(); err != nil {
			return nil, err
		}
		if s.blobs == nil {
			return nil, core.NewRuleError(CodeLogoStorageNotConfigured, "logos cannot be stored: no blob store is configured")
		}
	}
	var (
		out     *organization.Organization
		oldLogo core.BlobRef
		newLogo core.BlobRef
	)
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		o, err := r.Organizations.Get(ctx, org)
		if errors.Is(err, core.ErrNotFound) {
			return notFound("organization")
		}
		if err != nil {
			return fmt.Errorf("get organization: %w", err)
		}
		if u.Slug.Set {
			slug := strings.ToLower(strings.TrimSpace(u.Slug.Value))
			if slug != o.Slug {
				if err := checkSlug(slug); err != nil {
					return err
				}
			}
			o.Slug = slug
		}
		if u.Email.Set {
			o.Email = nil
			if e := payment.SanitizeEmail(u.Email.Value); e != "" {
				o.Email = &e
			}
		}
		u.Address.Apply(&o.Address)
		if u.DocumentNumberPrefix.Set {
			o.DocumentNumberPrefix = strings.ToUpper(u.DocumentNumberPrefix.Value)
		}
		if u.FromEmailEnabled.Set {
			o.FromEmailEnabled = u.FromEmailEnabled.Value
		}
		var additions, deletions []organization.AuthenticationMethod
		if u.AuthenticationMethods.Set {
			next := u.AuthenticationMethods.Value
			deletions = slices.DeleteFunc(slices.Clone(o.AuthenticationMethods), func(m organization.AuthenticationMethod) bool { return slices.Contains(next, m) })
			additions = slices.DeleteFunc(slices.Clone(next), func(m organization.AuthenticationMethod) bool { return slices.Contains(o.AuthenticationMethods, m) })
			o.AuthenticationMethods = slices.Clone(next)
		}
		if err := o.Validate(); err != nil {
			return err
		}
		if u.Logo.Set {
			oldLogo = o.Logo
			o.Logo = ""
			if len(u.Logo.Value) > 0 {
				t := logoContentType(u.Logo.Value)
				ref, err := s.blobs.Put(ctx, org, "logo."+strings.TrimPrefix(t, "image/"), t, bytes.NewReader(u.Logo.Value))
				if err != nil {
					return fmt.Errorf("store logo: %w", err)
				}
				o.Logo, newLogo = ref, ref
			}
		}
		o.UpdatedAt = s.d.now()
		if err := r.Organizations.Update(ctx, o); err != nil {
			if errors.Is(err, core.ErrConflict) {
				return fieldError("slug", core.CodeAlreadyExists)
			}
			return fmt.Errorf("update organization: %w", err)
		}
		if !actor.IsZero() && (len(additions) > 0 || len(deletions) > 0) {
			user, err := r.Users.Get(ctx, actor)
			if err != nil {
				return fmt.Errorf("get user: %w", err)
			}
			if err := enqueueMail(ctx, r, MailJob{Template: MailAuthenticationMethodsUpdated, OrganizationID: org, Data: map[string]string{
				"actor_email": user.Email, "additions": joinMethods(additions), "deletions": joinMethods(deletions),
			}}); err != nil {
				return err
			}
		}
		out = o
		return nil
	})
	if err != nil {
		if !newLogo.IsZero() {
			s.dropLogo(ctx, org, newLogo)
		}
		return nil, err
	}
	if oldLogo != out.Logo {
		s.dropLogo(ctx, org, oldLogo)
	}
	return out, nil
}

// dropLogo deletes a logo no longer referenced; a failure is only logged.
func (s *OrganizationSettings) dropLogo(ctx context.Context, org core.OrganizationID, ref core.BlobRef) {
	if ref.IsZero() || s.blobs == nil {
		return
	}
	if err := s.blobs.Delete(ctx, org, ref); err != nil {
		s.d.logger().WarnContext(ctx, "delete organization logo", "organization_id", org.String(), "error", err)
	}
}

// reservedSlugs are the slugs an organization cannot take: the
// dashboard's routes (lago app/models/concerns/organizations/sluggable.rb:9-17).
var reservedSlugs = []string{
	"auth", "login", "sign-up", "forgot-password", "reset-password", "invitation",
	"customer-portal", "404", "forbidden", "api", "admin", "graphql", "webhooks", "google", "okta", "entra",
	"settings", "new", "design-system", "devtool",
	"customers", "customer", "plans", "plan", "invoices", "invoice", "subscriptions",
	"coupons", "coupon", "add-ons", "add-on", "billable-metrics", "billable-metric",
	"credit-notes", "analytics", "analytics-v2", "forecasts", "payments", "payment",
	"features", "feature", "tax", "webhook", "api-keys", "create", "update", "duplicate",
}

// checkSlug applies the rules of a changed slug: 3 to 40 characters and
// not reserved; the format is checked by [organization.Organization.Validate]
// (lago sluggable.rb:20-26).
func checkSlug(slug string) error {
	var v core.Validator
	v.Check(len(slug) >= 3, "slug", core.CodeTooShort)
	v.Check(len(slug) <= 40, "slug", core.CodeTooLong)
	v.Check(!slices.Contains(reservedSlugs, slug), "slug", core.CodeInvalid)
	return v.Err()
}

func logoContentType(b []byte) string {
	t, _, _ := strings.Cut(http.DetectContentType(b), ";")
	return t
}

func joinMethods(ms []organization.AuthenticationMethod) string {
	s := make([]string, len(ms))
	for i, m := range ms {
		s[i] = string(m)
	}
	return strings.Join(s, ",")
}

// SplitMethods parses the comma-separated methods of a
// [MailAuthenticationMethodsUpdated] job.
func SplitMethods(s string) []organization.AuthenticationMethod {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]organization.AuthenticationMethod, len(parts))
	for i, p := range parts {
		out[i] = organization.AuthenticationMethod(p)
	}
	return out
}
