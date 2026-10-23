package payments

import (
	"context"
	"errors"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// RequestInput creates a payment request.
type RequestInput struct {
	// OrganizationID is the customer's organization.
	OrganizationID core.OrganizationID
	// Customer is the asked customer.
	Customer CustomerRef
	// InvoiceIDs are the requested invoices.
	InvoiceIDs []core.InvoiceID
	// Email is where the request is sent; nil means the customer's email.
	Email *string
	// PaymentMethodType is the payment method kind; "" means the
	// customer's.
	PaymentMethodType core.PaymentMethodType
	// PaymentMethodID is the provider payment method.
	PaymentMethodID core.PaymentMethodID
	// DunningCampaignID is the campaign creating the request.
	DunningCampaignID core.DunningCampaignID
}

// CreateRequest asks a customer to pay overdue invoices at once (lago
// PaymentRequests::CreateService). The customer must exist and the
// invoices, among the customer's, must be found, overdue, of one currency
// and one billing entity, and ready for payment processing (each a
// [core.RuleError] in this order); a payment method must be the
// customer's ([payment.ValidateMethodChoice]). The amount is what is left
// to pay on the invoices net of their finalized credit note offsets. The
// request is announced by the payment_request.created webhook.
func (s *Service) CreateRequest(ctx context.Context, in RequestInput) (*payment.Request, error) {
	return inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*payment.Request, error) {
		org := in.OrganizationID
		c, err := in.Customer.find(ctx, r, org)
		if err != nil {
			return nil, err
		}
		invs, err := r.Invoices.ListByIDs(ctx, org, in.InvoiceIDs)
		if err != nil {
			return nil, wrap(err, "load invoices")
		}
		invs = slices.DeleteFunc(invs, func(inv *invoice.Invoice) bool {
			return inv.CustomerID != c.ID || !inv.Visible()
		})
		if len(invs) == 0 {
			return nil, notFound("invoice")
		}
		if err := checkRequestInvoices(invs); err != nil {
			return nil, err
		}
		found := false
		if !in.PaymentMethodID.IsZero() {
			m, err := r.PaymentMethods.Get(ctx, org, in.PaymentMethodID)
			switch {
			case err == nil:
				found = m.CustomerID == c.ID && m.DeletedAt == nil
			case !errors.Is(err, core.ErrNotFound):
				return nil, wrap(err, "load payment method %s", in.PaymentMethodID)
			}
		}
		if err := payment.ValidateMethodChoice(in.PaymentMethodType, in.PaymentMethodID, found); err != nil {
			return nil, err
		}
		amount, err := requestedAmount(ctx, r, invs)
		if err != nil {
			return nil, err
		}
		email := in.Email
		if email == nil {
			email = c.Email
		}
		now := s.d.Now()
		q := &payment.Request{
			ID:                        core.NewID[core.PaymentRequestID](s.d.IDs),
			OrganizationID:            org,
			CustomerID:                c.ID,
			Amount:                    amount,
			Currency:                  invs[0].Currency,
			Email:                     email,
			PaymentStatus:             core.PaymentStatusPending,
			DunningCampaignID:         in.DunningCampaignID,
			PaymentMethodType:         in.PaymentMethodType,
			PaymentMethodID:           in.PaymentMethodID,
			ReadyForPaymentProcessing: true,
			CreatedAt:                 now,
			UpdatedAt:                 now,
		}
		for _, inv := range invs {
			q.InvoiceIDs = append(q.InvoiceIDs, inv.ID)
		}
		if err := q.Validate(); err != nil {
			return nil, err
		}
		if err := r.PaymentRequests.Create(ctx, q); err != nil {
			return nil, wrap(err, "create payment request")
		}
		if err := kernel.Emit(ctx, s.d, org, webhook.EventTypePaymentRequestCreated, "payment_request", q.ID.UUID(), q); err != nil {
			return nil, err
		}
		return q, nil
	})
}

