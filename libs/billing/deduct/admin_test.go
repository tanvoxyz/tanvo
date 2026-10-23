package deduct_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/deduct"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// newCoupon returns an unsaved fixed amount coupon of 100 USD cents used
// once, expiring in three days.
func newCoupon(h *billingtest.Harness, code string) *coupon.Coupon {
	c := coupon.New(h.OrgID(), core.CouponID{}, code, "Free beer", coupon.TypeFixedAmount, h.Now())
	exp := h.Now().AddDate(0, 0, 3)
	c.Amount, c.Currency, c.Frequency = ptr[int64](100), billingtest.USD, coupon.FrequencyOnce
	c.Expiration, c.ExpirationAt = coupon.ExpirationTimeLimit, &exp
	return c
}

// wantValidation checks that err is a validation error holding code for
// field.
func wantValidation(t *testing.T, err error, field, code string) {
	t.Helper()
	var ve *core.ValidationError
	if !errors.As(err, &ve) || !ve.Has(field, code) {
		t.Fatalf("got error = %v, want = %s %s", err, field, code)
	}
}

// wantRule checks that err is a rule error with code.
func wantRule(t *testing.T, err error, code string) {
	t.Helper()
	var re *core.RuleError
	if !errors.As(err, &re) || re.Code != code {
		t.Fatalf("got error = %v, want = %s", err, code)
	}
}

func TestCreateCoupon(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(h *billingtest.Harness, c *coupon.Coupon, p *catalog.Plan, m *catalog.BillableMetric)
		wantErr func(t *testing.T, err error)
	}{
		// source: spec/services/coupons/create_service_spec.rb:30
		{name: "fixed amount"},
		// source: spec/services/coupons/create_service_spec.rb:67
		{name: "percentage", mutate: func(_ *billingtest.Harness, c *coupon.Coupon, _ *catalog.Plan, _ *catalog.BillableMetric) {
			r := money.MustParse("10")
			c.Type, c.Amount, c.Currency, c.PercentageRate = coupon.TypePercentage, nil, money.Currency{}, &r
		}},
		// source: spec/services/coupons/create_service_spec.rb:87
		{
			name: "code already used",
			mutate: func(h *billingtest.Harness, c *coupon.Coupon, _ *catalog.Plan, _ *catalog.BillableMetric) {
				h.FixedAmountCoupon(10, func(o *coupon.Coupon) { o.Code = c.Code })
			},
			wantErr: func(t *testing.T, err error) { wantValidation(t, err, "code", core.CodeAlreadyExists) },
		},
		// source: spec/services/coupons/create_service_spec.rb:99
		{
			name: "expiration in the past",
			mutate: func(h *billingtest.Harness, c *coupon.Coupon, _ *catalog.Plan, _ *catalog.BillableMetric) {
				past := h.Now().AddDate(0, 0, -3)
				c.ExpirationAt = &past
			},
			wantErr: func(t *testing.T, err error) { wantValidation(t, err, "expiration_at", core.CodeNotInFuture) },
		},
		// source: spec/services/coupons/create_service_spec.rb:135
		{
			name: "recurring without duration",
			mutate: func(_ *billingtest.Harness, c *coupon.Coupon, _ *catalog.Plan, _ *catalog.BillableMetric) {
				c.Frequency = coupon.FrequencyRecurring
			},
			wantErr: func(t *testing.T, err error) { wantValidation(t, err, "frequency_duration", core.CodeRequired) },
		},
		// source: spec/services/coupons/create_service_spec.rb:167
		// source: spec/services/coupons/create_service_spec.rb:200
		{name: "plan limitation", mutate: func(_ *billingtest.Harness, c *coupon.Coupon, p *catalog.Plan, _ *catalog.BillableMetric) {
			c.LimitedPlanIDs = []core.PlanID{p.ID}
		}},
		// source: spec/services/coupons/create_service_spec.rb:249
		// source: spec/services/coupons/create_service_spec.rb:340
		{name: "billable metric limitation", mutate: func(_ *billingtest.Harness, c *coupon.Coupon, _ *catalog.Plan, m *catalog.BillableMetric) {
			c.LimitedBillableMetricIDs = []core.BillableMetricID{m.ID}
		}},
		// source: spec/services/coupons/create_service_spec.rb:280
		{
			name: "both limitation types",
			mutate: func(_ *billingtest.Harness, c *coupon.Coupon, p *catalog.Plan, m *catalog.BillableMetric) {
				c.LimitedPlanIDs, c.LimitedBillableMetricIDs = []core.PlanID{p.ID}, []core.BillableMetricID{m.ID}
			},
			wantErr: func(t *testing.T, err error) { wantRule(t, err, coupon.CodeOnlyOneLimitationType) },
		},
		// source: spec/services/coupons/create_service_spec.rb:308
		{
			name: "unknown billable metric",
			mutate: func(h *billingtest.Harness, c *coupon.Coupon, _ *catalog.Plan, _ *catalog.BillableMetric) {
				c.LimitedBillableMetricIDs = []core.BillableMetricID{core.NewID[core.BillableMetricID](h.IDs)}
			},
			wantErr: func(t *testing.T, err error) {
				if !errors.Is(err, core.ErrNotFound) {
					t.Fatalf("got error = %v, want = %v", err, core.ErrNotFound)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			h := f.h
			p := h.Plan(period.Monthly, 100)
			m := h.Metric("api", catalog.AggregationTypeCount, "")
			c := newCoupon(h, "free-beer")
			if tt.mutate != nil {
				tt.mutate(h, c, p, m)
			}
			err := f.s.CreateCoupon(h.Ctx, c)
			if tt.wantErr != nil {
				tt.wantErr(t, err)
				return
			}
			mustOK(t, err)
			got := readCoupon(h, c.ID)
			eq(t, "status", got.Status, coupon.StatusActive)
			eq(t, "code", got.Code, "free-beer")
			eq(t, "plans", len(got.LimitedPlanIDs), len(c.LimitedPlanIDs))
			eq(t, "metrics", len(got.LimitedBillableMetricIDs), len(c.LimitedBillableMetricIDs))
		})
	}
}

