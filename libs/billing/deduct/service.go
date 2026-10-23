package deduct

import (
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
)

// Service is the deduct service.
type Service struct {
	d *kernel.Deps
}

// New returns the service over the checked dependencies d.
func New(d *kernel.Deps) *Service { return &Service{d: d} }

// Options tune an application of the deductions.
type Options struct {
	// Draft marks a draft invoice: only progressive billing credits apply;
	// coupons, credit notes and prepaid credits wait for finalization, so a
	// draft never consumes balances (domain §22.8).
	Draft bool
	// Preview computes the deductions on the invoice and its fees in memory
	// without taking locks, writing credits, consuming coupons, credit
	// notes or wallets, or emitting anything (coupon previews, estimates of
	// the wallets' ongoing balances).
	Preview bool
	// Plans are the plans of subscriptions that are not stored, for
	// previews of new subscriptions; stored subscriptions are looked up.
	Plans map[core.SubscriptionID]*catalog.Plan
}

// FeeAmount is an amount on one fee, in minor units.
type FeeAmount struct {
	// FeeID is the fee.
	FeeID core.FeeID
	// Amount is the amount, in minor units.
	Amount int64
}

// ProgressiveCreditNote is what a progressive billing invoice billed in
// the period beyond what the invoice could deduct: the caller credits it
// back with a credit note on that invoice (lago
// CreditNotes::CreateFromProgressiveBillingInvoice).
type ProgressiveCreditNote struct {
	// InvoiceID is the progressive billing invoice.
	InvoiceID core.InvoiceID
	// Amount is Σ Items.
	Amount int64
	// Items are the amounts to credit per fee of the progressive billing
	// invoice.
	Items []FeeAmount
}

// BeforeTaxes is the outcome of [Service.ApplyBeforeTaxes].
type BeforeTaxes struct {
	// ProgressiveBillingCredit is the step 1 deduction.
	ProgressiveBillingCredit int64
	// Coupons is the step 2 deduction.
	Coupons int64
	// Credits are the credits created (computed only, in a preview).
	Credits []*kernel.Credit
	// ProgressiveCreditNotes are the credit notes to create on progressive
	// billing invoices.
	ProgressiveCreditNotes []ProgressiveCreditNote
}

// AfterTaxes is the outcome of [Service.ApplyAfterTaxes].
type AfterTaxes struct {
	// CreditNotes is the step 4 deduction.
	CreditNotes int64
	// PrepaidCredit is the step 5 deduction.
	PrepaidCredit int64
	// PrepaidGrantedCredit is the part of PrepaidCredit consumed from
	// granted credits; set only when every wallet of the customer is
	// traceable.
	PrepaidGrantedCredit int64
	// PrepaidPurchasedCredit is the part of PrepaidCredit consumed from
	// purchased credits; set only when every wallet of the customer is
	// traceable.
	PrepaidPurchasedCredit int64
	// Credits are the credit-note credits created (computed only, in a
	// preview).
	Credits []*kernel.Credit
	// WalletTransactions are the outbound transactions created (computed
	// only, in a preview), one per wallet used.
	WalletTransactions []*wallet.Transaction
}

// Deductions returns the deductions of b and a for [invoice.ComputeTotals];
// Taxes is left to the caller.
func Deductions(b BeforeTaxes, a AfterTaxes) invoice.Deductions {
	return invoice.Deductions{
		ProgressiveBillingCredit: b.ProgressiveBillingCredit,
		Coupons:                  b.Coupons,
		CreditNotes:              a.CreditNotes,
		PrepaidCredit:            a.PrepaidCredit,
	}
}

// receivesCredits reports whether an invoice of type t receives coupons,
// credit notes and prepaid credits: one-off and credit invoices do not
// (domain §11.6, §22.7).
func receivesCredits(t invoice.Type) bool {
	return t != invoice.TypeOneOff && t != invoice.TypeCredit
}
