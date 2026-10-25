package payments_test

import (
	"context"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// eventGateway is a Stripe adapter whose webhooks decode to fixed events
// and whose payment methods have fixed details.
type eventGateway struct {
	*fakeGateway

	events   []payment.Event
	parseErr error

	method    payment.ProviderMethod
	methodErr error
	fetched   []string
}

func (g *eventGateway) ParseWebhook(context.Context, payment.Connection, []byte) ([]payment.Event, error) {
	return g.events, g.parseErr
}

func (g *eventGateway) PaymentMethod(_ context.Context, _ payment.Connection, id string) (payment.ProviderMethod, error) {
	g.fetched = append(g.fetched, id)
	return g.method, g.methodErr
}

// eventFixture is a provider fixture whose connection delivers webhooks.
type eventFixture struct {
	*providerFixture
	eg   *eventGateway
	sync *recordedSync
}

func newEventFixture(t *testing.T, opts ...func(*invoice.Invoice)) *eventFixture {
	t.Helper()
	f := &eventFixture{providerFixture: newProviderFixture(t, opts...), sync: &recordedSync{}}
	f.eg = &eventGateway{fakeGateway: f.gw}
	set, err := port.NewPaymentProviderSet(f.eg)
	if err != nil {
		t.Fatal(err)
	}
	f.svc = payments.New(f.h.Deps).WithProviders(payments.Providers{Gateways: set}).WithPaymentSync(f.sync)
	return f
}

// deliver handles a stored webhook of the connection decoding to evs.
func (f *eventFixture) deliver(evs ...payment.Event) error {
	f.eg.events = evs
	return f.svc.HandleWebhook(f.h.Ctx, &payment.InboundWebhook{
		ID: core.NewID[core.InboundWebhookID](f.h.IDs), OrganizationID: f.h.OrgID(), Source: "stripe",
		IntegrationID: f.integ.ID, EventType: "payment_intent.succeeded", Payload: []byte("{}"),
	})
}

// storedPayment creates the pending payment ch_123456 of the payable.
func (f *eventFixture) storedPayment(p payment.Payable, amount int64) *payment.Payment {
	f.h.T.Helper()
	pay := f.h.StoredPayment(p, f.cust.ID, amount, payment.StatePending, f.integ.ID)
	id := "ch_123456"
	pay.ProviderPaymentID = &id
	f.h.Do(func(ctx context.Context, r port.Repos) error { return r.Payments.Update(ctx, pay) })
	return pay
}

// payment reloads a payment.
func (f *eventFixture) payment(id core.PaymentID) *payment.Payment {
	f.h.T.Helper()
	return billingtest.Read(f.h, func(ctx context.Context, r port.Repos) (*payment.Payment, error) {
		return r.Payments.Get(ctx, f.h.OrgID(), id)
	})
}

// paymentEvent is a live Stripe event on ch_123456.
func paymentEvent(st payment.State, providerStatus string, opts ...func(*payment.PaymentEvent)) payment.PaymentEvent {
	ev := payment.PaymentEvent{
		EventMeta:         payment.EventMeta{ProviderEventID: "evt_1", Type: "payment_intent.succeeded", Livemode: true, OccurredAt: now},
		ProviderPaymentID: "ch_123456", ProviderStatus: providerStatus, Status: st,
	}
	for _, o := range opts {
		o(&ev)
	}
	return ev
}

func withErrorCode(code string) func(*payment.PaymentEvent) {
	return func(ev *payment.PaymentEvent) { ev.ErrorCode = code }
}

// TestPaymentEventStatus ports the update_payment_status cases of an
// invoice's known payment.
func TestPaymentEventStatus(t *testing.T) {
	type want struct {
		payment payment.State
		errCode string
		status  core.PaymentStatus
		ready   bool
		paid    int64
		synced  int
		emitted bool
	}
	tests := []struct {
		name  string
		setup func(f *eventFixture) *invoice.Invoice
		after func(f *eventFixture, p *payment.Payment)
		ev    payment.PaymentEvent
		want  want
	}{
		{
			// source: spec/services/invoices/payments/stripe_service_spec.rb:235, 253, 264
			name: "succeeded", ev: paymentEvent(payment.StateSucceeded, "succeeded"),
			want: want{payment: payment.StateSucceeded, status: core.PaymentStatusSucceeded, paid: 10000, synced: 1, emitted: true},
		},
		{
			// source: spec/services/invoices/payments/stripe_service_spec.rb:298, 321
			name: "failed", ev: paymentEvent(payment.StateFailed, "canceled"),
			want: want{payment: payment.StateFailed, status: core.PaymentStatusFailed, ready: true},
		},
		{
			// source: spec/services/invoices/payments/stripe_service_spec.rb:346
			name: "authentication required with 3D Secure",
			setup: func(f *eventFixture) *invoice.Invoice {
				f.integ.Settings = []byte(`{"supports_3ds":"true"}`)
				f.h.Do(func(ctx context.Context, r port.Repos) error { return r.Integrations.Update(ctx, f.integ) })
				return f.inv
			},
			ev:   paymentEvent(payment.StateFailed, "requires_payment_method", withErrorCode("authentication_required")),
			want: want{payment: payment.StateFailed, errCode: "authentication_required", status: core.PaymentStatusPending, ready: true},
		},
		{
			// source: spec/services/invoices/payments/stripe_service_spec.rb:374
			name: "authentication required without 3D Secure",
			ev:   paymentEvent(payment.StateFailed, "requires_payment_method", withErrorCode("authentication_required")),
			want: want{payment: payment.StateFailed, errCode: "authentication_required", status: core.PaymentStatusFailed, ready: true},
		},
		{
			// source: spec/services/invoices/payments/stripe_service_spec.rb:409
			name: "authentication required for a payment-gated subscription",
			setup: func(f *eventFixture) *invoice.Invoice {
				inv := f.h.StoredInvoice(f.cust, func(inv *invoice.Invoice) {
					inv.Status = invoice.StatusOpen
					inv.TotalAmount = 200
					inv.ReadyForPaymentProcessing = true
				})
				sub := f.h.Subscription(f.cust, f.h.Plan(period.Monthly, 200), func(s *subscription.Subscription) {
					s.Status = subscription.StatusIncomplete
				})
				f.h.LinkSubscription(inv, sub, invoice.InvoicingReasonSubscriptionStarting, now, now.AddDate(0, 1, 0))
				rule := subscription.NewActivationRule(f.h.OrgID(), core.NewID[core.ActivationRuleID](f.h.IDs), sub.ID,
					subscription.ActivationRuleTypePayment, 0, f.h.Now())
				rule.Status = subscription.ActivationRuleStatusPending
				f.h.Do(func(ctx context.Context, r port.Repos) error { return r.ActivationRules.Create(ctx, rule) })
				return inv
			},
			ev:   paymentEvent(payment.StateFailed, "requires_payment_method", withErrorCode("authentication_required")),
			want: want{payment: payment.StateFailed, errCode: "authentication_required", status: core.PaymentStatusPending, ready: true},
		},
		{
			// source: spec/services/invoices/payments/stripe_service_spec.rb:429
			name: "another payment requires action",
			// The webhook of the first, failed, payment arrives once its
			// retry waits for the customer's authentication.
			after: func(f *eventFixture, p *payment.Payment) {
				p.Status = payment.StateFailed
				f.h.Do(func(ctx context.Context, r port.Repos) error { return r.Payments.Update(ctx, p) })
				other := f.h.StoredPayment(payment.InvoicePayable(f.inv.ID), f.cust.ID, 10000, payment.StateProcessing, f.integ.ID)
				pid, st := "pi_retry", payment.ProviderStatusRequiresAction
				other.ProviderPaymentID, other.ProviderStatus = &pid, &st
				f.h.Do(func(ctx context.Context, r port.Repos) error { return r.Payments.Update(ctx, other) })
			},
			ev:   paymentEvent(payment.StateFailed, "canceled"),
			want: want{payment: payment.StateFailed, status: core.PaymentStatusPending, ready: true},
		},
		{
			// source: spec/services/invoices/payments/stripe_service_spec.rb:459
			name: "invoice already paid",
			setup: func(f *eventFixture) *invoice.Invoice {
				f.inv.PaymentStatus = core.PaymentStatusSucceeded
				f.h.SaveInvoice(f.inv)
				return f.inv
			},
			ev:   paymentEvent(payment.StateSucceeded, "succeeded"),
			want: want{payment: payment.StatePending, status: core.PaymentStatusSucceeded, ready: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newEventFixture(t)
			inv := f.inv
			if tt.setup != nil {
				inv = tt.setup(f)
			}
			p := f.storedPayment(payment.InvoicePayable(inv.ID), inv.TotalAmount)
			if tt.after != nil {
				tt.after(f, p)
			}
			if err := f.deliver(tt.ev); err != nil {
				t.Fatalf("HandleWebhook: %v", err)
			}
			got := f.payment(p.ID)
			if got.Status != tt.want.payment {
				t.Errorf("payment status: got = %s, want = %s", got.Status, tt.want.payment)
			}
			if code := deref(got.ErrorCode); code != tt.want.errCode {
				t.Errorf("error code: got = %q, want = %q", code, tt.want.errCode)
			}
			ginv := f.h.Invoice(inv.ID)
			if ginv.PaymentStatus != tt.want.status || ginv.ReadyForPaymentProcessing != tt.want.ready {
				t.Errorf("invoice: got = %s ready %t, want = %s ready %t", ginv.PaymentStatus, ginv.ReadyForPaymentProcessing, tt.want.status, tt.want.ready)
			}
			if ginv.TotalPaidAmount != tt.want.paid {
				t.Errorf("paid: got = %d, want = %d", ginv.TotalPaidAmount, tt.want.paid)
			}
			if got := len(f.sync.payments); got != tt.want.synced {
				t.Errorf("synced payments: got = %d, want = %d", got, tt.want.synced)
			}
			if got := hasWebhook(f.h, webhook.EventTypePaymentSucceeded); got != tt.want.emitted {
				t.Errorf("payment.succeeded: got = %t, want = %t", got, tt.want.emitted)
			}
			wantJob := tt.ev.Status == payment.StateSucceeded
			if got := slices.Contains(f.h.JobKinds(), kernel.JobSetPaymentMethod); got != wantJob {
				t.Errorf("set payment method job: got = %t, want = %t", got, wantJob)
			}
		})
	}
}

// TestPaymentEventMissingPayment ports the update_payment_status cases of
// a payment Tanvo does not have.
func TestPaymentEventMissingPayment(t *testing.T) {
	unknown := core.NewID[core.InvoiceID](core.UUIDv7Generator{})
	tests := []struct {
		name     string
		oneTime  bool
		invoice  func(f *eventFixture) core.InvoiceID
		amount   int64
		wantErr  string
		created  bool
		wantPaid int64
	}{
		{
			// source: spec/services/invoices/payments/stripe_service_spec.rb:505, 538
			name: "one-time payment", oneTime: true, created: true, wantPaid: 10000,
			invoice: func(f *eventFixture) core.InvoiceID { return f.inv.ID },
		},
		{
			// source: spec/services/invoices/payments/stripe_service_spec.rb:524
			name: "one-time payment with an amount", oneTime: true, amount: 4242, created: true, wantPaid: 4242,
			invoice: func(f *eventFixture) core.InvoiceID { return f.inv.ID },
		},
		{
			// source: spec/services/invoices/payments/stripe_service_spec.rb:560
			name: "one-time payment of an unknown invoice", oneTime: true, wantErr: "invoice",
			invoice: func(*eventFixture) core.InvoiceID { return unknown },
		},
		{
			// source: spec/services/invoices/payments/stripe_service_spec.rb:578
			name: "no invoice reference",
		},
		{
			// source: spec/services/invoices/payments/stripe_service_spec.rb:600
			name:    "unknown invoice reference",
			invoice: func(*eventFixture) core.InvoiceID { return unknown },
		},
		{
			// source: spec/services/invoices/payments/stripe_service_spec.rb:627
			name: "invoice reference", created: true, wantPaid: 10000,
			invoice: func(f *eventFixture) core.InvoiceID { return f.inv.ID },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newEventFixture(t)
			ev := paymentEvent(payment.StateSucceeded, "succeeded", func(ev *payment.PaymentEvent) {
				ev.OneTime, ev.Amount = tt.oneTime, tt.amount
				if tt.invoice != nil {
					ev.Refs.InvoiceID = tt.invoice(f)
				}
			})
			err := f.deliver(ev)
			if tt.wantErr != "" {
				wantNotFound(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("HandleWebhook: %v", err)
			}
			ps := f.payments()
			if !tt.created {
				if len(ps) != 0 {
					t.Fatalf("payments: got = %d, want = 0", len(ps))
				}
				return
			}
			if len(ps) != 1 {
				t.Fatalf("payments: got = %d, want = 1", len(ps))
			}
			p := ps[0]
			if p.Status != payment.StateSucceeded || deref(p.ProviderPaymentID) != "ch_123456" || p.IntegrationID != f.integ.ID ||
				p.Amount != tt.wantPaid || p.Currency != f.inv.Currency || p.CustomerID != f.cust.ID {
				t.Errorf("payment: got = %s %s %s %d %s, want = succeeded ch_123456 %s %d %s",
					p.Status, deref(p.ProviderPaymentID), p.IntegrationID, p.Amount, p.Currency, f.integ.ID, tt.wantPaid, f.inv.Currency)
			}
			inv := f.h.Invoice(f.inv.ID)
			if inv.PaymentStatus != core.PaymentStatusSucceeded || inv.ReadyForPaymentProcessing {
				t.Errorf("invoice: got = %s ready %t, want = succeeded ready false", inv.PaymentStatus, inv.ReadyForPaymentProcessing)
			}
		})
	}
}

// TestPaymentEventTestMode checks that a test-mode event naming a resource
// Tanvo does not have is acknowledged (handle_event_service.rb
// NotFoundFailure), and that an unknown payable is acknowledged.
func TestPaymentEventTestMode(t *testing.T) {
	f := newEventFixture(t)
	ev := paymentEvent(payment.StateSucceeded, "succeeded", func(ev *payment.PaymentEvent) {
		ev.Livemode, ev.OneTime = false, true
		ev.Refs.InvoiceID = core.NewID[core.InvoiceID](f.h.IDs)
	})
	if err := f.deliver(ev); err != nil {
		t.Errorf("test-mode event of an unknown invoice: got = %v, want = nil", err)
	}
	f.eg.parseErr = payment.ErrUnknownPayable
	if err := f.deliver(); err != nil {
		t.Errorf("event of an unknown payable: got = %v, want = nil", err)
	}
}

// TestPaymentSucceededSetsMethod ports payment_intent.succeeded: the
// payment's method is recorded with its details and its receipt issued.
func TestPaymentSucceededSetsMethod(t *testing.T) {
	details := payment.MethodDetails{Type: "card", Brand: "visa", Last4: "4242", ExpMonth: 8, ExpYear: 2030}
	tests := []struct {
		name    string
		payable func(f *eventFixture) payment.Payable
	}{
		// source: spec/services/payment_providers/stripe/webhooks/payment_intent_succeeded_service_spec.rb:22, 58
		{"invoice", func(f *eventFixture) payment.Payable { return payment.InvoicePayable(f.inv.ID) }},
		// source: spec/services/payment_providers/stripe/webhooks/payment_intent_succeeded_service_spec.rb:84, 115
		{"payment request", func(f *eventFixture) payment.Payable {
			q := f.h.StoredPaymentRequest(f.cust.ID, core.PaymentStatusPending, f.inv)
			return payment.RequestPayable(q.ID)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newEventFixture(t)
			f.eg.method = payment.ProviderMethod{ID: "pm_1", ProviderCustomerID: "cus_1", Details: details}
			payable := tt.payable(f)
			p := f.storedPayment(payable, 10000)
			if err := f.deliver(paymentEvent(payment.StateSucceeded, "succeeded", func(ev *payment.PaymentEvent) {
				ev.ProviderMethodID = "pm_1"
			})); err != nil {
				t.Fatalf("HandleWebhook: %v", err)
			}
			if !slices.Contains(f.h.JobKinds(), kernel.JobSetPaymentMethod) {
				t.Fatalf("jobs: got = %v, want = %s", f.h.JobKinds(), kernel.JobSetPaymentMethod)
			}
			if err := f.svc.SetPaymentMethod(f.h.Ctx, f.h.OrgID(), kernel.PaymentMethodArgs{PaymentID: p.ID, ProviderMethodID: "pm_1"}); err != nil {
				t.Fatalf("SetPaymentMethod: %v", err)
			}
			got := f.payment(p.ID)
			if deref(got.ProviderPaymentMethodID) != "pm_1" || got.ProviderPaymentMethod != details {
				t.Errorf("method: got = %s %+v, want = pm_1 %+v", deref(got.ProviderPaymentMethodID), got.ProviderPaymentMethod, details)
			}
			if got.Status != payment.StateSucceeded {
				t.Errorf("payment status: got = %s, want = succeeded", got.Status)
			}
			if receiptOf(f.h, p.ID) == nil {
				t.Errorf("receipt: got = nil, want = one")
			}
			if payable.Type == payment.PayableTypePaymentRequest {
				q := billingtest.Read(f.h, func(ctx context.Context, r port.Repos) (*payment.Request, error) {
					return r.PaymentRequests.Get(ctx, f.h.OrgID(), payable.PaymentRequestID)
				})
				if q.PaymentStatus != core.PaymentStatusSucceeded {
					t.Errorf("payment request: got = %s, want = succeeded", q.PaymentStatus)
				}
				if inv := f.h.Invoice(f.inv.ID); inv.PaymentStatus != core.PaymentStatusSucceeded || inv.TotalPaidAmount != 10000 {
					t.Errorf("invoice: got = %s paid %d, want = succeeded paid 10000", inv.PaymentStatus, inv.TotalPaidAmount)
				}
				if !hasWebhook(f.h, webhook.EventTypePaymentRequestPaymentStatusUpdated) {
					t.Errorf("webhooks: got = %v, want = %s", f.h.Webhooks(), webhook.EventTypePaymentRequestPaymentStatusUpdated)
				}
			}
		})
	}
}

// TestSetPaymentMethodLinksCustomerMethod checks that a payment takes the
// customer's method with the provider's identifier, whose details are
// updated (set_payment_method_and_create_receipt_job.rb:27-33).
func TestSetPaymentMethodLinksCustomerMethod(t *testing.T) {
	f := newEventFixture(t)
	m := storedMethod(f.h, f.cust, false)
	m.IntegrationID = f.integ.ID
	f.h.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentMethods.Update(ctx, m) })
	details := payment.MethodDetails{Type: "card", Brand: "mastercard", Last4: "4444"}
	f.eg.method = payment.ProviderMethod{ID: m.ProviderMethodID, Details: details}
	p := f.storedPayment(payment.InvoicePayable(f.inv.ID), 10000)
	if err := f.svc.SetPaymentMethod(f.h.Ctx, f.h.OrgID(), kernel.PaymentMethodArgs{PaymentID: p.ID, ProviderMethodID: m.ProviderMethodID}); err != nil {
		t.Fatalf("SetPaymentMethod: %v", err)
	}
	if got := f.payment(p.ID); got.PaymentMethodID != m.ID {
		t.Errorf("payment method: got = %s, want = %s", got.PaymentMethodID, m.ID)
	}
	if got := method(f.h, m.ID); got.Details != details {
		t.Errorf("method details: got = %+v, want = %+v", got.Details, details)
	}
	// A payment already recording the method is not fetched again.
	f.eg.fetched = nil
	if err := f.svc.SetPaymentMethod(f.h.Ctx, f.h.OrgID(), kernel.PaymentMethodArgs{PaymentID: p.ID, ProviderMethodID: m.ProviderMethodID}); err != nil {
		t.Fatalf("SetPaymentMethod again: %v", err)
	}
	if len(f.eg.fetched) != 0 {
		t.Errorf("fetches: got = %v, want = none", f.eg.fetched)
	}
	// A removed payment is ignored.
	if err := f.svc.SetPaymentMethod(f.h.Ctx, f.h.OrgID(), kernel.PaymentMethodArgs{PaymentID: core.NewID[core.PaymentID](f.h.IDs)}); err != nil {
		t.Errorf("SetPaymentMethod of a removed payment: got = %v, want = nil", err)
	}
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// TestPaymentMethodDetached ports PaymentProviderCustomers::StripeService#delete_payment_method:
// a method removed at the provider is deleted, and an event of a provider
// customer Tanvo does not know is ignored unless it names one of its
// customers.
func TestPaymentMethodDetached(t *testing.T) {
	tests := []struct {
		name        string
		pcust       string
		method      string
		refCustomer func(f *eventFixture) core.CustomerID
		wantDeleted bool
		wantErr     bool
	}{
		// source: spec/services/payment_provider_customers/stripe_service_spec.rb:411, 440
		{name: "removed", pcust: "cus_1", wantDeleted: true},
		// source: spec/services/payment_provider_customers/stripe_service_spec.rb:422, 453
		{name: "another method", pcust: "cus_1", method: "pm_other"},
		// source: spec/services/payment_provider_customers/stripe_service_spec.rb:466
		{name: "unknown provider customer", pcust: "cus_InvaLid"},
		// source: spec/services/payment_provider_customers/stripe_service_spec.rb:477
		{
			name: "unknown customer in the metadata", pcust: "cus_InvaLid",
			refCustomer: func(*eventFixture) core.CustomerID { return core.NewID[core.CustomerID](core.UUIDv7Generator{}) },
		},
		// source: spec/services/payment_provider_customers/stripe_service_spec.rb:492
		{
			name: "known customer in the metadata", pcust: "cus_InvaLid", wantErr: true,
			refCustomer: func(f *eventFixture) core.CustomerID { return f.cust.ID },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newEventFixture(t)
			m := storedMethod(f.h, f.cust, false)
			m.IntegrationID = f.integ.ID
			f.h.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentMethods.Update(ctx, m) })
			ev := payment.PaymentMethodDetached{
				EventMeta:          payment.EventMeta{ProviderEventID: "evt_1", Type: "payment_method.detached", Livemode: true, OccurredAt: now},
				ProviderCustomerID: tt.pcust, ProviderMethodID: m.ProviderMethodID,
			}
			if tt.method != "" {
				ev.ProviderMethodID = tt.method
			}
			if tt.refCustomer != nil {
				ev.Refs.CustomerID = tt.refCustomer(f)
			}
			err := f.deliver(ev)
			if tt.wantErr {
				wantNotFound(t, err, "payment_provider_customer")
				return
			}
			if err != nil {
				t.Fatalf("HandleWebhook: %v", err)
			}
			if got := method(f.h, m.ID).Deleted(); got != tt.wantDeleted {
				t.Errorf("method deleted: got = %t, want = %t", got, tt.wantDeleted)
			}
		})
	}
}
