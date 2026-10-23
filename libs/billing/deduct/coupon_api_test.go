package deduct_test

import (
	"errors"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/deduct"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// wantNotFound checks that err is the not-found error of resource.
func wantNotFound(t *testing.T, err error, resource string) {
	t.Helper()
	if !errors.Is(err, core.ErrNotFound) || err.Error() != resource+"_not_found" {
		t.Fatalf("got error = %v, want = %s_not_found", err, resource)
	}
}

// couponInput returns the input of a fixed amount coupon of 100 USD cents
// used once.
func couponInput(code string) deduct.CreateCouponInput {
	return deduct.CreateCouponInput{
		Code: code, Name: "Free beer", Type: coupon.TypeFixedAmount,
		Amount: ptr[int64](100), Currency: billingtest.USD, Frequency: coupon.FrequencyOnce,
		Expiration: coupon.ExpirationNoExpiration,
	}
}

func TestCreateCouponFrom(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*billingtest.Harness, *deduct.CreateCouponInput, *catalog.Plan, *catalog.BillableMetric)
		check   func(*testing.T, *coupon.Coupon, *catalog.Plan, *catalog.BillableMetric)
		wantErr func(*testing.T, error)
	}{
		// source: spec/services/coupons/create_service_spec.rb:30
		{
			name: "reusable by default",
			check: func(t *testing.T, got *coupon.Coupon, _ *catalog.Plan, _ *catalog.BillableMetric) {
				eq(t, "reusable", got.Reusable, true)
				eq(t, "status", got.Status, coupon.StatusActive)
			},
		},
		// source: spec/services/coupons/create_service_spec.rb:205
		{
			name: "plan limitation by code",
			mutate: func(_ *billingtest.Harness, in *deduct.CreateCouponInput, p *catalog.Plan, _ *catalog.BillableMetric) {
				in.Plans = []refs.Ref{refs.ByCode(p.Code)}
			},
			check: func(t *testing.T, got *coupon.Coupon, p *catalog.Plan, _ *catalog.BillableMetric) {
				eq(t, "plans", len(got.LimitedPlanIDs), 1)
				eq(t, "plan", got.LimitedPlanIDs[0], p.ID)
			},
		},
		// source: spec/services/coupons/create_service_spec.rb:345
		{
			name: "billable metric limitation by code",
			mutate: func(_ *billingtest.Harness, in *deduct.CreateCouponInput, _ *catalog.Plan, m *catalog.BillableMetric) {
				in.BillableMetrics = []refs.Ref{refs.ByCode(m.Code)}
			},
			check: func(t *testing.T, got *coupon.Coupon, _ *catalog.Plan, m *catalog.BillableMetric) {
				eq(t, "metrics", len(got.LimitedBillableMetricIDs), 1)
				eq(t, "metric", got.LimitedBillableMetricIDs[0], m.ID)
			},
		},
		// source: spec/services/coupons/create_service_spec.rb:308
		{
			name: "unknown billable metric",
			mutate: func(_ *billingtest.Harness, in *deduct.CreateCouponInput, _ *catalog.Plan, m *catalog.BillableMetric) {
				in.BillableMetrics = []refs.Ref{refs.ByID(m.ID), refs.ByCode("invalid")}
			},
			wantErr: func(t *testing.T, err error) { wantNotFound(t, err, "billable_metrics") },
		},
		{
			name: "unknown plan",
			mutate: func(_ *billingtest.Harness, in *deduct.CreateCouponInput, _ *catalog.Plan, _ *catalog.BillableMetric) {
				in.Plans = []refs.Ref{refs.ByCode("invalid")}
			},
			wantErr: func(t *testing.T, err error) { wantNotFound(t, err, "plans") },
		},
		// source: spec/services/coupons/create_service_spec.rb:280
		{
			name: "both limitation types",
			mutate: func(_ *billingtest.Harness, in *deduct.CreateCouponInput, p *catalog.Plan, m *catalog.BillableMetric) {
				in.Plans, in.BillableMetrics = []refs.Ref{refs.ByID(p.ID)}, []refs.Ref{refs.ByID(m.ID)}
			},
			wantErr: func(t *testing.T, err error) { wantRule(t, err, coupon.CodeOnlyOneLimitationType) },
		},
		// source: spec/services/coupons/create_service_spec.rb:99
		// coupons/create_service.rb:18 validates the date before the
		// limitations.
		{
			name: "expiration in the past before an unknown plan",
			mutate: func(h *billingtest.Harness, in *deduct.CreateCouponInput, _ *catalog.Plan, _ *catalog.BillableMetric) {
				past := h.Now().AddDate(0, 0, -1)
				in.Expiration, in.ExpirationAt = coupon.ExpirationTimeLimit, &past
				in.Plans = []refs.Ref{refs.ByCode("invalid")}
			},
			wantErr: func(t *testing.T, err error) { wantValidation(t, err, "expiration_at", core.CodeNotInFuture) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			h := f.h
			p, m := h.Plan(period.Monthly, 100), h.Metric("api_calls", catalog.AggregationTypeCount, "")
			in := couponInput("free-beer")
			if tt.mutate != nil {
				tt.mutate(h, &in, p, m)
			}
			got, err := f.s.CreateCouponFrom(h.Ctx, h.OrgID(), in)
			if tt.wantErr != nil {
				tt.wantErr(t, err)
				return
			}
			mustOK(t, err)
			tt.check(t, got, p, m)
			tt.check(t, readCoupon(h, got.ID), p, m)
		})
	}
}

