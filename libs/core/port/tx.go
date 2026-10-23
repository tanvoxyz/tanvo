package port

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
)

// Transactor is the transaction boundary (architecture §5.3).
type Transactor interface {
	// InTx runs fn in one transaction with repositories bound to it. The
	// transaction commits when fn returns nil and rolls back otherwise
	// (including on panic, which is re-raised); InTx returns fn's error. A
	// nested InTx (ctx already carries a transaction) joins the outer
	// transaction.
	InTx(ctx context.Context, fn func(ctx context.Context, r Repos) error) error
}

// LockKey names one advisory lock of an organization.
type LockKey struct {
	// Scope is the kind of lock ("customer:prepaid_credits", "subscription",
	// "sequence:invoice", …).
	Scope string
	// ID is the locked object.
	ID uuid.UUID
}

// String returns "scope/id".
func (k LockKey) String() string { return k.Scope + "/" + k.ID.String() }

// CustomerLock is the lock of scope s on customer c (domain §6): coupon,
// credit note, wallet and billing operations on one customer serialize on
// it.
func CustomerLock(s customer.LockScope, c core.CustomerID) LockKey {
	return LockKey{Scope: "customer:" + s.String(), ID: c.UUID()}
}

// SubscriptionLock is the lock of subscription s; billing runs and
// lifecycle changes of a subscription serialize on it.
func SubscriptionLock(s core.SubscriptionID) LockKey {
	return LockKey{Scope: "subscription", ID: s.UUID()}
}

// SequenceLock is the lock of sequence k; it serializes number assignment
// when a store hands out numbers by reading the last one.
func SequenceLock(k SequenceKey) LockKey {
	return LockKey{Scope: "sequence:" + k.Name.String(), ID: k.Scope}
}

// Locker takes advisory locks held until the transaction ends.
type Locker interface {
	// Lock blocks until the lock k of org is held by the current
	// transaction; it is released at commit or rollback. Taking a lock the
	// transaction already holds is a no-op.
	Lock(ctx context.Context, org core.OrganizationID, k LockKey) error
}

// SequenceName names a gapless counter.
type SequenceName string

// Sequence names (domain §22.11, §6, §15.2, §21).
const (
	// SequenceCustomer numbers an organization's customers (scope: nil).
	SequenceCustomer SequenceName = "customer"
	// SequenceInvoiceOrganization numbers an organization's invoices
	// (scope: nil).
	SequenceInvoiceOrganization SequenceName = "invoice_organization"
	// SequenceInvoiceCustomer numbers a customer's invoices (scope: the
	// customer).
	SequenceInvoiceCustomer SequenceName = "invoice_customer"
	// SequenceInvoiceBillingEntity numbers a billing entity's invoices
	// per month (scope: the billing entity; the month is part of the
	// number, the counter is not reset).
	SequenceInvoiceBillingEntity SequenceName = "invoice_billing_entity"
	// SequenceCreditNote numbers an invoice's credit notes (scope: the
	// invoice).
	SequenceCreditNote SequenceName = "credit_note"
	// SequencePaymentReceipt numbers a customer's payment receipts (scope:
	// the customer).
	SequencePaymentReceipt SequenceName = "payment_receipt"
	// SequenceQuote numbers an organization's quotes (scope: nil).
	SequenceQuote SequenceName = "quote"
	// SequenceOrderForm numbers an organization's order forms (scope: nil).
	SequenceOrderForm SequenceName = "order_form"
	// SequenceOrder numbers an organization's orders (scope: nil).
	SequenceOrder SequenceName = "order"
)

// String returns the name.
func (n SequenceName) String() string { return string(n) }

// SequenceKey identifies one counter: a name and the object it counts for
// (uuid.Nil for organization-wide counters).
type SequenceKey struct {
	// Name is the counter's name.
	Name SequenceName
	// Scope is the counted-for object, or uuid.Nil.
	Scope uuid.UUID
}

// String returns "name/scope".
func (k SequenceKey) String() string { return fmt.Sprintf("%s/%s", k.Name, k.Scope) }

// Sequences hands out gapless numbers inside the transaction: a number
// taken by a rolled-back transaction is handed out again.
type Sequences interface {
	// Next returns the next value (1, 2, …) of counter k of org.
	Next(ctx context.Context, org core.OrganizationID, k SequenceKey) (int64, error)
}
