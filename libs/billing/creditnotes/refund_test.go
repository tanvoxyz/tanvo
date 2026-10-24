package creditnotes_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// refundFixture is a paid invoice with a credit note refunding 12 of it.
type refundFixture struct {
	*fixture
	integration core.IntegrationID
	cn          *creditnote.CreditNote
	pay         *payment.Payment
}

// newRefundFixture creates the invoice of newFixture paid in full, its
// payment (unless viaRequest, then a payment request's), and a finalized
// credit note refunding 12 (pending).
func newRefundFixture(t *testing.T, viaRequest bool, opts ...func(*creditnote.CreditNote)) *refundFixture {
	t.Helper()
	f := &refundFixture{fixture: newFixture(t)}
	h := f.h
	f.inv.TotalPaidAmount = f.inv.TotalAmount
	h.SaveInvoice(f.inv)
	f.integration = core.NewID[core.IntegrationID](h.IDs)
	payable := payment.InvoicePayable(f.inv.ID)
	if viaRequest {
		req := h.StoredPaymentRequest(f.cust.ID, core.PaymentStatusSucceeded, f.inv)
		payable = payment.RequestPayable(req.ID)
	}
	f.pay = h.StoredPayment(payable, f.cust.ID, f.inv.TotalAmount, payment.StateSucceeded, f.integration)
	f.cn = h.StoredCreditNote(f.inv, 0, append([]func(*creditnote.CreditNote){func(cn *creditnote.CreditNote) {
		cn.CreditStatus, cn.BalanceAmount = "", 0
		cn.RefundAmount, cn.TotalAmount, cn.RefundStatus = 12, 12, core.RefundStatusPending
	}}, opts...)...)
	return f
}