func TestUpdateCouponFrom(t *testing.T) {
	tests := []struct {
		name    string
		applied bool
		in      func([]*catalog.Plan, *catalog.BillableMetric) deduct.UpdateCouponInput
		check   func(*testing.T, *coupon.Coupon, []*catalog.Plan, *catalog.BillableMetric)
		wantErr func(*testing.T, error)
	}{
		// source: spec/services/coupons/update_service_spec.rb:107
		{
			name: "plan limitations by code",
			in: func(p []*catalog.Plan, _ *catalog.BillableMetric) deduct.UpdateCouponInput {
				return deduct.UpdateCouponInput{Plans: core.Some([]refs.Ref{refs.ByCode(p[0].Code), refs.ByCode(p[1].Code)})}
			},
			check: func(t *testing.T, got *coupon.Coupon, _ []*catalog.Plan, _ *catalog.BillableMetric) {
				eq(t, "plans", len(got.LimitedPlanIDs), 2)
			},
		},
		// source: spec/services/coupons/update_service_spec.rb:124
		{
			name: "plans removed",
			in: func([]*catalog.Plan, *catalog.BillableMetric) deduct.UpdateCouponInput {
				return deduct.UpdateCouponInput{Plans: core.Some([]refs.Ref{})}
			},
			check: func(t *testing.T, got *coupon.Coupon, _ []*catalog.Plan, _ *catalog.BillableMetric) {
				eq(t, "plans", len(got.LimitedPlanIDs), 0)
			},
		},
		// coupons/update_service.rb:44-54: limiting to billable metrics
		// removes the plans.
		{
			name: "billable metrics replace plans",
			in: func(_ []*catalog.Plan, m *catalog.BillableMetric) deduct.UpdateCouponInput {
				return deduct.UpdateCouponInput{BillableMetrics: core.Some([]refs.Ref{refs.ByCode(m.Code)})}
			},
			check: func(t *testing.T, got *coupon.Coupon, _ []*catalog.Plan, m *catalog.BillableMetric) {
				eq(t, "plans", len(got.LimitedPlanIDs), 0)
				eq(t, "metrics", len(got.LimitedBillableMetricIDs), 1)
				eq(t, "metric", got.LimitedBillableMetricIDs[0], m.ID)
			},
		},
		// source: spec/services/coupons/update_service_spec.rb:165
		{
			name: "both limitation types",
			in: func(p []*catalog.Plan, m *catalog.BillableMetric) deduct.UpdateCouponInput {
				return deduct.UpdateCouponInput{
					Plans:           core.Some([]refs.Ref{refs.ByID(p[0].ID)}),
					BillableMetrics: core.Some([]refs.Ref{refs.ByID(m.ID)}),
				}
			},
			wantErr: func(t *testing.T, err error) { wantRule(t, err, coupon.CodeOnlyOneLimitationType) },
		},
		// source: spec/services/coupons/update_service_spec.rb:181
		{
			name: "unknown billable metric",
			in: func(_ []*catalog.Plan, m *catalog.BillableMetric) deduct.UpdateCouponInput {
				return deduct.UpdateCouponInput{BillableMetrics: core.Some([]refs.Ref{refs.ByID(m.ID), refs.ByCode("invalid")})}
			},
			wantErr: func(t *testing.T, err error) { wantNotFound(t, err, "billable_metrics") },
		},
		// source: spec/services/coupons/update_service_spec.rb:32
		// coupons/update_service.rb:29-31: an applied coupon keeps its
		// limitations, and the ones given are not looked up.
		{
			name: "applied coupon", applied: true,
			in: func([]*catalog.Plan, *catalog.BillableMetric) deduct.UpdateCouponInput {
				return deduct.UpdateCouponInput{
					Name:   core.Some("renamed"),
					Amount: core.Some(ptr[int64](50)),
					Plans:  core.Some([]refs.Ref{refs.ByCode("invalid")}),
				}
			},
			check: func(t *testing.T, got *coupon.Coupon, p []*catalog.Plan, _ *catalog.BillableMetric) {
				eq(t, "name", got.Name, "renamed")
				eq(t, "amount", *got.Amount, int64(10))
				eq(t, "plans", len(got.LimitedPlanIDs), 1)
				eq(t, "plan", got.LimitedPlanIDs[0], p[0].ID)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			h := f.h
			plans := []*catalog.Plan{h.Plan(period.Monthly, 100), h.Plan(period.Monthly, 100)}
			m := h.Metric("api_calls", catalog.AggregationTypeCount, "")
			c := h.FixedAmountCoupon(10, func(c *coupon.Coupon) { c.LimitedPlanIDs = []core.PlanID{plans[0].ID} })
			if tt.applied {
				h.ApplyCoupon(c, f.cust)
			}
			got, err := f.s.UpdateCouponFrom(h.Ctx, h.OrgID(), c.ID, tt.in(plans, m))
			if tt.wantErr != nil {
				tt.wantErr(t, err)
				return
			}
			mustOK(t, err)
			tt.check(t, got, plans, m)
			tt.check(t, readCoupon(h, c.ID), plans, m)
		})
	}
}

