package payments_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// fakeGateway is a Stripe adapter answering with fixed results; the
// methods it does not override panic.
type fakeGateway struct {
	payment.Gateway

	charge    payment.PaymentResult
	chargeErr error
	charges   []payment.PaymentParams

	get       payment.PaymentResult
	getErr    error
	cancel    payment.PaymentResult
	cancelErr error
	cancelled []string

	refund    payment.RefundResult
	refundErr error
	refunds   []payment.RefundParams

	referenceErr error
	references   []string
}

func (g *fakeGateway) UpdatePaymentReference(_ context.Context, _ payment.Connection, id, number string) error {
	g.references = append(g.references, id+" "+number)
	return g.referenceErr
}

func (g *fakeGateway) Provider() integration.Provider { return integration.ProviderStripe }

func (g *fakeGateway) CreatePayment(_ context.Context, _ payment.Connection, p payment.PaymentParams) (payment.PaymentResult, error) {
	g.charges = append(g.charges, p)
	return g.charge, g.chargeErr
}

func (g *fakeGateway) GetPayment(context.Context, payment.Connection, string) (payment.PaymentResult, error) {
	return g.get, g.getErr
}

func (g *fakeGateway) CancelPayment(_ context.Context, _ payment.Connection, id string) (payment.PaymentResult, error) {
	g.cancelled = append(g.cancelled, id)
	return g.cancel, g.cancelErr
}

func (g *fakeGateway) Refund(_ context.Context, _ payment.Connection, p payment.RefundParams) (payment.RefundResult, error) {
	g.refunds = append(g.refunds, p)
	return g.refund, g.refundErr
}

// providerFixture is a customer of a Stripe connection with a default
// payment method and a finalized invoice of 100.00.
type providerFixture struct {
	h     *billingtest.Harness
	svc   *payments.Service
	gw    *fakeGateway
	integ *integration.Integration
	cust  *customer.Customer
	inv   *invoice.Invoice
}

