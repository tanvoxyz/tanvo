package payments

import (
	"context"
	"errors"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Codes of the payment URL failures (lago
// Invoices::Payments::GeneratePaymentUrlService and
// PaymentRequests::Payments::GeneratePaymentUrlService), each a
// [core.ValidationError] on "base".
const (
	// CodeNoLinkedPaymentProvider: the customer has no payment connection.
	CodeNoLinkedPaymentProvider = "no_linked_payment_provider"
	// CodeInvalidPaymentProvider: the connection's provider takes no
	// payment links (GoCardless).
	CodeInvalidPaymentProvider = "invalid_payment_provider"
	// CodeInvalidInvoiceStatusOrPaymentStatus: the invoice is a draft,
	// voided or already paid.
	CodeInvalidInvoiceStatusOrPaymentStatus = "invalid_invoice_status_or_payment_status"
	// CodeInvalidPaymentStatus: the payment request is already paid.
	CodeInvalidPaymentStatus = "invalid_payment_status"
	// CodeMissingPaymentProvider: the connection was removed.
	CodeMissingPaymentProvider = "missing_payment_provider"
	// CodeMissingPaymentProviderCustomer: the customer is not known at a
	// provider that needs a provider customer.
	CodeMissingPaymentProviderCustomer = "missing_payment_provider_customer"
	// CodePaymentProviderError: the provider returned no URL.
	CodePaymentProviderError = "payment_provider_error"
)

// baseError is the validation failure code on "base".
func baseError(code string) error {
	return &core.ValidationError{Errors: []core.FieldError{{Field: "base", Code: code}}}
}

// requiresProviderCustomer reports whether a payment link of the vendor p
// needs the customer known at the provider (lago
// PaymentProviderCustomers::BaseCustomer#require_provider_payment_id?,
// false for Cashfree and Flutterwave).
func requiresProviderCustomer(p integration.Provider) bool {
	return p != integration.ProviderCashfree && p != integration.ProviderFlutterwave
}

// linkConnection is the connection a payment link is made at.
type linkConnection struct {
	pcust *payment.ProviderCustomer
	integ *integration.Integration
}

// paymentLinkConnection returns the customer's connection a payment link
// is made at, or the validation failure of a customer that cannot get
// one; payable checks the payable between the vendor check and the
// connection checks.
func paymentLinkConnection(ctx context.Context, r port.Repos, org core.OrganizationID, id core.CustomerID, payable func() error) (linkConnection, error) {
	pcs, err := r.PaymentProviderCustomers.ListByCustomer(ctx, org, id)
	if err != nil {
		return linkConnection{}, wrap(err, "list payment connections of customer %s", id)
	}
	pcust := payment.DefaultProviderCustomer(pcs)
	if pcust == nil {
		return linkConnection{}, baseError(CodeNoLinkedPaymentProvider)
	}
	stored, err := r.Integrations.Get(ctx, org, pcust.IntegrationID)
	if err != nil && !isNotFound(err) {
		return linkConnection{}, wrap(err, "load integration %s", pcust.IntegrationID)
	}
	if err == nil && stored.Provider == integration.ProviderGocardless {
		return linkConnection{}, baseError(CodeInvalidPaymentProvider)
	}
	if err := payable(); err != nil {
		return linkConnection{}, err
	}
	integ, err := liveIntegration(ctx, r, org, pcust.IntegrationID)
	if err != nil {
		return linkConnection{}, err
	}
	if integ == nil {
		return linkConnection{}, baseError(CodeMissingPaymentProvider)
	}
	if providerCustomerID(pcust) == "" && requiresProviderCustomer(integ.Provider) {
		return linkConnection{}, baseError(CodeMissingPaymentProviderCustomer)
	}
	return linkConnection{pcust: pcust, integ: integ}, nil
}

// InvoicePaymentURL returns the hosted checkout URL paying the invoice id
// (lago Invoices::Payments::GeneratePaymentUrlService and
// PaymentIntents::FetchService, app/services/payment_intents/fetch_service.rb):
//
//   - the customer must have a payment connection
//     (no_linked_payment_provider), not GoCardless
//     (invalid_payment_provider); the invoice must be neither a draft, nor
//     voided, nor paid (invalid_invoice_status_or_payment_status); the
//     connection must be live (missing_payment_provider) and the customer
//     known at a provider that needs it
//     (missing_payment_provider_customer);
//   - the invoice's active link past its validity is expired, and the
//     active link reused, or a new one created valid
//     [payment.IntentValidity];
//   - a link without a URL gets one from the provider, keyed by the link,
//     stored with its checkout session; a provider returning none fails
//     with payment_provider_error, and a provider error is announced by
//     the invoice's payment failure webhook and returned.
func (s *Service) InvoicePaymentURL(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (string, error) {
	if s.providers == nil {
		return "", ErrNoProviders
	}
	pr, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*invoiceLink, error) {
		if err := kernel.LockInvoice(ctx, r, org, id); err != nil {
			return nil, err
		}
		inv, err := r.Invoices.Get(ctx, org, id)
		if isNotFound(err) {
			return nil, notFound("invoice")
		}
		if err != nil {
			return nil, wrap(err, "load invoice %s", id)
		}
		lc, err := paymentLinkConnection(ctx, r, org, inv.CustomerID, func() error {
			if inv.PaymentStatus == core.PaymentStatusSucceeded || inv.Status == invoice.StatusVoided || inv.Status == invoice.StatusDraft {
				return baseError(CodeInvalidInvoiceStatusOrPaymentStatus)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		it, err := s.fetchIntent(ctx, r, inv)
		if err != nil {
			return nil, err
		}
		if it.PaymentURL != nil && *it.PaymentURL != "" {
			return &invoiceLink{url: *it.PaymentURL}, nil
		}
		pc, err := s.open(ctx, lc.integ)
		if err != nil {
			return nil, err
		}
		c, err := r.Customers.Get(ctx, org, inv.CustomerID)
		if err != nil {
			return nil, wrap(err, "load customer %s", inv.CustomerID)
		}
		o, err := r.Organizations.Get(ctx, org)
		if err != nil {
			return nil, wrap(err, "load organization %s", org)
		}
		params := payment.PaymentURLParams{
			IntentID:           it.ID,
			Payable:            payment.InvoicePayable(inv.ID),
			InvoiceType:        string(inv.Type),
			IssuingDate:        inv.IssuingDate,
			CustomerID:         c.ID,
			ProviderCustomerID: providerCustomerID(lc.pcust),
			MethodTypes:        lc.pcust.ProviderPaymentMethods,
			CustomerCountry:    c.BillingAddress.Country,
			// invoices/payments/stripe_service.rb:194-196.
			Description: o.Name + " - Invoice " + inv.Number,
			Lines:       []payment.PaymentURLLine{{InvoiceID: inv.ID, Name: inv.Number, Amount: dueAmount(inv)}},
			Currency:    inv.Currency,
			ExpiresAt:   it.ExpiresAt,
			Customer:    payment.CustomerParamsOf(c),
			// flutterwave_service.rb customizations; the logo URL needs
			// the document blob store, not wired here.
			OrganizationName: o.Name,
		}
		return &invoiceLink{pc: pc, params: params, intent: it.ID, inv: inv, lc: lc}, nil
	})
	if err != nil {
		return "", err
	}
	if pr.url != "" {
		return pr.url, nil
	}
	res, cerr := pr.pc.gw.CreatePaymentURL(ctx, pr.pc.conn, pr.params)
	if cerr != nil {
		if _, ok := payment.AsProviderError(cerr); ok {
			if err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
				src, err := failureSourceOf(ctx, r, org, pr.inv.CustomerID, pr.lc.pcust, pr.lc.integ)
				if err != nil {
					return err
				}
				return s.emitInvoiceFailure(ctx, r, pr.inv, src, cerr, nil)
			}); err != nil {
				return "", err
			}
		}
		return "", wrap(cerr, "payment url of invoice %s", id)
	}
	if res.URL == "" {
		return "", baseError(CodePaymentProviderError)
	}
	err = s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		it, err := r.PaymentIntents.GetActiveByInvoice(ctx, org, id)
		if isNotFound(err) {
			return nil
		}
		if err != nil {
			return wrap(err, "load checkout link of invoice %s", id)
		}
		if it.ID != pr.intent {
			return nil
		}
		it.PaymentURL = &res.URL
		if res.SessionID != "" {
			it.ProviderSessionID = &res.SessionID
		}
		it.UpdatedAt = s.d.Now()
		return wrap0(r.PaymentIntents.Update(ctx, it), "store checkout link of invoice %s", id)
	})
	if err != nil {
		return "", err
	}
	return res.URL, nil
}

