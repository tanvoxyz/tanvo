package invoicing_test

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// progressive is a fixture at 10 March 10:00 with a sum charge of 1.00
// per unit, a subscription started a week before and one event of one
// unit an hour before.
func progressive(t *testing.T) (*fixture, *subscription.Subscription, *catalog.UsageThreshold) {
	t.Helper()
	now := billingtest.Date(2026, time.March, 10, 10)
	f := newFixture(t, now)
	f.taxCustomer("20")
	m := f.Metric("value_sum", catalog.AggregationTypeSum, "value")
	f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
	th := f.UsageThreshold(f.plan, 50, false)
	sub := f.Subscription(f.cust, f.plan, billingtest.SubscribedAt(now.Add(-7*24*time.Hour)))
	f.Event(sub, "value_sum", now.Add(-time.Hour), "value", "1")
	return f, sub, th
}

// bill runs the progressive billing of sub for the thresholds at the
// lifetime usage amount.
func (f *fixture) progressiveBill(sub *subscription.Subscription, amount int64, ths ...*catalog.UsageThreshold) *invoice.Invoice {
	f.T.Helper()
	lu := &subscription.LifetimeUsage{OrganizationID: f.OrgID(), SubscriptionID: sub.ID, CurrentUsageAmount: amount}
	return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*invoice.Invoice, error) {
		return f.svc.ProgressiveBilling(ctx, r, sub, ths, lu)
	})
}

// source: spec/services/invoices/progressive_billing_service_spec.rb:40
func TestProgressiveBilling(t *testing.T) {
	f, sub, th := progressive(t)
	inv := f.Invoice(f.progressiveBill(sub, 100, th).ID)
	if inv.Type != invoice.TypeProgressiveBilling || inv.Status != invoice.StatusFinalized {
		t.Fatalf("type, status = %s, %s, want = progressive_billing, finalized", inv.Type, inv.Status)
	}
	checkAmounts(t, inv, 100, 20, 120)
	if got := len(f.invoiceSubscriptions(inv.ID)); got != 1 {
		t.Errorf("invoice subscriptions = %d, want = 1", got)
	}
	if got := len(f.Fees(inv.ID)); got != 1 {
		t.Errorf("fees = %d, want = 1", got)
	}
	applied := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*subscription.AppliedUsageThreshold, error) {
		return r.AppliedUsageThresholds.ListByInvoice(ctx, f.OrgID(), inv.ID)
	})
	if len(applied) != 1 || applied[0].LifetimeUsageAmount != 100 || applied[0].UsageThresholdID != th.ID {
		t.Fatalf("applied thresholds = %+v, want = one at 100", applied)
	}
	// source: spec/services/invoices/progressive_billing_service_spec.rb:249
	if !f.emitted(webhook.EventTypeInvoiceCreated) {
		t.Errorf("webhooks = %v, want = invoice.created", f.Webhooks())
	}
	// source: spec/services/invoices/progressive_billing_service_spec.rb:308
	if f.enqueued(kernel.JobCreateInvoicePayment) != 1 || f.enqueued(kernel.JobGenerateInvoiceDocument) != 1 {
		t.Errorf("jobs = %v, want = a payment and a document", f.JobKinds())
	}
}

// source: spec/services/invoices/progressive_billing_service_spec.rb:137
func TestProgressiveBillingMultipleThresholds(t *testing.T) {
	f, sub, th := progressive(t)
	second := f.UsageThreshold(f.plan, 80, false)
	inv := f.progressiveBill(sub, 100, th, second)
	applied := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*subscription.AppliedUsageThreshold, error) {
		return r.AppliedUsageThresholds.ListByInvoice(ctx, f.OrgID(), inv.ID)
	})
	if len(applied) != 2 {
		t.Fatalf("applied thresholds = %d, want = 2", len(applied))
	}
}

// source: spec/services/invoices/progressive_billing_service_spec.rb:195
func TestProgressiveBillingAlreadyBilled(t *testing.T) {
	f, sub, th := progressive(t)
	first := f.progressiveBill(sub, 100, th)
	f.Event(sub, "value_sum", f.Now().Add(-30*time.Minute), "value", "2")
	f.Clock.Advance(time.Hour)
	second := f.UsageThreshold(f.plan, 250, false)
	inv := f.Invoice(f.progressiveBill(sub, 300, second).ID)
	if inv.ID == first.ID {
		t.Fatalf("second invoice = first, want = a new invoice")
	}
	// The first invoice's 1.00 is credited: 3.00 of usage less 1.00.
	if inv.ProgressiveBillingCreditAmount != 100 || inv.FeesAmount != 300 || inv.TotalAmount != 240 {
		t.Errorf("credit, fees, total = %d, %d, %d, want = 100, 300, 240",
			inv.ProgressiveBillingCreditAmount, inv.FeesAmount, inv.TotalAmount)
	}
}

// source: spec/services/invoices/progressive_billing_service_spec.rb:123
func TestProgressiveBillingPendingTaxes(t *testing.T) {
	f, sub, th := progressive(t)
	f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
	f.Taxes.Compute = pendingTaxes
	inv := f.progressiveBill(sub, 100, th)
	if inv.TaxStatus != invoice.TaxStatusPending {
		t.Fatalf("tax status = %s, want = pending", inv.TaxStatus)
	}
}
