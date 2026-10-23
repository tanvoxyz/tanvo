package invoicing_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
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

// withStatus sets the status of a stored invoice.
func withStatus(s invoice.Status) func(*invoice.Invoice) {
	return func(inv *invoice.Invoice) { inv.Status = s }
}

// paymentProvider links the customer to a payment provider connection of
// the provider.
func (f *fixture) paymentProvider(c *customer.Customer, p integration.Provider) {
	f.T.Helper()
	in := &integration.Integration{
		ID: core.NewID[core.IntegrationID](f.IDs), OrganizationID: f.OrgID(), Code: "pay_" + string(p), Name: string(p),
		Category: integration.CategoryPayment, Provider: p, CreatedAt: f.Now(), UpdatedAt: f.Now(),
	}
	pc := &payment.ProviderCustomer{
		ID: core.NewID[core.PaymentProviderCustomerID](f.IDs), OrganizationID: f.OrgID(), CustomerID: c.ID,
		IntegrationID: in.ID, CreatedAt: f.Now(), UpdatedAt: f.Now(),
	}
	f.Do(func(ctx context.Context, r port.Repos) error {
		if err := r.Integrations.Create(ctx, in); err != nil {
			return err
		}
		return r.PaymentProviderCustomers.Create(ctx, pc)
	})
}

func TestDelete(t *testing.T) {
	// source: spec/services/invoices/delete_service_spec.rb:13
	t.Run("draft", func(t *testing.T) {
		f := newFixture(t, billingDay)
		inv := f.StoredInvoice(f.cust, withStatus(invoice.StatusDraft))
		f.StoredFee(inv, 100)
		if err := f.svc.Delete(f.Ctx, f.OrgID(), inv.ID); err != nil {
			t.Fatalf("delete: %v", err)
		}
		_, err := f.svc.View(f.Ctx, f.OrgID(), inv.ID)
		wantNotFound(t, err, "invoice")
		if got := f.Invoice(inv.ID).Status; got != invoice.StatusDeleted {
			t.Errorf("status got = %s, want = deleted", got)
		}
		if got := len(f.Fees(inv.ID)); got != 1 {
			t.Errorf("fees = %d, want = 1 kept", got)
		}
		// source: spec/services/invoices/delete_service_spec.rb:21
		if !f.emitted(webhook.EventTypeInvoiceDeleted) {
			t.Errorf("webhooks = %v, want = invoice.deleted", f.Webhooks())
		}
	})
	// source: spec/services/invoices/delete_service_spec.rb:32
	t.Run("missing", func(t *testing.T) {
		f := newFixture(t, billingDay)
		wantNotFound(t, f.svc.Delete(f.Ctx, f.OrgID(), core.NewID[core.InvoiceID](f.IDs)), "invoice")
	})
	// source: spec/services/invoices/delete_service_spec.rb:44
	t.Run("finalized", func(t *testing.T) {
		f := newFixture(t, billingDay)
		inv := f.StoredInvoice(f.cust, withStatus(invoice.StatusFinalized))
		wantRule(t, f.svc.Delete(f.Ctx, f.OrgID(), inv.ID), invoicing.CodeNotDeletable)
		if f.Invoice(inv.ID).Status != invoice.StatusFinalized {
			t.Errorf("the finalized invoice was changed")
		}
		// source: spec/services/invoices/delete_service_spec.rb:56
		if f.emitted(webhook.EventTypeInvoiceDeleted) {
			t.Errorf("webhooks = %v, want = no invoice.deleted", f.Webhooks())
		}
	})
	// source: spec/services/invoices/delete_service_spec.rb:61
	t.Run("synced externally", func(t *testing.T) {
		f := newFixture(t, billingDay)
		inv := f.StoredInvoice(f.cust, withStatus(invoice.StatusDraft))
		f.Do(func(ctx context.Context, r port.Repos) error {
			return r.IntegrationResources.Create(ctx, &integration.Resource{
				ID: core.NewID[core.IntegrationResourceID](f.IDs), OrganizationID: f.OrgID(),
				IntegrationID: core.NewID[core.IntegrationID](f.IDs), ResourceType: integration.ResourceTypeInvoice,
				SyncableID: inv.ID.UUID(), ExternalID: "ext", CreatedAt: f.Now(), UpdatedAt: f.Now(),
			})
		})
		wantRule(t, f.svc.Delete(f.Ctx, f.OrgID(), inv.ID), invoicing.CodeInvoiceSyncedToExternalSystem)
		if got := f.Invoice(inv.ID).Status; got != invoice.StatusDraft {
			t.Errorf("status got = %s, want = draft", got)
		}
	})
	// source: spec/services/invoices/delete_service_spec.rb:74
	t.Run("draft credit note", func(t *testing.T) {
		f := newFixture(t, billingDay)
		inv := f.StoredInvoice(f.cust, withStatus(invoice.StatusDraft))
		cn := f.StoredCreditNote(inv, 100, func(cn *creditnote.CreditNote) { cn.Status = creditnote.StatusDraft })
		if err := f.svc.Delete(f.Ctx, f.OrgID(), inv.ID); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if got := f.CreditNotes(inv.ID); len(got) != 1 || got[0].ID != cn.ID || got[0].Status != creditnote.StatusDeleted {
			t.Errorf("credit notes got = %v, want = %s kept as deleted", got, cn.ID)
		}
	})
	// source: spec/services/invoices/delete_service_spec.rb:108
	t.Run("credit note not deletable", func(t *testing.T) {
		f := newFixture(t, billingDay)
		inv := f.StoredInvoice(f.cust, withStatus(invoice.StatusDraft))
		f.StoredCreditNote(inv, 100, func(cn *creditnote.CreditNote) { cn.Status = creditnote.StatusDraft })
		f.StoredCreditNote(inv, 100)
		wantRule(t, f.svc.Delete(f.Ctx, f.OrgID(), inv.ID), invoicing.CodeCreditNoteNotDeletable)
		if got := len(f.CreditNotes(inv.ID)); got != 2 {
			t.Errorf("credit notes = %d, want = 2 untouched", got)
		}
		if got := f.Invoice(inv.ID).Status; got != invoice.StatusDraft {
			t.Errorf("status got = %s, want = draft", got)
		}
		// source: spec/services/invoices/delete_service_spec.rb:127
		if f.emitted(webhook.EventTypeInvoiceDeleted) {
			t.Errorf("webhooks = %v, want = no invoice.deleted", f.Webhooks())
		}
	})
}

