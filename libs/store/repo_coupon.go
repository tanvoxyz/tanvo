package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindCoupon binds the repositories of the coupon aggregates.
func (t *txState) bindCoupon(r *port.Repos) {
	r.Coupons = couponRepo{t}
	r.AppliedCoupons = appliedCouponRepo{t}
}

// couponRepo implements coupon.Repository.
type couponRepo struct{ t *txState }

var _ coupon.Repository = couponRepo{}

func couponParams(c *coupon.Coupon) db.CreateCouponParams {
	p := db.CreateCouponParams{
		OrganizationID:    c.OrganizationID.UUID(),
		ID:                c.ID.UUID(),
		Code:              c.Code,
		Name:              c.Name,
		Description:       c.Description,
		CouponType:        string(c.Type),
		Amount:            c.Amount,
		AmountCurrency:    currencyCode(c.Currency),
		Frequency:         string(c.Frequency),
		FrequencyDuration: optInt32(c.FrequencyDuration),
		Reusable:          c.Reusable,
		Expiration:        string(c.Expiration),
		ExpirationAt:      optUTC(c.ExpirationAt),
		Status:            string(c.Status),
		TerminatedAt:      optUTC(c.TerminatedAt),
		CreatedAt:         utc(c.CreatedAt),
		UpdatedAt:         utc(c.UpdatedAt),
	}
	if c.PercentageRate != nil {
		v := *c.PercentageRate
		p.PercentageRate = &v
	}
	return p
}

// couponRow is the common shape of the coupon reads; the row types sqlc
// generates for each query convert to it.
type couponRow struct {
	Coupon            db.Coupon
	PlanIds           []uuid.UUID
	BillableMetricIds []uuid.UUID
}

func couponOf(row couponRow) (*coupon.Coupon, error) {
	r := row.Coupon
	cur, err := currency(r.AmountCurrency)
	if err != nil {
		return nil, fmt.Errorf("coupon %s: %w", r.ID, err)
	}
	var rate *money.Decimal
	if r.PercentageRate != nil {
		v := *r.PercentageRate
		rate = &v
	}
	return &coupon.Coupon{
		ID:                       idOf[core.CouponID](r.ID),
		OrganizationID:           idOf[core.OrganizationID](r.OrganizationID),
		Code:                     r.Code,
		Name:                     r.Name,
		Description:              r.Description,
		Type:                     coupon.Type(r.CouponType),
		Amount:                   r.Amount,
		Currency:                 cur,
		PercentageRate:           rate,
		Frequency:                coupon.Frequency(r.Frequency),
		FrequencyDuration:        optInt(r.FrequencyDuration),
		Reusable:                 r.Reusable,
		Expiration:               coupon.Expiration(r.Expiration),
		ExpirationAt:             optUTC(r.ExpirationAt),
		LimitedPlanIDs:           idsOf[core.PlanID](row.PlanIds),
		LimitedBillableMetricIDs: idsOf[core.BillableMetricID](row.BillableMetricIds),
		Status:                   coupon.Status(r.Status),
		TerminatedAt:             optUTC(r.TerminatedAt),
		CreatedAt:                utc(r.CreatedAt),
		UpdatedAt:                utc(r.UpdatedAt),
	}, nil
}

