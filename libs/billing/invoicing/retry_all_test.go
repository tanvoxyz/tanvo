package invoicing_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// batchArgs returns the args of the only job of the kind taken from the
// queue.
func (f *fixture) batchArgs(kind string) kernel.InvoiceIDsArgs {
	f.T.Helper()
	var out []kernel.InvoiceIDsArgs
	for _, j := range f.Store.TakeJobs() {
		if j.Kind != kind {
			continue
		}
		var a kernel.InvoiceIDsArgs
		if err := json.Unmarshal(j.Args, &a); err != nil {
			f.T.Fatalf("decode %s args: %v", kind, err)
		}
		out = append(out, a)
	}
	if len(out) != 1 {
		f.T.Fatalf("%s jobs got = %d, want = 1", kind, len(out))
	}
	return out[0]
}

// sameIDs reports whether got and want hold the same invoices.
func sameIDs(got, want []core.InvoiceID) bool {
	got, want = slices.Clone(got), slices.Clone(want)
	cmp := func(a, b core.InvoiceID) int { return strings.Compare(a.String(), b.String()) }
	slices.SortFunc(got, cmp)
	slices.SortFunc(want, cmp)
	return slices.Equal(got, want)
}

func TestRetryAll(t *testing.T) {
	// source: spec/services/invoices/retry_batch_service_spec.rb:12
	t.Run("enqueues", func(t *testing.T) {
		f := newFixture(t, billingDay)
		first := f.StoredInvoice(f.cust, withStatus(invoice.StatusFailed))
		second := f.StoredInvoice(f.cust, withStatus(invoice.StatusFailed))
		f.StoredInvoice(f.cust, withStatus(invoice.StatusDraft))
		f.Store.TakeJobs()
		n, err := f.svc.RetryAll(f.Ctx, f.OrgID())
		if err != nil || n != 2 {
			t.Fatalf("retry all got = %d, %v, want = 2, nil", n, err)
		}
		if a := f.batchArgs(kernel.JobRetryInvoices); !sameIDs(a.InvoiceIDs, []core.InvoiceID{first.ID, second.ID}) {
			t.Errorf("job invoices got = %v, want = %v", a.InvoiceIDs, []core.InvoiceID{first.ID, second.ID})
		}
	})
	t.Run("nothing to retry", func(t *testing.T) {
		f := newFixture(t, billingDay)
		f.StoredInvoice(f.cust, withStatus(invoice.StatusDraft))
		f.Store.TakeJobs()
		if n, err := f.svc.RetryAll(f.Ctx, f.OrgID()); err != nil || n != 0 {
			t.Fatalf("retry all got = %d, %v, want = 0, nil", n, err)
		}
		if n := f.enqueued(kernel.JobRetryInvoices); n != 0 {
			t.Errorf("retry jobs got = %d, want = 0", n)
		}
	})
	// source: spec/services/invoices/retry_batch_service_spec.rb:56
	t.Run("retries", func(t *testing.T) {
		f := newFixture(t, billingDay)
		first := f.StoredInvoice(f.cust, withStatus(invoice.StatusFailed))
		second := f.StoredInvoice(f.cust, withStatus(invoice.StatusFailed))
		n, err := f.svc.RetryInvoices(f.Ctx, f.OrgID(), []core.InvoiceID{first.ID, second.ID})
		if err != nil || n != 2 {
			t.Fatalf("retry invoices got = %d, %v, want = 2, nil", n, err)
		}
		for _, id := range []core.InvoiceID{first.ID, second.ID} {
			if got := f.Invoice(id); got.Status != invoice.StatusOpen || got.TaxStatus != invoice.TaxStatusPending {
				t.Errorf("invoice %s status, tax status = %s, %s, want = open, pending", id, got.Status, got.TaxStatus)
			}
		}
		if n := f.enqueued(kernel.JobPullProviderTaxes); n != 2 {
			t.Errorf("tax pulls got = %d, want = 2", n)
		}
	})
	// source: spec/services/invoices/retry_batch_service_spec.rb:63
	t.Run("stops at the first failure", func(t *testing.T) {
		f := newFixture(t, billingDay)
		draft := f.StoredInvoice(f.cust, withStatus(invoice.StatusDraft))
		failed := f.StoredInvoice(f.cust, withStatus(invoice.StatusFailed))
		n, err := f.svc.RetryInvoices(f.Ctx, f.OrgID(), []core.InvoiceID{draft.ID, failed.ID})
		wantRule(t, err, invoicing.CodeInvalidStatus)
		if n != 0 {
			t.Errorf("retried got = %d, want = 0", n)
		}
		if got := f.Invoice(failed.ID); got.Status != invoice.StatusFailed {
			t.Errorf("status after the failure got = %s, want = failed", got.Status)
		}
	})
}

