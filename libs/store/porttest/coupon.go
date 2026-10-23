package porttest

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

func testCoupon(t *testing.T, h Harness) {
	t.Run("coupons", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org := a.OrganizationID
		fixed := couponFixtureFixed(e, org, "fixed")
		fixed.Description = ptr("Ten euros off")
		fixed.Frequency = coupon.FrequencyRecurring
		fixed.FrequencyDuration = ptr(3)
		fixed.Reusable = false
		fixed.Expiration = coupon.ExpirationTimeLimit
		fixed.ExpirationAt = ptr(at(30 * 24 * time.Hour))
		pct := couponFixturePercentage(e, org, "pct", "12.345")
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create() fixed amount", r.Coupons.Create(ctx, fixed))
			noErr(t, "Create() percentage", r.Coupons.Create(ctx, pct))
			wantErr(t, "Create() with the same ID", r.Coupons.Create(ctx, fixed), core.ErrConflict)
			for _, c := range []*coupon.Coupon{fixed, pct} {
				got, err := r.Coupons.Get(ctx, org, c.ID)
				noErr(t, "Get()", err)
				equal(t, "Get()", got, c)
			}
			got, err := r.Coupons.GetByCode(ctx, org, "pct")
			noErr(t, "GetByCode()", err)
			equal(t, "GetByCode()", got, pct)
			_, err = r.Coupons.Get(ctx, org, core.NewID[core.CouponID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)
			_, err = r.Coupons.GetByCode(ctx, org, "nope")
			wantErr(t, "GetByCode() unknown", err, core.ErrNotFound)

			// Isolation.
			_, err = r.Coupons.Get(ctx, b.OrganizationID, fixed.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.Coupons.GetByCode(ctx, b.OrganizationID, "fixed")
			wantErr(t, "GetByCode() from another organization", err, core.ErrNotFound)
			other := *fixed
			other.OrganizationID = b.OrganizationID
			wantErr(t, "Update() through another organization", r.Coupons.Update(ctx, &other), core.ErrNotFound)
			p, err := r.Coupons.List(ctx, b.OrganizationID, coupon.Filter{}, core.PageRequest{})
			noErr(t, "List() of another organization", err)
			equal(t, "List() of another organization", len(p.Items), 0)
			noErr(t, "Create() with the code in another organization", r.Coupons.Create(ctx, couponFixtureFixed(e, b.OrganizationID, "fixed")))

			// Codes are unique among active coupons.
			wantErr(t, "Create() with a taken code", r.Coupons.Create(ctx, couponFixtureFixed(e, org, "fixed")), core.ErrConflict)
			pct.Code = "fixed"
			wantErr(t, "Update() to a taken code", r.Coupons.Update(ctx, pct), core.ErrConflict)
			pct.Code = "pct"

			// Update every field, then clear the optional ones.
			fixed.Name = "Renamed"
			fixed.Description = nil
			fixed.Amount = ptr(int64(2500))
			fixed.Currency = money.MustCurrency("USD")
			fixed.Frequency = coupon.FrequencyForever
			fixed.FrequencyDuration = nil
			fixed.Reusable = true
			fixed.Expiration = coupon.ExpirationNoExpiration
			fixed.ExpirationAt = nil
			fixed.UpdatedAt = at(time.Minute)
			noErr(t, "Update()", r.Coupons.Update(ctx, fixed))
			got, err = r.Coupons.Get(ctx, org, fixed.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, fixed)
			pct.PercentageRate = ptr(money.MustParse("100"))
			noErr(t, "Update() percentage", r.Coupons.Update(ctx, pct))
			got, err = r.Coupons.Get(ctx, org, pct.ID)
			noErr(t, "Get() percentage after Update()", err)
			equal(t, "Get() percentage after Update()", got, pct)
			wantErr(t, "Update() unknown", r.Coupons.Update(ctx, couponFixtureFixed(e, org, "missing")), core.ErrNotFound)

			// A terminated coupon frees its code and stays readable.
			noErr(t, "Terminate()", fixed.Terminate(at(time.Hour)))
			noErr(t, "Update() terminated", r.Coupons.Update(ctx, fixed))
			got, err = r.Coupons.Get(ctx, org, fixed.ID)
			noErr(t, "Get() terminated", err)
			equal(t, "Get() terminated", got, fixed)
			_, err = r.Coupons.GetByCode(ctx, org, "fixed")
			wantErr(t, "GetByCode() terminated", err, core.ErrNotFound)
			again := couponFixtureFixed(e, org, "fixed")
			noErr(t, "Create() with a freed code", r.Coupons.Create(ctx, again))
			got, err = r.Coupons.GetByCode(ctx, org, "fixed")
			noErr(t, "GetByCode() of the new coupon", err)
			equal(t, "GetByCode() of the new coupon", got, again)
			return nil
		})
	})

	t.Run("limitations", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		_, a := e.org("Acme")
		org := a.OrganizationID
		plans := make([]*catalog.Plan, 3)
		for i := range plans {
			plans[i] = &catalog.Plan{
				ID: core.NewID[core.PlanID](e.ids), OrganizationID: org, Code: "plan-" + string(rune('a'+i)), Name: "Plan",
				Interval: period.Monthly, Amount: 1000, Currency: money.MustCurrency("EUR"),
				Status: catalog.PlanStatusActive, CreatedAt: now, UpdatedAt: now,
			}
		}
		metrics := make([]*catalog.BillableMetric, 2)
		for i := range metrics {
			metrics[i] = &catalog.BillableMetric{
				ID: core.NewID[core.BillableMetricID](e.ids), OrganizationID: org, Code: "bm-" + string(rune('a'+i)), Name: "Metric",
				AggregationType: catalog.AggregationTypeCount, CreatedAt: now, UpdatedAt: now,
			}
		}
		byPlan := couponFixturePercentage(e, org, "by-plan", "10")
		byPlan.LimitedPlanIDs = []core.PlanID{plans[2].ID, plans[0].ID}
		byMetric := couponFixturePercentage(e, org, "by-metric", "10")
		byMetric.LimitedBillableMetricIDs = []core.BillableMetricID{metrics[1].ID, metrics[0].ID}
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, p := range plans {
				noErr(t, "Plans.Create()", r.Plans.Create(ctx, p))
			}
			for _, m := range metrics {
				noErr(t, "Metrics.Create()", r.Metrics.Create(ctx, m))
			}
			noErr(t, "Create() limited to plans", r.Coupons.Create(ctx, byPlan))
			noErr(t, "Create() limited to metrics", r.Coupons.Create(ctx, byMetric))
			for _, c := range []*coupon.Coupon{byPlan, byMetric} {
				got, err := r.Coupons.Get(ctx, org, c.ID)
				noErr(t, "Get()", err)
				equal(t, "Get()", got, c)
			}
			byPlan.LimitedPlanIDs = []core.PlanID{plans[1].ID}
			noErr(t, "Update() limitations", r.Coupons.Update(ctx, byPlan))
			byMetric.LimitedBillableMetricIDs = nil
			noErr(t, "Update() without limitations", r.Coupons.Update(ctx, byMetric))
			all := listAll(t, 1, func(p core.PageRequest) (core.Page[*coupon.Coupon], error) {
				return r.Coupons.List(ctx, org, coupon.Filter{}, p)
			})
			equal(t, "List()", all, []*coupon.Coupon{byPlan, byMetric})
			return nil
		})
	})

	t.Run("list", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org := a.OrganizationID
		c1 := couponFixtureFixed(e, org, "SPRING-10")
		c1.Name = "Spring sale"
		c2 := couponFixturePercentage(e, org, "summer", "5")
		c2.Name = "Summer 100%"
		c3 := couponFixtureFixed(e, org, "winter_1")
		c3.Name = "Winter"
		noErr(t, "Terminate()", c3.Terminate(at(time.Hour)))
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, c := range []*coupon.Coupon{c1, c2, c3} {
				noErr(t, "Create()", r.Coupons.Create(ctx, c))
			}
			noErr(t, "Create() in another organization", r.Coupons.Create(ctx, couponFixtureFixed(e, b.OrganizationID, "spring")))
			cid := func(c *coupon.Coupon) core.CouponID { return c.ID }
			for _, tc := range []struct {
				name string
				f    coupon.Filter
				want []*coupon.Coupon
			}{
				{"all", coupon.Filter{}, []*coupon.Coupon{c1, c2, c3}},
				{"active", coupon.Filter{Status: coupon.StatusActive}, []*coupon.Coupon{c1, c2}},
				{"terminated", coupon.Filter{Status: coupon.StatusTerminated}, []*coupon.Coupon{c3}},
				{"search code", coupon.Filter{Search: "spring"}, []*coupon.Coupon{c1}},
				{"search name", coupon.Filter{Search: "SALE"}, []*coupon.Coupon{c1}},
				{"search percent", coupon.Filter{Search: "0%"}, []*coupon.Coupon{c2}},
				{"search underscore", coupon.Filter{Search: "r_"}, []*coupon.Coupon{c3}},
				{"search and status", coupon.Filter{Search: "er", Status: coupon.StatusActive}, []*coupon.Coupon{c2}},
			} {
				got := listAll(t, 1, func(p core.PageRequest) (core.Page[*coupon.Coupon], error) {
					return r.Coupons.List(ctx, org, tc.f, p)
				})
				equal(t, "List() "+tc.name, ids(got, cid), ids(tc.want, cid))
			}
			all := listAll(t, 2, func(p core.PageRequest) (core.Page[*coupon.Coupon], error) {
				return r.Coupons.List(ctx, org, coupon.Filter{}, p)
			})
			equal(t, "List() items", all, []*coupon.Coupon{c1, c2, c3})
			return nil
		})
	})

	t.Run("expired", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		expiring := func(org core.OrganizationID, code string, d time.Duration) *coupon.Coupon {
			c := couponFixtureFixed(e, org, code)
			c.Expiration = coupon.ExpirationTimeLimit
			c.ExpirationAt = ptr(at(d))
			return c
		}
		pastA := expiring(a.OrganizationID, "past", -time.Hour)
		exactlyB := expiring(b.OrganizationID, "exact", 0)
		pastB := expiring(b.OrganizationID, "past", -time.Minute)
		future := expiring(a.OrganizationID, "future", time.Second)
		terminated := expiring(a.OrganizationID, "terminated", -time.Hour)
		noErr(t, "Terminate()", terminated.Terminate(now))
		unlimited := couponFixtureFixed(e, a.OrganizationID, "unlimited")
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, c := range []*coupon.Coupon{pastA, exactlyB, pastB, future, terminated, unlimited} {
				noErr(t, "Create()", r.Coupons.Create(ctx, c))
			}
			got := listAll(t, 1, func(p core.PageRequest) (core.Page[*coupon.Coupon], error) {
				return r.Coupons.ListExpired(ctx, now, p)
			})
			equal(t, "ListExpired()", got, []*coupon.Coupon{pastA, exactlyB, pastB})
			got = listAll(t, 10, func(p core.PageRequest) (core.Page[*coupon.Coupon], error) {
				return r.Coupons.ListExpired(ctx, at(-30*time.Minute), p)
			})
			equal(t, "ListExpired() earlier", ids(got, func(c *coupon.Coupon) core.CouponID { return c.ID }), []core.CouponID{pastA.ID})
			return nil
		})
	})

	t.Run("applied coupons", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org := a.OrganizationID
		cust1 := couponFixtureCustomer(e, a, "c1")
		cust2 := couponFixtureCustomer(e, a, "c2")
		otherCust := couponFixtureCustomer(e, b, "c1")
		fixed := couponFixtureFixed(e, org, "fixed")
		pct := couponFixturePercentage(e, org, "pct", "15.5")
		pct.Frequency = coupon.FrequencyRecurring
		pct.FrequencyDuration = ptr(2)
		otherCoupon := couponFixtureFixed(e, b.OrganizationID, "fixed")

		// Applications are created out of creation-instant order: the
		// customer's list is ordered by application instant.
		ac1 := coupon.Apply(fixed, core.NewID[core.AppliedCouponID](e.ids), cust1.ID, coupon.Overrides{}, at(2*time.Hour))
		ac2 := coupon.Apply(pct, core.NewID[core.AppliedCouponID](e.ids), cust1.ID, coupon.Overrides{PercentageRate: ptr(money.MustParse("7.25"))}, at(time.Hour))
		ac3 := coupon.Apply(fixed, core.NewID[core.AppliedCouponID](e.ids), cust1.ID, coupon.Overrides{Amount: ptr(int64(300))}, at(3*time.Hour))
		ac4 := coupon.Apply(fixed, core.NewID[core.AppliedCouponID](e.ids), cust2.ID, coupon.Overrides{}, now)
		otherAC := coupon.Apply(otherCoupon, core.NewID[core.AppliedCouponID](e.ids), otherCust.ID, coupon.Overrides{}, now)
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, c := range []*customer.Customer{cust1, cust2, otherCust} {
				noErr(t, "Customers.Create()", r.Customers.Create(ctx, c))
			}
			for _, c := range []*coupon.Coupon{fixed, pct, otherCoupon} {
				noErr(t, "Coupons.Create()", r.Coupons.Create(ctx, c))
			}
			for _, x := range []*coupon.AppliedCoupon{ac1, ac2, ac3, ac4, otherAC} {
				noErr(t, "Create()", r.AppliedCoupons.Create(ctx, x))
			}
			wantErr(t, "Create() with the same ID", r.AppliedCoupons.Create(ctx, ac1), core.ErrConflict)
			for _, x := range []*coupon.AppliedCoupon{ac1, ac2, ac3} {
				got, err := r.AppliedCoupons.Get(ctx, org, x.ID)
				noErr(t, "Get()", err)
				equal(t, "Get()", got, x)
			}
			_, err := r.AppliedCoupons.Get(ctx, org, core.NewID[core.AppliedCouponID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)
			_, err = r.AppliedCoupons.Get(ctx, b.OrganizationID, ac1.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			moved := *ac1
			moved.OrganizationID = b.OrganizationID
			wantErr(t, "Update() through another organization", r.AppliedCoupons.Update(ctx, &moved), core.ErrNotFound)
			missing := coupon.Apply(fixed, core.NewID[core.AppliedCouponID](e.ids), cust1.ID, coupon.Overrides{}, now)
			wantErr(t, "Update() unknown", r.AppliedCoupons.Update(ctx, missing), core.ErrNotFound)

			acID := func(x *coupon.AppliedCoupon) core.AppliedCouponID { return x.ID }
			active, err := r.AppliedCoupons.ListActiveByCustomer(ctx, org, cust1.ID)
			noErr(t, "ListActiveByCustomer()", err)
			equal(t, "ListActiveByCustomer()", active, []*coupon.AppliedCoupon{ac2, ac1, ac3})
			active, err = r.AppliedCoupons.ListActiveByCustomer(ctx, b.OrganizationID, cust1.ID)
			noErr(t, "ListActiveByCustomer() from another organization", err)
			equal(t, "ListActiveByCustomer() from another organization", len(active), 0)

			// Consuming terminates; terminated applications leave the active
			// lists but still count.
			noErr(t, "Consume()", ac1.Consume(*ac1.Amount, at(4*time.Hour)))
			noErr(t, "Update()", r.AppliedCoupons.Update(ctx, ac1))
			got, err := r.AppliedCoupons.Get(ctx, org, ac1.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, ac1)
			noErr(t, "Consume() recurring", ac2.Consume(0, at(4*time.Hour)))
			noErr(t, "Update() recurring", r.AppliedCoupons.Update(ctx, ac2))
			got, err = r.AppliedCoupons.Get(ctx, org, ac2.ID)
			noErr(t, "Get() recurring after Update()", err)
			equal(t, "Get() recurring after Update()", got, ac2)

			active, err = r.AppliedCoupons.ListActiveByCustomer(ctx, org, cust1.ID)
			noErr(t, "ListActiveByCustomer() after termination", err)
			equal(t, "ListActiveByCustomer() after termination", ids(active, acID), []core.AppliedCouponID{ac2.ID, ac3.ID})
			byCoupon, err := r.AppliedCoupons.ListActiveByCoupon(ctx, org, fixed.ID)
			noErr(t, "ListActiveByCoupon()", err)
			equal(t, "ListActiveByCoupon()", byCoupon, []*coupon.AppliedCoupon{ac3, ac4})
			byCoupon, err = r.AppliedCoupons.ListActiveByCoupon(ctx, b.OrganizationID, fixed.ID)
			noErr(t, "ListActiveByCoupon() from another organization", err)
			equal(t, "ListActiveByCoupon() from another organization", len(byCoupon), 0)

			for _, tc := range []struct {
				name     string
				coupon   core.CouponID
				customer core.CustomerID
				org      core.OrganizationID
				want     int
			}{
				{"fixed to c1", fixed.ID, cust1.ID, org, 2},
				{"pct to c1", pct.ID, cust1.ID, org, 1},
				{"pct to c2", pct.ID, cust2.ID, org, 0},
				{"another organization", fixed.ID, cust1.ID, b.OrganizationID, 0},
			} {
				n, err := r.AppliedCoupons.CountByCouponAndCustomer(ctx, tc.org, tc.coupon, tc.customer)
				noErr(t, "CountByCouponAndCustomer() "+tc.name, err)
				equal(t, "CountByCouponAndCustomer() "+tc.name, n, tc.want)
			}

			for _, tc := range []struct {
				name string
				f    coupon.AppliedFilter
				want []*coupon.AppliedCoupon
			}{
				{"all", coupon.AppliedFilter{}, []*coupon.AppliedCoupon{ac1, ac2, ac3, ac4}},
				{"customer", coupon.AppliedFilter{CustomerID: cust2.ID}, []*coupon.AppliedCoupon{ac4}},
				{"coupon", coupon.AppliedFilter{CouponID: fixed.ID}, []*coupon.AppliedCoupon{ac1, ac3, ac4}},
				{"status", coupon.AppliedFilter{Status: coupon.StatusTerminated}, []*coupon.AppliedCoupon{ac1}},
				{"customer, coupon and status", coupon.AppliedFilter{CustomerID: cust1.ID, CouponID: fixed.ID, Status: coupon.StatusActive}, []*coupon.AppliedCoupon{ac3}},
			} {
				got := listAll(t, 1, func(p core.PageRequest) (core.Page[*coupon.AppliedCoupon], error) {
					return r.AppliedCoupons.List(ctx, org, tc.f, p)
				})
				equal(t, "List() "+tc.name, got, tc.want)
			}
			p, err := r.AppliedCoupons.List(ctx, b.OrganizationID, coupon.AppliedFilter{CustomerID: cust1.ID}, core.PageRequest{})
			noErr(t, "List() from another organization", err)
			equal(t, "List() from another organization", len(p.Items), 0)
			return nil
		})
	})
}

// couponFixtureFixed returns an unsaved active fixed-amount coupon of 10 EUR.
func couponFixtureFixed(e *env, org core.OrganizationID, code string) *coupon.Coupon {
	c := coupon.New(org, core.NewID[core.CouponID](e.ids), code, "Coupon "+code, coupon.TypeFixedAmount, now)
	c.Amount = ptr(int64(1000))
	c.Currency = money.MustCurrency("EUR")
	return c
}

// couponFixturePercentage returns an unsaved active percentage coupon.
func couponFixturePercentage(e *env, org core.OrganizationID, code, rate string) *coupon.Coupon {
	c := coupon.New(org, core.NewID[core.CouponID](e.ids), code, "Coupon "+code, coupon.TypePercentage, now)
	c.PercentageRate = ptr(money.MustParse(rate))
	return c
}

// couponFixtureCustomer returns an unsaved customer of the billing entity
// be in EUR.
func couponFixtureCustomer(e *env, be *organization.BillingEntity, ext string) *customer.Customer {
	c := customer.New(be.OrganizationID, core.NewID[core.CustomerID](e.ids), ext, be.ID, now)
	c.Currency = money.MustCurrency("EUR")
	return c
}