// writeLinks replaces the plan and billable metric limitations of c.
func (r couponRepo) writeLinks(ctx context.Context, q *db.Queries, c *coupon.Coupon, replace bool) error {
	org, id := c.OrganizationID.UUID(), c.ID.UUID()
	if replace {
		if err := q.DeleteCouponPlans(ctx, db.DeleteCouponPlansParams{OrganizationID: org, CouponID: id}); err != nil {
			return err
		}
		if err := q.DeleteCouponBillableMetrics(ctx, db.DeleteCouponBillableMetricsParams{OrganizationID: org, CouponID: id}); err != nil {
			return err
		}
	}
	if len(c.LimitedPlanIDs) > 0 {
		if err := q.InsertCouponPlans(ctx, db.InsertCouponPlansParams{OrganizationID: org, CouponID: id, PlanIds: uuids(c.LimitedPlanIDs)}); err != nil {
			return err
		}
	}
	if len(c.LimitedBillableMetricIDs) > 0 {
		if err := q.InsertCouponBillableMetrics(ctx, db.InsertCouponBillableMetricsParams{
			OrganizationID: org, CouponID: id, BillableMetricIds: uuids(c.LimitedBillableMetricIDs),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r couponRepo) Create(ctx context.Context, c *coupon.Coupon) error {
	q, err := r.t.queries(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	p := couponParams(c)
	if !c.Limited() {
		n, err := q.CreateCoupon(ctx, p)
		return insertErr("coupon", n, err)
	}
	// A missing plan or metric must not abort the transaction: guard the
	// insert with its limitations.
	var n int64
	err = r.t.guard(ctx, func() error {
		var err error
		n, err = q.CreateCoupon(ctx, p)
		if err != nil || n == 0 {
			return err
		}
		return r.writeLinks(ctx, q, c, false)
	})
	return insertErr("coupon", n, err)
}

func (r couponRepo) Update(ctx context.Context, c *coupon.Coupon) error {
	q, err := r.t.queries(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	p := couponParams(c)
	// The code is unique among active coupons: guard the update so that a
	// conflict keeps the transaction usable.
	var n int64
	err = r.t.guard(ctx, func() error {
		var err error
		n, err = q.UpdateCoupon(ctx, db.UpdateCouponParams(p))
		if err != nil || n == 0 {
			return err
		}
		return r.writeLinks(ctx, q, c, true)
	})
	return updateErr("coupon", c.ID, n, err)
}

func (r couponRepo) Get(ctx context.Context, org core.OrganizationID, id core.CouponID) (*coupon.Coupon, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetCoupon(ctx, db.GetCouponParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("coupon", id, err)
	}
	return couponOf(couponRow(row))
}

func (r couponRepo) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*coupon.Coupon, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetCouponByCode(ctx, db.GetCouponByCodeParams{OrganizationID: org.UUID(), Code: code})
	if err != nil {
		return nil, getErr("coupon", code, err)
	}
	return couponOf(couponRow(row))
}

func (r couponRepo) List(ctx context.Context, org core.OrganizationID, f coupon.Filter, page core.PageRequest) (core.Page[*coupon.Coupon], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*coupon.Coupon]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*coupon.Coupon]{}, err
	}
	var search *string
	if f.Search != "" {
		search = new(likePattern(f.Search))
	}
	rows, err := q.ListCoupons(ctx, db.ListCouponsParams{
		OrganizationID: org.UUID(), Status: optString(string(f.Status)), Search: search, After: after, Lim: lim,
	})
	if err != nil {
		return core.Page[*coupon.Coupon]{}, fmt.Errorf("list coupons: %w", mapErr(err))
	}
	return pageOf(rows, page,
		func(r db.ListCouponsRow) (*coupon.Coupon, error) { return couponOf(couponRow(r)) },
		func(c *coupon.Coupon) string { return c.ID.String() })
}

// ListExpired is not organization-scoped: the termination job scans every
// organization.
func (r couponRepo) ListExpired(ctx context.Context, now time.Time, page core.PageRequest) (core.Page[*coupon.Coupon], error) {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return core.Page[*coupon.Coupon]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*coupon.Coupon]{}, err
	}
	rows, err := q.ListExpiredCoupons(ctx, db.ListExpiredCouponsParams{Now: utc(now), After: after, Lim: lim})
	if err != nil {
		return core.Page[*coupon.Coupon]{}, fmt.Errorf("list expired coupons: %w", mapErr(err))
	}
	return pageOf(rows, page,
		func(r db.ListExpiredCouponsRow) (*coupon.Coupon, error) { return couponOf(couponRow(r)) },
		func(c *coupon.Coupon) string { return c.ID.String() })
}

// appliedCouponRepo implements coupon.AppliedRepository.
type appliedCouponRepo struct{ t *txState }

var _ coupon.AppliedRepository = appliedCouponRepo{}

func appliedCouponParams(a *coupon.AppliedCoupon) db.CreateAppliedCouponParams {
	p := db.CreateAppliedCouponParams{
		OrganizationID:             a.OrganizationID.UUID(),
		ID:                         a.ID.UUID(),
		CouponID:                   a.CouponID.UUID(),
		CustomerID:                 a.CustomerID.UUID(),
		Amount:                     a.Amount,
		AmountCurrency:             currencyCode(a.Currency),
		Frequency:                  string(a.Frequency),
		FrequencyDuration:          optInt32(a.FrequencyDuration),
		FrequencyDurationRemaining: optInt32(a.FrequencyDurationRemaining),
		RemainingAmount:            a.RemainingAmount,
		Status:                     string(a.Status),
		TerminatedAt:               optUTC(a.TerminatedAt),
		CreatedAt:                  utc(a.CreatedAt),
		UpdatedAt:                  utc(a.UpdatedAt),
	}
	if a.PercentageRate != nil {
		v := *a.PercentageRate
		p.PercentageRate = &v
	}
	return p
}

