package invoicing_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// failingTx fails the n-th transaction (from 1) with err, without running
// it.
type failingTx struct {
	inner port.Transactor
	n     int
	err   error
}

func (t *failingTx) InTx(ctx context.Context, fn func(ctx context.Context, r port.Repos) error) error {
	if t.n--; t.n == 0 {
		return t.err
	}
	return t.inner.InTx(ctx, fn)
}

// billJobs takes the jobs of the fixture's queue and returns the bill
// jobs among them.
func billJobs(t *testing.T, f *fixture) []port.Job {
	t.Helper()
	var out []port.Job
	for _, j := range f.Store.TakeJobs() {
		if j.Kind == kernel.JobBillSubscription {
			out = append(out, j)
		}
	}
	return out
}

func billArgs(t *testing.T, j port.Job) kernel.BillSubscriptionArgs {
	t.Helper()
	var a kernel.BillSubscriptionArgs
	if err := json.Unmarshal(j.Args, &a); err != nil {
		t.Fatalf("decode %s: %v", j.Kind, err)
	}
	return a
}

// generationErrors returns the invoice_generation_error details of id.
func generationErrors(f *fixture, id core.InvoiceID) []*invoice.ErrorDetail {
	f.T.Helper()
	details := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*invoice.ErrorDetail, error) {
		return r.ErrorDetails.ListByInvoice(ctx, f.OrgID(), id)
	})
	var out []*invoice.ErrorDetail
	for _, d := range details {
		if d.Code == invoice.ErrorCodeInvoiceGenerationError {
			out = append(out, d)
		}
	}
	return out
}

// A failed generation keeps the reserved invoice generating and is
// retried, 5 minutes later for a periodic bill; a failed retry records an
// invoice_generation_error; a transient error is returned for the job's
// retry policy and the rerun reuses the reservation.
//
// source: app/jobs/bill_subscription_job.rb:16-60,
// spec/jobs/bill_subscription_job_spec.rb:26-100,175-185
func TestBillSubscriptionFailure(t *testing.T) {
	errBoom := errors.New("boom")
	tests := []struct {
		name      string
		err       error
		reason    invoice.InvoicingReason
		wantErr   bool
		wantRetry time.Duration // zero: no retry job
	}{
		{name: "periodic", err: errBoom, reason: invoice.InvoicingReasonSubscriptionPeriodic, wantRetry: 5 * time.Minute},
		{name: "other reason", err: errBoom, reason: invoice.InvoicingReasonSubscriptionStarting, wantRetry: 3 * time.Second},
		{name: "transient", err: fmt.Errorf("lock: %w", core.ErrConflict), reason: invoice.InvoicingReasonSubscriptionPeriodic, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, billingDay)
			sub := f.subscribe()
			d := *f.Deps
			// The first transaction reserves the invoice, the second
			// generates it.
			d.Tx = &failingTx{inner: f.Deps.Tx, n: 2, err: tc.err}
			svc := invoicing.New(&d)
			args := kernel.BillSubscriptionArgs{SubscriptionIDs: []core.SubscriptionID{sub.ID}, BillingAt: billingDay, Reason: string(tc.reason)}
			_, err := svc.BillSubscription(f.Ctx, f.OrgID(), args)
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Fatalf("BillSubscription() error got = %v, want error = %v", err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, core.ErrConflict) {
				t.Errorf("BillSubscription() error got = %v, want = %v", err, core.ErrConflict)
			}
			jobs := billJobs(t, f)
			if tc.wantRetry == 0 {
				if len(jobs) != 0 {
					t.Fatalf("retry jobs got = %d, want = 0", len(jobs))
				}
				// The job's rerun generates the reserved invoice.
				invs, err := f.svc.BillSubscription(f.Ctx, f.OrgID(), args)
				if err != nil || len(invs) != 1 {
					t.Fatalf("rerun got = %d invoices, %v, want = 1, nil", len(invs), err)
				}
				if all := f.Invoices(f.cust.ID); len(all) != 1 || all[0].ID != invs[0].ID {
					t.Errorf("invoices got = %d, want = the reserved one only", len(all))
				}
				return
			}
			if len(jobs) != 1 {
				t.Fatalf("retry jobs got = %d, want = 1", len(jobs))
			}
			retry := billArgs(t, jobs[0])
			if retry.InvoiceID.IsZero() || retry.Reason != string(tc.reason) || len(retry.SubscriptionIDs) != 1 || retry.SubscriptionIDs[0] != sub.ID {
				t.Errorf("retry args got = %+v, want = the reserved invoice of %s", retry, sub.ID)
			}
			if want := f.Now().Add(tc.wantRetry); !jobs[0].RunAt.Equal(want) {
				t.Errorf("retry run at got = %v, want = %v", jobs[0].RunAt, want)
			}
			if want := invoicing.RetryGenerationKey(retry.InvoiceID); jobs[0].UniqueKey != want {
				t.Errorf("retry key got = %q, want = %q", jobs[0].UniqueKey, want)
			}
			if inv := f.Invoice(retry.InvoiceID); inv.Status != invoice.StatusGenerating {
				t.Errorf("reserved invoice status got = %s, want = %s", inv.Status, invoice.StatusGenerating)
			}

			// A failed retry records the error and is returned.
			d.Tx = &failingTx{inner: f.Deps.Tx, n: 1, err: errBoom}
			if _, err := invoicing.New(&d).BillSubscription(f.Ctx, f.OrgID(), retry); !errors.Is(err, errBoom) {
				t.Errorf("failed retry error got = %v, want = %v", err, errBoom)
			}
			if got := generationErrors(f, retry.InvoiceID); len(got) != 1 {
				t.Errorf("generation errors got = %d, want = 1", len(got))
			}
			if jobs := billJobs(t, f); len(jobs) != 0 {
				t.Errorf("jobs after a failed retry got = %d, want = 0", len(jobs))
			}

			// A successful retry generates the reserved invoice.
			invs, err := f.svc.BillSubscription(f.Ctx, f.OrgID(), retry)
			if err != nil || len(invs) != 1 || invs[0].ID != retry.InvoiceID || invs[0].Status != invoice.StatusFinalized {
				t.Errorf("retry got = %+v, %v, want = the reserved invoice finalized", invs, err)
			}
		})
	}
}

