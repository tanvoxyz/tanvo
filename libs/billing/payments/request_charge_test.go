package payments_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// chargeRequestFixture is a provider fixture with a payment request of two
// finalized invoices of 2.00 and 5.99.
type chargeRequestFixture struct {
	*providerFixture
	invs []*invoice.Invoice
	req  *payment.Request
}

func newChargeRequestFixture(t *testing.T, totals ...int64) *chargeRequestFixture {
	t.Helper()
	f := &chargeRequestFixture{providerFixture: newProviderFixture(t)}
	if len(totals) == 0 {
		totals = []int64{200, 599}
	}
	for _, total := range totals {
		f.invs = append(f.invs, f.h.StoredInvoice(f.cust, finalized(total, 0)))
	}
	f.req = f.h.StoredPaymentRequest(f.cust.ID, core.PaymentStatusPending, f.invs...)
	f.gw.charge = payment.PaymentResult{ProviderPaymentID: "pi_1", ProviderStatus: "succeeded", Status: payment.StateSucceeded}
	return f
}

func (f *chargeRequestFixture) charge(args kernel.PaymentRequestArgs) error {
	args.PaymentRequestID = f.req.ID
	return f.svc.CreateRequestPayment(f.h.Ctx, f.h.OrgID(), args)
}

func (f *chargeRequestFixture) request() *payment.Request {
	return billingtest.Read(f.h, func(ctx context.Context, r port.Repos) (*payment.Request, error) {
		return r.PaymentRequests.Get(ctx, f.h.OrgID(), f.req.ID)
	})
}

func (f *chargeRequestFixture) requestPayments() []*payment.Payment {
	return billingtest.Read(f.h, func(ctx context.Context, r port.Repos) ([]*payment.Payment, error) {
		return r.Payments.ListByPayable(ctx, f.h.OrgID(), payment.RequestPayable(f.req.ID))
	})
}

func (f *chargeRequestFixture) emailed() bool {
	return slices.Contains(f.h.JobKinds(), kernel.JobSendPaymentRequestEmail)
}

