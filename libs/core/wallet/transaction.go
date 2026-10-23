package wallet

import (
	"cmp"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Transaction moves credits in or out of a wallet (domain §14.2).
type Transaction struct {
	// ID identifies the transaction.
	ID core.WalletTransactionID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// WalletID is the wallet the credits move in or out of.
	WalletID core.WalletID
	// TransactionType is the direction of the movement.
	TransactionType TransactionType
	// TransactionStatus is the kind of movement; it must agree with
	// TransactionType (see [ValidTransactionStatus]).
	TransactionStatus TransactionStatus
	// Status is the settlement status.
	Status SettlementStatus
	// Source tells what created the transaction.
	Source TransactionSource
	// Currency is the wallet's currency.
	Currency money.Currency
	// Amount is the value of CreditAmount, in minor units of Currency:
	// round(credit_amount × rate_amount).
	Amount int64
	// CreditAmount is the number of credits moved.
	CreditAmount money.Decimal
	// RemainingAmount is, on inbound transactions of traceable wallets, the
	// value not consumed yet, in minor units of Currency; nil otherwise.
	RemainingAmount *int64
	// Priority orders consumption of inbound transactions, 1–50; inherited
	// from the wallet.
	Priority int
	// Name is the display name, 1–255 characters.
	Name *string
	// InvoiceID is the credit invoice (purchased) or the consuming invoice
	// (invoiced).
	InvoiceID core.InvoiceID
	// CreditNoteID is the credit note whose refund voided the credits.
	CreditNoteID core.CreditNoteID
	// RecurringTransactionRuleID is the rule that created the transaction,
	// for interval and threshold sources.
	RecurringTransactionRuleID core.RecurringTransactionRuleID
	// Metadata holds at most 15 items.
	Metadata core.Metadata
	// PaymentMethodType is how the credit invoice is collected; "" inherits
	// the wallet's.
	PaymentMethodType core.PaymentMethodType
	// PaymentMethodID is the payment method of the credit invoice.
	PaymentMethodID core.PaymentMethodID
	// InvoiceRequiresSuccessfulPayment settles purchased credits only once
	// the credit invoice is paid.
	InvoiceRequiresSuccessfulPayment bool
	// PurchaseOrderNumber (≤ 255 characters) is the request's on a manual
	// transaction, the rule's resolved one on a rule top-up, and the
	// transaction's own on a recredit; the credit invoice takes it, else the
	// wallet's.
	PurchaseOrderNumber *string
	// SettledAt is set once settled.
	SettledAt *time.Time
	// FailedAt is set once failed.
	FailedAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// ValidTransactionStatus reports whether a kind of movement belongs to a
// direction: inbound transactions are purchased or granted, outbound ones
// invoiced or voided.
func ValidTransactionStatus(t TransactionType, s TransactionStatus) bool {
	switch t {
	case TransactionTypeInbound:
		return s == TransactionStatusPurchased || s == TransactionStatusGranted
	case TransactionTypeOutbound:
		return s == TransactionStatusInvoiced || s == TransactionStatusVoided
	default:
		return false
	}
}

// InitialStatus returns the settlement status a transaction is born with:
// purchased credits wait for their invoice, everything else settles at once.
func InitialStatus(s TransactionStatus) SettlementStatus {
	if s == TransactionStatusPurchased {
		return SettlementStatusPending
	}
	return SettlementStatusSettled
}

// Validate checks the invariants of domain §14.2.
func (t *Transaction) Validate() error {
	var v core.Validator
	v.Enum("transaction_type", t.TransactionType)
	if v.Enum("transaction_status", t.TransactionStatus) && t.TransactionType.Valid() {
		v.Check(ValidTransactionStatus(t.TransactionType, t.TransactionStatus), "transaction_status", core.CodeInvalid)
	}
	v.Enum("status", t.Status)
	v.Enum("source", t.Source)
	v.Currency("currency", t.Currency)
	v.NonNegative("amount", t.Amount)
	v.DecimalNonNegative("credit_amount", t.CreditAmount)
	if t.RemainingAmount != nil {
		if t.TransactionType == TransactionTypeOutbound {
			v.Add("remaining_amount", core.CodeForbidden)
		} else {
			v.NonNegative("remaining_amount", *t.RemainingAmount)
		}
	}
	v.IntRange("priority", t.Priority, MinPriority, MaxPriority)
	if t.Name != nil && v.Required("name", *t.Name) {
		v.MaxLen("name", *t.Name, 255)
	}
	v.Merge("metadata", t.Metadata.Validate(core.WalletTransactionMetadataLimits()))
	v.OptionalEnum("payment_method_type", t.PaymentMethodType)
	v.OptionalString("purchase_order_number", t.PurchaseOrderNumber, core.MaxPurchaseOrderNumberLength)
	if t.Status == SettlementStatusPending {
		v.Check(t.TransactionStatus == TransactionStatusPurchased, "status", core.CodeInvalid)
	}
	return v.Err()
}

// Inbound reports whether the transaction adds credits.
func (t *Transaction) Inbound() bool { return t.TransactionType == TransactionTypeInbound }

// Outbound reports whether the transaction removes credits.
func (t *Transaction) Outbound() bool { return t.TransactionType == TransactionTypeOutbound }

// Available reports whether a settled inbound transaction still has value
// left to consume (traceable wallets).
func (t *Transaction) Available() bool {
	return t.Inbound() && t.Status == SettlementStatusSettled && t.RemainingAmount != nil && *t.RemainingAmount > 0
}

// RemainingCredits converts RemainingAmount to credits at rate, or returns
// nil when it is unset.
func (t *Transaction) RemainingCredits(rate money.Decimal) *money.Decimal {
	if t.RemainingAmount == nil {
		return nil
	}
	c := CreditsFromMinor(*t.RemainingAmount, t.Currency, rate)
	return &c
}

// CanSettlementTransition reports whether a transaction may go from one
// settlement status to another: pending → settled or failed.
func CanSettlementTransition(from, to SettlementStatus) bool {
	return from == SettlementStatusPending && (to == SettlementStatusSettled || to == SettlementStatusFailed)
}

// Settle settles a pending transaction at now (its credit invoice was
// finalized or paid).
func (t *Transaction) Settle(now time.Time) error {
	if err := t.transition(SettlementStatusSettled); err != nil {
		return err
	}
	t.SettledAt = &now
	t.UpdatedAt = now
	return nil
}

// Fail marks a pending transaction failed at now (its payment failed or its
// invoice was voided).
func (t *Transaction) Fail(now time.Time) error {
	if err := t.transition(SettlementStatusFailed); err != nil {
		return err
	}
	t.FailedAt = &now
	t.UpdatedAt = now
	return nil
}

func (t *Transaction) transition(to SettlementStatus) error {
	if !CanSettlementTransition(t.Status, to) {
		return &core.TransitionError{Entity: "wallet_transaction", From: string(t.Status), To: string(to)}
	}
	t.Status = to
	return nil
}

// statusRank orders kinds of movement within a priority: granted credits
// are consumed before purchased ones.
func statusRank(s TransactionStatus) int {
	switch s {
	case TransactionStatusGranted:
		return 0
	case TransactionStatusPurchased:
		return 1
	case TransactionStatusVoided:
		return 2
	default:
		return 3
	}
}

// CompareForConsumption orders transactions as traceable wallets consume
// their inbound transactions (domain §14.2): priority ascending, then
// granted before purchased (then voided, then invoiced), then oldest first,
// then ID.
func CompareForConsumption(a, b *Transaction) int {
	if c := cmp.Compare(a.Priority, b.Priority); c != 0 {
		return c
	}
	if c := cmp.Compare(statusRank(a.TransactionStatus), statusRank(b.TransactionStatus)); c != 0 {
		return c
	}
	if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
		return c
	}
	return a.ID.Compare(b.ID)
}

// Consumption links an outbound transaction of a traceable wallet to the
// inbound transaction it consumed (domain §14.2).
type Consumption struct {
	// ID identifies the consumption.
	ID core.WalletTransactionConsumptionID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// InboundTransactionID is the consumed inbound transaction.
	InboundTransactionID core.WalletTransactionID
	// OutboundTransactionID is the consuming outbound transaction.
	OutboundTransactionID core.WalletTransactionID
	// ConsumedAmount is the value consumed, in minor units of the wallet's
	// currency; > 0.
	ConsumedAmount int64
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Validate checks the consumption against the two transactions it links:
// the consumed amount is positive, inbound is inbound and outbound is
// outbound.
func (c *Consumption) Validate(inbound, outbound *Transaction) error {
	var v core.Validator
	v.Positive("consumed_amount", c.ConsumedAmount)
	if inbound != nil {
		v.Check(inbound.Inbound(), "inbound_transaction_id", core.CodeInvalid)
	}
	if outbound != nil {
		v.Check(outbound.Outbound(), "outbound_transaction_id", core.CodeInvalid)
	}
	return v.Err()
}

// CreditAmount converts the consumed amount to credits at rate.
func (c *Consumption) CreditAmount(currency money.Currency, rate money.Decimal) money.Decimal {
	return CreditsFromMinor(c.ConsumedAmount, currency, rate)
}
