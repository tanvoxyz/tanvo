package usage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// refresh refreshes the lifetime usage of s.
func (f *fixture) refresh(s *subscription.Subscription) *subscription.LifetimeUsage {
	f.T.Helper()
	return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*subscription.LifetimeUsage, error) {
		return f.svc.RefreshLifetimeUsage(ctx, r, f.OrgID(), s.ID, nil)
	})
}

// chargeFees stores n charge fees of amount on inv for s.
func (f *fixture) chargeFees(inv *invoice.Invoice, s *subscription.Subscription, n int, amount int64) {
	f.T.Helper()
	for range n {
		f.StoredFee(inv, amount, billingtest.ChargeFee(s, f.charge.ID))
	}
}

func TestRefreshLifetimeUsageInvoiced(t *testing.T) {
	t.Parallel()
	now := billingtest.Date(2024, time.May, 15)
	flagged := func(lu *subscription.LifetimeUsage) { lu.RecalculateInvoicedUsage = true }
	cases := []struct {
		name  string
		setup func(f *fixture)
		want  int64
	}{
		// source: spec/services/lifetime_usages/calculate_service_spec.rb:41
		{name: "without previous invoices", setup: func(*fixture) {}, want: 0},
		// source: spec/services/lifetime_usages/calculate_service_spec.rb:58
		{name: "draft invoice", setup: func(f *fixture) {
			f.chargeFees(f.invoiceOf(f.sub, invoice.TypeSubscription, invoice.StatusDraft), f.sub, 2, 100)
		}, want: 200},
		// source: spec/services/lifetime_usages/calculate_service_spec.rb:71
		{name: "finalized invoice", setup: func(f *fixture) {
			f.chargeFees(f.invoiceOf(f.sub, invoice.TypeSubscription, invoice.StatusFinalized), f.sub, 2, 100)
		}, want: 200},
		// Voided and progressive billing invoices are not invoiced usage.
		{name: "voided and progressive invoices", setup: func(f *fixture) {
			f.chargeFees(f.invoiceOf(f.sub, invoice.TypeSubscription, invoice.StatusVoided), f.sub, 2, 100)
			f.chargeFees(f.invoiceOf(f.sub, invoice.TypeProgressiveBilling, invoice.StatusFinalized), f.sub, 2, 100)
		}, want: 0},
		// source: spec/services/lifetime_usages/calculate_service_spec.rb:116
		{name: "invoices of the previous subscription", setup: func(f *fixture) {
			prev := f.Subscription(f.cust, f.plan, billingtest.SubscribedAt(f.sub.SubscriptionAt), subscriptionNamed(f.sub.ExternalID), status(subscription.StatusTerminated))
			f.chargeFees(f.invoiceOf(prev, invoice.TypeSubscription, invoice.StatusFinalized), prev, 2, 100)
		}, want: 200},
		// source: spec/services/lifetime_usages/calculate_service_spec.rb:139
		{name: "invoice shared with another subscription", setup: func(f *fixture) {
			other := f.Subscription(f.cust, f.plan, billingtest.SubscribedAt(f.sub.SubscriptionAt))
			inv := f.invoiceOf(f.sub, invoice.TypeSubscription, invoice.StatusFinalized)
			f.LinkSubscription(inv, other, invoice.InvoicingReasonSubscriptionPeriodic, now.AddDate(0, 0, -14), now.AddDate(0, 0, 17))
			f.chargeFees(inv, f.sub, 1, 100)
			f.chargeFees(inv, other, 1, 900)
		}, want: 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, now, "10")
			f.lifetime(f.sub, flagged)
			tc.setup(f)
			lu := f.refresh(f.sub)
			if lu.InvoicedUsageAmount != tc.want || lu.RecalculateInvoicedUsage || lu.InvoicedUsageAmountRefreshedAt == nil {
				t.Errorf("got invoiced = %d, flagged %t, refreshed at %v, want = %d, false, set", lu.InvoicedUsageAmount, lu.RecalculateInvoicedUsage, lu.InvoicedUsageAmountRefreshedAt, tc.want)
			}
			// source: spec/services/lifetime_usages/calculate_service_spec.rb:48
			if lu.CurrentUsageAmountRefreshedAt == nil {
				t.Errorf("got current usage refreshed at = nil, want it set")
			}
			if got := f.reload(f.sub); got.InvoicedUsageAmount != tc.want {
				t.Errorf("got stored invoiced = %d, want = %d", got.InvoicedUsageAmount, tc.want)
			}
		})
	}
}