// TestCreateRequestPayment ports the charge outcomes of
// PaymentRequests::Payments::CreateService.
func TestCreateRequestPayment(t *testing.T) {
	tests := []struct {
		name        string
		result      payment.PaymentResult
		err         error
		wantErr     bool
		wantRequest core.PaymentStatus
		wantReady   bool
		wantPayment payment.State
		wantEmail   bool
		wantFailure bool
	}{
		{
			// source: spec/services/payment_requests/payments/create_service_spec.rb:213, 235, 242, 249, 135
			name:        "succeeded",
			result:      payment.PaymentResult{ProviderPaymentID: "pi_1", ProviderStatus: "succeeded", Status: payment.StateSucceeded},
			wantRequest: core.PaymentStatusSucceeded, wantPayment: payment.StateSucceeded,
		},
		{
			// source: spec/services/payment_requests/payments/create_service_spec.rb:261
			name:        "failed",
			result:      payment.PaymentResult{ProviderPaymentID: "pi_1", ProviderStatus: "canceled", Status: payment.StateFailed},
			wantRequest: core.PaymentStatusFailed, wantReady: true, wantPayment: payment.StateFailed, wantEmail: true,
		},
		{
			// source: spec/services/payment_requests/payments/create_service_spec.rb:581
			name:        "processing",
			result:      payment.PaymentResult{ProviderPaymentID: "pi_1", ProviderStatus: "processing", Status: payment.StateProcessing},
			wantRequest: core.PaymentStatusPending, wantPayment: payment.StateProcessing,
		},
		{
			// source: spec/services/payment_requests/payments/create_service_spec.rb:514, 541
			// source: spec/services/payment_requests/payments/deliver_error_webhook_service_spec.rb:20
			name:        "provider failure",
			err:         &payment.ProviderError{Provider: "stripe", Kind: payment.KindPermanent, Code: "code", Message: "error"},
			wantErr:     true,
			wantRequest: core.PaymentStatusFailed, wantReady: true, wantPayment: payment.StateFailed, wantEmail: true, wantFailure: true,
		},
		{
			// source: spec/services/payment_requests/payments/create_service_spec.rb:558
			name:        "amount too small",
			err:         &payment.ProviderError{Provider: "stripe", Kind: payment.KindAmountTooSmall, Code: "amount_too_small", Message: "stripe_error"},
			wantRequest: core.PaymentStatusPending, wantPayment: payment.StatePending, wantEmail: true, wantFailure: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newChargeRequestFixture(t)
			f.gw.charge, f.gw.chargeErr = tt.result, tt.err
			err := f.charge(kernel.PaymentRequestArgs{})
			if got := err != nil; got != tt.wantErr {
				t.Fatalf("error: got = %v, want error %t", err, tt.wantErr)
			}
			if len(f.gw.charges) != 1 {
				t.Fatalf("charges: got = %d, want = 1", len(f.gw.charges))
			}
			ch := f.gw.charges[0]
			if ch.Amount != 799 || ch.Payable != payment.RequestPayable(f.req.ID) || ch.ProviderCustomerID != "cus_1" ||
				ch.Description != f.h.BillingEntity.Name+" - Overdue invoices" {
				t.Errorf("charge: got = %d %s %s %q, want = 799 of the request for cus_1, overdue invoices", ch.Amount, ch.Payable, ch.ProviderCustomerID, ch.Description)
			}
			q := f.request()
			if q.PaymentStatus != tt.wantRequest || q.PaymentAttempts != 1 || q.ReadyForPaymentProcessing != tt.wantReady {
				t.Errorf("request: got = %s attempts %d ready %t, want = %s attempts 1 ready %t",
					q.PaymentStatus, q.PaymentAttempts, q.ReadyForPaymentProcessing, tt.wantRequest, tt.wantReady)
			}
			ps := f.requestPayments()
			if len(ps) != 1 {
				t.Fatalf("payments: got = %d, want = 1", len(ps))
			}
			if p := ps[0]; p.Status != tt.wantPayment || p.Amount != 799 || p.Currency != q.Currency || p.IntegrationID != f.integ.ID {
				t.Errorf("payment: got = %s %d %s %s, want = %s 799 %s %s", p.Status, p.Amount, p.Currency, p.IntegrationID, tt.wantPayment, q.Currency, f.integ.ID)
			}
			for _, inv := range f.invs {
				got := f.h.Invoice(inv.ID)
				switch {
				case tt.wantRequest == core.PaymentStatusSucceeded:
					if got.PaymentStatus != core.PaymentStatusSucceeded || got.TotalPaidAmount != inv.TotalAmount {
						t.Errorf("invoice %s: got = %s paid %d, want = succeeded paid %d", inv.Number, got.PaymentStatus, got.TotalPaidAmount, inv.TotalAmount)
					}
				case tt.err != nil:
					// A failure is recorded on the request only.
					if got.PaymentStatus != core.PaymentStatusPending {
						t.Errorf("invoice %s: got = %s, want = pending", inv.Number, got.PaymentStatus)
					}
				default:
					if got.PaymentStatus != tt.wantRequest {
						t.Errorf("invoice %s: got = %s, want = %s", inv.Number, got.PaymentStatus, tt.wantRequest)
					}
				}
			}
			if tt.wantRequest == core.PaymentStatusSucceeded && receiptOf(f.h, ps[0].ID) == nil {
				t.Errorf("receipt: got = nil, want = one")
			}
			if got := f.emailed(); got != tt.wantEmail {
				t.Errorf("payment request email: got = %t, want = %t", got, tt.wantEmail)
			}
			var failures []json.RawMessage
			for _, ev := range f.h.Emitted.Events() {
				if ev.Type == webhook.EventTypePaymentRequestPaymentFailure {
					failures = append(failures, ev.Payload)
				}
			}
			if got := len(failures) == 1; got != tt.wantFailure {
				t.Fatalf("payment_request.payment_failure: got = %d, want = %t", len(failures), tt.wantFailure)
			}
			if tt.wantFailure {
				var payload struct {
					ProviderCustomerID string `json:"provider_customer_id"`
					ProviderError      struct {
						Message   string `json:"message"`
						ErrorCode string `json:"error_code"`
					} `json:"provider_error"`
				}
				if err := json.Unmarshal(failures[0], &payload); err != nil {
					t.Fatalf("payload: %v", err)
				}
				var pe *payment.ProviderError
				errors.As(tt.err, &pe)
				if payload.ProviderCustomerID != "cus_1" || payload.ProviderError.Message != pe.Message || payload.ProviderError.ErrorCode != pe.Code {
					t.Errorf("payload: got = %+v, want = cus_1 %q %q", payload, pe.Message, pe.Code)
				}
			}
		})
	}
}

// TestCreateRequestPaymentAlreadyPaidInvoices ports
// create_service_spec.rb:273: a failed request leaves its invoices paid
// through another path as they are.
func TestCreateRequestPaymentAlreadyPaidInvoices(t *testing.T) {
	// source: spec/services/payment_requests/payments/create_service_spec.rb:273
	f := newChargeRequestFixture(t)
	for _, inv := range f.invs {
		inv.PaymentStatus = core.PaymentStatusSucceeded
		f.h.SaveInvoice(inv)
	}
	f.gw.charge = payment.PaymentResult{ProviderPaymentID: "pi_1", ProviderStatus: "canceled", Status: payment.StateFailed}
	if err := f.charge(kernel.PaymentRequestArgs{}); err != nil {
		t.Fatalf("CreateRequestPayment: %v", err)
	}
	for _, inv := range f.invs {
		if got := f.h.Invoice(inv.ID).PaymentStatus; got != core.PaymentStatusSucceeded {
			t.Errorf("invoice %s: got = %s, want = succeeded", inv.Number, got)
		}
	}
}

