package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/libs/billing"
	"github.com/tanvoxyz/tanvo/libs/billing/customers"
	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/jobs"
	"github.com/tanvoxyz/tanvo/libs/mail"
	"github.com/tanvoxyz/tanvo/libs/webhooks"
	"github.com/tanvoxyz/tanvo/providers/adyen"
	"github.com/tanvoxyz/tanvo/providers/cashfree"
	"github.com/tanvoxyz/tanvo/providers/flutterwave"
	"github.com/tanvoxyz/tanvo/providers/gocardless"
	"github.com/tanvoxyz/tanvo/providers/moneyhash"
	"github.com/tanvoxyz/tanvo/providers/stripe"
)

// paymentProviders returns the vendors of the payment provider adapters
// the server wires.
func paymentProviders() []integration.Provider {
	return []integration.Provider{
		integration.ProviderStripe, integration.ProviderAdyen, integration.ProviderGocardless,
		integration.ProviderMoneyhash, integration.ProviderCashfree, integration.ProviderFlutterwave,
	}
}

// paymentsWiring is the payments orchestration with its provider
// adapters, and the inbound provider webhooks.
type paymentsWiring struct {
	svc     *payments.Service
	inbound *webhooks.Inbound
	// gocardlessOAuth connects GoCardless connections by OAuth with the
	// installation's partner application; it is registered with the
	// integrations service's gateways.
	gocardlessOAuth integration.OAuthGateway
}

// newPayments returns the payments service over the provider adapters
// (Stripe, Adyen, GoCardless, MoneyHash, Cashfree and Flutterwave), the credentials sealed with the installation secret, and the
// provider webhook endpoints under cfg.APIURL; it emits its webhooks with
// emitter. It is bound to the billing engine with [paymentsWiring.bind].
func newPayments(cfg config.Config, tx port.Transactor, clock core.Clock, log *slog.Logger, tp trace.TracerProvider, mp metric.MeterProvider, emitter port.WebhookEmitter) (*paymentsWiring, error) {
	gc := gocardless.New(gocardless.WithTracerProvider(tp), gocardless.WithMeterProvider(mp), gocardless.WithLogger(log), gocardless.WithClock(clock.Now))
	set, err := port.NewPaymentProviderSet(
		stripe.New(stripe.WithTracerProvider(tp), stripe.WithMeterProvider(mp), stripe.WithLogger(log), stripe.WithClock(clock.Now)),
		adyen.New(adyen.WithTracerProvider(tp), adyen.WithMeterProvider(mp), adyen.WithLogger(log), adyen.WithClock(clock.Now)),
		gc,
		moneyhash.New(moneyhash.WithTracerProvider(tp), moneyhash.WithMeterProvider(mp), moneyhash.WithLogger(log), moneyhash.WithClock(clock.Now)),
		cashfree.New(cashfree.WithTracerProvider(tp), cashfree.WithMeterProvider(mp), cashfree.WithLogger(log), cashfree.WithClock(clock.Now)),
		flutterwave.New(flutterwave.WithTracerProvider(tp), flutterwave.WithMeterProvider(mp), flutterwave.WithLogger(log), flutterwave.WithClock(clock.Now)),
	)
	if err != nil {
		return nil, fmt.Errorf("payments: %w", err)
	}
	p := payments.Providers{Gateways: set, WebhookURL: providerWebhookURL(cfg.APIURL)}
	secret, err := cfg.SecretKeyBytes()
	if err != nil {
		return nil, fmt.Errorf("payments: %w", err)
	}
	if secret != nil {
		box, err := NewSecretBox(secret)
		if err != nil {
			return nil, fmt.Errorf("payments: %w", err)
		}
		p.Secrets = integrations.Sealer(box)
	}
	deps := &kernel.Deps{
		Tx: tx, Clock: clock, IDs: core.UUIDv7Generator{}, Webhooks: emitter, Logger: log,
		ActivityLogs: auditlog.NewActivityProducer(clock, core.UUIDv7Generator{}, rpc.ActivityActor),
		Transient:    retriedByBillingRetry,
	}
	svc := payments.New(deps).WithProviders(p)
	sources := map[string]webhooks.Source{}
	for _, pr := range paymentProviders() {
		sources[string(pr)] = paymentSource{svc: svc, provider: pr}
	}
	return &paymentsWiring{
		svc:     svc,
		inbound: &webhooks.Inbound{Tx: tx, Sources: sources, Clock: clock, IDs: core.UUIDv7Generator{}},
		gocardlessOAuth: gocardlessOAuth{gw: gc, client: gocardless.OAuthClient{
			ClientID: cfg.GoCardlessClientID, ClientSecret: cfg.GoCardlessClientSecret, Sandbox: cfg.GoCardlessSandbox,
		}},
	}, nil
}