func TestLoseDispute(t *testing.T) {
	for _, tt := range []struct {
		name, source string
		status       invoice.Status
		ok           bool
	}{
		{"voided", "spec/services/invoices/lose_dispute_service_spec.rb:29", invoice.StatusVoided, true},
		{"draft", "spec/services/invoices/lose_dispute_service_spec.rb:52", invoice.StatusDraft, false},
		{"finalized", "spec/services/invoices/lose_dispute_service_spec.rb:68", invoice.StatusFinalized, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, billingDay)
			inv := f.StoredInvoice(f.cust, withStatus(tt.status), func(inv *invoice.Invoice) { inv.PaymentOverdue = tt.status == invoice.StatusFinalized })
			f.Store.TakeJobs()
			v, err := f.svc.LoseDispute(f.Ctx, f.OrgID(), inv.ID)
			if !tt.ok {
				wantRule(t, err, invoicing.CodeNotDisputable)
				if f.emitted(webhook.EventTypeInvoicePaymentDisputeLost) {
					t.Errorf("webhooks = %v, want = none", f.Webhooks())
				}
				return
			}
			if err != nil {
				t.Fatalf("lose dispute: %v", err)
			}
			got := f.Invoice(inv.ID)
			if got.PaymentDisputeLostAt == nil || !got.PaymentDisputeLostAt.Equal(billingDay) || got.PaymentOverdue {
				t.Errorf("dispute lost at, overdue = %v, %v, want = %v, false", got.PaymentDisputeLostAt, got.PaymentOverdue, billingDay)
			}
			if v.Invoice.PaymentDisputeLostAt == nil {
				t.Errorf("view: payment_dispute_lost_at got = nil, want = set")
			}
			if !f.emitted(webhook.EventTypeInvoicePaymentDisputeLost) {
				t.Errorf("webhooks = %v, want = invoice.payment_dispute_lost", f.Webhooks())
			}
			if got := f.enqueued(kernel.JobVoidProviderTaxes); got != 1 {
				t.Errorf("void provider taxes jobs = %d, want = 1", got)
			}
		})
	}
	// source: spec/services/invoices/lose_dispute_service_spec.rb:9
	t.Run("missing", func(t *testing.T) {
		f := newFixture(t, billingDay)
		_, err := f.svc.LoseDispute(f.Ctx, f.OrgID(), core.NewID[core.InvoiceID](f.IDs))
		wantNotFound(t, err, "invoice")
	})
}