// TestCreateRequestPaymentChoosesMethod ports the payment method choice of
// create_service.rb determine_payment_method.
func TestCreateRequestPaymentChoosesMethod(t *testing.T) {
	tests := []struct {
		name string
		args func(f *chargeRequestFixture, chosen *payment.Method) kernel.PaymentRequestArgs
		want func(def, chosen *payment.Method) core.PaymentMethodID
	}{
		{
			// source: spec/services/payment_requests/payments/create_service_spec.rb:287
			name: "manual",
			args: func(*chargeRequestFixture, *payment.Method) kernel.PaymentRequestArgs {
				return kernel.PaymentRequestArgs{PaymentMethodType: core.PaymentMethodTypeManual}
			},
			want: func(_, _ *payment.Method) core.PaymentMethodID { return core.PaymentMethodID{} },
		},
		{
			// source: spec/services/payment_requests/payments/create_service_spec.rb:299
			name: "chosen method",
			args: func(_ *chargeRequestFixture, chosen *payment.Method) kernel.PaymentRequestArgs {
				return kernel.PaymentRequestArgs{PaymentMethodID: chosen.ID}
			},
			want: func(_, chosen *payment.Method) core.PaymentMethodID { return chosen.ID },
		},
		{
			// source: spec/services/payment_requests/payments/create_service_spec.rb:308
			name: "default method",
			args: func(*chargeRequestFixture, *payment.Method) kernel.PaymentRequestArgs {
				return kernel.PaymentRequestArgs{}
			},
			want: func(def, _ *payment.Method) core.PaymentMethodID { return def.ID },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newChargeRequestFixture(t)
			def := defaultMethodOf(f.providerFixture)
			chosen := storedMethod(f.h, f.cust, false)
			if err := f.charge(tt.args(f, chosen)); err != nil {
				t.Fatalf("CreateRequestPayment: %v", err)
			}
			ps := f.requestPayments()
			if len(ps) != 1 {
				t.Fatalf("payments: got = %d, want = 1", len(ps))
			}
			if want := tt.want(def, chosen); ps[0].PaymentMethodID != want {
				t.Errorf("payment method: got = %s, want = %s", ps[0].PaymentMethodID, want)
			}
		})
	}
}

// defaultMethodOf returns the customer's default payment method.
func defaultMethodOf(f *providerFixture) *payment.Method {
	ms := billingtest.Read(f.h, func(ctx context.Context, r port.Repos) ([]*payment.Method, error) {
		return r.PaymentMethods.ListByCustomer(ctx, f.h.OrgID(), f.cust.ID)
	})
	for _, m := range ms {
		if m.IsDefault {
			return m
		}
	}
	f.h.T.Fatal("no default payment method")
	return nil
}

// TestCreateRequestPaymentReference ports create_service_spec.rb:321: a
// request of one invoice names it in the payment's reference.
func TestCreateRequestPaymentReference(t *testing.T) {
	// source: spec/services/payment_requests/payments/create_service_spec.rb:321
	f := newChargeRequestFixture(t, 200)
	if err := f.charge(kernel.PaymentRequestArgs{}); err != nil {
		t.Fatalf("CreateRequestPayment: %v", err)
	}
	want := f.h.BillingEntity.Name + " - Overdue invoices: " + f.invs[0].Number
	if got := f.gw.charges[0].Description; got != want {
		t.Errorf("reference: got = %q, want = %q", got, want)
	}
}

