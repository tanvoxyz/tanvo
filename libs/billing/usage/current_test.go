package usage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// customerUsageFixture is the setup of the lago spec: a standard charge of
// 12.66 with two events and a 20% billing entity tax.
func customerUsageFixture(t *testing.T, now time.Time, subOpts ...func(*subscription.Subscription)) *fixture {
	t.Helper()
	f := newFixture(t, now, "12.66", subOpts...)
	tax := f.Tax("vat", "20")
	f.UpdateBillingEntity(func(be *organization.BillingEntity) { be.TaxIDs = []core.TaxID{tax.ID} })
	f.events(f.sub, 2, now.Add(-time.Hour))
	return f
}

// usageOf computes the current usage of s.
func (f *fixture) usageOf(s *subscription.Subscription, opts usage.Options) (*usage.Usage, error) {
	f.T.Helper()
	var u *usage.Usage
	err := f.Deps.Tx.InTx(f.Ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		u, err = f.svc.CurrentUsage(ctx, r, f.OrgID(), s.ID, opts)
		return err
	})
	return u, err
}

func TestCurrentUsage(t *testing.T) {
	t.Parallel()
	type want struct {
		from, to             time.Time
		issuing              core.Date
		amount, taxes, total int64
		fees                 int
	}
	june := billingtest.Date(2025, time.June, 15)
	cases := []struct {
		name    string
		now     time.Time
		subOpts []func(*subscription.Subscription)
		opts    usage.Options
		want    want
	}{
		// source: spec/services/invoices/customer_usage_service_spec.rb:134
		{
			name: "initializes an invoice", now: june,
			want: want{billingtest.Date(2025, time.June, 1), billingtest.Date(2025, time.July, 1), core.NewDate(2025, time.June, 30), 2532, 506, 3038, 1},
		},
		// source: spec/services/invoices/customer_usage_service_spec.rb:165
		{
			name: "without taxes", now: june, opts: usage.Options{SkipTaxes: true},
			want: want{billingtest.Date(2025, time.June, 1), billingtest.Date(2025, time.July, 1), core.NewDate(2025, time.June, 30), 2532, 0, 2532, 1},
		},
		// source: spec/services/invoices/customer_usage_service_spec.rb:429
		{
			name: "subscription started in the current period", now: june,
			subOpts: []func(*subscription.Subscription){billingtest.SubscribedAt(billingtest.Date(2025, time.June, 10))},
			want:    want{billingtest.Date(2025, time.June, 10), billingtest.Date(2025, time.July, 1), core.NewDate(2025, time.June, 30), 2532, 506, 3038, 1},
		},
		// source: spec/services/invoices/customer_usage_service_spec.rb:454
		{
			name: "anniversary billing", now: billingtest.Date(2022, time.June, 22),
			subOpts: []func(*subscription.Subscription){billingtest.SubscribedAt(billingtest.Date(2022, time.March, 7)), billingtest.Anniversary},
			want:    want{billingtest.Date(2022, time.June, 7), billingtest.Date(2022, time.July, 7), core.NewDate(2022, time.July, 6), 2532, 506, 3038, 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := customerUsageFixture(t, tc.now, tc.subOpts...)
			u, err := f.usageOf(f.sub, tc.opts)
			if err != nil {
				t.Fatalf("current usage: %v", err)
			}
			got := want{u.From, u.To, u.IssuingDate, u.Amount, u.TaxesAmount, u.TotalAmount, len(u.Fees)}
			if !got.from.Equal(tc.want.from) || !got.to.Equal(tc.want.to) || got.issuing != tc.want.issuing ||
				got.amount != tc.want.amount || got.taxes != tc.want.taxes || got.total != tc.want.total || got.fees != tc.want.fees {
				t.Errorf("got usage = %+v, want = %+v", got, tc.want)
			}
			if u.Currency != billingtest.USD || u.Invoice.TotalAmount != tc.want.total || u.Invoice.PrepaidCreditAmount != 0 || u.Invoice.TotalPaidAmount != 0 {
				t.Errorf("got invoice = %+v, want a USD invoice totalling %d", u.Invoice, tc.want.total)
			}
			if got := f.Invoices(f.cust.ID); len(got) != 0 {
				t.Errorf("got %d stored invoices, want = 0", len(got))
			}
		})
	}
}

