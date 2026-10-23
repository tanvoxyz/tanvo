package invoicing_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// billingDay is the periodic billing instant of the tests: the start of
// February 2026, ending January.
var billingDay = billingtest.Date(2026, time.February, 1)

// fixture is a harness with the invoicing service, one customer and a
// monthly calendar plan of 10.00 paid in arrears.
type fixture struct {
	*billingtest.Harness
	svc  *invoicing.Service
	cust *customer.Customer
	plan *catalog.Plan
}

// newFixture returns a fixture whose clock reads now.
func newFixture(t *testing.T, now time.Time, planOpts ...func(*catalog.Plan)) *fixture {
	t.Helper()
	h := billingtest.New(t, now)
	return &fixture{
		Harness: h, svc: invoicing.New(h.Deps), cust: h.Customer(),
		plan: h.Plan(period.Monthly, 1000, planOpts...),
	}
}

// subscribe subscribes the fixture's customer to its plan on 1 January.
func (f *fixture) subscribe(opts ...func(*subscription.Subscription)) *subscription.Subscription {
	f.T.Helper()
	return f.Subscription(f.cust, f.plan, append([]func(*subscription.Subscription){billingtest.SubscribedAt(billingtest.Date(2026, time.January, 1))}, opts...)...)
}

// bill bills the subscriptions with the reason at the instant.
func (f *fixture) bill(at time.Time, reason invoice.InvoicingReason, subs ...*subscription.Subscription) []*invoice.Invoice {
	f.T.Helper()
	req := invoicing.BillRequest{OrganizationID: f.OrgID(), BillingAt: at}
	for _, s := range subs {
		req.Items = append(req.Items, invoicing.BillItem{SubscriptionID: s.ID, Reason: reason})
	}
	return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*invoice.Invoice, error) {
		return f.svc.Bill(ctx, r, req)
	})
}

// billOne bills the periodic invoice of s at the billing day and returns
// the only invoice.
func (f *fixture) billOne(s *subscription.Subscription) *invoice.Invoice {
	f.T.Helper()
	invs := f.bill(billingDay, invoice.InvoicingReasonSubscriptionPeriodic, s)
	if len(invs) != 1 {
		f.T.Fatalf("invoices = %d, want = 1", len(invs))
	}
	return f.Invoice(invs[0].ID)
}

// taxCustomer applies a tax of the rate to the fixture's customer.
func (f *fixture) taxCustomer(rate string) *catalog.Tax {
	f.T.Helper()
	tx := f.Tax("vat_"+rate, rate)
	f.UpdateCustomer(f.cust, func(c *customer.Customer) { c.TaxIDs = []core.TaxID{tx.ID} })
	return tx
}

// emitted reports whether the event type was emitted.
func (f *fixture) emitted(e webhook.EventType) bool { return slices.Contains(f.Webhooks(), e) }

// enqueued counts the jobs of the kind.
func (f *fixture) enqueued(kind string) int {
	n := 0
	for _, k := range f.JobKinds() {
		if k == kind {
			n++
		}
	}
	return n
}

// checkAmounts compares the totals of an invoice.
func checkAmounts(t *testing.T, inv *invoice.Invoice, fees, taxes, total int64) {
	t.Helper()
	if inv.FeesAmount != fees || inv.TaxesAmount != taxes || inv.TotalAmount != total {
		t.Errorf("amounts = fees %d taxes %d total %d, want = fees %d taxes %d total %d",
			inv.FeesAmount, inv.TaxesAmount, inv.TotalAmount, fees, taxes, total)
	}
}

// invoiceSubscriptions returns the invoice subscriptions of id.
func (f *fixture) invoiceSubscriptions(id core.InvoiceID) []*invoice.Subscription {
	f.T.Helper()
	return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*invoice.Subscription, error) {
		return r.InvoiceSubscriptions.ListByInvoice(ctx, f.OrgID(), id)
	})
}
