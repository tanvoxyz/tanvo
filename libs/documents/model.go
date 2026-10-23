package documents

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
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// Image is an image printed on a document, such as the billing entity's
// logo.
type Image struct {
	// ContentType is "image/png", "image/jpeg", "image/gif" or
	// "image/svg+xml".
	ContentType string
	// Data is the file content.
	Data []byte
}

// Party is the issuer or the recipient of a document: the billing entity
// and the customer of every document.
type Party struct {
	// BillingEntity issues invoices, credit notes and receipts.
	BillingEntity organization.BillingEntity
	// Customer receives them.
	Customer customer.Customer
	// Logo is the billing entity's logo, nil when it has none.
	Logo *Image
}

// PaymentTermType is the kind of a structured payment term (domain §24,
// item 12).
type PaymentTermType string

// Payment term types.
const (
	// PaymentTermDueOnReceipt is due on the issuing date.
	PaymentTermDueOnReceipt PaymentTermType = "due_on_receipt"
	// PaymentTermNet is due Days after the issuing date.
	PaymentTermNet PaymentTermType = "net"
	// PaymentTermEndOfMonth is due at the end of the issuing month.
	PaymentTermEndOfMonth PaymentTermType = "end_of_month"
	// PaymentTermNetEndOfMonth is due Days after the end of the issuing
	// month.
	PaymentTermNetEndOfMonth PaymentTermType = "net_end_of_month"
	// PaymentTermDaysEndOfMonth is due at the end of the month containing
	// the issuing date plus Days.
	PaymentTermDaysEndOfMonth PaymentTermType = "days_end_of_month"
	// PaymentTermDayOfMonth is due on DayOfMonth, MonthOffset months after
	// the issuing month.
	PaymentTermDayOfMonth PaymentTermType = "day_of_month"
)

// PaymentTerm is the payment term printed on an invoice.
type PaymentTerm struct {
	// Type is the kind of term.
	Type PaymentTermType
	// Days is the number of days of net, net_end_of_month and
	// days_end_of_month terms.
	Days int
	// DayOfMonth is the due day (1–31) of day_of_month terms.
	DayOfMonth int
	// MonthOffset is the number of months after the issuing month of
	// day_of_month terms.
	MonthOffset int
}

// Invoice is the data of an invoice PDF: the invoice with every object its
// template prints, resolved by the caller (see [FromCore]).
type Invoice struct {
	Party
	// Invoice is the invoice.
	Invoice invoice.Invoice
	// PurchaseOrderNumber is the invoice's purchase order number, if any.
	PurchaseOrderNumber string
	// PaymentTerm is the invoice's payment term.
	PaymentTerm PaymentTerm
	// Subscriptions are the invoice's subscriptions, in invoice order.
	Subscriptions []Subscription
	// Fees are the invoice's fees with their catalog objects.
	Fees []Fee
	// AppliedTaxes are the invoice's tax lines.
	AppliedTaxes []invoice.AppliedTax
	// Coupons are the coupon credits applied to the invoice, in application
	// order.
	Coupons []CouponCredit
	// ProgressiveBillingCredits are, by subscription, the credits of
	// progressive billing invoices deducted from this invoice.
	ProgressiveBillingCredits map[core.SubscriptionID]int64
	// UsageThreshold is the last usage threshold a progressive billing
	// invoice passed, nil otherwise.
	UsageThreshold *AppliedUsageThreshold
	// CustomSections are the invoice's custom sections.
	CustomSections []invoice.CustomSection
	// HasWalletTransactions reports whether wallet transactions paid part
	// of the invoice (it then lists its prepaid credits).
	HasWalletTransactions bool
}

// Subscription is a subscription billed on an invoice.
type Subscription struct {
	// InvoiceSubscription holds the billed periods.
	InvoiceSubscription invoice.Subscription
	// Subscription is the subscription.
	Subscription subscription.Subscription
	// Plan is the subscription's plan.
	Plan catalog.Plan
}

// InvoiceName returns the name of the subscription on invoices.
func (s *Subscription) InvoiceName() string {
	return s.Subscription.InvoiceName(s.Plan.Name, s.Plan.InvoiceDisplayName)
}