func TestRefreshLifetimeUsageCurrent(t *testing.T) {
	t.Parallel()
	now := billingtest.Date(2024, time.May, 15)
	currentFlag := func(lu *subscription.LifetimeUsage) { lu.RecalculateCurrentUsage = true }
	// source: spec/services/lifetime_usages/calculate_service_spec.rb:149
	t.Run("without usage", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, now, "10")
		f.lifetime(f.sub, currentFlag)
		lu := f.refresh(f.sub)
		// source: spec/services/lifetime_usages/calculate_service_spec.rb:157
		if lu.CurrentUsageAmount != 0 || lu.CurrentUsageAmountRefreshedAt == nil || lu.InvoicedUsageAmountRefreshedAt != nil {
			t.Errorf("got lifetime usage = %+v, want zero current usage refreshed, invoiced untouched", lu)
		}
	})
	// source: spec/services/lifetime_usages/calculate_service_spec.rb:178
	// source: spec/services/lifetime_usages/calculate_service_spec.rb:87
	// source: spec/services/lifetime_usages/calculate_service_spec.rb:93
	t.Run("with usage and invoices", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, now, "10")
		f.lifetime(f.sub, currentFlag, func(lu *subscription.LifetimeUsage) { lu.RecalculateInvoicedUsage = true })
		f.chargeFees(f.invoiceOf(f.sub, invoice.TypeSubscription, invoice.StatusFinalized), f.sub, 2, 100)
		f.events(f.sub, 2, now.Add(-time.Hour))
		lu := f.refresh(f.sub)
		if lu.CurrentUsageAmount != 2000 || lu.InvoicedUsageAmount != 200 || lu.RecalculateCurrentUsage || lu.RecalculateInvoicedUsage {
			t.Errorf("got current = %d, invoiced = %d, flags %t %t, want = 2000, 200, false false", lu.CurrentUsageAmount, lu.InvoicedUsageAmount, lu.RecalculateCurrentUsage, lu.RecalculateInvoicedUsage)
		}
	})
	// source: spec/services/lifetime_usages/calculate_service_spec.rb:164
	// source: spec/services/lifetime_usages/calculate_service_spec.rb:168
	t.Run("terminated subscription", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, now, "10")
		f.lifetime(f.sub, currentFlag, func(lu *subscription.LifetimeUsage) { lu.RecalculateInvoicedUsage = true })
		f.UpdateSubscription(f.sub, func(s *subscription.Subscription) { s.Status = subscription.StatusTerminated })
		f.events(f.sub, 2, now.Add(-time.Hour))
		lu := f.refresh(f.sub)
		if lu.RecalculateCurrentUsage || lu.RecalculateInvoicedUsage || lu.CurrentUsageAmountRefreshedAt != nil || lu.CurrentUsageAmount != 0 {
			t.Errorf("got lifetime usage = %+v, want flags cleared and nothing refreshed", lu)
		}
	})
	t.Run("missing lifetime usage is created", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, now, "10")
		f.events(f.sub, 1, now.Add(-time.Hour))
		if lu := f.refresh(f.sub); lu.CurrentUsageAmount != 1000 || f.reload(f.sub).CurrentUsageAmount != 1000 {
			t.Errorf("got current = %d, want = 1000 stored", lu.CurrentUsageAmount)
		}
	})
}

