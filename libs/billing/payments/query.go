package payments

import (
	"context"
	"errors"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Detail is a payment with what the API shows beside it.
type Detail struct {
	*payment.Payment
	// InvoiceIDs are the invoices paid, directly or through the payment
	// request.
	InvoiceIDs []core.InvoiceID
	// ReceiptID is the payment's receipt; zero without one.
	ReceiptID core.PaymentReceiptID
}

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

// find returns the customer c names, or a *core.NotFoundError.
func (c CustomerRef) find(ctx context.Context, r port.Repos, org core.OrganizationID) (*customer.Customer, error) {
	var (
		cu  *customer.Customer
		err error
	)
	if c.ExternalID != "" {
		cu, err = r.Customers.GetByExternalID(ctx, org, c.ExternalID)
	} else {
		cu, err = r.Customers.Get(ctx, org, c.ID)
	}
	if errors.Is(err, core.ErrNotFound) || err == nil && cu.Deleted() {
		return nil, notFound("customer")
	}
	if err != nil {
		return nil, wrap(err, "load customer")
	}
	return cu, nil
}

// filterID resolves c for a list filter: ok is false when c names no
// existing customer, so that the list is empty (lago filters on the
// external ID and finds nothing).
func (c CustomerRef) filterID(ctx context.Context, r port.Repos, org core.OrganizationID) (id core.CustomerID, ok bool, err error) {
	if c.ExternalID == "" {
		return c.ID, true, nil
	}
	cu, err := r.Customers.GetByExternalID(ctx, org, c.ExternalID)
	if errors.Is(err, core.ErrNotFound) {
		return core.CustomerID{}, false, nil
	}
	if err != nil {
		return core.CustomerID{}, false, wrap(err, "load customer")
	}
	return cu.ID, true, nil
}

// listCustomer resolves the customer of a list: parent, the customer whose
// own list it is, must exist, else a *core.NotFoundError (lago
// Api::V1::Customers::BaseController#find_customer); filter, a customer
// filter, lists nothing when it names no customer, or another customer
// than parent. ok is false when the list is empty; the zero ID is no
// customer restriction.
func listCustomer(ctx context.Context, r port.Repos, org core.OrganizationID, parent, filter CustomerRef) (id core.CustomerID, ok bool, err error) {
	if !parent.IsZero() {
		cu, err := parent.find(ctx, r, org)
		if err != nil {
			return core.CustomerID{}, false, err
		}
		id = cu.ID
	}
	if filter.IsZero() {
		return id, true, nil
	}
	fid, ok, err := filter.filterID(ctx, r, org)
	if err != nil || !ok || !id.IsZero() && fid != id {
		return core.CustomerID{}, false, err
	}
	return fid, true, nil
}

// Get returns a payment, unless it pays an invoice the API does not show
// (lago Payment.for_organization).
func (s *Service) Get(ctx context.Context, org core.OrganizationID, id core.PaymentID) (*Detail, error) {
	return inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*Detail, error) {
		p, err := r.Payments.Get(ctx, org, id)
		if errors.Is(err, core.ErrNotFound) {
			return nil, notFound("payment")
		}
		if err != nil {
			return nil, wrap(err, "load payment %s", id)
		}
		if !p.Payable.InvoiceID.IsZero() {
			inv, err := r.Invoices.Get(ctx, org, p.Payable.InvoiceID)
			if errors.Is(err, core.ErrNotFound) || err == nil && !inv.Visible() {
				return nil, notFound("payment")
			}
			if err != nil {
				return nil, wrap(err, "load invoice %s", p.Payable.InvoiceID)
			}
		}
		return detail(ctx, r, p)
	})
}

// ListInput selects payments.
type ListInput struct {
	// Customer makes the list the customer's own: a customer it does not
	// name is not found.
	Customer CustomerRef
	// FilterCustomer keeps the payments of one customer: one it does not
	// name lists nothing.
	FilterCustomer CustomerRef
	// Filter narrows the list further; its customer, visibility and order
	// are set by List.
	Filter payment.Filter
}

// List returns a page of the payments of org, newest first then by ID,
// without those of invoices the API does not show (lago PaymentsQuery).
func (s *Service) List(ctx context.Context, org core.OrganizationID, in ListInput, page core.PageRequest) (core.Page[*Detail], error) {
	return inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (core.Page[*Detail], error) {
		f := in.Filter
		id, ok, err := listCustomer(ctx, r, org, in.Customer, in.FilterCustomer)
		if err != nil || !ok {
			return core.Page[*Detail]{}, err
		}
		f.CustomerID, f.Visible, f.Order = id, true, core.OrderRecent
		ps, err := r.Payments.List(ctx, org, f, page)
		if err != nil {
			return core.Page[*Detail]{}, wrap(err, "list payments")
		}
		out := core.Page[*Detail]{Items: make([]*Detail, 0, len(ps.Items)), NextToken: ps.NextToken}
		for _, p := range ps.Items {
			d, err := detail(ctx, r, p)
			if err != nil {
				return core.Page[*Detail]{}, err
			}
			out.Items = append(out.Items, d)
		}
		return out, nil
	})
}

// detail loads what the API shows beside p.
func detail(ctx context.Context, r port.Repos, p *payment.Payment) (*Detail, error) {
	d := &Detail{Payment: p}
	if p.Payable.Type == payment.PayableTypePaymentRequest {
		q, err := r.PaymentRequests.Get(ctx, p.OrganizationID, p.Payable.PaymentRequestID)
		if err != nil {
			return nil, wrap(err, "load payment request %s", p.Payable.PaymentRequestID)
		}
		d.InvoiceIDs = q.InvoiceIDs
	} else {
		d.InvoiceIDs = []core.InvoiceID{p.Payable.InvoiceID}
	}
	rc, err := r.PaymentReceipts.GetByPayment(ctx, p.OrganizationID, p.ID)
	switch {
	case err == nil:
		d.ReceiptID = rc.ID
	case !errors.Is(err, core.ErrNotFound):
		return nil, wrap(err, "load receipt of payment %s", p.ID)
	}
	return d, nil
}
