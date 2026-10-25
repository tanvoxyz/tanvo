package wallet

import (
	"cmp"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Wallet defaults and bounds (domain §14.1).
const (
	// DefaultPriority is the priority of a wallet created without one.
	DefaultPriority = 50
	// MinPriority is the highest priority (consumed first).
	MinPriority = 1
	// MaxPriority is the lowest priority.
	MaxPriority = 50
)

// Field error codes specific to wallets.
const (
	// CodeMaxBelowMin: paid_top_up_max_amount is below the minimum.
	CodeMaxBelowMin = "must_be_greater_than_or_equal_min"
	// CodeTooManyWallets: the customer already holds the maximum number of
	// active wallets.
	CodeTooManyWallets = "wallets_limit_reached"
	// CodeInvalidAmount: a top-up of zero, negative or missing credits.
	CodeInvalidAmount = "invalid_amount"
	// CodeRoundsToZero: a top-up whose amount rounds to 0 minor units.
	CodeRoundsToZero = "amount_rounds_to_zero"
	// CodeBelowMinimum: a paid top-up below the wallet's minimum.
	CodeBelowMinimum = "amount_below_minimum"
	// CodeAboveMaximum: a paid top-up above the wallet's maximum.
	CodeAboveMaximum = "amount_above_maximum"
)

// Wallet holds prepaid credits of a customer (domain §14.1). Balances
// change only through transactions, under the customer's prepaid_credits
// lock.
type Wallet struct {
	// ID identifies the wallet.
	ID core.WalletID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// CustomerID is the owning customer.
	CustomerID core.CustomerID
	// Code is unique among the customer's active wallets, ≤ 255 characters.
	Code *string
	// Name is the display name, ≤ 255 characters.
	Name *string
	// Status is the lifecycle status.
	Status Status
	// Currency is the wallet's own currency: the one given at creation,
	// else the customer's default; immutable. The wallet pays only
	// invoices in it (domain §14.1, §22.7).
	Currency money.Currency
	// RateAmount is the value of one credit in Currency; > 0.
	RateAmount money.Decimal
	// Priority orders consumption, 1–50 (lower first); default 50.
	Priority int
	// ExpirationAt is the instant the wallet terminates automatically.
	ExpirationAt *time.Time
	// BalanceAmount is the balance, in minor units of Currency.
	BalanceAmount int64
	// CreditsBalance is the balance in credits.
	CreditsBalance money.Decimal
	// OngoingBalanceAmount is the balance minus current usage not yet
	// invoiced, in minor units of Currency.
	OngoingBalanceAmount int64
	// CreditsOngoingBalance is the ongoing balance in credits.
	CreditsOngoingBalance money.Decimal
	// OngoingUsageBalanceAmount is the current usage not yet invoiced, in
	// minor units of Currency.
	OngoingUsageBalanceAmount int64
	// CreditsOngoingUsageBalance is the ongoing usage in credits.
	CreditsOngoingUsageBalance money.Decimal
	// ConsumedAmount is the lifetime consumption, in minor units of
	// Currency.
	ConsumedAmount int64
	// ConsumedCredits is the lifetime consumption in credits.
	ConsumedCredits money.Decimal
	// PaidTopUpMinAmount is the minimum paid top-up, in minor units.
	PaidTopUpMinAmount *int64
	// PaidTopUpMaxAmount is the maximum paid top-up, in minor units.
	PaidTopUpMaxAmount *int64
	// InvoiceRequiresSuccessfulPayment settles purchased credits only once
	// their credit invoice is paid.
	InvoiceRequiresSuccessfulPayment bool
	// AllowedFeeTypes restricts the fees the wallet pays; empty = all.
	AllowedFeeTypes []core.FeeType
	// BillableMetricIDs restricts the charge fees the wallet pays; empty =
	// all.
	BillableMetricIDs []core.BillableMetricID
	// Traceable records consumption per inbound transaction.
	Traceable bool
	// PurchaseOrderNumber is the default purchase order number of its
	// transactions and recurring rules, ≤ 255 characters.
	PurchaseOrderNumber *string
	// X402Enabled lets an x402 agent top the wallet up.
	X402Enabled bool
	// BillingEntityID overrides the customer's billing entity for credit
	// invoices.
	BillingEntityID core.BillingEntityID
	// PaymentMethodType is how credit invoices are collected.
	PaymentMethodType core.PaymentMethodType
	// PaymentMethodID is the payment method of credit invoices.
	PaymentMethodID core.PaymentMethodID
	// LastBalanceSyncAt is the last ongoing balance refresh.
	LastBalanceSyncAt *time.Time
	// LastConsumedCreditAt is the last consumption.
	LastConsumedCreditAt *time.Time
	// DepletedOngoingBalance is set once the ongoing balance reached 0.
	DepletedOngoingBalance bool
	// Metadata is free key/value data ([core.ItemMetadataLimits]).
	Metadata core.Metadata
	// InvoiceCustomSections are the invoice custom sections of its credit invoices, unless a transaction selects its own.
	InvoiceCustomSections CustomSections
	// TerminatedAt is set once terminated.
	TerminatedAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// New returns an active wallet with the domain defaults (priority 50,
// provider payment method type).
func New(org core.OrganizationID, id core.WalletID, customer core.CustomerID, currency money.Currency, rate money.Decimal, now time.Time) *Wallet {
	return &Wallet{
		ID:                id,
		OrganizationID:    org,
		CustomerID:        customer,
		Status:            StatusActive,
		Currency:          currency,
		RateAmount:        rate,
		Priority:          DefaultPriority,
		PaymentMethodType: core.PaymentMethodTypeProvider,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

// Validate checks the invariants of domain §14.1 that need no other
// aggregate. The expiration being in the future is checked on write with
// [ValidateExpiration].
func (w *Wallet) Validate() error {
	var v core.Validator
	if w.Code != nil {
		v.String("code", *w.Code, 255)
	}
	v.OptionalString("name", w.Name, 255)
	v.Enum("status", w.Status)
	v.Currency("currency", w.Currency)
	v.DecimalPositive("rate_amount", w.RateAmount)
	v.IntRange("priority", w.Priority, MinPriority, MaxPriority)
	if w.PaidTopUpMinAmount != nil {
		v.Positive("paid_top_up_min_amount", *w.PaidTopUpMinAmount)
	}
	if w.PaidTopUpMaxAmount != nil {
		v.Positive("paid_top_up_max_amount", *w.PaidTopUpMaxAmount)
	}
	if w.PaidTopUpMinAmount != nil && w.PaidTopUpMaxAmount != nil && *w.PaidTopUpMaxAmount < *w.PaidTopUpMinAmount {
		v.Add("paid_top_up_max_amount", CodeMaxBelowMin)
	}
	if w.Traceable {
		v.NonNegative("balance_amount", w.BalanceAmount)
	}
	for _, t := range w.AllowedFeeTypes {
		if !v.Check(t.Valid(), "allowed_fee_types", core.CodeInvalid) {
			break
		}
	}
	v.Check(len(w.AllowedFeeTypes) == len(dedup(w.AllowedFeeTypes)), "allowed_fee_types", core.CodeDuplicated)
	v.Check(len(w.BillableMetricIDs) == len(dedup(w.BillableMetricIDs)), "billable_metric_ids", core.CodeDuplicated)
	v.OptionalEnum("payment_method_type", w.PaymentMethodType)
	v.OptionalString("purchase_order_number", w.PurchaseOrderNumber, core.MaxPurchaseOrderNumberLength)
	if len(w.Metadata) > 0 {
		v.Merge("metadata", w.Metadata.Validate(core.ItemMetadataLimits()))
	}
	return v.Err()
}

// ValidateExpiration checks that a new or changed expiration is in the
// future (domain §14.1).
func ValidateExpiration(expirationAt *time.Time, now time.Time) error {
	var v core.Validator
	if expirationAt != nil {
		v.Future("expiration_at", *expirationAt, now)
	}
	return v.Err()
}

// ValidateForCustomer checks the rules that depend on the customer: a new
// active wallet stays within maxActive (the organization's
// max_wallets_per_customer), given the number of active wallets the
// customer already holds besides w. The wallet's currency need not be the
// customer's, which is only a default (domain §6).
func (w *Wallet) ValidateForCustomer(activeWallets, maxActive int) error {
	var v core.Validator
	if w.Status == StatusActive {
		v.Check(activeWallets < maxActive, "customer_id", CodeTooManyWallets)
	}
	return v.Err()
}

// ResolvePurchaseOrderNumber returns the first of pos that is set and not
// blank, normalized by [core.NormalizePurchaseOrderNumber], or nil: a
// transaction's purchase order number is the request's, else the rule's,
// else the wallet's (domain §14.2).
func ResolvePurchaseOrderNumber(pos ...*string) *string {
	for _, p := range pos {
		if n := core.NormalizePurchaseOrderNumber(p); n != nil {
			return n
		}
	}
	return nil
}

// Active reports whether the wallet accepts transactions.
func (w *Wallet) Active() bool { return w.Status == StatusActive }

// Expired reports whether the wallet's expiration has passed at now.
func (w *Wallet) Expired(now time.Time) bool {
	return w.ExpirationAt != nil && !w.ExpirationAt.After(now)
}

// LimitedFeeTypes reports whether the wallet pays only some fee types.
func (w *Wallet) LimitedFeeTypes() bool { return len(w.AllowedFeeTypes) > 0 }

// LimitedToBillableMetrics reports whether the wallet pays only some
// metrics' charge fees.
func (w *Wallet) LimitedToBillableMetrics() bool { return len(w.BillableMetricIDs) > 0 }

// Unrestricted reports whether the wallet may pay every fee.
func (w *Wallet) Unrestricted() bool { return !w.LimitedFeeTypes() && !w.LimitedToBillableMetrics() }

// Pays reports whether the wallet may pay a fee of type t for metric m (m
// is zero for fees without a metric): all when unrestricted, otherwise
// when t is allowed or m is targeted (domain §22.7 step 5).
func (w *Wallet) Pays(t core.FeeType, m core.BillableMetricID) bool {
	if w.Unrestricted() {
		return true
	}
	if slices.Contains(w.AllowedFeeTypes, t) {
		return true
	}
	return !m.IsZero() && slices.Contains(w.BillableMetricIDs, m)
}

// PaidTopUpMinCredits returns the minimum paid top-up in credits, or nil.
func (w *Wallet) PaidTopUpMinCredits() *money.Decimal {
	return w.minorToCredits(w.PaidTopUpMinAmount)
}

// PaidTopUpMaxCredits returns the maximum paid top-up in credits, or nil.
func (w *Wallet) PaidTopUpMaxCredits() *money.Decimal {
	return w.minorToCredits(w.PaidTopUpMaxAmount)
}

func (w *Wallet) minorToCredits(amount *int64) *money.Decimal {
	if amount == nil {
		return nil
	}
	c := CreditsFromMinor(*amount, w.Currency, w.RateAmount)
	return &c
}

// ValidateTopUp checks credits offered on field (paid_credits,
// granted_credits, …) of a top-up: they must be positive and worth at least
// one minor unit (domain §22.9).
func (w *Wallet) ValidateTopUp(field string, credits money.Decimal) error {
	var v core.Validator
	if v.Check(NormalizeCredits(credits).IsPositive(), field, CodeInvalidAmount) {
		v.Check(!RoundsToZero(credits, w.Currency, w.RateAmount), field, CodeRoundsToZero)
	}
	return v.Err()
}

// ValidatePaidTopUp checks paid credits offered on field against the
// wallet's paid top-up limits, unless ignoreLimits is set (domain §14.2).
// The credits must be positive in any case.
func (w *Wallet) ValidatePaidTopUp(field string, credits money.Decimal, ignoreLimits bool) error {
	var v core.Validator
	credits = NormalizeCredits(credits)
	if !v.Check(credits.IsPositive(), field, CodeInvalidAmount) || ignoreLimits {
		return v.Err()
	}
	if w.PaidTopUpMinAmount == nil && w.PaidTopUpMaxAmount == nil {
		return nil
	}
	amount, err := AmountFromCredits(credits, w.Currency, w.RateAmount)
	if err != nil {
		v.Add(field, CodeInvalidAmount)
		return v.Err()
	}
	switch {
	case w.PaidTopUpMinAmount != nil && amount < *w.PaidTopUpMinAmount:
		v.Add(field, CodeBelowMinimum)
	case w.PaidTopUpMaxAmount != nil && amount > *w.PaidTopUpMaxAmount:
		v.Add(field, CodeAboveMaximum)
	}
	return v.Err()
}

// CanTransition reports whether a wallet may go from one status to
// another: only active → terminated.
func CanTransition(from, to Status) bool { return from == StatusActive && to == StatusTerminated }

// Terminate terminates the wallet at now. The caller terminates its
// recurring rules ([RecurringTransactionRule.Terminate]) in the same
// transaction.
func (w *Wallet) Terminate(now time.Time) error {
	if !CanTransition(w.Status, StatusTerminated) {
		return &core.TransitionError{Entity: "wallet", From: string(w.Status), To: string(StatusTerminated)}
	}
	w.Status = StatusTerminated
	w.TerminatedAt = &now
	w.UpdatedAt = now
	return nil
}

// CompareForApplication orders wallets as invoices consume them (domain
// §22.7 step 5): priority ascending, then expiration (wallets without one
// last), then creation, then ID.
func CompareForApplication(a, b *Wallet) int {
	if c := cmp.Compare(a.Priority, b.Priority); c != 0 {
		return c
	}
	switch {
	case a.ExpirationAt != nil && b.ExpirationAt == nil:
		return -1
	case a.ExpirationAt == nil && b.ExpirationAt != nil:
		return 1
	case a.ExpirationAt != nil:
		if c := a.ExpirationAt.Compare(*b.ExpirationAt); c != 0 {
			return c
		}
	}
	if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
		return c
	}
	return a.ID.Compare(b.ID)
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
