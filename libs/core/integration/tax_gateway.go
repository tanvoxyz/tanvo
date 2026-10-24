package integration

import (
	"context"
	"errors"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// ErrCompanyNotFound: no Avalara company has the connection's company
// code (lago company_not_found,
// app/services/integrations/aggregator/taxes/avalara/fetch_company_id_service.rb).
var ErrCompanyNotFound = errors.New("integration provider: company not found")

// TaxGateway is the port of tax providers (Anrok, Avalara). A transaction
// is identified by [TaxTransaction.ID]; quotes record nothing.
type TaxGateway interface {
	Gateway
	// QuoteTaxes computes the taxes of a draft, a preview or current usage
	// without recording a transaction (lago draft_invoices).
	QuoteTaxes(ctx context.Context, conn Connection, t *TaxTransaction) (*TaxResult, error)
	// ReportTaxes records the transaction t, creating it or replacing the
	// one with the same ID, and returns its taxes (lago
	// finalized_invoices and credit notes).
	ReportTaxes(ctx context.Context, conn Connection, t *TaxTransaction) (*TaxResult, error)
	// VoidTaxes voids the recorded transaction id. A transaction already
	// filed or locked is a [ProviderError] of [KindLocked].
	VoidTaxes(ctx context.Context, conn Connection, id string) error
}

// TaxNegator is implemented by tax adapters that negate a filed
// transaction instead of voiding it (Anrok createNegation).
type TaxNegator interface {
	// NegateTaxes records negationID as the negation of the transaction
	// id.
	NegateTaxes(ctx context.Context, conn Connection, id, negationID string) error
}

// TaxCompanyFinder is implemented by tax adapters whose connection names
// a company to resolve once (Avalara).
type TaxCompanyFinder interface {
	// FindCompany returns the provider's ID of the connection's company,
	// or an error wrapping [ErrCompanyNotFound].
	FindCompany(ctx context.Context, conn Connection) (string, error)
}

// TaxTransaction is the neutral document of a tax request: an invoice, a
// set of standalone fees or a credit note, with its lines already
// grouped and mapped by libs/billing.
type TaxTransaction struct {
	// ID identifies the transaction at the provider: the invoice ID,
	// "cn_<credit note ID>", or a random ID for standalone fees; empty
	// for quotes.
	ID string
	// Return marks a refund (a voided invoice or a credit note): its
	// amounts are negative.
	Return bool
	// IssuingDate is the document date.
	IssuingDate core.Date
	// TaxDate is the date taxes are computed at; zero means IssuingDate.
	TaxDate core.Date
	// Currency is the document currency.
	Currency money.Currency
	// Customer is the taxed customer.
	Customer TaxCustomer
	// Origin is the address of the billing entity selling.
	Origin core.Address
	// Lines are the taxed lines.
	Lines []TaxLine
}

// TaxCustomer is the customer of a [TaxTransaction].
type TaxCustomer struct {
	// LinkedID is the customer's ID at the provider, from its integration
	// customer; empty when none is stored yet.
	LinkedID string
	// ExternalID is the customer's external ID in Tanvo.
	ExternalID string
	// Name is the customer's name.
	Name string
	// Address is the effective shipping address.
	Address core.Address
	// TaxNumber is the tax identification number; a customer with one is
	// taxable as a business.
	TaxNumber string
}

// TaxLine is one line of a [TaxTransaction].
type TaxLine struct {
	// ItemKey identifies the line in the request (a fee, or the charge of
	// grouped fees).
	ItemKey string
	// ItemID is the fee ID, or the charge ID of grouped fees.
	ItemID string
	// ItemCode is the provider's product code from the mappings; empty
	// when unmapped.
	ItemCode string
	// AmountCents is the taxable amount in minor units, negative for a
	// return.
	AmountCents int64
	// Units is the quantity.
	Units money.Decimal
}

// TaxResult is a provider's answer to a [TaxTransaction].
type TaxResult struct {
	// TransactionID is the provider's ID of the recorded transaction;
	// empty for quotes.
	TransactionID string
	// Lines are the taxes of each line, matched by ItemKey and ItemID.
	Lines []TaxLineResult
}

// TaxLineResult is the taxes of one [TaxLine].
type TaxLineResult struct {
	// ItemKey and ItemID are the line's.
	ItemKey string
	// ItemID is the line's.
	ItemID string
	// ItemCode is the line's.
	ItemCode string
	// AmountCents is the taxed amount.
	AmountCents money.Decimal
	// TaxAmountCents is the tax the provider booked on the line in minor
	// units, nil when it gave none.
	TaxAmountCents *int64
	// Breakdown are the line's taxes per jurisdiction.
	Breakdown []TaxBreakdown
}

// TaxBreakdown is one jurisdiction's tax of a line, as the provider
// reports it.
type TaxBreakdown struct {
	// Name is the tax name.
	Name string
	// Reason explains a zero-tax ruling ("reverseCharge"), if any.
	Reason string
	// Type is the tax type ("tax", "exempt", "notCollecting"…).
	Type string
	// Rate is the rate as a fraction (0.1 for 10 %), nil when none.
	Rate *money.Decimal
	// TaxAmount is the exact tax in minor units, possibly fractional.
	TaxAmount money.Decimal
}