func TestRefund(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		request bool
		setup   func(f *refundFixture)
		opts    []func(*creditnote.CreditNote)
		check   func(t *testing.T, f *refundFixture, refund *payment.Refund, err error)
	}{
		{
			name: "refunds the invoice payment", source: "spec/services/credit_notes/refunds/stripe_service_spec.rb:52",
			check: func(t *testing.T, f *refundFixture, refund *payment.Refund, err error) {
				must(t, err)
				eq(t, "requests", len(f.h.Refunds.Requests), 1)
				req := f.h.Refunds.Requests[0]
				eq(t, "request payment", req.PaymentID, f.pay.ID)
				eq(t, "request amount", req.Amount, 12)
				// source: app/services/credit_notes/refunds/stripe_service.rb:105
				eq(t, "idempotency key", req.IdempotencyKey, f.cn.ID.String())
				stored := f.h.CreditNoteRefunds(f.cn.ID)
				eq(t, "refunds", len(stored), 1)
				eq(t, "refund", refund.ID, stored[0].ID)
				eq(t, "payment", refund.PaymentID, f.pay.ID)
				eq(t, "amount", refund.Amount, 12)
				eq(t, "status", refund.Status, core.RefundStatusPending)
				eq(t, "integration", refund.IntegrationID, f.integration)
				eq(t, "reason", refund.Reason, payment.RefundReasonCreditNote)
				if refund.ProviderRefundID == nil || *refund.ProviderRefundID != "re_fake" {
					t.Errorf("provider refund: got = %v, want = re_fake", refund.ProviderRefundID)
				}
				eq(t, "credit note status", f.h.ReloadCreditNote(f.cn.ID).RefundStatus, core.RefundStatusPending)
			},
		},
		{
			name: "a provider succeeding at once succeeds the note", source: "spec/services/credit_notes/refunds/stripe_service_spec.rb:52",
			setup: func(f *refundFixture) { f.h.Refunds.Status = core.RefundStatusSucceeded },
			check: func(t *testing.T, f *refundFixture, refund *payment.Refund, err error) {
				must(t, err)
				eq(t, "status", refund.Status, core.RefundStatusSucceeded)
				cn := f.h.ReloadCreditNote(f.cn.ID)
				eq(t, "credit note status", cn.RefundStatus, core.RefundStatusSucceeded)
				if cn.RefundedAt == nil {
					t.Error("refunded at: got = nil, want = set")
				}
			},
		},
		{
			name: "refunds the payment of a payment request", source: "spec/services/credit_notes/refunds/stripe_service_spec.rb:105",
			request: true,
			check: func(t *testing.T, f *refundFixture, refund *payment.Refund, err error) {
				must(t, err)
				eq(t, "payment", refund.PaymentID, f.pay.ID)
				eq(t, "refunds", len(f.h.CreditNoteRefunds(f.cn.ID)), 1)
			},
		},
		{
			name: "a provider rejection fails the refund with a webhook", source: "app/services/credit_notes/refunds/stripe_service.rb:46",
			setup: func(f *refundFixture) { f.h.Deps.Refunds = failingGateway{creditnotes.ErrRefundRejected} },
			check: func(t *testing.T, f *refundFixture, refund *payment.Refund, err error) {
				if !errors.Is(err, creditnotes.ErrRefund) || !errors.Is(err, creditnotes.ErrRefundRejected) {
					t.Fatalf("got = %v, want = %v wrapping %v", err, creditnotes.ErrRefund, creditnotes.ErrRefundRejected)
				}
				if refund != nil {
					t.Errorf("refund: got = %+v, want = nil", refund)
				}
				eq(t, "refunds", len(f.h.CreditNoteRefunds(f.cn.ID)), 0)
				eq(t, "credit note status", f.h.ReloadCreditNote(f.cn.ID).RefundStatus, core.RefundStatusFailed)
				if !slices.Contains(f.h.Webhooks(), webhook.EventTypeCreditNoteRefundFailure) {
					t.Errorf("webhooks: got = %v, want = including %v", f.h.Webhooks(), webhook.EventTypeCreditNoteRefundFailure)
				}
			},
		},
		{
			name: "a transient provider error leaves the refund pending", source: "app/jobs/credit_notes/refunds/stripe_create_job.rb:10",
			setup: func(f *refundFixture) { f.h.Deps.Refunds = failingGateway{billingtest.ErrFake} },
			check: func(t *testing.T, f *refundFixture, refund *payment.Refund, err error) {
				if !errors.Is(err, creditnotes.ErrRefund) || !errors.Is(err, billingtest.ErrFake) {
					t.Fatalf("got = %v, want = %v wrapping %v", err, creditnotes.ErrRefund, billingtest.ErrFake)
				}
				if refund != nil {
					t.Errorf("refund: got = %+v, want = nil", refund)
				}
				eq(t, "refunds", len(f.h.CreditNoteRefunds(f.cn.ID)), 0)
				eq(t, "credit note status", f.h.ReloadCreditNote(f.cn.ID).RefundStatus, core.RefundStatusPending)
				if slices.Contains(f.h.Webhooks(), webhook.EventTypeCreditNoteRefundFailure) {
					t.Errorf("webhooks: got = %v, want = without %v", f.h.Webhooks(), webhook.EventTypeCreditNoteRefundFailure)
				}
			},
		},
		{
			name: "a pending provider refund is not sent again", source: "app/services/credit_notes/refunds/stripe_service.rb:105",
			setup: func(f *refundFixture) {
				if _, err := f.svc.Refund(f.h.Ctx, f.h.OrgID(), f.cn.ID); err != nil {
					f.h.T.Fatalf("first refund: got = %v, want = nil", err)
				}
			},
			check: func(t *testing.T, f *refundFixture, refund *payment.Refund, err error) {
				must(t, err)
				if refund != nil {
					t.Errorf("refund: got = %+v, want = nil", refund)
				}
				eq(t, "requests", len(f.h.Refunds.Requests), 1)
				eq(t, "refunds", len(f.h.CreditNoteRefunds(f.cn.ID)), 1)
			},
		},
		{
			name: "a note without refund sends nothing", source: "spec/services/credit_notes/refunds/stripe_service_spec.rb:211",
			opts: []func(*creditnote.CreditNote){func(cn *creditnote.CreditNote) {
				cn.RefundAmount, cn.RefundStatus, cn.CreditAmount, cn.BalanceAmount = 0, "", 12, 12
				cn.CreditStatus = creditnote.CreditStatusAvailable
			}},
			check: noRefund,
		},
		{
			name: "an invoice without payment sends nothing", source: "spec/services/credit_notes/refunds/stripe_service_spec.rb:226",
			setup: func(f *refundFixture) {
				f.pay.Status = payment.StateFailed
				f.h.Do(updatePayment(f.pay))
			},
			check: noRefund,
		},
		{
			name: "a lost dispute sends nothing", source: "spec/services/credit_notes/refunds/stripe_service_spec.rb:265",
			setup: func(f *refundFixture) {
				lost := f.h.Now()
				f.inv.PaymentDisputeLostAt = &lost
				f.h.SaveInvoice(f.inv)
			},
			check: noRefund,
		},
		{
			name: "a refund no longer pending is not sent again", source: "tanvo: the refund job is idempotent",
			opts:  []func(*creditnote.CreditNote){func(cn *creditnote.CreditNote) { cn.RefundStatus = core.RefundStatusFailed }},
			check: noRefund,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			f := newRefundFixture(t, tt.request, tt.opts...)
			if tt.setup != nil {
				tt.setup(f)
			}
			refund, err := f.svc.Refund(f.h.Ctx, f.h.OrgID(), f.cn.ID)
			tt.check(t, f, refund, err)
		})
	}
}