// source: spec/requests/api/v1/coupons_controller_spec.rb:120
// app/models/coupon.rb:67: a deleted coupon is out of the default scope.
func TestGetCouponHidesDeleted(t *testing.T) {
	f := newFixture(t)
	c := f.h.FixedAmountCoupon(10)
	got, err := f.s.GetCouponByCode(f.h.Ctx, f.h.OrgID(), c.Code)
	mustOK(t, err)
	eq(t, "id", got.ID, c.ID)
	_, err = f.s.DestroyCoupon(f.h.Ctx, f.h.OrgID(), c.ID)
	mustOK(t, err)
	_, err = f.s.GetCoupon(f.h.Ctx, f.h.OrgID(), c.ID)
	wantNotFound(t, err, "coupon")
	_, err = f.s.GetCouponByCode(f.h.Ctx, f.h.OrgID(), c.Code)
	wantNotFound(t, err, "coupon")
	_, err = f.s.TerminateCoupon(f.h.Ctx, f.h.OrgID(), c.ID)
	wantNotFound(t, err, "coupon")
}

// source: spec/queries/coupons_query_spec.rb:26
// source: spec/queries/coupons_query_spec.rb:80
// app/models/coupon.rb:68-76: active coupons come first.
func TestListCoupons(t *testing.T) {
	f := newFixture(t)
	h := f.h
	terminated := h.FixedAmountCoupon(10, func(c *coupon.Coupon) {
		now := h.Now()
		c.Status, c.TerminatedAt = coupon.StatusTerminated, &now
	})
	active := h.FixedAmountCoupon(10)
	deleted := h.FixedAmountCoupon(10)
	_, err := f.s.DestroyCoupon(h.Ctx, h.OrgID(), deleted.ID)
	mustOK(t, err)

	page, err := f.s.ListCoupons(h.Ctx, h.OrgID(), coupon.Filter{}, core.PageRequest{})
	mustOK(t, err)
	eq(t, "count", len(page.Items), 2)
	eq(t, "first", page.Items[0].ID, active.ID)
	eq(t, "second", page.Items[1].ID, terminated.ID)

	page, err = f.s.ListCoupons(h.Ctx, h.OrgID(), coupon.Filter{Status: coupon.StatusTerminated}, core.PageRequest{})
	mustOK(t, err)
	eq(t, "terminated count", len(page.Items), 1)
	eq(t, "terminated", page.Items[0].ID, terminated.ID)
}

// source: spec/queries/applied_coupons_query_spec.rb:46
// source: spec/queries/applied_coupons_query_spec.rb:93
func TestListAppliedCoupons(t *testing.T) {
	f := newFixture(t)
	h := f.h
	c := h.FixedAmountCoupon(10)
	kept := h.ApplyCoupon(c, f.cust)
	gone := h.Customer()
	h.ApplyCoupon(c, gone)
	h.UpdateCustomer(gone, func(c *customer.Customer) { mustOK(t, c.Archive(h.Now())) })
	_, err := f.s.DestroyCoupon(h.Ctx, h.OrgID(), c.ID)
	mustOK(t, err)

	page, err := f.s.ListAppliedCoupons(h.Ctx, h.OrgID(), coupon.AppliedFilter{CouponCodes: []string{c.Code}}, core.PageRequest{})
	mustOK(t, err)
	eq(t, "count", len(page.Items), 1)
	eq(t, "applied coupon", page.Items[0].ID, kept.ID)
}

// source: spec/services/applied_coupons/create_service_spec.rb:211
func TestGetCouponCustomer(t *testing.T) {
	f := newFixture(t)
	h := f.h
	got, err := f.s.GetCouponCustomer(h.Ctx, h.OrgID(), core.CustomerID{}, f.cust.ExternalID)
	mustOK(t, err)
	eq(t, "id", got.ID, f.cust.ID)
	h.UpdateCustomer(f.cust, func(c *customer.Customer) { mustOK(t, c.Archive(h.Now())) })
	_, err = f.s.GetCouponCustomer(h.Ctx, h.OrgID(), f.cust.ID, "")
	wantNotFound(t, err, "customer")
	_, err = f.s.ApplyCoupon(h.Ctx, deduct.ApplyCouponInput{OrganizationID: h.OrgID(), CustomerID: f.cust.ID, CouponID: h.FixedAmountCoupon(10).ID})
	wantNotFound(t, err, "customer")
}
