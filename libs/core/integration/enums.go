package integration

import (
	"github.com/tanvoxyz/tanvo/libs/core"
)

// Category is the kind of service an integration connects to (domain §17.1).
type Category string

// Category values.
const (
	// CategoryPayment is "payment": a payment provider.
	CategoryPayment Category = "payment"
	// CategoryAccounting is "accounting": an accounting system.
	CategoryAccounting Category = "accounting"
	// CategoryTax is "tax": a tax provider.
	CategoryTax Category = "tax"
	// CategoryCRM is "crm": a CRM.
	CategoryCRM Category = "crm"
	// CategoryAuth is "auth": an identity provider.
	CategoryAuth Category = "auth"
)

// CategoryValues returns every Category, in declaration order.
func CategoryValues() []Category {
	return []Category{CategoryPayment, CategoryAccounting, CategoryTax, CategoryCRM, CategoryAuth}
}

// Valid reports whether v is one of the declared Category values.
func (v Category) Valid() bool {
	switch v {
	case CategoryPayment, CategoryAccounting, CategoryTax, CategoryCRM, CategoryAuth:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v Category) String() string { return string(v) }

// ParseCategory parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseCategory(s string) (Category, error) {
	if v := Category(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("integration.Category", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v Category) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *Category) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseCategory(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// Provider is the vendor of an integration (domain §17.1).
type Provider string

// Provider values.
const (
	// ProviderStripe is "stripe": Stripe payments.
	ProviderStripe Provider = "stripe"
	// ProviderAdyen is "adyen": Adyen payments.
	ProviderAdyen Provider = "adyen"
	// ProviderGocardless is "gocardless": GoCardless direct debits.
	ProviderGocardless Provider = "gocardless"
	// ProviderCashfree is "cashfree": Cashfree payments.
	ProviderCashfree Provider = "cashfree"
	// ProviderMoneyhash is "moneyhash": Moneyhash payments.
	ProviderMoneyhash Provider = "moneyhash"
	// ProviderFlutterwave is "flutterwave": Flutterwave payments.
	ProviderFlutterwave Provider = "flutterwave"
	// ProviderNetsuite is "netsuite": Oracle NetSuite accounting.
	ProviderNetsuite Provider = "netsuite"
	// ProviderXero is "xero": Xero accounting.
	ProviderXero Provider = "xero"
	// ProviderAnrok is "anrok": Anrok tax.
	ProviderAnrok Provider = "anrok"
	// ProviderAvalara is "avalara": Avalara tax.
	ProviderAvalara Provider = "avalara"
	// ProviderHubspot is "hubspot": HubSpot CRM.
	ProviderHubspot Provider = "hubspot"
	// ProviderSalesforce is "salesforce": Salesforce CRM.
	ProviderSalesforce Provider = "salesforce"
	// ProviderOkta is "okta": Okta identity.
	ProviderOkta Provider = "okta"
	// ProviderEntraID is "entra_id": Microsoft Entra ID identity.
	ProviderEntraID Provider = "entra_id"
)

// ProviderValues returns every Provider, in declaration order.
func ProviderValues() []Provider {
	return []Provider{ProviderStripe, ProviderAdyen, ProviderGocardless, ProviderCashfree, ProviderMoneyhash, ProviderFlutterwave, ProviderNetsuite, ProviderXero, ProviderAnrok, ProviderAvalara, ProviderHubspot, ProviderSalesforce, ProviderOkta, ProviderEntraID}
}

// Valid reports whether v is one of the declared Provider values.
func (v Provider) Valid() bool {
	switch v {
	case ProviderStripe, ProviderAdyen, ProviderGocardless, ProviderCashfree, ProviderMoneyhash, ProviderFlutterwave, ProviderNetsuite, ProviderXero, ProviderAnrok, ProviderAvalara, ProviderHubspot, ProviderSalesforce, ProviderOkta, ProviderEntraID:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v Provider) String() string { return string(v) }

// ParseProvider parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseProvider(s string) (Provider, error) {
	if v := Provider(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("integration.Provider", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v Provider) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *Provider) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseProvider(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// MappableType is the kind of catalog object an item mapping maps (domain §17.3).
type MappableType string

// MappableType values.
const (
	// MappableTypeAddOn is "add_on": an add-on.
	MappableTypeAddOn MappableType = "add_on"
	// MappableTypeBillableMetric is "billable_metric": a billable metric.
	MappableTypeBillableMetric MappableType = "billable_metric"
)

// MappableTypeValues returns every MappableType, in declaration order.
func MappableTypeValues() []MappableType {
	return []MappableType{MappableTypeAddOn, MappableTypeBillableMetric}
}

// Valid reports whether v is one of the declared MappableType values.
func (v MappableType) Valid() bool {
	switch v {
	case MappableTypeAddOn, MappableTypeBillableMetric:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v MappableType) String() string { return string(v) }

// ParseMappableType parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseMappableType(s string) (MappableType, error) {
	if v := MappableType(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("integration.MappableType", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v MappableType) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *MappableType) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseMappableType(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// CollectionMappingType is the kind of fallback mapping of an integration (domain §17.3).
type CollectionMappingType string

// CollectionMappingType values.
const (
	// CollectionMappingTypeFallbackItem is "fallback_item": the item used when nothing else maps.
	CollectionMappingTypeFallbackItem CollectionMappingType = "fallback_item"
	// CollectionMappingTypeCoupon is "coupon": coupon lines.
	CollectionMappingTypeCoupon CollectionMappingType = "coupon"
	// CollectionMappingTypeSubscriptionFee is "subscription_fee": subscription fees.
	CollectionMappingTypeSubscriptionFee CollectionMappingType = "subscription_fee"
	// CollectionMappingTypeMinimumCommitment is "minimum_commitment": minimum commitment true-ups.
	CollectionMappingTypeMinimumCommitment CollectionMappingType = "minimum_commitment"
	// CollectionMappingTypeTax is "tax": tax lines.
	CollectionMappingTypeTax CollectionMappingType = "tax"
	// CollectionMappingTypePrepaidCredit is "prepaid_credit": prepaid credit lines.
	CollectionMappingTypePrepaidCredit CollectionMappingType = "prepaid_credit"
	// CollectionMappingTypeCreditNote is "credit_note": credit notes.
	CollectionMappingTypeCreditNote CollectionMappingType = "credit_note"
	// CollectionMappingTypeAccount is "account": the receivable account.
	CollectionMappingTypeAccount CollectionMappingType = "account"
	// CollectionMappingTypeCurrencies is "currencies": the currency code mapping.
	CollectionMappingTypeCurrencies CollectionMappingType = "currencies"
)

// CollectionMappingTypeValues returns every CollectionMappingType, in declaration order.
func CollectionMappingTypeValues() []CollectionMappingType {
	return []CollectionMappingType{CollectionMappingTypeFallbackItem, CollectionMappingTypeCoupon, CollectionMappingTypeSubscriptionFee, CollectionMappingTypeMinimumCommitment, CollectionMappingTypeTax, CollectionMappingTypePrepaidCredit, CollectionMappingTypeCreditNote, CollectionMappingTypeAccount, CollectionMappingTypeCurrencies}
}

// Valid reports whether v is one of the declared CollectionMappingType values.
func (v CollectionMappingType) Valid() bool {
	switch v {
	case CollectionMappingTypeFallbackItem, CollectionMappingTypeCoupon, CollectionMappingTypeSubscriptionFee, CollectionMappingTypeMinimumCommitment, CollectionMappingTypeTax, CollectionMappingTypePrepaidCredit, CollectionMappingTypeCreditNote, CollectionMappingTypeAccount, CollectionMappingTypeCurrencies:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v CollectionMappingType) String() string { return string(v) }

// ParseCollectionMappingType parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseCollectionMappingType(s string) (CollectionMappingType, error) {
	if v := CollectionMappingType(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("integration.CollectionMappingType", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v CollectionMappingType) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *CollectionMappingType) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseCollectionMappingType(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// ItemType is the kind of a cached external catalog item (domain §17.3).
type ItemType string

// ItemType values.
const (
	// ItemTypeStandard is "standard": a sellable item.
	ItemTypeStandard ItemType = "standard"
	// ItemTypeTax is "tax": a tax item.
	ItemTypeTax ItemType = "tax"
	// ItemTypeAccount is "account": a ledger account.
	ItemTypeAccount ItemType = "account"
)

// ItemTypeValues returns every ItemType, in declaration order.
func ItemTypeValues() []ItemType {
	return []ItemType{ItemTypeStandard, ItemTypeTax, ItemTypeAccount}
}

// Valid reports whether v is one of the declared ItemType values.
func (v ItemType) Valid() bool {
	switch v {
	case ItemTypeStandard, ItemTypeTax, ItemTypeAccount:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v ItemType) String() string { return string(v) }

// ParseItemType parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseItemType(s string) (ItemType, error) {
	if v := ItemType(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("integration.ItemType", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v ItemType) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *ItemType) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseItemType(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// ResourceType is the kind of a local object synced to an integration (domain §17.3).
type ResourceType string

// ResourceType values.
const (
	// ResourceTypeInvoice is "invoice": an invoice.
	ResourceTypeInvoice ResourceType = "invoice"
	// ResourceTypePayment is "payment": a payment.
	ResourceTypePayment ResourceType = "payment"
	// ResourceTypeCreditNote is "credit_note": a credit note.
	ResourceTypeCreditNote ResourceType = "credit_note"
	// ResourceTypeSubscription is "subscription": a subscription.
	ResourceTypeSubscription ResourceType = "subscription"
)

// ResourceTypeValues returns every ResourceType, in declaration order.
func ResourceTypeValues() []ResourceType {
	return []ResourceType{ResourceTypeInvoice, ResourceTypePayment, ResourceTypeCreditNote, ResourceTypeSubscription}
}

// Valid reports whether v is one of the declared ResourceType values.
func (v ResourceType) Valid() bool {
	switch v {
	case ResourceTypeInvoice, ResourceTypePayment, ResourceTypeCreditNote, ResourceTypeSubscription:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v ResourceType) String() string { return string(v) }

// ParseResourceType parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseResourceType(s string) (ResourceType, error) {
	if v := ResourceType(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("integration.ResourceType", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v ResourceType) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *ResourceType) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseResourceType(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}
