package integration

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Field error codes specific to integrations.
const (
	// CodeProviderCategoryMismatch: the provider does not serve the
	// category.
	CodeProviderCategoryMismatch = "provider_category_mismatch"
	// CodeSyncNotSupported: sync flags are only for accounting and CRM
	// integrations.
	CodeSyncNotSupported = "sync_not_supported"
)

// CategoryOf returns the category a provider serves (the lago STI class of
// each integration), and false for an unknown provider.
func CategoryOf(p Provider) (Category, bool) {
	switch p {
	case ProviderStripe, ProviderAdyen, ProviderGocardless, ProviderCashfree, ProviderMoneyhash, ProviderFlutterwave:
		return CategoryPayment, true
	case ProviderNetsuite, ProviderXero:
		return CategoryAccounting, true
	case ProviderAnrok, ProviderAvalara:
		return CategoryTax, true
	case ProviderHubspot, ProviderSalesforce:
		return CategoryCRM, true
	case ProviderOkta, ProviderEntraID:
		return CategoryAuth, true
	default:
		return "", false
	}
}

// Integration is a connection to an external service (domain §17.1).
type Integration struct {
	// ID identifies the integration.
	ID core.IntegrationID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// Code is unique per organization, ≤ 255 characters.
	Code string
	// Name is required, ≤ 255 characters.
	Name string
	// Category is the kind of service; it must match [CategoryOf] Provider.
	Category Category
	// Provider is the vendor.
	Provider Provider
	// IsDefault marks the default connection of its category (at most one
	// per category and organization).
	IsDefault bool
	// Settings holds the non-secret provider settings (account ids, flags)
	// as a JSON object.
	Settings json.RawMessage
	// Secrets holds the encrypted credentials (AES-256-GCM envelope,
	// architecture §15). Core never decrypts them.
	Secrets []byte
	// SyncInvoices pushes finalized invoices (accounting and CRM).
	SyncInvoices bool
	// SyncCreditNotes pushes credit notes (accounting).
	SyncCreditNotes bool
	// SyncPayments pushes payments (accounting).
	SyncPayments bool
	// SyncSubscriptions pushes subscriptions (CRM).
	SyncSubscriptions bool
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Validate checks the invariants of domain §17.1.
func (i *Integration) Validate() error {
	var v core.Validator
	v.Code("code", i.Code, 255)
	v.String("name", i.Name, 255)
	okCat := v.Enum("category", i.Category)
	if v.Enum("provider", i.Provider) && okCat {
		c, _ := CategoryOf(i.Provider)
		v.Check(c == i.Category, "provider", CodeProviderCategoryMismatch)
	}
	if len(i.Settings) > 0 {
		v.Check(isJSONObject(i.Settings), "settings", core.CodeInvalidFormat)
	}
	if i.Category != CategoryAccounting && i.Category != CategoryCRM {
		v.Check(!i.SyncInvoices && !i.SyncCreditNotes && !i.SyncPayments && !i.SyncSubscriptions, "sync_invoices", CodeSyncNotSupported)
	}
	return v.Err()
}

// Setting returns the string value of key in Settings, if present (lago
// get_from_settings).
func (i *Integration) Setting(key string) (string, bool) { return getSetting(i.Settings, key) }

// SetSetting stores the string value of key in Settings (lago
// push_to_settings).
func (i *Integration) SetSetting(key, value string) error {
	s, err := setSetting(i.Settings, key, value)
	if err != nil {
		return err
	}
	i.Settings = s
	return nil
}

// DefaultConflict reports whether making candidate the default of its
// category conflicts with another default among existing (at most one
// default per category).
func DefaultConflict(candidate *Integration, existing []*Integration) bool {
	if !candidate.IsDefault {
		return false
	}
	return slices.ContainsFunc(existing, func(x *Integration) bool {
		return x.ID != candidate.ID && x.OrganizationID == candidate.OrganizationID && x.Category == candidate.Category && x.IsDefault
	})
}

func isJSONObject(b json.RawMessage) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(b, &m) == nil && m != nil
}

func getSetting(raw json.RawMessage, key string) (string, bool) {
	if key == "" || len(raw) == 0 {
		return "", false
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return "", false
	}
	s, ok := m[key].(string)
	return s, ok
}

func setSetting(raw json.RawMessage, key, value string) (json.RawMessage, error) {
	m := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		if m == nil {
			m = map[string]any{}
		}
	}
	m[key] = value
	return json.Marshal(m)
}
