package porttest

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// testClockWork checks the scans of the invoice, subscription and order
// form clock jobs (organization.Repository.WithWork): organization a has a
// finalized invoice due yesterday, an active subscription ending in 30
// days, an incomplete subscription whose activation rule expired an hour
// ago and an order form expiring in 10 days; organization b has a
// finalized invoice due tomorrow, one due yesterday with a lost dispute,
// an active subscription that ended yesterday, an incomplete subscription
// whose rule expires in an hour and an order form expiring tomorrow.
func testClockWork(t *testing.T, h Harness) {
	t.Run("organizations with clock work", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		pa, pb := subscriptionFixtures(e)
		a, b := pa.org, pb.org
		qa, qb := quoteFixtureParents(e, pa.entity), quoteFixtureParents(e, pb.entity)
		va, vb := quoteFixtureChain(e, qa, 1)[0], quoteFixtureChain(e, qb, 1)[0]
		today := core.DateOf(now, time.UTC)
		e.tx(func(ctx context.Context, r port.Repos) error {
			finalized := func(p subscriptionParents, due core.Date, disputeLost bool) {
				inv := subscriptionFixtureInvoice(ctx, e, r, p.customer)
				noErr(t, "Transition()", inv.Transition(invoice.StatusFinalized, now))
				inv.PaymentDueDate = due
				if disputeLost {
					inv.PaymentDisputeLostAt = ptr(now)
				}
				noErr(t, "Invoices.Update() finalized", r.Invoices.Update(ctx, inv))
			}
			finalized(pa, today.AddDays(-1), false)
			finalized(pb, today.AddDays(1), false)
			finalized(pb, today.AddDays(-1), true)

			ending := pa.newSub(e, "clock-ending")
			ending.EndingAt = ptr(at(30 * 24 * time.Hour))
			ended := pb.newSub(e, "clock-ended")
			ended.EndingAt = ptr(today.Midnight(time.UTC).Add(-time.Minute))
			for _, s := range []*subscription.Subscription{ending, ended} {
				noErr(t, "Subscriptions.Create()", r.Subscriptions.Create(ctx, s))
			}

			incomplete := func(p subscriptionParents, externalID string, started time.Time) {
				s := subscription.New(p.org, core.NewID[core.SubscriptionID](e.ids), p.customer.ID, p.plan.ID, externalID,
					at(-24*time.Hour), subscription.PlanFacts{}, now)
				noErr(t, "MarkIncomplete()", s.MarkIncomplete(at(-24*time.Hour)))
				noErr(t, "Subscriptions.Create() incomplete", r.Subscriptions.Create(ctx, s))
				rule := subscription.NewActivationRule(p.org, core.NewID[core.ActivationRuleID](e.ids), s.ID, subscription.ActivationRuleTypePayment, 2, now)
				noErr(t, "Start()", rule.Start(started))
				noErr(t, "ActivationRules.Create()", r.ActivationRules.Create(ctx, rule))
			}
			incomplete(pa, "clock-expired", at(-3*time.Hour))
			incomplete(pb, "clock-waiting", at(-time.Hour))

			fa := quoteFixtureForm(e, qa, va, 100, now)
			fa.ExpiresAt = ptr(at(10 * 24 * time.Hour))
			fb := quoteFixtureForm(e, qb, vb, 100, now)
			fb.ExpiresAt = ptr(at(24 * time.Hour))
			noErr(t, "OrderForms.Create()", r.OrderForms.Create(ctx, fa))
			noErr(t, "OrderForms.Create()", r.OrderForms.Create(ctx, fb))

			// Scheduled orders: due in a, in the future in b.
			oa := quoteFixtureOrder(e, fa, 100, now)
			oa.ExecutionMode, oa.ExecuteAt = quote.ExecutionModeExecute, ptr(at(-time.Minute))
			ob := quoteFixtureOrder(e, fb, 100, now)
			ob.ExecutionMode, ob.ExecuteAt = quote.ExecutionModeExecute, ptr(at(time.Hour))
			noErr(t, "Orders.Create()", r.Orders.Create(ctx, oa))
			noErr(t, "Orders.Create()", r.Orders.Create(ctx, ob))
			return nil
		})

		for _, tc := range []struct {
			work organization.Work
			want []core.OrganizationID
		}{
			{organization.WorkOverdueInvoices, []core.OrganizationID{a}},
			{organization.WorkEndingAtSubscriptions, []core.OrganizationID{a}},
			{organization.WorkExpirableSubscriptions, []core.OrganizationID{a}},
			{organization.WorkExpiringOrderForms, []core.OrganizationID{b}},
			{organization.WorkExecutableOrders, []core.OrganizationID{a}},
		} {
			e.tx(func(ctx context.Context, r port.Repos) error {
				got, err := r.Organizations.WithWork(ctx, tc.work, now)
				noErr(t, "WithWork("+tc.work.String()+")", err)
				// Other tests may share the database: only a and b count.
				got = slices.DeleteFunc(got, func(o core.OrganizationID) bool { return o != a && o != b })
				if !slices.Equal(got, tc.want) {
					t.Errorf("WithWork(%s) got = %v, want = %v", tc.work, got, tc.want)
				}
				return nil
			})
		}
	})
}