// source: spec/services/coupons/create_service_spec.rb:42
func TestCreateCouponCodeOfDestroyed(t *testing.T) {
	f := newFixture(t)
	old := f.h.FixedAmountCoupon(10, func(c *coupon.Coupon) { c.Code = "free-beer" })
	_, err := f.s.DestroyCoupon(f.h.Ctx, f.h.OrgID(), old.ID)
	mustOK(t, err)
	mustOK(t, f.s.CreateCoupon(f.h.Ctx, newCoupon(f.h, "free-beer")))
}

// source: spec/services/coupons/create_service_spec.rb:215
// A child plan's fees match a coupon limited to its parent.
func TestCouponParentPlan(t *testing.T) {
	f := newFixture(t)
	h := f.h
	parent := h.Plan(period.Monthly, 100)
	child := h.Plan(period.Monthly, 100, func(p *catalog.Plan) { p.ParentID = parent.ID })
	sub := h.Subscription(f.cust, child)
	c := newCoupon(h, "parent")
	c.LimitedPlanIDs = []core.PlanID{parent.ID}
	mustOK(t, f.s.CreateCoupon(h.Ctx, c))
	h.ApplyCoupon(readCoupon(h, c.ID), f.cust)
	inv := h.StoredInvoice(f.cust)
	h.StoredFee(inv, 300, billingtest.SubscriptionFee(sub))
	out, _, err := f.before(inv, deduct.Options{})
	mustOK(t, err)
	eq(t, "coupons", out.Coupons, 100)
}

func readCoupon(h *billingtest.Harness, id core.CouponID) *coupon.Coupon {
	h.T.Helper()
	return readCouponOf(h, h.OrgID(), id)
}

func readCouponOf(h *billingtest.Harness, org core.OrganizationID, id core.CouponID) *coupon.Coupon {
	h.T.Helper()
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) (*coupon.Coupon, error) {
		return r.Coupons.Get(ctx, org, id)
	})
}