func TestFlagRefreshFromInvoice(t *testing.T) {
	t.Parallel()
	now := billingtest.Date(2024, time.May, 15)
	flag := func(f *fixture, inv *invoice.Invoice) []*subscription.LifetimeUsage {
		return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*subscription.LifetimeUsage, error) {
			return f.svc.FlagRefreshFromInvoice(ctx, r, inv)
		})
	}
	// source: spec/services/lifetime_usages/flag_refresh_from_invoice_service_spec.rb:23
	t.Run("flags the lifetime usages", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, now, "10")
		f.lifetime(f.sub)
		if got := flag(f, f.invoiceOf(f.sub, invoice.TypeSubscription, invoice.StatusFinalized)); len(got) != 1 || !f.reload(f.sub).RecalculateInvoicedUsage {
			t.Errorf("got %d flagged, want = 1 stored flagged", len(got))
		}
	})
	// source: spec/services/lifetime_usages/flag_refresh_from_invoice_service_spec.rb:32
	t.Run("not a subscription invoice", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, now, "10")
		lu := f.lifetime(f.sub)
		if got := flag(f, f.invoiceOf(f.sub, invoice.TypeOneOff, invoice.StatusFinalized)); len(got) != 0 || f.reload(f.sub).RecalculateInvoicedUsage {
			t.Errorf("got %d flagged, want none (%+v)", len(got), lu)
		}
	})
	// source: spec/services/lifetime_usages/flag_refresh_from_invoice_service_spec.rb:44
	t.Run("creates a missing lifetime usage", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, now, "10")
		flag(f, f.invoiceOf(f.sub, invoice.TypeSubscription, invoice.StatusFinalized))
		if lu := f.reload(f.sub); !lu.RecalculateInvoicedUsage || lu.SubscriptionID != f.sub.ID || lu.Currency != billingtest.USD {
			t.Errorf("got lifetime usage = %+v, want a flagged one of %s", lu, f.sub.ID)
		}
	})
}

func TestFlagRefreshFromPlanUpdate(t *testing.T) {
	t.Parallel()
	now := billingtest.Date(2024, time.May, 15)
	f := newFixture(t, now, "10")
	// source: spec/services/lifetime_usages/flag_refresh_from_plan_update_service_spec.rb:13
	count := func() int {
		return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (int, error) {
			return f.svc.FlagRefreshFromPlanUpdate(ctx, r, f.plan)
		})
	}
	other := f.Subscription(f.cust, f.plan)
	// source: spec/services/lifetime_usages/flag_refresh_from_plan_update_service_spec.rb:48
	if got := count(); got != 0 {
		t.Errorf("without lifetime usages: got = %d, want = 0", got)
	}
	terminated := f.Subscription(f.cust, f.plan, status(subscription.StatusTerminated))
	otherPlan := f.Plan(f.plan.Interval, 0)
	foreign := f.Subscription(f.cust, otherPlan)
	for _, s := range []*subscription.Subscription{f.sub, other, terminated, foreign} {
		f.lifetime(s)
	}
	// source: spec/services/lifetime_usages/flag_refresh_from_plan_update_service_spec.rb:33
	// source: spec/services/lifetime_usages/flag_refresh_from_plan_update_service_spec.rb:66
	if got := count(); got != 2 {
		t.Errorf("got = %d, want = 2", got)
	}
	for s, want := range map[*subscription.Subscription]bool{f.sub: true, other: true, terminated: false, foreign: false} {
		if got := f.reload(s).RecalculateInvoicedUsage; got != want {
			t.Errorf("subscription %s: got flagged = %t, want = %t", s.ID, got, want)
		}
	}
}

func TestUpdateHistoricalUsage(t *testing.T) {
	t.Parallel()
	now := billingtest.Date(2024, time.May, 15)
	cases := []struct {
		name    string
		missing bool
		amount  int64
		wantErr error
	}{
		// source: spec/services/lifetime_usages/update_service_spec.rb:17
		{name: "updates the historical usage", amount: 20},
		// source: spec/services/lifetime_usages/update_service_spec.rb:27
		{name: "without lifetime usage", missing: true, amount: 20, wantErr: core.ErrNotFound},
		// source: spec/services/lifetime_usages/update_service_spec.rb:38
		{name: "negative amount", amount: -20, wantErr: core.ErrInvalidValue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, now, "10")
			if !tc.missing {
				f.lifetime(f.sub)
			}
			err := f.Deps.Tx.InTx(f.Ctx, func(ctx context.Context, r port.Repos) error {
				_, err := f.svc.UpdateHistoricalUsage(ctx, r, f.OrgID(), f.sub.ExternalID, tc.amount)
				return err
			})
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("got error = %v, want = %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("update: %v", err)
			}
			if got := f.reload(f.sub).HistoricalUsageAmount; got != tc.amount {
				t.Errorf("got historical = %d, want = %d", got, tc.amount)
			}
		})
	}
}
