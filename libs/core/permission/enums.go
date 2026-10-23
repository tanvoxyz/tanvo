package permission

import (
	"github.com/tanvoxyz/tanvo/libs/core"
)

// BuiltInRole is the code of a predefined, immutable role (domain §5.3).
type BuiltInRole string

// BuiltInRole values.
const (
	// BuiltInRoleAdmin is "admin": every permission.
	BuiltInRoleAdmin BuiltInRole = "admin"
	// BuiltInRoleFinance is "finance": finance operations: invoices, credit notes, payments, analytics.
	BuiltInRoleFinance BuiltInRole = "finance"
	// BuiltInRoleManager is "manager": customer management: customers, subscriptions, coupons, wallets.
	BuiltInRoleManager BuiltInRole = "manager"
)

// BuiltInRoleValues returns every BuiltInRole, in declaration order.
func BuiltInRoleValues() []BuiltInRole {
	return []BuiltInRole{BuiltInRoleAdmin, BuiltInRoleFinance, BuiltInRoleManager}
}

// Valid reports whether v is one of the declared BuiltInRole values.
func (v BuiltInRole) Valid() bool {
	switch v {
	case BuiltInRoleAdmin, BuiltInRoleFinance, BuiltInRoleManager:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v BuiltInRole) String() string { return string(v) }

// ParseBuiltInRole parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseBuiltInRole(s string) (BuiltInRole, error) {
	if v := BuiltInRole(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("permission.BuiltInRole", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v BuiltInRole) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *BuiltInRole) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseBuiltInRole(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// Level is the access level an API key has on a resource (domain §5.5); write includes read.
type Level string

// Level values.
const (
	// LevelRead is "read": read-only requests.
	LevelRead Level = "read"
	// LevelWrite is "write": read and mutating requests.
	LevelWrite Level = "write"
)

// LevelValues returns every Level, in declaration order.
func LevelValues() []Level {
	return []Level{LevelRead, LevelWrite}
}

// Valid reports whether v is one of the declared Level values.
func (v Level) Valid() bool {
	switch v {
	case LevelRead, LevelWrite:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v Level) String() string { return string(v) }

// ParseLevel parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseLevel(s string) (Level, error) {
	if v := Level(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("permission.Level", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v Level) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *Level) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseLevel(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// APIResource is a resource an API key permission is granted on (domain §5.5).
type APIResource string

// APIResource values.
const (
	// APIResourceActivityLog is the APIResource "activity_log".
	APIResourceActivityLog APIResource = "activity_log"
	// APIResourceAddOn is the APIResource "add_on".
	APIResourceAddOn APIResource = "add_on"
	// APIResourceAlert is the APIResource "alert".
	APIResourceAlert APIResource = "alert"
	// APIResourceAnalytic is the APIResource "analytic".
	APIResourceAnalytic APIResource = "analytic"
	// APIResourceAPILog is the APIResource "api_log".
	APIResourceAPILog APIResource = "api_log"
	// APIResourceAppliedCoupon is the APIResource "applied_coupon".
	APIResourceAppliedCoupon APIResource = "applied_coupon"
	// APIResourceAttributedUsage is the APIResource "attributed_usage".
	APIResourceAttributedUsage APIResource = "attributed_usage"
	// APIResourceBillableMetric is the APIResource "billable_metric".
	APIResourceBillableMetric APIResource = "billable_metric"
	// APIResourceBillingEntity is the APIResource "billing_entity".
	APIResourceBillingEntity APIResource = "billing_entity"
	// APIResourceContract is the APIResource "contract".
	APIResourceContract APIResource = "contract"
	// APIResourceContractRateCard is the APIResource "contract_rate_card".
	APIResourceContractRateCard APIResource = "contract_rate_card"
	// APIResourceCoupon is the APIResource "coupon".
	APIResourceCoupon APIResource = "coupon"
	// APIResourceCreditNote is the APIResource "credit_note".
	APIResourceCreditNote APIResource = "credit_note"
	// APIResourceCustomer is the APIResource "customer".
	APIResourceCustomer APIResource = "customer"
	// APIResourceCustomerUsage is the APIResource "customer_usage".
	APIResourceCustomerUsage APIResource = "customer_usage"
	// APIResourceEvent is the APIResource "event".
	APIResourceEvent APIResource = "event"
	// APIResourceFeature is the APIResource "feature".
	APIResourceFeature APIResource = "feature"
	// APIResourceFee is the APIResource "fee".
	APIResourceFee APIResource = "fee"
	// APIResourceInvoice is the APIResource "invoice".
	APIResourceInvoice APIResource = "invoice"
	// APIResourceInvoiceCustomSection is the APIResource "invoice_custom_section".
	APIResourceInvoiceCustomSection APIResource = "invoice_custom_section"
	// APIResourceLifetimeUsage is the APIResource "lifetime_usage".
	APIResourceLifetimeUsage APIResource = "lifetime_usage"
	// APIResourceOrder is the APIResource "order".
	APIResourceOrder APIResource = "order"
	// APIResourceOrderForm is the APIResource "order_form".
	APIResourceOrderForm APIResource = "order_form"
	// APIResourceOrganization is the APIResource "organization".
	APIResourceOrganization APIResource = "organization"
	// APIResourcePayment is the APIResource "payment".
	APIResourcePayment APIResource = "payment"
	// APIResourcePaymentMethod is the APIResource "payment_method".
	APIResourcePaymentMethod APIResource = "payment_method"
	// APIResourcePaymentReceipt is the APIResource "payment_receipt".
	APIResourcePaymentReceipt APIResource = "payment_receipt"
	// APIResourcePaymentRequest is the APIResource "payment_request".
	APIResourcePaymentRequest APIResource = "payment_request"
	// APIResourcePlan is the APIResource "plan".
	APIResourcePlan APIResource = "plan"
	// APIResourcePlanRateCard is the APIResource "plan_rate_card".
	APIResourcePlanRateCard APIResource = "plan_rate_card"
	// APIResourceProduct is the APIResource "product".
	APIResourceProduct APIResource = "product"
	// APIResourceProductCategory is the APIResource "product_category".
	APIResourceProductCategory APIResource = "product_category"
	// APIResourceQuote is the APIResource "quote".
	APIResourceQuote APIResource = "quote"
	// APIResourceRateCard is the APIResource "rate_card".
	APIResourceRateCard APIResource = "rate_card"
	// APIResourceSecurityLog is the APIResource "security_log".
	APIResourceSecurityLog APIResource = "security_log"
	// APIResourceSubscription is the APIResource "subscription".
	APIResourceSubscription APIResource = "subscription"
	// APIResourceTax is the APIResource "tax".
	APIResourceTax APIResource = "tax"
	// APIResourceUsageAttributionType is the APIResource "usage_attribution_type".
	APIResourceUsageAttributionType APIResource = "usage_attribution_type"
	// APIResourceWallet is the APIResource "wallet".
	APIResourceWallet APIResource = "wallet"
	// APIResourceWalletTransaction is the APIResource "wallet_transaction".
	APIResourceWalletTransaction APIResource = "wallet_transaction"
	// APIResourceWebhookEndpoint is the APIResource "webhook_endpoint".
	APIResourceWebhookEndpoint APIResource = "webhook_endpoint"
	// APIResourceX402 is the APIResource "x402".
	APIResourceX402 APIResource = "x402"
	// APIResourceX402Connection is the APIResource "x402_connection".
	APIResourceX402Connection APIResource = "x402_connection"
)

// APIResourceValues returns every APIResource, in declaration order.
func APIResourceValues() []APIResource {
	return []APIResource{APIResourceActivityLog, APIResourceAddOn, APIResourceAlert, APIResourceAnalytic, APIResourceAPILog, APIResourceAppliedCoupon, APIResourceAttributedUsage, APIResourceBillableMetric, APIResourceBillingEntity, APIResourceContract, APIResourceContractRateCard, APIResourceCoupon, APIResourceCreditNote, APIResourceCustomer, APIResourceCustomerUsage, APIResourceEvent, APIResourceFeature, APIResourceFee, APIResourceInvoice, APIResourceInvoiceCustomSection, APIResourceLifetimeUsage, APIResourceOrder, APIResourceOrderForm, APIResourceOrganization, APIResourcePayment, APIResourcePaymentMethod, APIResourcePaymentReceipt, APIResourcePaymentRequest, APIResourcePlan, APIResourcePlanRateCard, APIResourceProduct, APIResourceProductCategory, APIResourceQuote, APIResourceRateCard, APIResourceSecurityLog, APIResourceSubscription, APIResourceTax, APIResourceUsageAttributionType, APIResourceWallet, APIResourceWalletTransaction, APIResourceWebhookEndpoint, APIResourceX402, APIResourceX402Connection}
}

// Valid reports whether v is one of the declared APIResource values.
func (v APIResource) Valid() bool {
	switch v {
	case APIResourceActivityLog, APIResourceAddOn, APIResourceAlert, APIResourceAnalytic, APIResourceAPILog, APIResourceAppliedCoupon, APIResourceAttributedUsage, APIResourceBillableMetric, APIResourceBillingEntity, APIResourceContract, APIResourceContractRateCard, APIResourceCoupon, APIResourceCreditNote, APIResourceCustomer, APIResourceCustomerUsage, APIResourceEvent, APIResourceFeature, APIResourceFee, APIResourceInvoice, APIResourceInvoiceCustomSection, APIResourceLifetimeUsage, APIResourceOrder, APIResourceOrderForm, APIResourceOrganization, APIResourcePayment, APIResourcePaymentMethod, APIResourcePaymentReceipt, APIResourcePaymentRequest, APIResourcePlan, APIResourcePlanRateCard, APIResourceProduct, APIResourceProductCategory, APIResourceQuote, APIResourceRateCard, APIResourceSecurityLog, APIResourceSubscription, APIResourceTax, APIResourceUsageAttributionType, APIResourceWallet, APIResourceWalletTransaction, APIResourceWebhookEndpoint, APIResourceX402, APIResourceX402Connection:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v APIResource) String() string { return string(v) }

// ParseAPIResource parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseAPIResource(s string) (APIResource, error) {
	if v := APIResource(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("permission.APIResource", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v APIResource) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *APIResource) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseAPIResource(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}