// source: spec/jobs/clock/retry_generating_subscription_invoices_job_spec.rb
func TestRetryGeneratingInvoices(t *testing.T) {
	tests := []struct {
		name      string
		age       time.Duration
		withError bool
		want      int
	}{
		{name: "stuck a day", age: 25 * time.Hour, want: 1},
		{name: "recent", age: time.Hour, want: 0},
		{name: "with a generation error", age: 25 * time.Hour, withError: true, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, billingDay)
			sub := f.subscribe()
			d := *f.Deps
			d.Tx = &failingTx{inner: f.Deps.Tx, n: 2, err: errors.New("boom")}
			args := kernel.BillSubscriptionArgs{SubscriptionIDs: []core.SubscriptionID{sub.ID}, BillingAt: billingDay}
			if _, err := invoicing.New(&d).BillSubscription(f.Ctx, f.OrgID(), args); err != nil {
				t.Fatalf("BillSubscription() error got = %v, want = nil", err)
			}
			retry := billArgs(t, billJobs(t, f)[0])
			if tc.withError {
				d.Tx = &failingTx{inner: f.Deps.Tx, n: 1, err: errors.New("boom")}
				_, _ = invoicing.New(&d).BillSubscription(f.Ctx, f.OrgID(), retry)
				billJobs(t, f)
			}
			at := billingDay.Add(tc.age)
			f.Clock.Set(at)
			n, err := f.svc.RetryGeneratingInvoices(f.Ctx, f.OrgID(), at)
			if err != nil {
				t.Fatalf("RetryGeneratingInvoices() error got = %v, want = nil", err)
			}
			jobs := billJobs(t, f)
			if n != tc.want || len(jobs) != tc.want {
				t.Fatalf("RetryGeneratingInvoices() got = %d (%d jobs), want = %d", n, len(jobs), tc.want)
			}
			if tc.want == 0 {
				return
			}
			got := billArgs(t, jobs[0])
			if got.InvoiceID != retry.InvoiceID || !got.BillingAt.Equal(billingDay) || got.Reason != string(invoice.InvoicingReasonSubscriptionPeriodic) {
				t.Errorf("args got = %+v, want = invoice %s billed at %v", got, retry.InvoiceID, billingDay)
			}
			if jobs[0].UniqueKey != invoicing.RetryGenerationKey(retry.InvoiceID) {
				t.Errorf("key got = %q, want = %q", jobs[0].UniqueKey, invoicing.RetryGenerationKey(retry.InvoiceID))
			}
		})
	}
}
