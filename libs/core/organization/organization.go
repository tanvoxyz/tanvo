package organization

import (
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Defaults of a new organization (domain §4.1).
const (
	// DefaultAuditLogsRetentionDays is the default audit log retention.
	DefaultAuditLogsRetentionDays = 30
	// DefaultMaxWalletsPerCustomer is the default number of active wallets a
	// customer may hold.
	DefaultMaxWalletsPerCustomer = 6
)

// Organization is the tenant (domain §4.1). It carries identity and access
// settings only; billing settings live on its billing entities
// ([Change] domain §4.1).
type Organization struct {
	// ID identifies the organization.
	ID core.OrganizationID
	// Name is the display name; required, ≤ 255 characters.
	Name string
	// Slug is the unique lowercase handle used in portal URLs, ≤ 63
	// characters, derived from the name with [Slugify].
	Slug string
	// Email is the contact address.
	Email *string
	// Logo references the logo file.
	Logo core.BlobRef
	// Address is the postal address.
	Address core.Address
	// AuthenticationMethods are the allowed sign-in methods; at least one.
	AuthenticationMethods []AuthenticationMethod
	// FromEmailEnabled sends document emails from the billing entity's
	// email and makes the organization's email the reply-to of
	// organization emails, instead of the instance sender (lago's
	// "from_email" premium integration, a plain setting in Tanvo).
	FromEmailEnabled bool
	// AuditLogsRetentionDays is how long audit logs are kept.
	AuditLogsRetentionDays int
	// MaxWalletsPerCustomer bounds the active wallets of a customer.
	MaxWalletsPerCustomer int
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// New returns an organization with the domain defaults: every
// authentication method, a slug derived from name, 30 days of audit logs
// and 6 wallets per customer.
func New(id core.OrganizationID, name string, now time.Time) *Organization {
	return &Organization{
		ID:                     id,
		Name:                   strings.TrimSpace(name),
		Slug:                   Slugify(name),
		AuthenticationMethods:  AuthenticationMethodValues(),
		AuditLogsRetentionDays: DefaultAuditLogsRetentionDays,
		MaxWalletsPerCustomer:  DefaultMaxWalletsPerCustomer,
		CreatedAt:              now,
		UpdatedAt:              now,
	}
}

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Validate checks the invariants of domain §4.1.
func (o *Organization) Validate() error {
	var v core.Validator
	v.String("name", o.Name, 255)
	if v.String("slug", o.Slug, 63) {
		v.Check(slugPattern.MatchString(o.Slug), "slug", core.CodeInvalidFormat)
	}
	if o.Email != nil {
		v.Email("email", *o.Email)
	}
	v.Merge("address", o.Address.Validate())
	if v.Check(len(o.AuthenticationMethods) > 0, "authentication_methods", core.CodeRequired) {
		for _, m := range o.AuthenticationMethods {
			if !v.Check(m.Valid(), "authentication_methods", core.CodeInvalid) {
				break
			}
		}
		v.Check(len(o.AuthenticationMethods) == len(dedup(o.AuthenticationMethods)), "authentication_methods", core.CodeDuplicated)
	}
	v.Check(o.AuditLogsRetentionDays > 0, "audit_logs_retention_days", core.CodeOutOfRange)
	v.Check(o.MaxWalletsPerCustomer > 0, "max_wallets_per_customer", core.CodeOutOfRange)
	return v.Err()
}

// AllowsAuthentication reports whether members may sign in with m.
func (o *Organization) AllowsAuthentication(m AuthenticationMethod) bool {
	return slices.Contains(o.AuthenticationMethods, m)
}

// Slugify derives an organization slug from a name: lower-cased ASCII
// letters and digits, other runs replaced by one "-", trimmed to 63
// characters.
func Slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
			continue
		}
		dash = true
	}
	s := b.String()
	if len(s) > 63 {
		s = strings.TrimRight(s[:63], "-")
	}
	return s
}

func dedup[T comparable](s []T) []T {
	out := make([]T, 0, len(s))
	for _, x := range s {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}