// invoiceLink is a payment link of an invoice prepared in a transaction:
// its URL when it has one, else what creating it at the provider needs.
type invoiceLink struct {
	url    string
	pc     providerConn
	params payment.PaymentURLParams
	intent payment.IntentID
	inv    *invoice.Invoice
	lc     linkConnection
}

// wrap0 is [wrap] keeping a nil error nil.
func wrap0(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return wrap(err, format, args...)
}

// fetchIntent expires the invoice's active link past its validity and
// returns its live active link, created when it has none
// (fetch_service.rb:16-17).
func (s *Service) fetchIntent(ctx context.Context, r port.Repos, inv *invoice.Invoice) (*payment.Intent, error) {
	org := inv.OrganizationID
	now := s.d.Now()
	it, err := r.PaymentIntents.GetActiveByInvoice(ctx, org, inv.ID)
	switch {
	case isNotFound(err):
		it = nil
	case err != nil:
		return nil, wrap(err, "load checkout link of invoice %s", inv.ID)
	}
	if it != nil && it.AwaitingExpiration(now) {
		if err := it.Expire(now); err != nil {
			return nil, err
		}
		if err := r.PaymentIntents.Update(ctx, it); err != nil {
			return nil, wrap(err, "expire checkout link of invoice %s", inv.ID)
		}
		it = nil
	}
	if it != nil {
		return it, nil
	}
	it = payment.NewIntent(org, core.NewID[payment.IntentID](s.d.IDs), inv.ID, now)
	if err := r.PaymentIntents.Create(ctx, it); err != nil {
		return nil, wrap(err, "create checkout link of invoice %s", inv.ID)
	}
	return it, nil
}