func newProviderFixture(t *testing.T, opts ...func(*invoice.Invoice)) *providerFixture {
	t.Helper()
	h := billingtest.New(t, now)
	gw := &fakeGateway{charge: payment.PaymentResult{
		ProviderPaymentID: "pi_1", ProviderStatus: "succeeded", Status: payment.StateSucceeded,
	}}
	set, err := port.NewPaymentProviderSet(gw)
	if err != nil {
		t.Fatal(err)
	}
	svc := payments.New(h.Deps).WithProviders(payments.Providers{Gateways: set})
	f := &providerFixture{h: h, svc: svc, gw: gw, cust: h.Customer()}
	f.integ = &integration.Integration{
		ID: core.NewID[core.IntegrationID](h.IDs), OrganizationID: h.OrgID(), Code: "stripe", Name: "Stripe",
		Category: integration.CategoryPayment, Provider: integration.ProviderStripe,
		Settings: []byte("{}"), CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	pcID := "cus_1"
	h.Do(func(ctx context.Context, r port.Repos) error {
		if err := r.Integrations.Create(ctx, f.integ); err != nil {
			return err
		}
		return r.PaymentProviderCustomers.Create(ctx, &payment.ProviderCustomer{
			ID: core.NewID[core.PaymentProviderCustomerID](h.IDs), OrganizationID: h.OrgID(), CustomerID: f.cust.ID,
			IntegrationID: f.integ.ID, ProviderCustomerID: &pcID, ProviderPaymentMethods: []string{"card"},
			CreatedAt: h.Now(), UpdatedAt: h.Now(),
		})
	})
	storedMethod(h, f.cust, true)
	f.inv = h.StoredInvoice(f.cust, append([]func(*invoice.Invoice){finalized(10000, 0), func(inv *invoice.Invoice) {
		inv.Type = invoice.TypeOneOff
	}}, opts...)...)
	return f
}

// pay runs the automatic payment of the invoice, without checkout delay.
func (f *providerFixture) pay() error {
	return f.svc.CreateInvoicePayment(f.h.Ctx, f.h.OrgID(), kernel.InvoiceArgs{InvoiceID: f.inv.ID, At: f.h.Now()})
}

func (f *providerFixture) payments() []*payment.Payment {
	return billingtest.Read(f.h, func(ctx context.Context, r port.Repos) ([]*payment.Payment, error) {
		return r.Payments.ListByPayable(ctx, f.h.OrgID(), payment.InvoicePayable(f.inv.ID))
	})
}

func TestCreateInvoicePayment(t *testing.T) {
	tests := []struct {
		name        string
		source      string
		err         error
		wantErr     error
		wantStatus  core.PaymentStatus
		wantPayment payment.State
		wantFailure bool
	}{
		{
			name: "succeeded", source: "spec/services/invoices/payments/create_service_spec.rb:48",
			wantStatus: core.PaymentStatusSucceeded, wantPayment: payment.StateSucceeded,
		},
		{
			name: "card declined", source: "spec/services/invoices/payments/create_service_spec.rb:653",
			err:        &payment.ProviderError{Provider: "stripe", Kind: payment.KindCardDeclined, Code: "card_declined"},
			wantStatus: core.PaymentStatusFailed, wantPayment: payment.StateFailed, wantFailure: true,
		},
		{
			name: "rate limited", source: "app/services/payment_providers/stripe/payments/create_service.rb:56-61",
			err:        &payment.ProviderError{Provider: "stripe", Kind: payment.KindRateLimited},
			wantErr:    payment.ErrRetryable,
			wantStatus: core.PaymentStatusPending, wantPayment: payment.StatePending,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source: " + tt.source)
			f := newProviderFixture(t)
			if tt.err != nil {
				f.gw.chargeErr = tt.err
				f.gw.charge = payment.PaymentResult{}
			}
			err := f.pay()
			if tt.wantErr == nil && err != nil || tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("error: got = %v, want = %v", err, tt.wantErr)
			}
			if got := len(f.gw.charges); got != 1 {
				t.Fatalf("charges: got = %d, want = 1", got)
			}
			if got := f.gw.charges[0]; got.Amount != 10000 || got.ProviderCustomerID != "cus_1" || got.ProviderMethodID == "" {
				t.Errorf("charge: got = %+v, want = 10000 of cus_1 with the default method", got)
			}
			inv := f.h.Invoice(f.inv.ID)
			if inv.PaymentStatus != tt.wantStatus {
				t.Errorf("payment status: got = %s, want = %s", inv.PaymentStatus, tt.wantStatus)
			}
			ps := f.payments()
			if len(ps) != 1 || ps[0].Status != tt.wantPayment {
				t.Fatalf("payments: got = %+v, want = one %s", ps, tt.wantPayment)
			}
			if got := hasWebhook(f.h, webhook.EventTypeInvoicePaymentFailure); got != tt.wantFailure {
				t.Errorf("invoice.payment_failure: got = %t, want = %t", got, tt.wantFailure)
			}
			if tt.wantPayment == payment.StateSucceeded && inv.TotalPaidAmount != 10000 {
				t.Errorf("total paid: got = %d, want = 10000", inv.TotalPaidAmount)
			}
		})
	}
}

func TestCreateInvoicePaymentSkips(t *testing.T) {
	t.Run("manual payment method", func(t *testing.T) {
		t.Log("source: spec/services/invoices/payments/create_service_spec.rb:354")
		f := newProviderFixture(t)
		err := f.svc.CreateInvoicePayment(f.h.Ctx, f.h.OrgID(), kernel.InvoiceArgs{
			InvoiceID: f.inv.ID, At: f.h.Now(), PaymentMethodType: core.PaymentMethodTypeManual,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := len(f.gw.charges); got != 0 {
			t.Errorf("charges: got = %d, want = 0", got)
		}
	})
	t.Run("paid invoice", func(t *testing.T) {
		t.Log("source: spec/services/invoices/payments/create_service_spec.rb:402")
		f := newProviderFixture(t, func(inv *invoice.Invoice) { inv.PaymentStatus = core.PaymentStatusSucceeded })
		if err := f.pay(); err != nil {
			t.Fatal(err)
		}
		if got := len(f.gw.charges); got != 0 {
			t.Errorf("charges: got = %d, want = 0", got)
		}
	})
	t.Run("checkout delay", func(t *testing.T) {
		t.Log("source: spec/services/invoices/payments/create_service_spec.rb:902")
		f := newProviderFixture(t)
		f.storeIntent()
		if err := f.svc.CreateInvoicePayment(f.h.Ctx, f.h.OrgID(), kernel.InvoiceArgs{InvoiceID: f.inv.ID}); err != nil {
			t.Fatal(err)
		}
		if got := len(f.gw.charges); got != 0 {
			t.Errorf("charges: got = %d, want = 0", got)
		}
		if !slices.Contains(f.h.JobKinds(), kernel.JobCreateInvoicePayment) {
			t.Errorf("jobs: got = %v, want = %s", f.h.JobKinds(), kernel.JobCreateInvoicePayment)
		}
	})
	t.Run("credit invoice without checkout delay", func(t *testing.T) {
		t.Log("source: spec/services/invoices/payments/create_service_spec.rb:914")
		f := newProviderFixture(t, func(inv *invoice.Invoice) { inv.Type = invoice.TypeCredit })
		f.h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.SkipCreditInvoicePaymentDelay = true })
		f.storeIntent()
		if err := f.svc.CreateInvoicePayment(f.h.Ctx, f.h.OrgID(), kernel.InvoiceArgs{InvoiceID: f.inv.ID}); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(f.h.JobKinds(), kernel.JobCreateInvoicePayment) {
			t.Errorf("jobs: got = %v, want = no delayed payment", f.h.JobKinds())
		}
	})
}