// TestCreateRequestPaymentSkips ports the requests
// PaymentRequests::Payments::CreateService does not charge.
func TestCreateRequestPaymentSkips(t *testing.T) {
	tests := []struct {
		name         string
		setup        func(f *chargeRequestFixture)
		wantStatus   core.PaymentStatus
		wantPayments int
	}{
		{
			// source: spec/services/payment_requests/payments/create_service_spec.rb:350
			name: "request already paid",
			setup: func(f *chargeRequestFixture) {
				f.req.PaymentStatus = core.PaymentStatusSucceeded
				f.h.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentRequests.Update(ctx, f.req) })
			},
			wantStatus: core.PaymentStatusSucceeded,
		},
		{
			// source: spec/services/payment_requests/payments/create_service_spec.rb:400
			name: "zero amount",
			setup: func(f *chargeRequestFixture) {
				f.req.Amount = 0
				f.h.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentRequests.Update(ctx, f.req) })
			},
			wantStatus: core.PaymentStatusSucceeded,
		},
		{
			// source: spec/services/payment_requests/payments/create_service_spec.rb:414
			name: "provider customer without an identifier",
			setup: func(f *chargeRequestFixture) {
				f.h.Do(func(ctx context.Context, r port.Repos) error {
					pcs, err := r.PaymentProviderCustomers.ListByCustomer(ctx, f.h.OrgID(), f.cust.ID)
					if err != nil {
						return err
					}
					pcs[0].ProviderCustomerID = nil
					return r.PaymentProviderCustomers.Update(ctx, pcs[0])
				})
			},
			wantStatus: core.PaymentStatusPending,
		},
		{
			// source: spec/services/payment_requests/payments/create_service_spec.rb:437
			name: "invoice not ready for payment processing",
			setup: func(f *chargeRequestFixture) {
				f.invs[0].ReadyForPaymentProcessing = false
				f.h.SaveInvoice(f.invs[0])
			},
			wantStatus: core.PaymentStatusPending,
		},
		{
			// source: spec/services/payment_requests/payments/create_service_spec.rb:642
			name: "payment already processing",
			setup: func(f *chargeRequestFixture) {
				p := f.h.StoredPayment(payment.RequestPayable(f.req.ID), f.cust.ID, 799, payment.StateProcessing, f.integ.ID)
				pid := "pi_0"
				p.ProviderPaymentID = &pid
				f.h.Do(func(ctx context.Context, r port.Repos) error { return r.Payments.Update(ctx, p) })
			},
			wantStatus: core.PaymentStatusPending, wantPayments: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newChargeRequestFixture(t)
			tt.setup(f)
			if err := f.charge(kernel.PaymentRequestArgs{}); err != nil {
				t.Fatalf("CreateRequestPayment: %v", err)
			}
			if len(f.gw.charges) != 0 {
				t.Errorf("charges: got = %d, want = 0", len(f.gw.charges))
			}
			q := f.request()
			if q.PaymentStatus != tt.wantStatus || q.PaymentAttempts != 0 {
				t.Errorf("request: got = %s attempts %d, want = %s attempts 0", q.PaymentStatus, q.PaymentAttempts, tt.wantStatus)
			}
			if got := len(f.requestPayments()); got != tt.wantPayments {
				t.Errorf("payments: got = %d, want = %d", got, tt.wantPayments)
			}
		})
	}
}

// TestCreateRequestPaymentReusesPending ports create_service_spec.rb:623:
// the request's pending payment is the one charged.
func TestCreateRequestPaymentReusesPending(t *testing.T) {
	// source: spec/services/payment_requests/payments/create_service_spec.rb:623
	f := newChargeRequestFixture(t)
	p := f.h.StoredPayment(payment.RequestPayable(f.req.ID), f.cust.ID, 799, payment.StatePending, f.integ.ID)
	if err := f.charge(kernel.PaymentRequestArgs{}); err != nil {
		t.Fatalf("CreateRequestPayment: %v", err)
	}
	ps := f.requestPayments()
	if len(ps) != 1 || ps[0].ID != p.ID {
		t.Fatalf("payments: got = %d, want = the pending one", len(ps))
	}
	if len(f.gw.charges) != 1 || f.gw.charges[0].PaymentID != p.ID {
		t.Errorf("charges: got = %+v, want = one of %s", f.gw.charges, p.ID)
	}
}

// TestCreateRequestPaymentWithoutConnection checks that a customer whose
// connection was removed after the job was enqueued is not charged.
func TestCreateRequestPaymentWithoutConnection(t *testing.T) {
	// source: spec/services/payment_requests/payments/create_service_spec.rb:366
	h, svc := newHarness(t)
	set, err := port.NewPaymentProviderSet(&fakeGateway{})
	if err != nil {
		t.Fatal(err)
	}
	svc = svc.WithProviders(payments.Providers{Gateways: set})
	c := h.Customer()
	inv := h.StoredInvoice(c, finalized(200, 0))
	q := h.StoredPaymentRequest(c.ID, core.PaymentStatusPending, inv)
	if err := svc.CreateRequestPayment(h.Ctx, h.OrgID(), kernel.PaymentRequestArgs{PaymentRequestID: q.ID}); err != nil {
		t.Fatalf("create request payment: got = %v, want = nil", err)
	}
	ps := billingtest.Read(h, func(ctx context.Context, r port.Repos) ([]*payment.Payment, error) {
		return r.Payments.ListByPayable(ctx, h.OrgID(), payment.RequestPayable(q.ID))
	})
	if len(ps) != 0 {
		t.Errorf("payments: got = %d, want = 0", len(ps))
	}
}