// noRefund checks that nothing was sent or stored.
func noRefund(t *testing.T, f *refundFixture, refund *payment.Refund, err error) {
	t.Helper()
	must(t, err)
	if refund != nil {
		t.Errorf("refund: got = %+v, want = nil", refund)
	}
	eq(t, "requests", len(f.h.Refunds.Requests), 0)
	eq(t, "refunds", len(f.h.CreditNoteRefunds(f.cn.ID)), 0)
}

func TestUpdateRefundStatus(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		provider string
		status   core.RefundStatus
		prepare  func(f *refundFixture)
		check    func(t *testing.T, f *refundFixture, refund *payment.Refund, err error)
	}{
		{
			name: "updates the refund and the note", source: "spec/services/credit_notes/refunds/stripe_service_spec.rb:285",
			provider: "re_fake", status: core.RefundStatusSucceeded,
			check: func(t *testing.T, f *refundFixture, refund *payment.Refund, err error) {
				must(t, err)
				eq(t, "status", refund.Status, core.RefundStatusSucceeded)
				eq(t, "stored", f.h.CreditNoteRefunds(f.cn.ID)[0].Status, core.RefundStatusSucceeded)
				eq(t, "credit note status", f.h.ReloadCreditNote(f.cn.ID).RefundStatus, core.RefundStatusSucceeded)
			},
		},
		{
			name: "an unknown refund is not found", source: "spec/services/credit_notes/refunds/stripe_service_spec.rb:344",
			provider: "re_unknown", status: core.RefundStatusSucceeded,
			check: func(t *testing.T, _ *refundFixture, _ *payment.Refund, err error) {
				if !errors.Is(err, core.ErrNotFound) {
					t.Fatalf("got = %v, want = %v", err, core.ErrNotFound)
				}
			},
		},
		{
			name: "an invalid status fails", source: "spec/services/credit_notes/refunds/stripe_service_spec.rb:360",
			provider: "re_fake", status: "foo",
			check: func(t *testing.T, _ *refundFixture, _ *payment.Refund, err error) {
				wantFields(t, err, "status:"+core.CodeInvalid)
			},
		},
		{
			name: "a failure fails the note with a webhook", source: "spec/services/credit_notes/refunds/stripe_service_spec.rb:378",
			provider: "re_fake", status: core.RefundStatusFailed,
			check: func(t *testing.T, f *refundFixture, refund *payment.Refund, err error) {
				wantRule(t, err, creditnotes.CodeRefundFailed)
				eq(t, "status", refund.Status, core.RefundStatusFailed)
				eq(t, "stored", f.h.CreditNoteRefunds(f.cn.ID)[0].Status, core.RefundStatusFailed)
				eq(t, "credit note status", f.h.ReloadCreditNote(f.cn.ID).RefundStatus, core.RefundStatusFailed)
				if !slices.Contains(f.h.Webhooks(), webhook.EventTypeCreditNoteRefundFailure) {
					t.Errorf("webhooks: got = %v, want = including %v", f.h.Webhooks(), webhook.EventTypeCreditNoteRefundFailure)
				}
			},
		},
		{
			name: "a failed refund may still succeed", source: "app/services/credit_notes/refunds/stripe_service.rb:63",
			provider: "re_fake", status: core.RefundStatusSucceeded,
			prepare: func(f *refundFixture) {
				_, err := f.svc.UpdateRefundStatus(f.h.Ctx, f.h.OrgID(), f.integration, "re_fake", core.RefundStatusFailed)
				if !errors.As(err, new(*core.RuleError)) {
					f.h.T.Fatalf("first update: got = %v, want = a rule error", err)
				}
			},
			check: func(t *testing.T, f *refundFixture, refund *payment.Refund, err error) {
				must(t, err)
				eq(t, "status", refund.Status, core.RefundStatusSucceeded)
				cn := f.h.ReloadCreditNote(f.cn.ID)
				eq(t, "credit note status", cn.RefundStatus, core.RefundStatusSucceeded)
				if cn.RefundedAt == nil {
					t.Error("refunded at: got = nil, want = set")
				}
			},
		},
		{
			name: "a note already refunded is left unchanged", source: "tanvo: provider webhooks may repeat",
			provider: "re_fake", status: core.RefundStatusFailed,
			prepare: func(f *refundFixture) {
				if _, err := f.svc.UpdateRefundStatus(f.h.Ctx, f.h.OrgID(), f.integration, "re_fake", core.RefundStatusSucceeded); err != nil {
					f.h.T.Fatalf("first update: got = %v, want = nil", err)
				}
			},
			check: func(t *testing.T, f *refundFixture, refund *payment.Refund, err error) {
				must(t, err)
				eq(t, "status", refund.Status, core.RefundStatusSucceeded)
				eq(t, "credit note status", f.h.ReloadCreditNote(f.cn.ID).RefundStatus, core.RefundStatusSucceeded)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			f := newRefundFixture(t, false)
			if _, err := f.svc.Refund(f.h.Ctx, f.h.OrgID(), f.cn.ID); err != nil {
				t.Fatalf("refund: got = %v, want = nil", err)
			}
			if tt.prepare != nil {
				tt.prepare(f)
			}
			refund, err := f.svc.UpdateRefundStatus(f.h.Ctx, f.h.OrgID(), f.integration, tt.provider, tt.status)
			tt.check(t, f, refund, err)
		})
	}
}

