package porttest

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// testWork checks the cross-organization scans of the clock jobs
// (organization.Repository.WithWork): organization a has an active
// subscription ending tomorrow, a draft invoice flagged for refresh, a
// lifetime usage whose invoiced usage is flagged, an expired wallet, an interval rule and an
// expired coupon; organization b has a pending subscription starting
// tomorrow, a draft invoice, a lifetime usage whose current usage only is
// flagged, an active wallet expiring next week, a
// threshold rule and a coupon expiring next week.
func testWork(t *testing.T, h Harness) {
	t.Run("organizations with work", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		pa, pb := subscriptionFixtures(e)
		a, b := pa.org, pb.org
		e.tx(func(ctx context.Context, r port.Repos) error {
			active := pa.newSub(e, "work-active")
			active.EndingAt = ptr(at(24 * time.Hour))
			noErr(t, "Subscriptions.Create()", r.Subscriptions.Create(ctx, active))
			pending := subscription.New(b, core.NewID[core.SubscriptionID](e.ids), pb.customer.ID, pb.plan.ID, "work-pending",
				at(24*time.Hour), subscription.PlanFacts{}, now)
			noErr(t, "Subscriptions.Create() pending", r.Subscriptions.Create(ctx, pending))
			late := subscription.New(b, core.NewID[core.SubscriptionID](e.ids), pb.customer.ID, pb.plan.ID, "work-late",
				at(10*24*time.Hour), subscription.PlanFacts{}, now)
			noErr(t, "Subscriptions.Create() late", r.Subscriptions.Create(ctx, late))

			flagged := subscriptionFixtureInvoice(ctx, e, r, pa.customer)
			flagged.RefreshRequested = true
			noErr(t, "Invoices.Update()", r.Invoices.Update(ctx, flagged))
			subscriptionFixtureInvoice(ctx, e, r, pb.customer)

			noErr(t, "LifetimeUsages.Upsert()", r.LifetimeUsages.Upsert(ctx, &subscription.LifetimeUsage{
				ID: core.NewID[core.LifetimeUsageID](e.ids), OrganizationID: a, SubscriptionID: active.ID,
				ExternalSubscriptionID: active.ExternalID, Currency: money.MustCurrency("EUR"), RecalculateInvoicedUsage: true,
				CreatedAt: now, UpdatedAt: now,
			}))
			// Only the invoiced usage flag counts.
			noErr(t, "LifetimeUsages.Upsert() current only", r.LifetimeUsages.Upsert(ctx, &subscription.LifetimeUsage{
				ID: core.NewID[core.LifetimeUsageID](e.ids), OrganizationID: b, SubscriptionID: pending.ID,
				ExternalSubscriptionID: pending.ExternalID, Currency: money.MustCurrency("EUR"), RecalculateCurrentUsage: true,
				CreatedAt: now, UpdatedAt: now,
			}))

			wa := wallet.New(a, core.NewID[core.WalletID](e.ids), pa.customer.ID, money.MustCurrency("EUR"), money.MustParse("1"), now)
			wa.ExpirationAt = ptr(at(-time.Hour))
			wb := wallet.New(b, core.NewID[core.WalletID](e.ids), pb.customer.ID, money.MustCurrency("EUR"), money.MustParse("1"), now)
			wb.ExpirationAt = ptr(at(7 * 24 * time.Hour))
			for _, w := range []*wallet.Wallet{wa, wb} {
				noErr(t, "Wallets.Create()", r.Wallets.Create(ctx, w))
			}
			rule := func(org core.OrganizationID, w core.WalletID, trigger wallet.RecurringTrigger) *wallet.RecurringTransactionRule {
				x := &wallet.RecurringTransactionRule{
					ID: core.NewID[core.RecurringTransactionRuleID](e.ids), OrganizationID: org, WalletID: w,
					Trigger: trigger, Method: wallet.RecurringMethodFixed, PaidCredits: money.MustParse("10"),
					Status: wallet.RuleStatusActive, CreatedAt: now, UpdatedAt: now,
				}
				if trigger == wallet.RecurringTriggerInterval {
					x.Interval = period.Monthly
				} else {
					x.ThresholdCredits = ptr(money.MustParse("1"))
				}
				return x
			}
			noErr(t, "RecurringTransactionRules.Create()", r.RecurringTransactionRules.Create(ctx, rule(a, wa.ID, wallet.RecurringTriggerInterval)))
			noErr(t, "RecurringTransactionRules.Create()", r.RecurringTransactionRules.Create(ctx, rule(b, wb.ID, wallet.RecurringTriggerThreshold)))

			ca := couponFixtureFixed(e, a, "work-a")
			ca.Expiration, ca.ExpirationAt = coupon.ExpirationTimeLimit, ptr(at(-time.Minute))
			cb := couponFixtureFixed(e, b, "work-b")
			cb.Expiration, cb.ExpirationAt = coupon.ExpirationTimeLimit, ptr(at(7*24*time.Hour))
			for _, c := range []*coupon.Coupon{ca, cb} {
				noErr(t, "Coupons.Create()", r.Coupons.Create(ctx, c))
			}
			return nil
		})

		ab := []core.OrganizationID{a, b}
		slices.SortFunc(ab, func(x, y core.OrganizationID) int { return x.Compare(y) })
		for _, tc := range []struct {
			work organization.Work
			want []core.OrganizationID
		}{
			{organization.WorkActiveSubscriptions, []core.OrganizationID{a}},
			{organization.WorkPendingSubscriptions, []core.OrganizationID{b}},
			{organization.WorkEndingSubscriptions, []core.OrganizationID{a}},
			{organization.WorkDraftInvoices, ab},
			{organization.WorkRefreshRequested, []core.OrganizationID{a}},
			{organization.WorkLifetimeUsages, []core.OrganizationID{a}},
			{organization.WorkExpiredWallets, []core.OrganizationID{a}},
			{organization.WorkIntervalRules, []core.OrganizationID{a}},
			{organization.WorkExpiredCoupons, []core.OrganizationID{a}},
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
		e.tx(func(ctx context.Context, r port.Repos) error {
			_, err := r.Organizations.WithWork(ctx, "nope", now)
			wantErr(t, "WithWork() undeclared", err, core.ErrInvalidValue)
			_, err = r.Organizations.WithWork(ctx, organization.WorkDraftInvoices, time.Time{})
			wantErr(t, "WithWork() zero instant", err, core.ErrInvalidValue)
			return nil
		})
	})
}