func TestRetryPayment(t *testing.T) {
	payable := func(inv *invoice.Invoice) {
		inv.Status, inv.ReadyForPaymentProcessing, inv.TotalAmount = invoice.StatusFinalized, true, 1000
	}
	// source: spec/services/invoices/payments/retry_service_spec.rb:13
	t.Run("provider", func(t *testing.T) {
		f := newFixture(t, billingDay)
		f.paymentProvider(f.cust, integration.ProviderStripe)
		inv := f.StoredInvoice(f.cust, payable)
		f.Store.TakeJobs()
		if err := f.svc.RetryPayment(f.Ctx, f.OrgID(), inv.ID, invoicing.PaymentMethodParams{}); err != nil {
			t.Fatalf("retry payment: %v", err)
		}
		if got := f.enqueued(kernel.JobCreateInvoicePayment); got != 1 {
			t.Errorf("create payment jobs = %d, want = 1", got)
		}
	})
	// source: spec/services/invoices/payments/retry_service_spec.rb:29
	t.Run("missing", func(t *testing.T) {
		f := newFixture(t, billingDay)
		wantNotFound(t, f.svc.RetryPayment(f.Ctx, f.OrgID(), core.NewID[core.InvoiceID](f.IDs), invoicing.PaymentMethodParams{}), "invoice")
	})
	for _, tt := range []struct {
		name, source, code string
		opt                func(*invoice.Invoice)
	}{
		{
			"succeeded", "spec/services/invoices/payments/retry_service_spec.rb:40", invoicing.CodeInvalidStatus,
			func(inv *invoice.Invoice) { inv.PaymentStatus = core.PaymentStatusSucceeded },
		},
		{
			"draft", "spec/services/invoices/payments/retry_service_spec.rb:60", invoicing.CodeInvalidStatus,
			func(inv *invoice.Invoice) { inv.Status = invoice.StatusDraft },
		},
		{
			"processing", "spec/services/invoices/payments/retry_service_spec.rb:74", invoicing.CodePaymentProcessing,
			func(inv *invoice.Invoice) { inv.ReadyForPaymentProcessing = false },
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, billingDay)
			f.paymentProvider(f.cust, integration.ProviderStripe)
			inv := f.StoredInvoice(f.cust, payable, tt.opt)
			wantRule(t, f.svc.RetryPayment(f.Ctx, f.OrgID(), inv.ID, invoicing.PaymentMethodParams{}), tt.code)
		})
	}
	// source: spec/services/invoices/payments/retry_service_spec.rb:94
	t.Run("no provider", func(t *testing.T) {
		f := newFixture(t, billingDay)
		inv := f.StoredInvoice(f.cust, payable)
		f.Store.TakeJobs()
		if err := f.svc.RetryPayment(f.Ctx, f.OrgID(), inv.ID, invoicing.PaymentMethodParams{}); err != nil {
			t.Fatalf("retry payment: %v", err)
		}
		if !f.emitted(webhook.EventTypeInvoicePaymentFailure) || f.enqueued(kernel.JobCreateInvoicePayment) != 0 {
			t.Errorf("webhooks, jobs = %v, %v, want = invoice.payment_failure and no payment", f.Webhooks(), f.JobKinds())
		}
	})
}

func TestPaymentURL(t *testing.T) {
	payable := func(inv *invoice.Invoice) { inv.Status = invoice.StatusFinalized }
	for _, tt := range []struct {
		name, source string
		provider     integration.Provider
		opt          func(*invoice.Invoice)
		code         string
	}{
		{"no provider", "spec/services/invoices/payments/generate_payment_url_service_spec.rb:66", "", payable, invoicing.CodeNoLinkedPaymentProvider},
		{"gocardless", "spec/services/invoices/payments/generate_payment_url_service_spec.rb:80", integration.ProviderGocardless, payable, invoicing.CodeInvalidPaymentProvider},
		{
			"paid", "spec/services/invoices/payments/generate_payment_url_service_spec.rb:92", integration.ProviderStripe,
			func(inv *invoice.Invoice) { payable(inv); inv.PaymentStatus = core.PaymentStatusSucceeded }, invoicing.CodeInvalidInvoiceStatusOrPaymentStatus,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, billingDay)
			if tt.provider != "" {
				f.paymentProvider(f.cust, tt.provider)
			}
			inv := f.StoredInvoice(f.cust, tt.opt)
			_, err := f.svc.PaymentURL(f.Ctx, f.OrgID(), inv.ID)
			wantField(t, err, "base", tt.code)
		})
	}
	// source: spec/services/invoices/payments/generate_payment_url_service_spec.rb:17
	t.Run("linked", func(t *testing.T) {
		f := newFixture(t, billingDay)
		f.paymentProvider(f.cust, integration.ProviderStripe)
		inv := f.StoredInvoice(f.cust, payable)
		if _, err := f.svc.PaymentURL(f.Ctx, f.OrgID(), inv.ID); !errors.Is(err, invoicing.ErrPaymentURLUnavailable) {
			t.Errorf("error got = %v, want = %v", err, invoicing.ErrPaymentURLUnavailable)
		}
	})
}