// RequestPaymentURL returns the hosted checkout URL paying the payment
// request id (lago PaymentRequests::Payments::GeneratePaymentUrlService):
// the checks of [Service.InvoicePaymentURL], a paid request failing with
// invalid_payment_status; the page lists each invoice with its amount due
// and is described "<billing entity> - Overdue invoices", followed by the
// number of a single invoice. A provider error is announced by
// payment_request.payment_failure and returned.
func (s *Service) RequestPaymentURL(ctx context.Context, org core.OrganizationID, id core.PaymentRequestID) (string, error) {
	if s.providers == nil {
		return "", ErrNoProviders
	}
	type prepared struct {
		pc     providerConn
		params payment.PaymentURLParams
		q      *payment.Request
		cust   *customer.Customer
		lc     linkConnection
	}
	pr, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*prepared, error) {
		q, err := r.PaymentRequests.Get(ctx, org, id)
		if isNotFound(err) {
			return nil, notFound("payment_request")
		}
		if err != nil {
			return nil, wrap(err, "load payment request %s", id)
		}
		lc, err := paymentLinkConnection(ctx, r, org, q.CustomerID, func() error {
			if q.PaymentStatus == core.PaymentStatusSucceeded {
				return baseError(CodeInvalidPaymentStatus)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		pc, err := s.open(ctx, lc.integ)
		if err != nil {
			return nil, err
		}
		c, err := r.Customers.Get(ctx, org, q.CustomerID)
		if err != nil {
			return nil, wrap(err, "load customer %s", q.CustomerID)
		}
		be, err := r.BillingEntities.Get(ctx, org, c.BillingEntityID)
		if err != nil {
			return nil, wrap(err, "load billing entity %s", c.BillingEntityID)
		}
		o, err := r.Organizations.Get(ctx, org)
		if err != nil {
			return nil, wrap(err, "load organization %s", org)
		}
		invs, err := r.Invoices.ListByIDs(ctx, org, q.InvoiceIDs)
		if err != nil {
			return nil, wrap(err, "load invoices of payment request %s", id)
		}
		// payment_requests/payments/stripe_service.rb:106-114 and 144-155.
		desc := be.Name + " - Overdue invoices"
		if len(invs) == 1 {
			desc += ": " + invs[0].Number
		}
		lines := make([]payment.PaymentURLLine, 0, len(invs))
		for _, inv := range invs {
			lines = append(lines, payment.PaymentURLLine{InvoiceID: inv.ID, Name: inv.Number, Amount: dueAmount(inv)})
		}
		params := payment.PaymentURLParams{
			Payable:            payment.RequestPayable(q.ID),
			CustomerID:         c.ID,
			ProviderCustomerID: providerCustomerID(lc.pcust),
			MethodTypes:        lc.pcust.ProviderPaymentMethods,
			CustomerCountry:    c.BillingAddress.Country,
			Description:        desc,
			Lines:              lines,
			Currency:           q.Currency,
			Customer:           payment.CustomerParamsOf(c),
			OrganizationName:   o.Name,
		}
		return &prepared{pc: pc, params: params, q: q, cust: c, lc: lc}, nil
	})
	if err != nil {
		return "", err
	}
	res, cerr := pr.pc.gw.CreatePaymentURL(ctx, pr.pc.conn, pr.params)
	if cerr != nil {
		if _, ok := payment.AsProviderError(cerr); ok {
			src := failureSource{cust: pr.cust, pc: pr.lc.pcust, integ: pr.lc.integ}
			if err := s.emitRequestFailure(ctx, pr.q, src, cerr); err != nil {
				return "", err
			}
		}
		return "", wrap(cerr, "payment url of payment request %s", id)
	}
	if res.URL == "" {
		return "", baseError(CodePaymentProviderError)
	}
	return res.URL, nil
}

// ExpireCheckout expires a checkout session at its provider (lago
// PaymentIntents::ExpireJob and Invoices::Payments::StripeService#expire_payment_url):
// the link was expired in Tanvo when it was enqueued; a removed connection
// is ignored, and a session already completed or expired is left as it is
// by the provider.
func (s *Service) ExpireCheckout(ctx context.Context, org core.OrganizationID, args kernel.CheckoutArgs) error {
	if s.providers == nil {
		return ErrNoProviders
	}
	if args.SessionID == "" {
		return nil
	}
	integ, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*integration.Integration, error) {
		return liveIntegration(ctx, r, org, args.IntegrationID)
	})
	if err != nil || integ == nil {
		return err
	}
	pc, err := s.open(ctx, integ)
	if err != nil {
		return err
	}
	err = pc.gw.ExpirePaymentURL(ctx, pc.conn, args.SessionID)
	if errors.Is(err, payment.ErrUnsupported) {
		return nil
	}
	if pe, ok := payment.AsProviderError(err); ok && (pe.Kind == payment.KindInvalidRequest || pe.Kind == payment.KindNotFound) {
		// invoices/payments/stripe_service.rb:108: an invalid request is
		// ignored, the other errors retried.
		return nil
	}
	return wrap0(err, "expire checkout session of invoice %s", args.InvoiceID)
}