func TestUpdateCoupon(t *testing.T) {
	tests := []struct {
		name    string
		applied bool
		fn      func(h *billingtest.Harness, c *coupon.Coupon, p []*catalog.Plan, m []*catalog.BillableMetric)
		wantErr func(t *testing.T, err error)
		check   func(t *testing.T, got *coupon.Coupon, p []*catalog.Plan, m []*catalog.BillableMetric)
	}{
		// source: spec/services/coupons/update_service_spec.rb:32
		{
			name: "name and expiration",
			fn: func(h *billingtest.Harness, c *coupon.Coupon, _ []*catalog.Plan, _ []*catalog.BillableMetric) {
				exp := h.Now().AddDate(0, 0, 30)
				c.Name, c.Expiration, c.ExpirationAt = "new name", coupon.ExpirationTimeLimit, &exp
			},
			check: func(t *testing.T, got *coupon.Coupon, _ []*catalog.Plan, _ []*catalog.BillableMetric) {
				eq(t, "name", got.Name, "new name")
				eq(t, "amount", *got.Amount, 10)
				eq(t, "expiration", got.Expiration, coupon.ExpirationTimeLimit)
			},
		},
		// source: spec/services/coupons/update_service_spec.rb:54
		{
			name: "missing name",
			fn: func(_ *billingtest.Harness, c *coupon.Coupon, _ []*catalog.Plan, _ []*catalog.BillableMetric) {
				c.Name = ""
			},
			wantErr: func(t *testing.T, err error) { wantValidation(t, err, "name", core.CodeRequired) },
		},
		// source: spec/services/coupons/update_service_spec.rb:76
		{
			name: "recurring without duration",
			fn: func(_ *billingtest.Harness, c *coupon.Coupon, _ []*catalog.Plan, _ []*catalog.BillableMetric) {
				c.Frequency = coupon.FrequencyRecurring
			},
			wantErr: func(t *testing.T, err error) { wantValidation(t, err, "frequency_duration", core.CodeRequired) },
		},
		// source: spec/services/coupons/update_service_spec.rb:98
		{
			name: "new plan limitations",
			fn: func(_ *billingtest.Harness, c *coupon.Coupon, p []*catalog.Plan, _ []*catalog.BillableMetric) {
				c.LimitedPlanIDs = []core.PlanID{p[0].ID, p[1].ID}
			},
			check: func(t *testing.T, got *coupon.Coupon, _ []*catalog.Plan, _ []*catalog.BillableMetric) {
				eq(t, "plans", len(got.LimitedPlanIDs), 2)
			},
		},
		// source: spec/services/coupons/update_service_spec.rb:124
		{
			name: "plans removed",
			fn: func(_ *billingtest.Harness, c *coupon.Coupon, _ []*catalog.Plan, _ []*catalog.BillableMetric) {
				c.LimitedPlanIDs = nil
			},
			check: func(t *testing.T, got *coupon.Coupon, _ []*catalog.Plan, _ []*catalog.BillableMetric) {
				eq(t, "plans", len(got.LimitedPlanIDs), 0)
			},
		},
		// source: spec/services/coupons/update_service_spec.rb:142
		{
			name: "new billable metric limitations",
			fn: func(_ *billingtest.Harness, c *coupon.Coupon, _ []*catalog.Plan, m []*catalog.BillableMetric) {
				c.LimitedPlanIDs, c.LimitedBillableMetricIDs = nil, []core.BillableMetricID{m[0].ID, m[1].ID}
			},
			check: func(t *testing.T, got *coupon.Coupon, _ []*catalog.Plan, _ []*catalog.BillableMetric) {
				eq(t, "metrics", len(got.LimitedBillableMetricIDs), 2)
			},
		},
		// source: spec/services/coupons/update_service_spec.rb:165
		{
			name: "both limitation types",
			fn: func(_ *billingtest.Harness, c *coupon.Coupon, _ []*catalog.Plan, m []*catalog.BillableMetric) {
				c.LimitedBillableMetricIDs = []core.BillableMetricID{m[0].ID}
			},
			wantErr: func(t *testing.T, err error) { wantRule(t, err, coupon.CodeOnlyOneLimitationType) },
		},
		// source: spec/services/coupons/update_service_spec.rb:181
		{
			name: "unknown billable metric",
			fn: func(h *billingtest.Harness, c *coupon.Coupon, _ []*catalog.Plan, _ []*catalog.BillableMetric) {
				c.LimitedPlanIDs, c.LimitedBillableMetricIDs = nil, []core.BillableMetricID{core.NewID[core.BillableMetricID](h.IDs)}
			},
			wantErr: func(t *testing.T, err error) {
				if !errors.Is(err, core.ErrNotFound) {
					t.Fatalf("got error = %v, want = %v", err, core.ErrNotFound)
				}
			},
		},
		// source: spec/services/coupons/update_service_spec.rb:32
		{
			name: "amount of an applied coupon", applied: true,
			fn: func(_ *billingtest.Harness, c *coupon.Coupon, _ []*catalog.Plan, _ []*catalog.BillableMetric) {
				c.Amount = ptr[int64](50)
			},
			check: func(t *testing.T, got *coupon.Coupon, _ []*catalog.Plan, _ []*catalog.BillableMetric) {
				eq(t, "amount", *got.Amount, int64(10))
			},
		},
		{
			name: "name of an applied coupon", applied: true,
			fn: func(_ *billingtest.Harness, c *coupon.Coupon, _ []*catalog.Plan, _ []*catalog.BillableMetric) {
				c.Name = "renamed"
			},
			check: func(t *testing.T, got *coupon.Coupon, _ []*catalog.Plan, _ []*catalog.BillableMetric) {
				eq(t, "name", got.Name, "renamed")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			h := f.h
			plans := []*catalog.Plan{h.Plan(period.Monthly, 100), h.Plan(period.Monthly, 100)}
			metrics := []*catalog.BillableMetric{h.Metric("a", catalog.AggregationTypeCount, ""), h.Metric("b", catalog.AggregationTypeCount, "")}
			c := h.FixedAmountCoupon(10, func(c *coupon.Coupon) { c.LimitedPlanIDs = []core.PlanID{plans[0].ID} })
			if tt.applied {
				h.ApplyCoupon(c, f.cust)
			}
			got, err := f.s.UpdateCoupon(h.Ctx, h.OrgID(), c.ID, func(c *coupon.Coupon) { tt.fn(h, c, plans, metrics) })
			if tt.wantErr != nil {
				tt.wantErr(t, err)
				return
			}
			mustOK(t, err)
			tt.check(t, got, plans, metrics)
			tt.check(t, readCoupon(h, c.ID), plans, metrics)
		})
	}
}

