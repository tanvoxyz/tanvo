package payments

import (
	"context"
	"errors"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// RequestEmail is what the payment request email shows (lago
// PaymentRequestMailer#requested, app/mailers/payment_request_mailer.rb,
// and app/views/payment_request_mailer/requested.slim).
type RequestEmail struct {
	// Request is the payment request.
	Request *payment.Request
	// BillingEntity is the customer's billing entity, which sends it.
	BillingEntity *organization.BillingEntity
	// FromEmailEnabled sends from the billing entity's email.
	FromEmailEnabled bool
	// Locale is the customer's applicable document locale.
	Locale string
	// Email is the request's recipient list.
	Email string
	// Bcc are the dunning campaign's BCC addresses.
	Bcc []string
	// CustomerName is the customer's display name.
	CustomerName string
	// Amount is the requested amount in currency units.
	Amount money.Decimal
	// NetPaymentTermDays is the customer's applicable payment term.
	NetPaymentTermDays int
	// PaymentURL is the hosted payment link; empty when none can be made.
	PaymentURL string
	// Invoices are the requested invoices.
	Invoices []RequestEmailInvoice
}

// RequestEmailInvoice is an invoice of a [RequestEmail].
type RequestEmailInvoice struct {
	// ID is the invoice.
	ID core.InvoiceID
	// Number is the invoice number.
	Number string
	// TotalDueAmount is what is left to pay, in currency units.
	TotalDueAmount money.Decimal
	// File is the invoice PDF; zero when it is not rendered yet.
	File core.BlobRef
}

// RequestEmailData loads the email of payment request args.PaymentRequestID
// (the [kernel.JobSendPaymentRequestEmail] job). It returns nil when the
// request was removed, has no email, or its billing entity has no email
// (payment_request_mailer.rb:11-12). The payment link is made with
// [Service.RequestPaymentURL]; a request that cannot have one (no or a
// GoCardless provider, a paid request, a provider refusal) is sent
// without it, as GeneratePaymentUrlService's failure leaves the link out.
func (s *Service) RequestEmailData(ctx context.Context, org core.OrganizationID, args kernel.PaymentRequestArgs) (*RequestEmail, error) {
	e, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*RequestEmail, error) {
		q, err := r.PaymentRequests.Get(ctx, org, args.PaymentRequestID)
		if isNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, wrap(err, "load payment request %s", args.PaymentRequestID)
		}
		if q.Email == nil || strings.TrimSpace(*q.Email) == "" {
			return nil, nil
		}
		env, err := kernel.LoadCustomerEnv(ctx, r, org, q.CustomerID, core.BillingEntityID{})
		if err != nil {
			return nil, err
		}
		be := env.BillingEntity
		if be.Email == nil || strings.TrimSpace(*be.Email) == "" {
			return nil, nil
		}
		o, err := r.Organizations.Get(ctx, org)
		if err != nil {
			return nil, wrap(err, "load organization %s", org)
		}
		c := env.Customer
		e := &RequestEmail{
			Request: q, BillingEntity: be, FromEmailEnabled: o.FromEmailEnabled,
			Locale: c.ApplicableDocumentLocale(env.Defaults), Email: *q.Email,
			CustomerName: c.DisplayName(true), Amount: q.Currency.FromMinor(q.Amount),
			NetPaymentTermDays: c.ApplicableNetPaymentTermDays(env.Defaults),
		}
		if !q.DunningCampaignID.IsZero() {
			dc, err := r.DunningCampaigns.Get(ctx, org, q.DunningCampaignID)
			switch {
			case isNotFound(err):
			case err != nil:
				return nil, wrap(err, "load dunning campaign %s", q.DunningCampaignID)
			default:
				e.Bcc = dc.BCCEmails
			}
		}
		invs, err := r.Invoices.ListByIDs(ctx, org, q.InvoiceIDs)
		if err != nil {
			return nil, wrap(err, "load invoices of payment request %s", q.ID)
		}
		for _, inv := range invs {
			e.Invoices = append(e.Invoices, RequestEmailInvoice{
				ID: inv.ID, Number: inv.Number, TotalDueAmount: inv.Currency.FromMinor(inv.TotalDueAmount()), File: inv.File,
			})
		}
		return e, nil
	})
	if err != nil || e == nil {
		return nil, err
	}
	url, err := s.RequestPaymentURL(ctx, org, e.Request.ID)
	var ve *core.ValidationError
	_, provider := payment.AsProviderError(err)
	switch {
	case err == nil:
		e.PaymentURL = url
	case errors.As(err, &ve), errors.Is(err, ErrNoProviders), provider && !errors.Is(err, payment.ErrRetryable):
	default:
		return nil, err
	}
	return e, nil
}
