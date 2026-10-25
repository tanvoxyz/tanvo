package docgen

import (
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// Image is an image printed on a document.
type Image struct {
	// ContentType is the media type.
	ContentType string
	// Data is the file content.
	Data []byte
}

// Party is the issuer and the recipient of a document.
type Party struct {
	// BillingEntity issues the document.
	BillingEntity organization.BillingEntity
	// Customer receives it.
	Customer customer.Customer
	// Logo is the billing entity's logo, nil when it has none or no blob
	// store holds it.
	Logo *Image
	// Locale is the customer's applicable document locale.
	Locale string
}

// Catalog holds, by ID, the objects the fees and subscriptions of a
// document refer to, kept or deleted.
type Catalog struct {
	// Plans by ID.
	Plans map[core.PlanID]catalog.Plan
	// Subscriptions by ID.
	Subscriptions map[core.SubscriptionID]subscription.Subscription
	// Charges by ID.
	Charges map[core.ChargeID]catalog.Charge
	// Metrics are the billable metrics by ID.
	Metrics map[core.BillableMetricID]catalog.BillableMetric
	// ChargeFilters by ID.
	ChargeFilters map[core.ChargeFilterID]catalog.ChargeFilter
	// FixedCharges by ID.
	FixedCharges map[core.FixedChargeID]catalog.FixedCharge
	// AddOns by ID.
	AddOns map[core.AddOnID]catalog.AddOn
	// PricingUnits by ID.
	PricingUnits map[core.PricingUnitID]catalog.PricingUnit
	// WalletTransactions by ID.
	WalletTransactions map[core.WalletTransactionID]wallet.Transaction
	// Wallets by ID.
	Wallets map[core.WalletID]wallet.Wallet
}

// FeeExtra is what a document prints for a fee beyond the fee and its
// catalog objects, resolved from the subscription as it is when the
// document is rendered.
type FeeExtra struct {
	// RecurringBreakdown is the daily breakdown of a recurring metric's
	// units.
	RecurringBreakdown []metering.BreakdownItem
	// ProrationDays and ProrationTotal are the days billed and the days
	// of the period of a prorated pay-in-advance charge, when
	// HasProration.
	ProrationDays, ProrationTotal int
	// HasProration reports whether ProrationDays is set.
	HasProration bool
	// PayInAdvanceInterval is the charges period shown instead of the
	// fee's period, nil for none.
	PayInAdvanceInterval *period.Period
}

// CouponCredit is a coupon deducted from an invoice.
type CouponCredit struct {
	// CouponName is the coupon's name.
	CouponName string
	// PercentageRate is the applied coupon's rate for percentage coupons.
	PercentageRate *money.Decimal
	// FixedAmount is the applied coupon's amount for fixed amount coupons.
	FixedAmount int64
	// Currency is the applied coupon's currency.
	Currency money.Currency
	// Amount is the amount deducted.
	Amount int64
}

// UsageThreshold is the last usage threshold a progressive billing invoice
// passed.
type UsageThreshold struct {
	// LifetimeUsageAmount is the lifetime usage when it passed.
	LifetimeUsageAmount int64
	// PassedThresholdAmount is the threshold's amount.
	PassedThresholdAmount int64
}

// Invoice is everything an invoice document prints.
type Invoice struct {
	Party
	// Invoice is the invoice.
	Invoice invoice.Invoice
	// Links are the invoice's subscription links, in invoice order.
	Links []invoice.Subscription
	// Fees are the invoice's fees.
	Fees []invoice.Fee
	// Catalog holds the objects the fees and links refer to.
	Catalog Catalog
	// Extras are the per-fee additions, by fee.
	Extras map[core.FeeID]FeeExtra
	// AppliedTaxes are the invoice's tax lines.
	AppliedTaxes []invoice.AppliedTax
	// Coupons are the coupon credits, in application order.
	Coupons []CouponCredit
	// ProgressiveBillingCredits are, by subscription, the progressive
	// billing credits deducted from the invoice.
	ProgressiveBillingCredits map[core.SubscriptionID]int64
	// UsageThreshold is the last threshold a progressive billing invoice
	// passed, nil otherwise.
	UsageThreshold *UsageThreshold
	// CustomSections are the sections printed on the invoice.
	CustomSections []invoice.CustomSection
	// HasWalletTransactions reports whether wallet transactions paid part
	// of the invoice.
	HasWalletTransactions bool
}

// CreditNote is everything a credit note document prints.
type CreditNote struct {
	Party
	// CreditNote is the credit note.
	CreditNote creditnote.CreditNote
	// Invoice is the credited invoice.
	Invoice invoice.Invoice
	// InvoiceAppliedTaxes are the credited invoice's tax lines.
	InvoiceAppliedTaxes []invoice.AppliedTax
	// Fees are the credited invoice's fees.
	Fees []invoice.Fee
	// Links are the credited invoice's subscription links.
	Links []invoice.Subscription
	// Catalog holds the objects the fees and links refer to.
	Catalog Catalog
	// WalletName is the name of the active wallet a credit invoice topped
	// up.
	WalletName string
}

// PaymentRequest is a payment request with its invoices.
type PaymentRequest struct {
	Party
	// Request is the payment request.
	Request payment.Request
	// Invoices are the requested invoices.
	Invoices []invoice.Invoice
}

// PaymentReceipt is everything a payment receipt document prints.
type PaymentReceipt struct {
	Party
	// Receipt is the receipt.
	Receipt payment.Receipt
	// Payment is the receipted payment.
	Payment payment.Payment
	// Provider is the payment's provider connection, nil for manual
	// payments or when it is gone.
	Provider *integration.Integration
	// Invoice is the paid invoice, nil for a payment request.
	Invoice *Invoice
	// Request is the paid payment request, nil for an invoice.
	Request *PaymentRequest
}

// Invoices returns the invoices the receipted payment settles.
func (d *PaymentReceipt) Invoices() []*invoice.Invoice {
	switch {
	case d.Invoice != nil:
		return []*invoice.Invoice{&d.Invoice.Invoice}
	case d.Request != nil:
		out := make([]*invoice.Invoice, 0, len(d.Request.Invoices))
		for i := range d.Request.Invoices {
			out = append(out, &d.Request.Invoices[i])
		}
		return out
	}
	return nil
}
