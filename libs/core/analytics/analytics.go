package analytics

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Scope narrows a report.
type Scope struct {
	// Now is the current instant; the report ends with its month (UTC).
	Now time.Time
	// Months keeps the last Months months up to Now's; 0 keeps every
	// month since the organization's creation.
	Months int
	// Currency keeps one currency; the zero currency keeps all.
	Currency money.Currency
	// BillingEntityID keeps the invoices of one billing entity; zero keeps
	// all.
	BillingEntityID core.BillingEntityID
	// CustomerID keeps the invoices of one customer; zero keeps all. Only
	// the gross revenue, the invoice collection and the overdue balance
	// take it.
	CustomerID core.CustomerID
	// CustomerTINEmpty keeps the invoices of customers without (true) or
	// with (false) a tax identification number; nil keeps all. Only the
	// invoice collection and the overdue balance take it.
	CustomerTINEmpty *bool
}

// Revenue is the gross revenue or MRR of a month in a currency (and, for
// the gross revenue, a billing entity).
type Revenue struct {
	// Month is the first day of the month, UTC.
	Month time.Time
	// Currency is the currency; zero on a month without revenue (MRR).
	Currency money.Currency
	// Amount is the amount in minor units; nil on a month without
	// revenue (MRR).
	Amount *int64
	// InvoicesCount is the number of invoices (gross revenue only).
	InvoicesCount int64
	// BillingEntityID is the billing entity of the invoices and fees
	// (gross revenue only).
	BillingEntityID core.BillingEntityID
}

// Collection is the finalized invoices of a month in a currency and
// payment status.
type Collection struct {
	// Month is the first day of the month, UTC.
	Month time.Time
	// PaymentStatus is the status; "" on a month without invoices.
	PaymentStatus core.PaymentStatus
	// Currency is the currency; zero on a month without invoices.
	Currency money.Currency
	// InvoicesCount is the number of invoices.
	InvoicesCount int64
	// Amount is their total in minor units.
	Amount int64
}

// Usage is the usage invoiced in a month for a billable metric.
type Usage struct {
	// Month is the first day of the month, UTC.
	Month time.Time
	// MetricCode is the billable metric's code.
	MetricCode string
	// Currency is the currency.
	Currency money.Currency
	// Amount is the amount in minor units.
	Amount int64
}

// Overdue is the unpaid balance of the overdue invoices due in a month,
// by currency and billing entity.
type Overdue struct {
	// Month is the first day of the payment due month, UTC.
	Month time.Time
	// Currency is the currency.
	Currency money.Currency
	// Amount is the balance in minor units.
	Amount int64
	// InvoiceIDs are the overdue invoices.
	InvoiceIDs []core.InvoiceID
	// BillingEntityID is the billing entity of the overdue invoices.
	BillingEntityID core.BillingEntityID
}

// Repository computes the reports of an organization, in month order
// (invoiced usage: the latest month first, the largest amount first).
type Repository interface {
	// GrossRevenue returns the revenue of the finalized invoices, less
	// their refunds, and of the fees paid in advance outside invoices.
	GrossRevenue(ctx context.Context, org core.OrganizationID, s Scope) ([]Revenue, error)
	// MRR returns the monthly recurring revenue of the subscription fees,
	// spread over the months they bill.
	MRR(ctx context.Context, org core.OrganizationID, s Scope) ([]Revenue, error)
	// InvoiceCollection returns the finalized invoices by payment status.
	InvoiceCollection(ctx context.Context, org core.OrganizationID, s Scope) ([]Collection, error)
	// InvoicedUsage returns the usage fees by billable metric.
	InvoicedUsage(ctx context.Context, org core.OrganizationID, s Scope) ([]Usage, error)
	// OverdueBalance returns the balance of the overdue invoices by due
	// month.
	OverdueBalance(ctx context.Context, org core.OrganizationID, s Scope) ([]Overdue, error)
}