// source: spec/services/coupons/update_service_spec.rb:210
// source: spec/services/coupons/destroy_service_spec.rb:51
func TestCouponNotFound(t *testing.T) {
	f := newFixture(t)
	id := core.NewID[core.CouponID](f.h.IDs)
	_, err := f.s.UpdateCoupon(f.h.Ctx, f.h.OrgID(), id, func(*coupon.Coupon) {})
	if !errors.Is(err, core.ErrNotFound) {
		t.Errorf("update: got error = %v, want = %v", err, core.ErrNotFound)
	}
	_, err = f.s.DestroyCoupon(f.h.Ctx, f.h.OrgID(), id)
	if !errors.Is(err, core.ErrNotFound) {
		t.Errorf("destroy: got error = %v, want = %v", err, core.ErrNotFound)
	}
}

// source: spec/services/coupons/destroy_service_spec.rb:18
// source: spec/services/coupons/destroy_service_spec.rb:39
func TestDestroyCoupon(t *testing.T) {
	f := newFixture(t)
	c := f.h.FixedAmountCoupon(10)
	a := f.h.ApplyCoupon(c, f.cust)
	got, err := f.s.DestroyCoupon(f.h.Ctx, f.h.OrgID(), c.ID)
	mustOK(t, err)
	eq(t, "coupon status", got.Status, coupon.StatusActive)
	eq(t, "coupon deleted", got.Deleted(), true)
	eq(t, "applied coupon status", f.h.ReloadAppliedCoupon(a.ID).Status, coupon.StatusTerminated)
}

