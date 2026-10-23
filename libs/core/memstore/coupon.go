package memstore

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
)

// couponData holds the tables of package coupon.
type couponData struct {
	coupons        *table[core.CouponID, coupon.Coupon]
	appliedCoupons *table[core.AppliedCouponID, coupon.AppliedCoupon]
}

func newCouponData() *couponData {
	return &couponData{
		coupons: newTable[core.CouponID](func(c coupon.Coupon) coupon.Coupon {
			c.LimitedPlanIDs = slices.Clone(c.LimitedPlanIDs)
			c.LimitedBillableMetricIDs = slices.Clone(c.LimitedBillableMetricIDs)
			return c
		}),
		appliedCoupons: newTable[core.AppliedCouponID](func(a coupon.AppliedCoupon) coupon.AppliedCoupon { return a }),
	}
}

func (d *couponData) snapshot() *couponData {
	return &couponData{
		coupons:        d.coupons.snapshot(),
		appliedCoupons: d.appliedCoupons.snapshot(),
	}
}

// couponRepo implements coupon.Repository.
type couponRepo struct{ d *couponData }

var _ coupon.Repository = couponRepo{}

func (r couponRepo) check(c *coupon.Coupon) error {
	if c.Status != coupon.StatusActive {
		return nil
	}
	if r.d.coupons.any(func(x *coupon.Coupon) bool {
		return x.ID != c.ID && x.OrganizationID == c.OrganizationID && x.Status == coupon.StatusActive && x.Code == c.Code
	}) {
		return conflict("coupon", "code")
	}
	return nil
}

func (r couponRepo) Create(_ context.Context, c *coupon.Coupon) error {
	if r.d.coupons.has(c.ID) {
		return conflict("coupon", "id")
	}
	if err := r.check(c); err != nil {
		return err
	}
	r.d.coupons.put(c.ID, *c)
	return nil
}

func (r couponRepo) Update(_ context.Context, c *coupon.Coupon) error {
	cur, ok := r.d.coupons.get(c.ID)
	if !ok || cur.OrganizationID != c.OrganizationID {
		return notFound("coupon", c.ID)
	}
	if err := r.check(c); err != nil {
		return err
	}
	r.d.coupons.put(c.ID, *c)
	return nil
}

func (r couponRepo) Get(_ context.Context, org core.OrganizationID, id core.CouponID) (*coupon.Coupon, error) {
	c, ok := r.d.coupons.get(id)
	if !ok || c.OrganizationID != org {
		return nil, notFound("coupon", id)
	}
	return c, nil
}

func (r couponRepo) GetByCode(_ context.Context, org core.OrganizationID, code string) (*coupon.Coupon, error) {
	c, ok := r.d.coupons.first(func(x *coupon.Coupon) bool {
		return x.OrganizationID == org && x.Status == coupon.StatusActive && x.Code == code
	})
	if !ok {
		return nil, notFound("coupon", stringer(code))
	}
	return c, nil
}

func (r couponRepo) List(_ context.Context, org core.OrganizationID, f coupon.Filter, page core.PageRequest) (core.Page[*coupon.Coupon], error) {
	search := strings.ToLower(f.Search)
	rows := r.d.coupons.find(func(x *coupon.Coupon) bool {
		if x.OrganizationID != org || (f.Status != "" && x.Status != f.Status) {
			return false
		}
		return search == "" || strings.Contains(strings.ToLower(x.Code), search) || strings.Contains(strings.ToLower(x.Name), search)
	})
	return paginate(rows, couponKey, page)
}

func (r couponRepo) ListExpired(_ context.Context, now time.Time, page core.PageRequest) (core.Page[*coupon.Coupon], error) {
	rows := r.d.coupons.find(func(x *coupon.Coupon) bool { return x.Status == coupon.StatusActive && x.Expired(now) })
	return paginate(rows, couponKey, page)
}

// appliedCouponRepo implements coupon.AppliedRepository.
type appliedCouponRepo struct{ d *couponData }

var _ coupon.AppliedRepository = appliedCouponRepo{}

func (r appliedCouponRepo) Create(_ context.Context, a *coupon.AppliedCoupon) error {
	if r.d.appliedCoupons.has(a.ID) {
		return conflict("applied_coupon", "id")
	}
	r.d.appliedCoupons.put(a.ID, *a)
	return nil
}

func (r appliedCouponRepo) Update(_ context.Context, a *coupon.AppliedCoupon) error {
	cur, ok := r.d.appliedCoupons.get(a.ID)
	if !ok || cur.OrganizationID != a.OrganizationID {
		return notFound("applied_coupon", a.ID)
	}
	r.d.appliedCoupons.put(a.ID, *a)
	return nil
}

func (r appliedCouponRepo) Get(_ context.Context, org core.OrganizationID, id core.AppliedCouponID) (*coupon.AppliedCoupon, error) {
	a, ok := r.d.appliedCoupons.get(id)
	if !ok || a.OrganizationID != org {
		return nil, notFound("applied_coupon", id)
	}
	return a, nil
}

func (r appliedCouponRepo) ListActiveByCustomer(_ context.Context, org core.OrganizationID, cust core.CustomerID) ([]*coupon.AppliedCoupon, error) {
	rows := r.d.appliedCoupons.find(func(x *coupon.AppliedCoupon) bool {
		return x.OrganizationID == org && x.CustomerID == cust && x.Active()
	})
	slices.SortStableFunc(rows, func(a, b *coupon.AppliedCoupon) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return rows, nil
}

func (r appliedCouponRepo) ListActiveByCoupon(_ context.Context, org core.OrganizationID, c core.CouponID) ([]*coupon.AppliedCoupon, error) {
	return r.d.appliedCoupons.find(func(x *coupon.AppliedCoupon) bool {
		return x.OrganizationID == org && x.CouponID == c && x.Active()
	}), nil
}

func (r appliedCouponRepo) CountByCouponAndCustomer(_ context.Context, org core.OrganizationID, c core.CouponID, cust core.CustomerID) (int, error) {
	return r.d.appliedCoupons.count(func(x *coupon.AppliedCoupon) bool {
		return x.OrganizationID == org && x.CouponID == c && x.CustomerID == cust
	}), nil
}

func (r appliedCouponRepo) List(_ context.Context, org core.OrganizationID, f coupon.AppliedFilter, page core.PageRequest) (core.Page[*coupon.AppliedCoupon], error) {
	rows := r.d.appliedCoupons.find(func(x *coupon.AppliedCoupon) bool {
		switch {
		case x.OrganizationID != org,
			!f.CustomerID.IsZero() && x.CustomerID != f.CustomerID,
			!f.CouponID.IsZero() && x.CouponID != f.CouponID,
			f.Status != "" && x.Status != f.Status:
			return false
		}
		return true
	})
	return paginate(rows, func(a *coupon.AppliedCoupon) core.AppliedCouponID { return a.ID }, page)
}

func couponKey(c *coupon.Coupon) core.CouponID { return c.ID }
