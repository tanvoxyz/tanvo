package storetest_test

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// Concurrent runs of one downgrade swap swap it once: the statuses are
// checked again under the subscription locks.
//
// source: app/services/subscriptions/terminate_service.rb:624
func TestConcurrentSwapDowngrade(t *testing.T) {
	jan := billingtest.Date(2026, time.January, 1)
	h, _ := newHarness(t, jan)
	e, err := billing.New(*h.Deps)
	if err != nil {
		t.Fatal(err)
	}
	c := h.Customer()
	cur := h.Subscription(c, h.Plan(period.Monthly, 1000), billingtest.SubscribedAt(jan))
	if _, err := e.Lifecycle.ChangePlan(h.Ctx, lifecycle.ChangePlanInput{OrganizationID: h.OrgID(), SubscriptionID: cur.ID, PlanID: h.Plan(period.Monthly, 10).ID}); err != nil {
		t.Fatalf("downgrade: %v", err)
	}
	at := billingtest.Date(2026, time.February, 1)
	h.Clock.Set(at)
	swapped := make([]bool, 4)
	errs := concurrently(len(swapped), func(i int) error {
		var err error
		swapped[i], err = e.SwapDowngrade(context.Background(), h.OrgID(), cur.ID, at)
		return err
	})
	n := 0
	for i, err := range errs {
		if err != nil {
			t.Errorf("run %d: got = %v, want = nil", i, err)
		}
		if swapped[i] {
			n++
		}
	}
	if n != 1 {
		t.Errorf("swaps: got = %d, want = 1", n)
	}
	if got := len(h.Invoices(c.ID)); got != 1 {
		t.Errorf("invoices: got = %d, want = 1", got)
	}
}