func TestTerminateCoupon(t *testing.T) {
	// source: spec/services/coupons/terminate_service_spec.rb:14
	t.Run("active", func(t *testing.T) {
		f := newFixture(t)
		c := f.h.FixedAmountCoupon(10)
		a := f.h.ApplyCoupon(c, f.cust)
		got, err := f.s.TerminateCoupon(f.h.Ctx, f.h.OrgID(), c.ID)
		mustOK(t, err)
		eq(t, "status", got.Status, coupon.StatusTerminated)
		eq(t, "applied coupon keeps applying", f.h.ReloadAppliedCoupon(a.ID).Status, coupon.StatusActive)
	})
	// source: spec/services/coupons/terminate_service_spec.rb:24
	t.Run("already terminated", func(t *testing.T) {
		f := newFixture(t)
		c := f.h.FixedAmountCoupon(10)
		first, err := f.s.TerminateCoupon(f.h.Ctx, f.h.OrgID(), c.ID)
		mustOK(t, err)
		f.h.Clock.Advance(time.Hour)
		got, err := f.s.TerminateCoupon(f.h.Ctx, f.h.OrgID(), c.ID)
		mustOK(t, err)
		eq(t, "terminated at", got.TerminatedAt.Equal(*first.TerminatedAt), true)
	})
}

// source: spec/services/coupons/terminate_service_spec.rb:67
func TestTerminateExpiredCoupons(t *testing.T) {
	f := newFixture(t)
	h := f.h
	var expired, kept []*coupon.Coupon
	for range 3 {
		expired = append(expired, h.FixedAmountCoupon(10, func(c *coupon.Coupon) {
			at := h.Now().AddDate(0, 0, 30)
			c.Expiration, c.ExpirationAt = coupon.ExpirationTimeLimit, &at
		}))
		kept = append(kept, h.FixedAmountCoupon(10, func(c *coupon.Coupon) {
			at := h.Now().AddDate(0, 0, 45)
			c.Expiration, c.ExpirationAt = coupon.ExpirationTimeLimit, &at
		}))
	}
	other := core.NewID[core.OrganizationID](h.IDs)
	foreign := h.FixedAmountCoupon(10, func(c *coupon.Coupon) {
		at := h.Now().AddDate(0, 0, 30)
		c.OrganizationID, c.Expiration, c.ExpirationAt = other, coupon.ExpirationTimeLimit, &at
	})
	tick := h.Now().Add(40 * 24 * time.Hour)
	// The worker runs the tick's job after the kept coupons expired: the
	// tick's instant decides.
	h.Clock.Set(tick.AddDate(0, 0, 10))
	n, err := f.s.TerminateExpiredCoupons(h.Ctx, h.OrgID(), tick)
	mustOK(t, err)
	eq(t, "terminated", n, 3)
	n, err = f.s.TerminateExpiredCoupons(h.Ctx, h.OrgID(), tick)
	mustOK(t, err)
	eq(t, "rerun terminated", n, 0)
	eq(t, "other organization status", readCouponOf(h, other, foreign.ID).Status, coupon.StatusActive)
	for _, c := range expired {
		eq(t, "expired status", readCoupon(h, c.ID).Status, coupon.StatusTerminated)
	}
	for _, c := range kept {
		eq(t, "kept status", readCoupon(h, c.ID).Status, coupon.StatusActive)
	}
}

// source: spec/services/coupons/validate_service_spec.rb:26
// source: spec/services/coupons/validate_service_spec.rb:33
func TestValidateCoupon(t *testing.T) {
	h := billingtest.New(t, billingtest.Date(2024, time.March, 1))
	c := newCoupon(h, "valid")
	mustOK(t, c.Validate(h.Now()))
	past := h.Now().AddDate(0, 0, -10)
	c.ExpirationAt = &past
	wantValidation(t, c.Validate(h.Now()), "expiration_at", core.CodeNotInFuture)
}