// storeIntent stores a checkout link of another invoice, making the
// organization one that uses checkout links.
func (f *providerFixture) storeIntent() {
	other := f.h.StoredInvoice(f.cust, finalized(500, 0))
	f.h.Do(func(ctx context.Context, r port.Repos) error {
		return r.PaymentIntents.Create(ctx, payment.NewIntent(f.h.OrgID(), core.NewID[payment.IntentID](f.h.IDs), other.ID, f.h.Now()))
	})
}

func TestRefund(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		manual     bool
		res        payment.RefundResult
		err        error
		wantErr    error
		wantID     string
		wantStatus core.RefundStatus
		wantCalls  int
	}{
		{
			name: "succeeded", source: "spec/services/credit_notes/refunds/stripe_service_spec.rb:37",
			res:    payment.RefundResult{ProviderRefundID: "re_1", Status: core.RefundStatusSucceeded},
			wantID: "re_1", wantStatus: core.RefundStatusSucceeded, wantCalls: 1,
		},
		{
			name: "rejected", source: "spec/services/credit_notes/refunds/stripe_service_spec.rb:143",
			err:     &payment.ProviderError{Provider: "stripe", Kind: payment.KindInvalidRequest, Code: "charge_already_refunded"},
			wantErr: creditnotes.ErrRefundRejected, wantCalls: 1,
		},
		{
			name: "unknown status", source: "spec/services/credit_notes/refunds/stripe_service_spec.rb:37",
			res: payment.RefundResult{ProviderRefundID: "re_2"}, err: payment.ErrUnknownStatus,
			wantID: "re_2", wantCalls: 1,
		},
		{
			name: "manual payment", source: "app/services/credit_notes/create_service.rb:255-264",
			manual: true, wantErr: creditnotes.ErrRefundSkipped,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source: " + tt.source)
			f := newProviderFixture(t)
			f.gw.refund, f.gw.refundErr = tt.res, tt.err
			p := f.h.StoredPayment(payment.InvoicePayable(f.inv.ID), f.cust.ID, 10000, payment.StateSucceeded, f.integ.ID)
			if !tt.manual {
				id := "pi_9"
				p.ProviderPaymentID = &id
			} else {
				p.Kind, p.IntegrationID = payment.KindManual, core.IntegrationID{}
			}
			f.h.Do(func(ctx context.Context, r port.Repos) error { return r.Payments.Update(ctx, p) })
			cn := f.h.StoredCreditNote(f.inv, 0, func(cn *creditnote.CreditNote) { cn.RefundAmount = 2500 })
			got, err := f.svc.Refunds().Refund(f.h.Ctx, kernel.RefundRequest{
				IdempotencyKey: cn.ID.String(), CreditNote: cn, Invoice: f.inv, PaymentID: p.ID,
				Amount: 2500, Currency: f.inv.Currency,
			})
			if tt.wantErr == nil && err != nil || tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("error: got = %v, want = %v", err, tt.wantErr)
			}
			if got.ProviderRefundID != tt.wantID || got.Status != tt.wantStatus {
				t.Errorf("result: got = %+v, want = %s %s", got, tt.wantID, tt.wantStatus)
			}
			if len(f.gw.refunds) != tt.wantCalls {
				t.Fatalf("refunds: got = %d, want = %d", len(f.gw.refunds), tt.wantCalls)
			}
			if tt.wantCalls > 0 {
				if r := f.gw.refunds[0]; r.ProviderPaymentID != "pi_9" || r.Amount != 2500 || r.CreditNoteID != cn.ID {
					t.Errorf("refund: got = %+v, want = 2500 of pi_9 for %s", r, cn.ID)
				}
			}
		})
	}
}