// providerWebhookURL returns the endpoint of a connection's webhooks
// under the API URL ([rpc.InboundWebhookPattern]); nil without one.
func providerWebhookURL(apiURL string) func(payment.Connection) string {
	if apiURL == "" {
		return nil
	}
	base := strings.TrimRight(apiURL, "/")
	return func(c payment.Connection) string {
		return base + "/webhooks/" + string(c.Provider) + "/" + c.ID.String()
	}
}

// bind connects the payments service and the billing engine: paid credit
// invoices settle their wallet purchase, provider refund statuses reach
// the credit notes, and the invoices' and customers' checkout links are
// made at the providers.
func (w *paymentsWiring) bind(e *billing.Engine) {
	w.svc.WithCreditInvoices(e.Wallets).WithRefundStatuses(e.CreditNotes)
	e.Invoices.WithPaymentURLs(w.svc)
	customers.WithCheckout(w.svc)(e.Customers)
}

// paymentSource is the [webhooks.Source] of a payment provider.
type paymentSource struct {
	svc      *payments.Service
	provider integration.Provider
}

// Verify implements [webhooks.Source].
func (s paymentSource) Verify(ctx context.Context, org core.OrganizationID, conn core.IntegrationID, payload []byte, header http.Header) (string, error) {
	return s.svc.VerifyWebhook(ctx, s.provider, org, conn, payload, header)
}

// Handle implements [webhooks.Source].
func (s paymentSource) Handle(ctx context.Context, w *payment.InboundWebhook) error {
	return s.svc.HandleWebhook(ctx, w)
}

// paymentJobs registers the payments orchestration jobs, the inbound
// webhook jobs and their clock dispatches.
type paymentJobs struct {
	w        *paymentsWiring
	billing  billingJobs
	composer *mail.Composer
	sender   mail.Sender
	log      *slog.Logger
}

// paymentRetry are the retry rules of the payment kinds: the store's
// transient failures as for billing, and a provider at its rate limit or
// unreachable, 6 attempts polynomially longer (lago
// Invoices::Payments::CreateJob, app/jobs/invoices/payments/create_job.rb:16-17,
// PaymentRequests::Payments::CreateJob, create_job.rb:16-18,
// PaymentProviderCustomers::StripeCreateJob, stripe_create_job.rb:7-9).
func paymentRetry() []jobs.RetryRule {
	return append(billingRetry(), jobs.RetryOn(jobs.HTTPRetryAttempts, jobs.Polynomial(0.15), payment.ErrRetryable))
}

// paymentSchedule is the payments part of the clock schedule, with the
// lago-api clock jobs it ports (app/jobs/clock/).
func paymentSchedule() []clockDispatch {
	return []clockDispatch{
		// cancel_abandoned_payments_job.rb
		{clock: jobs.KindCancelAbandonedPayments, work: organization.WorkAbandonedPayments, kind: kernel.JobCancelAbandonedPayments, args: instant},
		// inbound_webhooks_retry_job.rb
		{clock: jobs.KindRetryInboundWebhooks, work: organization.WorkInboundWebhooks, kind: webhooks.KindRetryInbound},
		// inbound_webhooks_cleanup_job.rb
		{clock: jobs.KindCleanInboundWebhooks, work: organization.WorkExpiredInboundWebhooks, kind: webhooks.KindCleanInbound},
	}
}