func TestRetryAllPayments(t *testing.T) {
	payable := func(ps core.PaymentStatus, ready bool) func(*invoice.Invoice) {
		return func(inv *invoice.Invoice) {
			inv.Status, inv.PaymentStatus, inv.ReadyForPaymentProcessing, inv.TotalAmount = invoice.StatusFinalized, ps, ready, 1000
		}
	}
	// source: spec/services/invoices/payments/retry_batch_service_spec.rb:12
	t.Run("enqueues", func(t *testing.T) {
		f := newFixture(t, billingDay)
		failed := f.StoredInvoice(f.cust, payable(core.PaymentStatusFailed, true))
		pending := f.StoredInvoice(f.cust, payable(core.PaymentStatusPending, true))
		f.StoredInvoice(f.cust, payable(core.PaymentStatusFailed, false))
		f.StoredInvoice(f.cust, payable(core.PaymentStatusSucceeded, true))
		f.StoredInvoice(f.cust, payable(core.PaymentStatusPending, true), withStatus(invoice.StatusDraft))
		f.Store.TakeJobs()
		n, err := f.svc.RetryAllPayments(f.Ctx, f.OrgID())
		if err != nil || n != 2 {
			t.Fatalf("retry all payments got = %d, %v, want = 2, nil", n, err)
		}
		if a := f.batchArgs(kernel.JobRetryInvoicePayments); !sameIDs(a.InvoiceIDs, []core.InvoiceID{failed.ID, pending.ID}) {
			t.Errorf("job invoices got = %v, want = %v", a.InvoiceIDs, []core.InvoiceID{failed.ID, pending.ID})
		}
	})
	// source: spec/services/invoices/payments/retry_batch_service_spec.rb:54
	t.Run("retries", func(t *testing.T) {
		f := newFixture(t, billingDay)
		f.paymentProvider(f.cust, integration.ProviderStripe)
		first := f.StoredInvoice(f.cust, payable(core.PaymentStatusFailed, true))
		second := f.StoredInvoice(f.cust, payable(core.PaymentStatusFailed, true))
		f.Store.TakeJobs()
		n, err := f.svc.RetryInvoicePayments(f.Ctx, f.OrgID(), []core.InvoiceID{first.ID, second.ID})
		if err != nil || n != 2 {
			t.Fatalf("retry invoice payments got = %d, %v, want = 2, nil", n, err)
		}
		if n := f.enqueued(kernel.JobCreateInvoicePayment); n != 2 {
			t.Errorf("payment jobs got = %d, want = 2", n)
		}
	})
	// source: spec/services/invoices/payments/retry_batch_service_spec.rb:67
	t.Run("stops at the first failure", func(t *testing.T) {
		f := newFixture(t, billingDay)
		f.paymentProvider(f.cust, integration.ProviderStripe)
		first := f.StoredInvoice(f.cust, payable(core.PaymentStatusFailed, true))
		second := f.StoredInvoice(f.cust, payable(core.PaymentStatusFailed, false))
		n, err := f.svc.RetryInvoicePayments(f.Ctx, f.OrgID(), []core.InvoiceID{first.ID, second.ID})
		wantRule(t, err, invoicing.CodePaymentProcessing)
		if n != 1 {
			t.Errorf("retried got = %d, want = 1", n)
		}
	})
}

// source: spec/jobs/clock/retry_failed_invoices_job_spec.rb:12-88
func TestRetryTaxFailedInvoices(t *testing.T) {
	for _, tt := range []struct {
		name, source, details string
		want                  int
	}{
		{
			"api limit", "spec/jobs/clock/retry_failed_invoices_job_spec.rb:78",
			`{"tax_error":"validationError","tax_error_message":"You've exceeded your API limit of 10 per second"}`, 1,
		},
		{
			"invalid product", "spec/jobs/clock/retry_failed_invoices_job_spec.rb:65",
			`{"tax_error":"productExternalIdUnknown"}`, 0,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, billingDay)
			failed := f.StoredInvoice(f.cust, withStatus(invoice.StatusFailed))
			finalized := f.StoredInvoice(f.cust, withStatus(invoice.StatusFinalized))
			for _, inv := range []*invoice.Invoice{failed, finalized} {
				f.Do(func(ctx context.Context, r port.Repos) error {
					return r.ErrorDetails.Create(ctx, &invoice.ErrorDetail{
						ID: core.NewID[core.ErrorDetailID](f.IDs), OrganizationID: f.OrgID(), InvoiceID: inv.ID,
						Code: invoice.ErrorCodeTaxError, Details: json.RawMessage(tt.details), CreatedAt: f.Now(),
					})
				})
			}
			n, err := f.svc.RetryTaxFailedInvoices(f.Ctx, f.OrgID())
			if err != nil || n != tt.want {
				t.Fatalf("retried got = %d, %v, want = %d, nil", n, err, tt.want)
			}
			wantStatus := invoice.StatusFailed
			if tt.want == 1 {
				wantStatus = invoice.StatusOpen
			}
			if got := f.Invoice(failed.ID).Status; got != wantStatus {
				t.Errorf("failed invoice status got = %s, want = %s", got, wantStatus)
			}
			if got := f.Invoice(finalized.ID).Status; got != invoice.StatusFinalized {
				t.Errorf("finalized invoice status got = %s, want = finalized", got)
			}
		})
	}
}