func TestCancelAbandonedPayment(t *testing.T) {
	tests := []struct {
		name          string
		source        string
		get           payment.PaymentResult
		wantCancelled bool
		wantStatus    payment.State
		wantReady     bool
	}{
		{
			name: "card waiting for the customer", source: "spec/services/invoices/payments/cancel_abandoned_service_spec.rb:36",
			get: payment.PaymentResult{
				ProviderStatus: payment.ProviderStatusRequiresAction, Status: payment.StateProcessing,
				MethodDetails: payment.MethodDetails{Type: "card"},
			},
			wantCancelled: true, wantStatus: payment.StateFailed, wantReady: true,
		},
		{
			name: "failed at the provider", source: "spec/services/invoices/payments/cancel_abandoned_service_spec.rb:162",
			get:        payment.PaymentResult{ProviderStatus: "requires_payment_method", Status: payment.StateFailed},
			wantStatus: payment.StateFailed, wantReady: true,
		},
		{
			name: "succeeded meanwhile", source: "spec/services/invoices/payments/cancel_abandoned_service_spec.rb:180",
			get:        payment.PaymentResult{ProviderStatus: "succeeded", Status: payment.StateSucceeded},
			wantStatus: payment.StateProcessing,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source: " + tt.source)
			f := newProviderFixture(t, func(inv *invoice.Invoice) { inv.ReadyForPaymentProcessing = false })
			f.gw.get = tt.get
			f.gw.cancel = payment.PaymentResult{ProviderStatus: "canceled", Status: payment.StateFailed}
			p := f.h.StoredPayment(payment.InvoicePayable(f.inv.ID), f.cust.ID, 10000, payment.StateProcessing, f.integ.ID)
			id, st := "pi_3", payment.ProviderStatusRequiresAction
			p.ProviderPaymentID, p.ProviderStatus = &id, &st
			p.ProviderPaymentData = []byte(`{"type":"redirect_to_url"}`)
			p.UpdatedAt = f.h.Now().Add(-48 * time.Hour)
			f.h.Do(func(ctx context.Context, r port.Repos) error { return r.Payments.Update(ctx, p) })
			if err := f.svc.CancelAbandonedPayment(f.h.Ctx, f.h.OrgID(), kernel.PaymentArgs{PaymentID: p.ID}); err != nil {
				t.Fatal(err)
			}
			if got := len(f.gw.cancelled) == 1; got != tt.wantCancelled {
				t.Errorf("cancelled: got = %t, want = %t", got, tt.wantCancelled)
			}
			ps := f.payments()
			if len(ps) != 1 || ps[0].Status != tt.wantStatus {
				t.Fatalf("payment: got = %+v, want = %s", ps, tt.wantStatus)
			}
			if got := f.h.Invoice(f.inv.ID).ReadyForPaymentProcessing; got != tt.wantReady {
				t.Errorf("ready for payment processing: got = %t, want = %t", got, tt.wantReady)
			}
		})
	}
}