func TestApplyCouponToCustomer(t *testing.T) {
	tests := []struct {
		name      string
		coupon    func(h *billingtest.Harness, p *catalog.Plan, m *catalog.BillableMetric) *coupon.Coupon
		existing  func(h *billingtest.Harness, p *catalog.Plan, m *catalog.BillableMetric) *coupon.Coupon
		reapply   bool
		overrides coupon.Overrides
		noCcy     bool
		wantErr   func(t *testing.T, err error)
		check     func(t *testing.T, a *coupon.AppliedCoupon, cust *customer.Customer)
	}{
		// source: spec/services/applied_coupons/create_service_spec.rb:37
		{
			name: "fixed amount",
			check: func(t *testing.T, a *coupon.AppliedCoupon, _ *customer.Customer) {
				eq(t, "amount", *a.Amount, 100)
				eq(t, "currency", a.Currency.Code(), "USD")
				eq(t, "status", a.Status, coupon.StatusActive)
			},
		},
		// source: spec/services/applied_coupons/create_service_spec.rb:71
		// source: spec/services/applied_coupons/create_service_spec.rb:75
		{
			name: "percentage", noCcy: true,
			coupon: func(h *billingtest.Harness, _ *catalog.Plan, _ *catalog.BillableMetric) *coupon.Coupon {
				return h.PercentageCoupon("10")
			},
			overrides: coupon.Overrides{PercentageRate: ptr(money.MustParse("20"))},
			check: func(t *testing.T, a *coupon.AppliedCoupon, cust *customer.Customer) {
				eqDec(t, "rate", *a.PercentageRate, "20")
				eq(t, "customer currency unset", cust.Currency.IsZero(), true)
			},
		},
		// source: spec/services/applied_coupons/create_service_spec.rb:85
		{
			name: "other coupon already applied",
			existing: func(h *billingtest.Harness, _ *catalog.Plan, _ *catalog.BillableMetric) *coupon.Coupon {
				return h.FixedAmountCoupon(50)
			},
			check: func(t *testing.T, a *coupon.AppliedCoupon, _ *customer.Customer) { eq(t, "amount", *a.Amount, 100) },
		},
		// source: spec/services/applied_coupons/create_service_spec.rb:100
		// source: spec/services/applied_coupons/create_service_spec.rb:101
		{
			name: "overridden amount", overrides: coupon.Overrides{Amount: ptr[int64](123), Currency: billingtest.USD},
			check: func(t *testing.T, a *coupon.AppliedCoupon, _ *customer.Customer) {
				eq(t, "amount", *a.Amount, 123)
				eq(t, "currency", a.Currency.Code(), "USD")
			},
		},
		// source: spec/services/applied_coupons/create_service_spec.rb:108
		{
			name: "overridden currency mismatch", overrides: coupon.Overrides{Amount: ptr[int64](123), Currency: money.MustCurrency("NOK")},
			check: func(t *testing.T, a *coupon.AppliedCoupon, cust *customer.Customer) {
				eq(t, "currency", a.Currency.Code(), "NOK")
				eq(t, "customer currency", cust.Currency.Code(), "USD")
			},
		},
		// source: spec/services/applied_coupons/create_service_spec.rb:141
		{
			name: "not reusable and already applied", reapply: true,
			coupon: func(h *billingtest.Harness, _ *catalog.Plan, _ *catalog.BillableMetric) *coupon.Coupon {
				return h.FixedAmountCoupon(100, func(c *coupon.Coupon) { c.Reusable = false })
			},
			wantErr: func(t *testing.T, err error) { wantValidation(t, err, "coupon", coupon.CodeNotReusable) },
		},
		// source: spec/services/applied_coupons/create_service_spec.rb:164
		{
			name: "same plan limitation",
			existing: func(h *billingtest.Harness, p *catalog.Plan, _ *catalog.BillableMetric) *coupon.Coupon {
				return h.FixedAmountCoupon(10, func(c *coupon.Coupon) { c.LimitedPlanIDs = []core.PlanID{p.ID} })
			},
			coupon: func(h *billingtest.Harness, p *catalog.Plan, _ *catalog.BillableMetric) *coupon.Coupon {
				return h.FixedAmountCoupon(10, func(c *coupon.Coupon) { c.LimitedPlanIDs = []core.PlanID{p.ID} })
			},
			wantErr: func(t *testing.T, err error) { wantRule(t, err, coupon.CodePlanOverlapping) },
		},
		// source: spec/services/applied_coupons/create_service_spec.rb:182
		{
			name: "metric of an applied plan limitation",
			existing: func(h *billingtest.Harness, p *catalog.Plan, _ *catalog.BillableMetric) *coupon.Coupon {
				return h.FixedAmountCoupon(10, func(c *coupon.Coupon) { c.LimitedPlanIDs = []core.PlanID{p.ID} })
			},
			coupon: func(h *billingtest.Harness, _ *catalog.Plan, m *catalog.BillableMetric) *coupon.Coupon {
				return h.FixedAmountCoupon(10, func(c *coupon.Coupon) { c.LimitedBillableMetricIDs = []core.BillableMetricID{m.ID} })
			},
			wantErr: func(t *testing.T, err error) { wantRule(t, err, coupon.CodePlanOverlapping) },
		},
		// source: spec/services/applied_coupons/create_service_spec.rb:203
		{
			name: "plan of an applied metric limitation",
			existing: func(h *billingtest.Harness, _ *catalog.Plan, m *catalog.BillableMetric) *coupon.Coupon {
				return h.FixedAmountCoupon(10, func(c *coupon.Coupon) { c.LimitedBillableMetricIDs = []core.BillableMetricID{m.ID} })
			},
			coupon: func(h *billingtest.Harness, p *catalog.Plan, _ *catalog.BillableMetric) *coupon.Coupon {
				return h.FixedAmountCoupon(10, func(c *coupon.Coupon) { c.LimitedPlanIDs = []core.PlanID{p.ID} })
			},
			wantErr: func(t *testing.T, err error) { wantRule(t, err, coupon.CodePlanOverlapping) },
		},
		// source: spec/services/applied_coupons/create_service_spec.rb:214
		{
			name: "terminated coupon",
			coupon: func(h *billingtest.Harness, _ *catalog.Plan, _ *catalog.BillableMetric) *coupon.Coupon {
				return h.FixedAmountCoupon(100, func(c *coupon.Coupon) {
					now := h.Now()
					c.Status, c.TerminatedAt = coupon.StatusTerminated, &now
				})
			},
			wantErr: func(t *testing.T, err error) {
				if !errors.Is(err, core.ErrNotFound) || err.Error() != "coupon_not_found" {
					t.Fatalf("got error = %v, want = coupon_not_found", err)
				}
			},
		},
		// source: spec/services/applied_coupons/create_service_spec.rb:226
		{
			name: "coupon currency differs from the customer's",
			coupon: func(h *billingtest.Harness, _ *catalog.Plan, _ *catalog.BillableMetric) *coupon.Coupon {
				return h.FixedAmountCoupon(100, func(c *coupon.Coupon) { c.Currency = money.MustCurrency("NOK") })
			},
			check: func(t *testing.T, a *coupon.AppliedCoupon, cust *customer.Customer) {
				eq(t, "currency", a.Currency.Code(), "NOK")
				eq(t, "customer currency", cust.Currency.Code(), "USD")
			},
		},
		// source: spec/services/applied_coupons/create_service_spec.rb:239
		{
			name: "customer without currency", noCcy: true, overrides: coupon.Overrides{Amount: ptr[int64](100), Currency: money.MustCurrency("NOK")},
			check: func(t *testing.T, a *coupon.AppliedCoupon, cust *customer.Customer) {
				eq(t, "customer currency", cust.Currency.Code(), "NOK")
			},
		},
		// source: spec/services/applied_coupons/create_service_spec.rb:260
		{
			name: "recurring without duration", overrides: coupon.Overrides{Frequency: coupon.FrequencyRecurring},
			wantErr: func(t *testing.T, err error) { wantValidation(t, err, "frequency_duration", core.CodeRequired) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			h := f.h
			if tt.noCcy {
				h.UpdateCustomer(f.cust, func(c *customer.Customer) { c.Currency = money.Currency{} })
			}
			p := h.Plan(period.Monthly, 100)
			m := h.Metric("api", catalog.AggregationTypeCount, "")
			h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
			if tt.existing != nil {
				h.ApplyCoupon(tt.existing(h, p, m), f.cust)
			}
			c := h.FixedAmountCoupon(100)
			if tt.coupon != nil {
				c = tt.coupon(h, p, m)
			}
			if tt.reapply {
				h.ApplyCoupon(c, f.cust)
			}
			a, err := f.s.ApplyCoupon(h.Ctx, deduct.ApplyCouponInput{
				OrganizationID: h.OrgID(), CouponID: c.ID, CustomerID: f.cust.ID, Overrides: tt.overrides,
			})
			if tt.wantErr != nil {
				tt.wantErr(t, err)
				return
			}
			mustOK(t, err)
			eq(t, "customer", a.CustomerID, f.cust.ID)
			eq(t, "coupon", a.CouponID, c.ID)
			stored := h.ReloadAppliedCoupon(a.ID)
			tt.check(t, stored, h.ReloadCustomer(f.cust.ID))
		})
	}
}