// dispatchQueue returns the queue of a per-organization job a clock
// dispatch enqueues: the payments queue for the inbound webhook kinds,
// [kernel.QueueOf] otherwise.
func dispatchQueue(kind string) port.Queue {
	switch kind {
	case webhooks.KindRetryInbound, webhooks.KindCleanInbound:
		return port.QueuePayments
	}
	return kernel.QueueOf(kind)
}

// register registers every payment kind, the inbound webhook kinds and
// the payments clock dispatches.
func (p paymentJobs) register(reg *jobs.Registry) error {
	for _, k := range kernel.PaymentJobKinds() {
		if err := jobs.Register(reg, jobs.Kind{Name: k, Retry: paymentRetry()}, jobs.HandlerFunc[json.RawMessage](p.run)); err != nil {
			return err
		}
	}
	in := p.w.inbound
	if err := jobs.Register(reg, jobs.Kind{Name: webhooks.KindProcessInbound, Retry: billingRetry()},
		jobs.HandlerFunc[webhooks.InboundArgs](func(ctx context.Context, j *jobs.Job[webhooks.InboundArgs]) error {
			return in.Process(ctx, j.OrganizationID, j.Args)
		})); err != nil {
		return err
	}
	if err := jobs.Register(reg, jobs.Kind{Name: webhooks.KindRetryInbound},
		jobs.HandlerFunc[struct{}](func(ctx context.Context, j *jobs.Job[struct{}]) error {
			n, err := in.RetryDue(ctx, j.OrganizationID)
			if n > 0 {
				p.log.InfoContext(ctx, "webhooks: inbound webhooks retried", "organization_id", j.OrganizationID.String(), "count", n)
			}
			return err
		})); err != nil {
		return err
	}
	if err := jobs.Register(reg, jobs.Kind{Name: webhooks.KindCleanInbound},
		jobs.HandlerFunc[struct{}](func(ctx context.Context, j *jobs.Job[struct{}]) error {
			n, err := in.Clean(ctx, j.OrganizationID)
			if n > 0 {
				p.log.InfoContext(ctx, "webhooks: inbound webhooks deleted", "organization_id", j.OrganizationID.String(), "count", n)
			}
			return err
		})); err != nil {
		return err
	}
	for _, d := range paymentSchedule() {
		if err := jobs.Register(reg, jobs.Kind{Name: d.clock}, jobs.HandlerFunc[struct{}](func(ctx context.Context, j *jobs.Job[struct{}]) error {
			n, err := p.billing.dispatch(ctx, d, p.billing.tick(j.ScheduledAt))
			if err != nil {
				return err
			}
			p.log.InfoContext(ctx, "payments: dispatched", "kind", d.kind, "organizations", n)
			return nil
		})); err != nil {
			return err
		}
	}
	return nil
}

// run runs a payment job with the payments service. A job about a
// connection whose vendor has no adapter wired completes with a warning:
// retrying cannot help.
func (p paymentJobs) run(ctx context.Context, j *jobs.Job[json.RawMessage]) error {
	err := p.runKind(ctx, j)
	if errors.Is(err, payments.ErrNoAdapter) {
		p.log.WarnContext(ctx, "payments: no adapter for the provider", "kind", j.Kind,
			"organization_id", j.OrganizationID.String(), "error", err)
		return nil
	}
	return err
}

