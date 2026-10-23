package wallets

import (
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
)

// Validation codes the service reports besides the core ones.
const (
	// CodeCustomerNotFound rejects a wallet of an unknown customer.
	CodeCustomerNotFound = "customer_not_found"
	// CodeInvalidPaidCredits rejects negative or oversized paid credits.
	CodeInvalidPaidCredits = "invalid_paid_credits"
	// CodeInvalidGrantedCredits rejects negative or oversized granted
	// credits.
	CodeInvalidGrantedCredits = "invalid_granted_credits"
	// CodeInvalidVoidedCredits rejects negative or oversized voided credits.
	CodeInvalidVoidedCredits = "invalid_voided_credits"
	// CodeInsufficientCredits rejects voiding more than the balance.
	CodeInsufficientCredits = "insufficient_credits"
	// CodeInvalidNumberOfRecurringRules rejects more than one recurring rule.
	CodeInvalidNumberOfRecurringRules = "invalid_number_of_recurring_rules"
	// CodeInvalidRecurringRule rejects an invalid recurring rule.
	CodeInvalidRecurringRule = "invalid_recurring_rule"
	// CodeInvalidLimitations rejects invalid fee type or metric limitations.
	CodeInvalidLimitations = "invalid_limitations"
	// CodeInvalidFeeTypes rejects unknown allowed fee types.
	CodeInvalidFeeTypes = "invalid_fee_types"
	// CodeInvalidIdentifier rejects unknown billable metrics.
	CodeInvalidIdentifier = "invalid_identifier"
	// CodeInvalidPaymentMethod rejects an inconsistent payment method.
	CodeInvalidPaymentMethod = "invalid_payment_method"
	// CodeWalletNotFound rejects a transaction on an unknown wallet.
	CodeWalletNotFound = "wallet_not_found"
	// CodeWalletIsTerminated rejects changes to a terminated wallet.
	CodeWalletIsTerminated = "wallet_is_terminated"
	// CodeWalletNotTraceable rejects voiding a given transaction of a wallet
	// that does not trace its consumption.
	CodeWalletNotTraceable = "wallet_not_traceable"
	// CodeWalletTransactionNotFound rejects voiding an unknown inbound
	// transaction.
	CodeWalletTransactionNotFound = "wallet_transaction_not_found"
	// CodeNoRemainingAmount rejects voiding an inbound transaction already
	// consumed.
	CodeNoRemainingAmount = "no_remaining_amount"
	// CodeWalletNotActive rejects recrediting a terminated wallet.
	CodeWalletNotActive = "wallet_not_active"
)

// Service is the wallets service.
type Service struct {
	d     *kernel.Deps
	usage UsageSource
}

// New returns the service over the checked dependencies d. Ongoing
// balances count no usage until a [UsageSource] is set with
// [Service.WithUsage].
func New(d *kernel.Deps) *Service { return &Service{d: d} }

// WithUsage returns a copy of the service that reads the customers' current
// usage from u when refreshing ongoing balances.
func (s *Service) WithUsage(u UsageSource) *Service {
	c := *s
	c.usage = u
	return &c
}

// PaymentMethod selects how paid top-ups are collected.
type PaymentMethod struct {
	// Type is the payment method type; empty keeps the default.
	Type core.PaymentMethodType
	// ID is the customer's payment method; zero uses the default one.
	ID core.PaymentMethodID
}

// Change is an optional update of one attribute: Set reports whether the
// attribute is sent, Value is its new value.
type Change[T any] struct {
	// Set reports whether the attribute changes.
	Set bool
	// Value is the new value.
	Value T
}

// To returns a change to v.
func To[T any](v T) Change[T] { return Change[T]{Set: true, Value: v} }

func fieldError(field, code string) error {
	var v core.Validator
	v.Add(field, code)
	return v.Err()
}
