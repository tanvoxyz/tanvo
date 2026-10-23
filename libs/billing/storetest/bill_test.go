package storetest_test

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// Concurrent runs of one periodic billing job bill the period once and
// both succeed: the second waits for the subscription lock, then finds the
// period billed (invoice_subscriptions charges key).
//
// source: app/services/invoices/subscription_service.rb:156
func TestConcurrentPeriodicBilling(t *testing.T) {
	billingDay := billingtest.Date(2026, time.February, 1)
	h, _ := newHarness(t, billingDay)
	c := h.Customer()
	p := h.Plan(period.Monthly, 1000)
	sub := h.Subscription(c, p, billingtest.SubscribedAt(billingtest.Date(2026, time.January, 1)))
	svc := invoicing.New(h.Deps)
	args := kernel.BillSubscriptionArgs{SubscriptionIDs: []core.SubscriptionID{sub.ID}, BillingAt: billingDay}
	errs := concurrently(4, func(int) error {
		_, err := svc.BillSubscription(context.Background(), h.OrgID(), args)
		return err
	})
	for i, err := range errs {
		if err != nil {
			t.Errorf("run %d: got = %v, want = nil", i, err)
		}
	}
	if got := len(h.Invoices(c.ID)); got != 1 {
		t.Errorf("invoices: got = %d, want = 1", got)
	}
}