func (p paymentJobs) runKind(ctx context.Context, j *jobs.Job[json.RawMessage]) error {
	svc, org := p.w.svc, j.OrganizationID
	switch j.Kind {
	case kernel.JobCreateInvoicePayment:
		return runWith(j, func(a kernel.InvoiceArgs) error { return svc.CreateInvoicePayment(ctx, org, a) })
	case kernel.JobCreateRequestPayment:
		return runWith(j, func(a kernel.PaymentRequestArgs) error { return svc.CreateRequestPayment(ctx, org, a) })
	case kernel.JobSendPaymentRequestEmail:
		return runWith(j, func(a kernel.PaymentRequestArgs) error { return p.sendRequestEmail(ctx, org, a) })
	case kernel.JobSyncProviderCustomer:
		return runWith(j, func(a kernel.ProviderCustomerArgs) error { return svc.SyncProviderCustomer(ctx, org, a) })
	case kernel.JobSetPaymentMethod:
		return runWith(j, func(a kernel.PaymentMethodArgs) error { return svc.SetPaymentMethod(ctx, org, a) })
	case kernel.JobExpireCheckout:
		return runWith(j, func(a kernel.CheckoutArgs) error { return svc.ExpireCheckout(ctx, org, a) })
	case kernel.JobCancelAbandonedPayments:
		return runWith(j, func(a kernel.InstantArgs) error {
			if a.At.IsZero() {
				return jobs.Permanent(fmt.Errorf("%s: %w: no instant", j.Kind, core.ErrInvalidValue))
			}
			return svc.CancelAbandonedPayments(ctx, org, a)
		})
	case kernel.JobCancelAbandonedPayment:
		return runWith(j, func(a kernel.PaymentArgs) error { return svc.CancelAbandonedPayment(ctx, org, a) })
	case kernel.JobRegisterProviderWebhook:
		return runWith(j, func(a kernel.IntegrationArgs) error { return svc.RegisterProviderWebhook(ctx, org, a) })
	case kernel.JobExpireConnectionCheckouts:
		return runWith(j, func(a kernel.IntegrationArgs) error { return svc.ExpireConnectionCheckouts(ctx, org, a) })
	case kernel.JobUpdatePaymentReference:
		return runWith(j, func(a kernel.InvoiceArgs) error { return svc.UpdatePaymentReference(ctx, org, a) })
	case kernel.JobCancelPayment:
		return runWith(j, func(a kernel.PaymentArgs) error { return svc.CancelPayment(ctx, org, a) })
	}
	return jobs.Permanent(fmt.Errorf("payments: kind %s: %w", j.Kind, core.ErrNotFound))
}

// runWith decodes the args of j and runs f with them; undecodable args
// fail for good.
func runWith[A any](j *jobs.Job[json.RawMessage], f func(A) error) error {
	var a A
	if len(j.Args) > 0 {
		if err := json.Unmarshal(j.Args, &a); err != nil {
			return jobs.Permanent(fmt.Errorf("%s: decode args: %w", j.Kind, err))
		}
	}
	return f(a)
}

// sendRequestEmail sends the email of a payment request (lago
// PaymentRequestMailer#requested). No blob store is wired yet, so the
// invoice numbers are shown without links to their PDFs.
func (p paymentJobs) sendRequestEmail(ctx context.Context, org core.OrganizationID, a kernel.PaymentRequestArgs) error {
	e, err := p.w.svc.RequestEmailData(ctx, org, a)
	if err != nil || e == nil {
		return err
	}
	in := mail.PaymentRequestEmail{
		BillingEntity: e.BillingEntity, FromEmailEnabled: e.FromEmailEnabled, Locale: e.Locale, Email: e.Email, Bcc: e.Bcc,
		CustomerName: e.CustomerName, Currency: e.Request.Currency.Code(), Amount: e.Amount,
		NetPaymentTermDays: e.NetPaymentTermDays, PaymentURL: e.PaymentURL,
	}
	for _, inv := range e.Invoices {
		in.Invoices = append(in.Invoices, mail.ReceiptInvoice{Number: inv.Number, Currency: in.Currency, TotalDueAmount: inv.TotalDueAmount})
	}
	m, err := p.composer.PaymentRequestRequested(in)
	if errors.Is(err, mail.ErrSkipped) {
		p.log.InfoContext(ctx, "mail: skipped", "template", string(mail.KindPaymentRequestRequested), "reason", err.Error())
		return nil
	}
	if err != nil {
		return err
	}
	return p.sender.Send(ctx, m)
}