func appliedCouponOf(r db.AppliedCoupon) (*coupon.AppliedCoupon, error) {
	cur, err := currency(r.AmountCurrency)
	if err != nil {
		return nil, fmt.Errorf("applied coupon %s: %w", r.ID, err)
	}
	var rate *money.Decimal
	if r.PercentageRate != nil {
		v := *r.PercentageRate
		rate = &v
	}
	return &coupon.AppliedCoupon{
		ID:                         idOf[core.AppliedCouponID](r.ID),
		OrganizationID:             idOf[core.OrganizationID](r.OrganizationID),
		CouponID:                   idOf[core.CouponID](r.CouponID),
		CustomerID:                 idOf[core.CustomerID](r.CustomerID),
		Amount:                     r.Amount,
		Currency:                   cur,
		PercentageRate:             rate,
		Frequency:                  coupon.Frequency(r.Frequency),
		FrequencyDuration:          optInt(r.FrequencyDuration),
		FrequencyDurationRemaining: optInt(r.FrequencyDurationRemaining),
		RemainingAmount:            r.RemainingAmount,
		Status:                     coupon.Status(r.Status),
		TerminatedAt:               optUTC(r.TerminatedAt),
		CreatedAt:                  utc(r.CreatedAt),
		UpdatedAt:                  utc(r.UpdatedAt),
	}, nil
}

func (r appliedCouponRepo) Create(ctx context.Context, a *coupon.AppliedCoupon) error {
	q, err := r.t.queries(ctx, a.OrganizationID)
	if err != nil {
		return err
	}
	p := appliedCouponParams(a)
	n, err := q.CreateAppliedCoupon(ctx, p)
	return insertErr("applied_coupon", n, err)
}

func (r appliedCouponRepo) Update(ctx context.Context, a *coupon.AppliedCoupon) error {
	q, err := r.t.queries(ctx, a.OrganizationID)
	if err != nil {
		return err
	}
	p := appliedCouponParams(a)
	n, err := q.UpdateAppliedCoupon(ctx, db.UpdateAppliedCouponParams(p))
	return updateErr("applied_coupon", a.ID, n, err)
}

func (r appliedCouponRepo) Get(ctx context.Context, org core.OrganizationID, id core.AppliedCouponID) (*coupon.AppliedCoupon, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetAppliedCoupon(ctx, db.GetAppliedCouponParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("applied_coupon", id, err)
	}
	return appliedCouponOf(row)
}

func (r appliedCouponRepo) ListActiveByCustomer(ctx context.Context, org core.OrganizationID, cust core.CustomerID) ([]*coupon.AppliedCoupon, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListActiveAppliedCouponsByCustomer(ctx, db.ListActiveAppliedCouponsByCustomerParams{OrganizationID: org.UUID(), CustomerID: cust.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list active applied coupons of customer %s: %w", cust, mapErr(err))
	}
	return convAll(rows, appliedCouponOf)
}

func (r appliedCouponRepo) ListActiveByCoupon(ctx context.Context, org core.OrganizationID, c core.CouponID) ([]*coupon.AppliedCoupon, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListActiveAppliedCouponsByCoupon(ctx, db.ListActiveAppliedCouponsByCouponParams{OrganizationID: org.UUID(), CouponID: c.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list active applications of coupon %s: %w", c, mapErr(err))
	}
	return convAll(rows, appliedCouponOf)
}

func (r appliedCouponRepo) CountByCouponAndCustomer(ctx context.Context, org core.OrganizationID, c core.CouponID, cust core.CustomerID) (int, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	n, err := q.CountAppliedCouponsByCouponAndCustomer(ctx, db.CountAppliedCouponsByCouponAndCustomerParams{
		OrganizationID: org.UUID(), CouponID: c.UUID(), CustomerID: cust.UUID(),
	})
	if err != nil {
		return 0, fmt.Errorf("count applications of coupon %s: %w", c, mapErr(err))
	}
	return int(n), nil
}

func (r appliedCouponRepo) List(ctx context.Context, org core.OrganizationID, f coupon.AppliedFilter, page core.PageRequest) (core.Page[*coupon.AppliedCoupon], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*coupon.AppliedCoupon]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*coupon.AppliedCoupon]{}, err
	}
	rows, err := q.ListAppliedCoupons(ctx, db.ListAppliedCouponsParams{
		OrganizationID: org.UUID(),
		CustomerID:     optUUID(f.CustomerID),
		CouponID:       optUUID(f.CouponID),
		Status:         optString(string(f.Status)),
		After:          after,
		Lim:            lim,
	})
	if err != nil {
		return core.Page[*coupon.AppliedCoupon]{}, fmt.Errorf("list applied coupons: %w", mapErr(err))
	}
	return pageOf(rows, page, appliedCouponOf, func(a *coupon.AppliedCoupon) string { return a.ID.String() })
}