func TestCurrentUsageErrors(t *testing.T) {
	t.Parallel()
	// source: spec/services/invoices/customer_usage_service_spec.rb:490
	t.Run("no active subscription", func(t *testing.T) {
		t.Parallel()
		f := customerUsageFixture(t, billingtest.Date(2025, time.June, 15))
		s := f.Subscription(f.cust, f.plan, billingtest.SubscribedAt(billingtest.Date(2025, time.July, 1)))
		if _, err := f.usageOf(s, usage.Options{}); !errors.Is(err, usage.ErrNoActiveSubscription) || !errors.Is(err, core.ErrNotAllowed) {
			t.Errorf("got error = %v, want = %v", err, usage.ErrNoActiveSubscription)
		}
	})
	// source: spec/services/invoices/customer_usage_service_spec.rb:479
	t.Run("subscription not found", func(t *testing.T) {
		t.Parallel()
		f := customerUsageFixture(t, billingtest.Date(2025, time.June, 15))
		if _, err := f.usageOf(&subscription.Subscription{ID: core.NewID[core.SubscriptionID](f.IDs)}, usage.Options{}); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("got error = %v, want = %v", err, core.ErrNotFound)
		}
	})
}

// source: spec/services/invoices/customer_usage_service_spec.rb:525
func TestCurrentUsageChargeFilter(t *testing.T) {
	t.Parallel()
	f := customerUsageFixture(t, billingtest.Date(2025, time.June, 15))
	other := f.Metric("other", catalog.AggregationTypeCount, "")
	f.Charge(f.plan, other, catalog.ChargeModelStandard, `{"amount":"1"}`)
	u, err := f.usageOf(f.sub, usage.Options{ChargeIDs: []core.ChargeID{f.charge.ID}})
	if err != nil {
		t.Fatalf("current usage: %v", err)
	}
	if len(u.Fees) != 1 || u.Fees[0].ChargeID != f.charge.ID {
		t.Errorf("got fees = %+v, want the fee of charge %s only", u.Fees, f.charge.ID)
	}
	all, err := f.usageOf(f.sub, usage.Options{})
	if err != nil {
		t.Fatalf("current usage: %v", err)
	}
	// Fees are sorted by metric name: "calls" before "other".
	if len(all.Fees) != 2 || all.Fees[0].ChargeID != f.charge.ID {
		t.Errorf("got %d fees, first of charge %s, want = 2, first of %s", len(all.Fees), all.Fees[0].ChargeID, f.charge.ID)
	}
}

// source: spec/services/invoices/customer_usage_service_spec.rb:109
func TestCurrentUsageProjection(t *testing.T) {
	t.Parallel()
	f := customerUsageFixture(t, billingtest.Date(2025, time.June, 15, 12))
	u, err := f.usageOf(f.sub, usage.Options{Projection: true})
	if err != nil {
		t.Fatalf("current usage: %v", err)
	}
	if len(u.Projections) != 1 {
		t.Fatalf("got %d projections, want = 1", len(u.Projections))
	}
	// 15 of 30 days passed: 2 units project to 4, priced 4 × 12.66.
	if p := u.Projections[0]; !p.Units.Equal(money.NewFromInt(4)) || p.Amount != 5064 {
		t.Errorf("got projection = (%s, %d), want = (4, 5064)", p.Units, p.Amount)
	}
	plain, err := f.usageOf(f.sub, usage.Options{})
	if err != nil {
		t.Fatalf("current usage: %v", err)
	}
	// source: spec/services/invoices/customer_usage_service_spec.rb:95
	if plain.Projections != nil {
		t.Errorf("got projections = %+v, want none", plain.Projections)
	}
}

func TestCustomerUsage(t *testing.T) {
	t.Parallel()
	f := customerUsageFixture(t, billingtest.Date(2025, time.June, 15))
	s2 := f.Subscription(f.cust, f.plan, billingtest.SubscribedAt(billingtest.Date(2025, time.January, 1)))
	f.events(s2, 1, billingtest.Date(2025, time.June, 2))
	f.Subscription(f.cust, f.plan, billingtest.SubscribedAt(billingtest.Date(2025, time.July, 1)))
	us := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*usage.Usage, error) {
		return f.svc.CustomerUsage(ctx, r, f.OrgID(), f.cust.ID, usage.Options{SkipTaxes: true})
	})
	if len(us) != 2 || us[0].Amount+us[1].Amount != 2532+1266 {
		t.Errorf("got %d usages, want = 2 totalling %d", len(us), 2532+1266)
	}
}
