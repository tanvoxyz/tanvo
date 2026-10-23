package analytics

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/analytics"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// ResourceBillingEntity is the resource name of an unknown billing entity.
const ResourceBillingEntity = "billing_entity"

// CodeCustomerNotSupported is the violation of a customer filter on a
// report that takes none.
const CodeCustomerNotSupported = "customer_filter_not_supported"

// CustomerRef names a customer by ID or by external ID; the zero value
// names none.
type CustomerRef struct {
	// ID is the customer's ID.
	ID core.CustomerID
	// ExternalID is the customer's external ID.
	ExternalID string
}

// IsZero reports whether the reference names no customer.
func (c CustomerRef) IsZero() bool { return c.ID.IsZero() && c.ExternalID == "" }

// Filter narrows a report.
type Filter struct {
	// Currency keeps one currency; zero keeps all.
	Currency money.Currency
	// BillingEntity keeps one billing entity (by ID or code); zero keeps
	// all.
	BillingEntity refs.Ref
	// Customer keeps one live customer; only the gross revenue and the
	// overdue balance take it.
	Customer CustomerRef
	// CustomerTINEmpty keeps the invoices of customers without (true) or
	// with (false) a tax identification number; nil keeps all. The
	// invoice collection and the overdue balance read it.
	CustomerTINEmpty *bool
	// Months keeps the last Months months, the current one included; 0
	// keeps every month since the organization's creation.
	Months int
}

// Service computes the analytics reports.
type Service struct{ d *kernel.Deps }

// New returns the service over d.
func New(d *kernel.Deps) *Service { return &Service{d: d} }

// GrossRevenue returns the gross revenue by month and currency.
func (s *Service) GrossRevenue(ctx context.Context, org core.OrganizationID, f Filter) ([]analytics.Revenue, error) {
	return report(ctx, s, org, f, true, analytics.Repository.GrossRevenue)
}

// MRR returns the monthly recurring revenue by month and currency; a month
// without revenue has no currency nor amount.
func (s *Service) MRR(ctx context.Context, org core.OrganizationID, f Filter) ([]analytics.Revenue, error) {
	return report(ctx, s, org, f, false, analytics.Repository.MRR)
}

// InvoiceCollection returns the finalized invoices by month, currency and
// payment status; a month without invoices has no currency nor status.
func (s *Service) InvoiceCollection(ctx context.Context, org core.OrganizationID, f Filter) ([]analytics.Collection, error) {
	return report(ctx, s, org, f, false, analytics.Repository.InvoiceCollection)
}

// InvoicedUsage returns the usage invoiced by month and billable metric.
func (s *Service) InvoicedUsage(ctx context.Context, org core.OrganizationID, f Filter) ([]analytics.Usage, error) {
	return report(ctx, s, org, f, false, analytics.Repository.InvoicedUsage)
}

// OverdueBalance returns the overdue balance by payment due month and
// currency.
func (s *Service) OverdueBalance(ctx context.Context, org core.OrganizationID, f Filter) ([]analytics.Overdue, error) {
	return report(ctx, s, org, f, true, analytics.Repository.OverdueBalance)
}

// report validates f, resolves its references and runs fn. A customer that
// does not exist, or is deleted, gives an empty report, as a filter that
// matches nothing.
func report[T any](
	ctx context.Context, s *Service, org core.OrganizationID, f Filter, byCustomer bool,
	fn func(analytics.Repository, context.Context, core.OrganizationID, analytics.Scope) ([]T, error),
) ([]T, error) {
	var v core.Validator
	v.Check(f.Months >= 0, "months", core.CodeInvalid)
	v.Check(byCustomer || f.Customer.IsZero(), "customer_id", CodeCustomerNotSupported)
	if err := v.Err(); err != nil {
		return nil, err
	}
	out := []T{}
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		scope := analytics.Scope{Now: s.d.Now(), Months: f.Months, Currency: f.Currency, CustomerTINEmpty: f.CustomerTINEmpty}
		if !f.BillingEntity.IsZero() {
			b, err := billingEntities(r, org).One(ctx, f.BillingEntity)
			if err != nil {
				return err
			}
			scope.BillingEntityID = b.ID
		}
		if !f.Customer.IsZero() {
			id, ok, err := customerID(ctx, r, org, f.Customer)
			if err != nil || !ok {
				return err
			}
			scope.CustomerID = id
		}
		rows, err := fn(r.Analytics, ctx, org, scope)
		if err != nil {
			return fmt.Errorf("analytics: %w", err)
		}
		out = rows
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// billingEntities finds billing entities by ID or code.
func billingEntities(r port.Repos, org core.OrganizationID) refs.Finder[core.BillingEntityID, *organization.BillingEntity] {
	return refs.Finder[core.BillingEntityID, *organization.BillingEntity]{
		Resource: ResourceBillingEntity,
		Get: func(ctx context.Context, id core.BillingEntityID) (*organization.BillingEntity, error) {
			return r.BillingEntities.Get(ctx, org, id)
		},
		GetByCode: func(ctx context.Context, code string) (*organization.BillingEntity, error) {
			return r.BillingEntities.GetByCode(ctx, org, code)
		},
		ID: func(b *organization.BillingEntity) core.BillingEntityID { return b.ID },
	}
}

// customerID resolves c to a live customer; ok is false when there is
// none.
func customerID(ctx context.Context, r port.Repos, org core.OrganizationID, c CustomerRef) (core.CustomerID, bool, error) {
	var (
		cu  *customer.Customer
		err error
	)
	if c.ExternalID != "" {
		cu, err = r.Customers.GetByExternalID(ctx, org, c.ExternalID)
	} else {
		cu, err = r.Customers.Get(ctx, org, c.ID)
	}
	switch {
	case errors.Is(err, core.ErrNotFound):
		return core.CustomerID{}, false, nil
	case err != nil:
		return core.CustomerID{}, false, fmt.Errorf("analytics: load customer: %w", err)
	case cu.Deleted():
		return core.CustomerID{}, false, nil
	}
	return cu.ID, true, nil
}