func TestCancelPayment(t *testing.T) {
	failed := payment.PaymentResult{ProviderStatus: "canceled", Status: payment.StateFailed}
	tests := []struct {
		name          string
		source        string
		state         payment.State
		manual        bool
		noProviderID  bool
		cancel        payment.PaymentResult
		cancelErr     error
		wantCancelled bool
		wantStatus    payment.State
		wantErr       bool
	}{
		{
			name: "no payment provider", source: "spec/services/payment_providers/cancel_payment_service_spec.rb:18",
			state: payment.StatePending, manual: true, wantStatus: payment.StatePending,
		},
		{
			name: "no provider payment", source: "spec/services/payment_providers/cancel_payment_service_spec.rb:38",
			state: payment.StatePending, noProviderID: true, wantStatus: payment.StatePending,
		},
		{
			name: "already succeeded", source: "spec/services/payment_providers/cancel_payment_service_spec.rb:57",
			state: payment.StateSucceeded, wantStatus: payment.StateSucceeded,
		},
		{
			name: "cancelled", source: "spec/services/payment_providers/stripe/payments/cancel_service_spec.rb:48",
			state: payment.StateProcessing, cancel: failed, wantCancelled: true, wantStatus: payment.StateFailed,
		},
		{
			// A zero result: the intent is no longer cancelable, or Adyen
			// confirms asynchronously.
			name: "not cancelable", source: "spec/services/payment_providers/stripe/payments/cancel_service_spec.rb:86",
			state: payment.StateProcessing, wantCancelled: true, wantStatus: payment.StateProcessing,
		},
		{
			name: "no cancellation at the provider", source: "spec/services/payment_providers/cancel_payment_service_spec.rb:141",
			state: payment.StatePending, cancelErr: payment.ErrUnsupported, wantCancelled: true, wantStatus: payment.StatePending,
		},
		{
			name: "provider failure", source: "spec/services/payment_providers/stripe/payments/cancel_service_spec.rb:115",
			state: payment.StatePending, cancelErr: &payment.ProviderError{Provider: "stripe", Kind: payment.KindRateLimited},
			wantCancelled: true, wantStatus: payment.StatePending, wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source: " + tt.source)
			f := newProviderFixture(t)
			f.gw.cancel, f.gw.cancelErr = tt.cancel, tt.cancelErr
			p := f.h.StoredPayment(payment.InvoicePayable(f.inv.ID), f.cust.ID, 10000, tt.state, f.integ.ID)
			if !tt.noProviderID {
				id := "pi_9"
				p.ProviderPaymentID = &id
			}
			if tt.manual {
				p.Kind, p.IntegrationID = payment.KindManual, core.IntegrationID{}
			}
			f.h.Do(func(ctx context.Context, r port.Repos) error { return r.Payments.Update(ctx, p) })
			err := f.svc.CancelPayment(f.h.Ctx, f.h.OrgID(), kernel.PaymentArgs{PaymentID: p.ID})
			if got := err != nil; got != tt.wantErr {
				t.Fatalf("err: got = %v, want error = %t", err, tt.wantErr)
			}
			if got := len(f.gw.cancelled) == 1; got != tt.wantCancelled {
				t.Errorf("cancelled: got = %t, want = %t", got, tt.wantCancelled)
			}
			if ps := f.payments(); len(ps) != 1 || ps[0].Status != tt.wantStatus {
				t.Fatalf("payment: got = %+v, want = %s", ps, tt.wantStatus)
			}
		})
	}
}

func TestUpdatePaymentReference(t *testing.T) {
	tests := []struct {
		name   string
		source string
		number string
		err    error
		want   []string
	}{
		{
			name: "numbered invoice", source: "spec/services/payment_providers/stripe/payments/update_reference_service_spec.rb:32",
			number: "INV-1", want: []string{"pi_7 INV-1"},
		},
		{
			name: "provider failure", source: "spec/services/payment_providers/stripe/payments/update_reference_service_spec.rb:94",
			number: "INV-1", err: &payment.ProviderError{Provider: "stripe", Kind: payment.KindInvalidRequest}, want: []string{"pi_7 INV-1"},
		},
		{
			name: "invoice without number", source: "spec/services/payment_providers/stripe/payments/update_reference_service_spec.rb:82",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source: " + tt.source)
			f := newProviderFixture(t, func(inv *invoice.Invoice) { inv.Number = tt.number })
			f.gw.referenceErr = tt.err
			p := f.h.StoredPayment(payment.InvoicePayable(f.inv.ID), f.cust.ID, 10000, payment.StateSucceeded, f.integ.ID)
			id := "pi_7"
			p.ProviderPaymentID = &id
			f.h.Do(func(ctx context.Context, r port.Repos) error { return r.Payments.Update(ctx, p) })
			if err := f.svc.UpdatePaymentReference(f.h.Ctx, f.h.OrgID(), kernel.InvoiceArgs{InvoiceID: f.inv.ID}); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(f.gw.references, tt.want) {
				t.Errorf("references: got = %v, want = %v", f.gw.references, tt.want)
			}
		})
	}
}