func TestReportTaxes(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		provider bool
		unknown  bool
		refusal  bool
		check    func(t *testing.T, f *fixture, cn *creditnote.CreditNote, err error)
	}{
		{
			name: "an unknown credit note is not found", source: "spec/services/credit_notes/provider_taxes/report_service_spec.rb:102",
			provider: true, unknown: true,
			check: func(t *testing.T, _ *fixture, _ *creditnote.CreditNote, err error) {
				if !errors.Is(err, core.ErrNotFound) {
					t.Fatalf("got = %v, want = %v", err, core.ErrNotFound)
				}
			},
		},
		{
			name: "reports to the customer's provider", source: "spec/services/credit_notes/provider_taxes/report_service_spec.rb:111 spec/jobs/credit_notes/provider_taxes/report_job_spec.rb:24",
			provider: true,
			check: func(t *testing.T, f *fixture, cn *creditnote.CreditNote, err error) {
				must(t, err)
				if !slices.Equal(f.h.Taxes.Reported, []core.CreditNoteID{cn.ID}) {
					t.Errorf("reported: got = %v, want = %v", f.h.Taxes.Reported, []core.CreditNoteID{cn.ID})
				}
			},
		},
		{
			name: "previous tax errors are discarded", source: "spec/services/credit_notes/provider_taxes/report_service_spec.rb:120",
			provider: true,
			check: func(t *testing.T, f *fixture, cn *creditnote.CreditNote, err error) {
				must(t, err)
				eq(t, "errors", len(creditNoteErrors(t, f, cn.ID)), 0)
			},
		},
		{
			name: "a refusal is kept as a tax error", source: "spec/services/credit_notes/provider_taxes/report_service_spec.rb:141",
			provider: true, refusal: true,
			check: func(t *testing.T, f *fixture, cn *creditnote.CreditNote, err error) {
				must(t, err)
				got := creditNoteErrors(t, f, cn.ID)
				if len(got) != 1 || got[0].Code != creditnote.ErrorCodeTaxError || string(got[0].Details) != `{"tax_error":"validationError"}` {
					t.Errorf("errors: got = %+v, want = one tax_error validationError", got)
				}
				if _, err := f.svc.RetryTaxReporting(f.h.Ctx, f.h.OrgID(), cn.ID); fieldCodeOf(err, "tax_error") != "validationError" {
					t.Errorf("retry: got = %v, want = tax_error validationError", err)
				}
				t.Log("source: spec/services/credit_notes/provider_taxes/report_service_spec.rb:132")
				f.h.Taxes.ReportErr = nil
				got2, err := f.svc.RetryTaxReporting(f.h.Ctx, f.h.OrgID(), cn.ID)
				must(t, err)
				eq(t, "errors after a retry", len(got2.ErrorDetails), 0)
			},
		},
		{
			name: "a customer without provider is skipped", source: "spec/jobs/credit_notes/provider_taxes/report_job_spec.rb:32",
			check: func(t *testing.T, f *fixture, _ *creditnote.CreditNote, err error) {
				must(t, err)
				eq(t, "reported", len(f.h.Taxes.Reported), 0)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			f := newFixture(t)
			if tt.provider {
				f.h.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
			}
			cn := f.h.StoredCreditNote(f.inv, 5)
			f.h.Do(func(ctx context.Context, r port.Repos) error {
				return r.CreditNoteErrorDetails.Create(ctx, &creditnote.ErrorDetail{
					ID: core.NewID[core.ErrorDetailID](f.h.IDs), OrganizationID: f.h.OrgID(), CreditNoteID: cn.ID,
					Code: creditnote.ErrorCodeTaxError, Details: []byte(`{"tax_error":"old"}`), CreatedAt: f.h.Now(),
				})
			})
			if tt.refusal {
				f.h.Taxes.ReportErr = &kernel.TaxProviderError{Code: "validationError", Message: "bad address"}
			}
			id := cn.ID
			if tt.unknown {
				id = core.NewID[core.CreditNoteID](f.h.IDs)
			}
			err := f.svc.ReportTaxes(f.h.Ctx, f.h.OrgID(), id)
			tt.check(t, f, cn, err)
		})
	}
}

// creditNoteErrors returns the error details of the credit note id.
func creditNoteErrors(t *testing.T, f *fixture, id core.CreditNoteID) []creditnote.ErrorDetail {
	t.Helper()
	cn, err := f.svc.Get(f.h.Ctx, f.h.OrgID(), id)
	must(t, err)
	return cn.ErrorDetails
}

// fieldCodeOf returns the code of field in the validation error err.
func fieldCodeOf(err error, field string) string {
	var ve *core.ValidationError
	if !errors.As(err, &ve) {
		return ""
	}
	for _, fe := range ve.Errors {
		if fe.Field == field {
			return fe.Code
		}
	}
	return ""
}

// failingGateway is a [kernel.RefundGateway] failing with err.
type failingGateway struct{ err error }

func (g failingGateway) Refund(context.Context, kernel.RefundRequest) (kernel.RefundResult, error) {
	return kernel.RefundResult{}, fmt.Errorf("provider: %w", g.err)
}

// updatePayment stores the changes of a payment.
func updatePayment(p *payment.Payment) func(ctx context.Context, r port.Repos) error {
	return func(ctx context.Context, r port.Repos) error { return r.Payments.Update(ctx, p) }
}