// ExpireConnectionCheckouts expires the active checkout links of the
// invoices of the customers of a payment connection (lago
// PaymentProviders::Stripe::ExpirePaymentIntentsJob and
// ExpirePaymentIntentsService), when a setting shaping the checkout pages
// changed; the next link is generated with the new setting.
func (s *Service) ExpireConnectionCheckouts(ctx context.Context, org core.OrganizationID, args kernel.IntegrationArgs) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		its, err := r.PaymentIntents.ListActive(ctx, org)
		if err != nil {
			return wrap(err, "list active checkout links")
		}
		now := s.d.Now()
		for _, it := range its {
			if err := kernel.LockInvoice(ctx, r, org, it.InvoiceID); err != nil {
				return err
			}
			inv, err := r.Invoices.Get(ctx, org, it.InvoiceID)
			if isNotFound(err) {
				continue
			}
			if err != nil {
				return wrap(err, "load invoice %s", it.InvoiceID)
			}
			pc, err := r.PaymentProviderCustomers.GetByCustomer(ctx, org, inv.CustomerID, args.IntegrationID)
			if isNotFound(err) || err == nil && pc.Deleted() {
				continue
			}
			if err != nil {
				return wrap(err, "load payment connection of customer %s", inv.CustomerID)
			}
			cur, err := r.PaymentIntents.GetActiveByInvoice(ctx, org, inv.ID)
			if isNotFound(err) {
				continue
			}
			if err != nil {
				return wrap(err, "load checkout link of invoice %s", inv.ID)
			}
			if err := s.expireIntent(ctx, r, inv, cur, now); err != nil {
				return err
			}
		}
		return nil
	})
}