// checkRequestInvoices applies the invoice checks of a payment request in
// lago's order (app/services/payment_requests/create_service.rb:76-90).
func checkRequestInvoices(invs []*invoice.Invoice) error {
	first := invs[0]
	codes := []struct {
		fails func(*invoice.Invoice) bool
		code  string
	}{
		{func(inv *invoice.Invoice) bool { return !inv.PaymentOverdue }, payment.CodeInvoicesNotOverdue},
		{func(inv *invoice.Invoice) bool { return inv.Currency.Code() != first.Currency.Code() }, payment.CodeInvoicesDifferentCurrencies},
		{func(inv *invoice.Invoice) bool { return inv.BillingEntityID != first.BillingEntityID }, payment.CodeInvoicesDifferentBillingEntities},
		{func(inv *invoice.Invoice) bool { return !inv.ReadyForPaymentProcessing }, payment.CodeInvoicesNotReady},
	}
	for _, c := range codes {
		if slices.ContainsFunc(invs, c.fails) {
			return core.NewRuleError(c.code, "the invoices cannot be requested together")
		}
	}
	return nil
}

// requestedAmount returns Σ (total − paid) of invs less the offsets of
// their finalized credit notes.
func requestedAmount(ctx context.Context, r port.Repos, invs []*invoice.Invoice) (int64, error) {
	var amount int64
	for _, inv := range invs {
		amount += inv.TotalAmount - inv.TotalPaidAmount
		cns, err := r.CreditNotes.ListByInvoice(ctx, inv.OrganizationID, inv.ID)
		if err != nil {
			return 0, wrap(err, "list credit notes of invoice %s", inv.ID)
		}
		for _, cn := range cns {
			if cn.Status == creditnote.StatusFinalized {
				amount -= cn.OffsetAmount
			}
		}
	}
	return amount, nil
}

// GetRequest returns a payment request.
func (s *Service) GetRequest(ctx context.Context, org core.OrganizationID, id core.PaymentRequestID) (*payment.Request, error) {
	return inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*payment.Request, error) {
		q, err := r.PaymentRequests.Get(ctx, org, id)
		if errors.Is(err, core.ErrNotFound) {
			return nil, notFound("payment_request")
		}
		if err != nil {
			return nil, wrap(err, "load payment request %s", id)
		}
		return q, nil
	})
}

// RequestListInput selects payment requests.
type RequestListInput struct {
	// Customer makes the list the customer's own: a customer it does not
	// name is not found.
	Customer CustomerRef
	// FilterCustomer keeps the requests of one customer: one it does not
	// name lists nothing.
	FilterCustomer CustomerRef
	// BillingEntities keeps the requests including an invoice of one of
	// the billing entities (IDs or codes); one that names no billing
	// entity is not found (lago PaymentRequestIndex billing_entity_codes).
	BillingEntities []refs.Ref
	// Filter narrows the list further; its customer, billing entities and
	// order are set by ListRequests.
	Filter payment.RequestFilter
}

// ListRequests returns a page of the payment requests of org, newest
// first then by ID (lago PaymentRequestsQuery).
func (s *Service) ListRequests(ctx context.Context, org core.OrganizationID, in RequestListInput, page core.PageRequest) (core.Page[*payment.Request], error) {
	return inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (core.Page[*payment.Request], error) {
		f := in.Filter
		// The customer route finds its customer first, then the billing
		// entities are checked (lago Customers::BaseController,
		// PaymentRequestIndex).
		id, ok, err := listCustomer(ctx, r, org, in.Customer, in.FilterCustomer)
		if err != nil {
			return core.Page[*payment.Request]{}, err
		}
		if len(in.BillingEntities) > 0 {
			bes := refs.Finder[core.BillingEntityID, *organization.BillingEntity]{
				Resource: "billing_entity",
				Get: func(ctx context.Context, id core.BillingEntityID) (*organization.BillingEntity, error) {
					return r.BillingEntities.Get(ctx, org, id)
				},
				GetByCode: func(ctx context.Context, code string) (*organization.BillingEntity, error) {
					return r.BillingEntities.GetByCode(ctx, org, code)
				},
				ID: func(b *organization.BillingEntity) core.BillingEntityID { return b.ID },
			}
			if f.BillingEntityIDs, err = bes.IDs(ctx, in.BillingEntities); err != nil {
				return core.Page[*payment.Request]{}, err
			}
		}
		if !ok {
			return core.Page[*payment.Request]{}, nil
		}
		f.CustomerID, f.Order = id, core.OrderRecent
		qs, err := r.PaymentRequests.List(ctx, org, f, page)
		if err != nil {
			return core.Page[*payment.Request]{}, wrap(err, "list payment requests")
		}
		return qs, nil
	})
}