// Fee is a fee with the catalog objects its line prints.
type Fee struct {
	// Fee is the fee.
	Fee invoice.Fee
	// Charge is the fee's charge (charge fees).
	Charge *catalog.Charge
	// Metric is the charge's billable metric (charge fees).
	Metric *catalog.BillableMetric
	// ChargeFilter is the fee's charge filter, if any.
	ChargeFilter *catalog.ChargeFilter
	// FixedCharge is the fee's fixed charge (fixed charge fees).
	FixedCharge *catalog.FixedCharge
	// AddOn is the fee's add-on (add-on fees) or the fixed charge's add-on
	// (fixed charge fees).
	AddOn *catalog.AddOn
	// PricingUnit is the charge's pricing unit, if any.
	PricingUnit *catalog.PricingUnit
	// WalletTransaction is the transaction of a credit fee.
	WalletTransaction *wallet.Transaction
	// Wallet is the wallet of a credit fee's transaction.
	Wallet *wallet.Wallet
	// TrueUpFee is the true-up fee attached to this fee, if any.
	TrueUpFee *invoice.Fee
	// RecurringBreakdown is the daily breakdown of a recurring metric's
	// units (sum and unique count metrics charged in arrears).
	RecurringBreakdown []BreakdownItem
	// ProrationDays is the proration of a prorated pay-in-advance charge:
	// the days billed and the days of the period.
	ProrationDays *DayRange
	// PayInAdvanceInterval is the charges period holding the billing
	// instant of a charge fee of a pay-in-advance charge billed in arrears
	// or not invoiceable, shown instead of the fee's period. The caller
	// resolves it when it renders the document, from the subscription as
	// it is then (lago Fee#date_boundaries, app/models/fee.rb:342-365;
	// billing's kernel.PayInAdvanceInterval); nil shows the fee's period.
	PayInAdvanceInterval *period.Period
}

// BreakdownAction is the change of a recurring metric's units on a day.
type BreakdownAction string

// Breakdown actions.
const (
	// BreakdownAdd adds units.
	BreakdownAdd BreakdownAction = "add"
	// BreakdownRemove removes units.
	BreakdownRemove BreakdownAction = "remove"
	// BreakdownAddAndRemove adds and removes units on the same day.
	BreakdownAddAndRemove BreakdownAction = "add_and_removed"
)

// BreakdownItem is one day of a recurring metric's breakdown.
type BreakdownItem struct {
	// Date is the day of the change.
	Date core.Date
	// Action is the change.
	Action BreakdownAction
	// Amount is the number of units changed.
	Amount money.Decimal
	// Duration is the number of days billed for the change.
	Duration int
	// TotalDuration is the number of days of the period.
	TotalDuration int
}

// DayRange is a number of days out of a period's days.
type DayRange struct {
	// Days is the number of days.
	Days int
	// Total is the number of days of the period.
	Total int
}

// CouponCredit is a coupon deducted from an invoice.
type CouponCredit struct {
	// CouponName is the coupon's name.
	CouponName string
	// PercentageRate is the applied coupon's rate for percentage coupons.
	PercentageRate *money.Decimal
	// FixedAmount is the applied coupon's amount for fixed amount coupons,
	// in minor units of Currency.
	FixedAmount int64
	// Currency is the applied coupon's currency.
	Currency money.Currency
	// Amount is the amount deducted, in minor units of the invoice's
	// currency.
	Amount int64
}

// AppliedUsageThreshold is a usage threshold passed by a progressive billing
// invoice.
type AppliedUsageThreshold struct {
	// LifetimeUsageAmount is the customer's lifetime usage when it passed.
	LifetimeUsageAmount int64
	// PassedThresholdAmount is the threshold's amount.
	PassedThresholdAmount int64
}

// CreditNote is the data of a credit note PDF.
type CreditNote struct {
	Party
	// CreditNote is the credit note.
	CreditNote creditnote.CreditNote
	// Invoice is the credited invoice.
	Invoice invoice.Invoice
	// InvoiceAppliedTaxes are the credited invoice's tax lines (they tell
	// whether a tax provider computed its taxes).
	InvoiceAppliedTaxes []invoice.AppliedTax
	// Fees are the credited invoice's fees referenced by the items (with
	// true-up fees).
	Fees []Fee
	// Subscriptions are the credited invoice's subscriptions.
	Subscriptions []Subscription
	// WalletName is the name of the wallet a credit invoice topped up.
	WalletName string
}

// PaymentReceipt is the data of a payment receipt PDF.
type PaymentReceipt struct {
	Party
	// Receipt is the receipt.
	Receipt payment.Receipt
	// Payment is the receipted payment.
	Payment payment.Payment
	// Provider is the payment's provider connection (Payment.IntegrationID),
	// nil for manual payments or when the connection is gone; its provider is
	// the payment's provider type (payment.rb:86-88 payment_provider_type).
	Provider *integration.Integration
	// Invoice is the paid invoice, nil when the payment pays a payment
	// request.
	Invoice *Invoice
	// Request is the paid payment request, nil when the payment pays an
	// invoice.
	Request *PaymentRequest
}

// providerType returns the payment provider of the receipted payment, empty
// for manual payments, as lago's payment_provider&.payment_type
// (app/models/payment.rb:86-88).
func (d *PaymentReceipt) providerType() string {
	if d.Provider == nil {
		return ""
	}
	return string(d.Provider.Provider)
}

// PaymentRequest is the data of a payment request PDF, and the payable of a
// receipt for a payment request.
type PaymentRequest struct {
	Party
	// Request is the payment request.
	Request payment.Request
	// Invoices are the requested invoices.
	Invoices []invoice.Invoice
}