// source: spec/services/invoices/finalize_batch_service_spec.rb:12
func TestFinalizeAll(t *testing.T) {
	f := newFixture(t, billingDay)
	for range 3 {
		f.StoredInvoice(f.cust, withStatus(invoice.StatusDraft))
	}
	f.StoredInvoice(f.cust, withStatus(invoice.StatusFinalized))
	f.Store.TakeJobs()
	n, err := f.svc.FinalizeAll(f.Ctx, f.OrgID())
	if err != nil {
		t.Fatalf("finalize all: %v", err)
	}
	if n != 3 || f.enqueued(kernel.JobFinalizeDraft) != 3 {
		t.Errorf("count, jobs = %d, %d, want = 3, 3", n, f.enqueued(kernel.JobFinalizeDraft))
	}
}

func TestFinalizeAndDownload(t *testing.T) {
	f := newFixture(t, billingDay)
	f.UpdateBillingEntity(func(b *organization.BillingEntity) { b.InvoiceGracePeriodDays = 30 })
	draft := f.billOne(f.subscribe())
	// source: spec/requests/api/v1/invoices_controller_spec.rb:895
	_, _, err := f.svc.Download(f.Ctx, f.OrgID(), draft.ID)
	wantNotFound(t, err, "invoice")
	v, err := f.svc.Finalize(f.Ctx, f.OrgID(), draft.ID)
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if v.Invoice.Status != invoice.StatusFinalized {
		t.Errorf("status got = %s, want = finalized", v.Invoice.Status)
	}
	_, err = f.svc.Finalize(f.Ctx, f.OrgID(), draft.ID)
	wantNotFound(t, err, "invoice")
	f.Store.TakeJobs()
	if _, ready, err := f.svc.Download(f.Ctx, f.OrgID(), draft.ID); err != nil || ready {
		t.Fatalf("download got = %v, %v, want = not ready", ready, err)
	}
	if got := f.enqueued(kernel.JobGenerateInvoiceDocument); got != 1 {
		t.Errorf("document jobs = %d, want = 1", got)
	}
	inv := f.Invoice(draft.ID)
	inv.File = "invoices/" + core.BlobRef(inv.ID.String()) + ".pdf"
	f.SaveInvoice(inv)
	if _, ready, err := f.svc.Download(f.Ctx, f.OrgID(), draft.ID); err != nil || !ready {
		t.Errorf("download got = %v, %v, want = ready", ready, err)
	}
}

func TestRetryView(t *testing.T) {
	// source: spec/services/invoices/retry_service_spec.rb:76
	f := newFixture(t, billingDay)
	inv := f.StoredInvoice(f.cust, withStatus(invoice.StatusFinalized))
	_, err := f.svc.RetryView(f.Ctx, f.OrgID(), inv.ID)
	wantRule(t, err, invoicing.CodeInvalidStatus)
	// source: spec/services/invoices/retry_service_spec.rb:67
	_, err = f.svc.RetryView(f.Ctx, f.OrgID(), core.NewID[core.InvoiceID](f.IDs))
	wantNotFound(t, err, "invoice")
}

func TestRefreshNotSubscription(t *testing.T) {
	// source: spec/services/invoices/refresh_draft_service_spec.rb:61
	f := newFixture(t, billingDay)
	inv := f.StoredInvoice(f.cust, withStatus(invoice.StatusDraft), func(inv *invoice.Invoice) { inv.Type = invoice.TypeOneOff })
	_, err := f.svc.Refresh(f.Ctx, f.OrgID(), inv.ID)
	wantRule(t, err, invoicing.CodeNotRefreshable)
	// A generating invoice is not visible.
	gen := f.StoredInvoice(f.cust)
	_, err = f.svc.Refresh(f.Ctx, f.OrgID(), gen.ID)
	wantNotFound(t, err, "invoice")
}

func TestListViews(t *testing.T) {
	f := newFixture(t, billingDay)
	f.StoredInvoice(f.cust, withStatus(invoice.StatusDraft))
	f.StoredInvoice(f.cust, withStatus(invoice.StatusFinalized))
	f.StoredInvoice(f.cust) // generating: not listed
	f.Clock.Advance(time.Second)
	p, err := f.svc.ListViews(f.Ctx, f.OrgID(), invoice.Filter{}, core.PageRequest{Size: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(p.Items) != 2 {
		t.Errorf("items = %d, want = 2", len(p.Items))
	}
	p, err = f.svc.ListViews(f.Ctx, f.OrgID(), invoice.Filter{Statuses: []invoice.Status{invoice.StatusDraft}}, core.PageRequest{Size: 10})
	if err != nil || len(p.Items) != 1 || p.Items[0].Invoice.Status != invoice.StatusDraft {
		t.Errorf("draft items got = %v, %v, want = one draft", p.Items, err)
	}
}