// source: spec/services/applied_coupons/create_service_spec.rb:119
// source: spec/services/applied_coupons/create_service_spec.rb:129
func TestApplyCouponToCustomerNotFound(t *testing.T) {
	f := newFixture(t)
	c := f.h.FixedAmountCoupon(100)
	_, err := f.s.ApplyCoupon(f.h.Ctx, deduct.ApplyCouponInput{OrganizationID: f.h.OrgID(), CouponID: c.ID, CustomerID: core.NewID[core.CustomerID](f.h.IDs)})
	if !errors.Is(err, core.ErrNotFound) {
		t.Errorf("customer: got error = %v, want = %v", err, core.ErrNotFound)
	}
	_, err = f.s.ApplyCoupon(f.h.Ctx, deduct.ApplyCouponInput{OrganizationID: f.h.OrgID(), CouponID: core.NewID[core.CouponID](f.h.IDs), CustomerID: f.cust.ID})
	if !errors.Is(err, core.ErrNotFound) {
		t.Errorf("coupon: got error = %v, want = %v", err, core.ErrNotFound)
	}
}

func TestTerminateAppliedCoupon(t *testing.T) {
	// source: spec/services/applied_coupons/terminate_service_spec.rb:14
	t.Run("active", func(t *testing.T) {
		f := newFixture(t)
		a := f.h.ApplyCoupon(f.h.FixedAmountCoupon(10), f.cust)
		got, err := f.s.TerminateAppliedCoupon(f.h.Ctx, f.h.OrgID(), a.ID)
		mustOK(t, err)
		eq(t, "status", got.Status, coupon.StatusTerminated)
	})
	// source: spec/services/applied_coupons/terminate_service_spec.rb:31
	t.Run("already terminated", func(t *testing.T) {
		f := newFixture(t)
		a := f.h.ApplyCoupon(f.h.FixedAmountCoupon(10), f.cust)
		first, err := f.s.TerminateAppliedCoupon(f.h.Ctx, f.h.OrgID(), a.ID)
		mustOK(t, err)
		f.h.Clock.Advance(time.Hour)
		got, err := f.s.TerminateAppliedCoupon(f.h.Ctx, f.h.OrgID(), a.ID)
		mustOK(t, err)
		eq(t, "status", got.Status, coupon.StatusTerminated)
		eq(t, "terminated at", got.TerminatedAt.Equal(*first.TerminatedAt), true)
	})
}
