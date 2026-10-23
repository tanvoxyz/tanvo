package deduct

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// CreateCouponInput is the input of [Service.CreateCouponFrom] (lago
// Api::V1::CouponsController create).
type CreateCouponInput struct {
	// Code is the unique code.
	Code string
	// Name is the display name.
	Name string
	// Description is free text.
	Description *string
	// Type is fixed_amount or percentage.
	Type coupon.Type
	// Amount is the discount of a fixed_amount coupon, in minor units.
	Amount *int64
	// Currency is the currency of Amount.
	Currency money.Currency
	// PercentageRate is the discount of a percentage coupon, in percent.
	PercentageRate *money.Decimal
	// Frequency is once, recurring or forever.
	Frequency coupon.Frequency
	// FrequencyDuration is the number of billing periods of a recurring
	// coupon.
	FrequencyDuration *int
	// Reusable allows applying the coupon several times to a customer;
	// nil is true (coupons/create_service.rb:22).
	Reusable *bool
	// Expiration is no_expiration or time_limit.
	Expiration coupon.Expiration
	// ExpirationAt is the expiry instant of a time_limit coupon.
	ExpirationAt *time.Time
	// Plans limit the coupon to these plans.
	Plans []refs.Ref
	// BillableMetrics limit the coupon to these billable metrics.
	BillableMetrics []refs.Ref
}

// CreateCouponFrom creates a coupon from in ([Service.CreateCoupon]): the
// plans and billable metrics are resolved by ID or code, and a missing one
// fails with core.NotFound("plans") or core.NotFound("billable_metrics")
// (coupons/create_service.rb:44-48).
func (s *Service) CreateCouponFrom(ctx context.Context, org core.OrganizationID, in CreateCouponInput) (*coupon.Coupon, error) {
	now := s.d.Now()
	c := coupon.New(org, core.CouponID{}, in.Code, in.Name, in.Type, now)
	c.Description = in.Description
	c.Amount, c.Currency, c.PercentageRate = in.Amount, in.Currency, in.PercentageRate
	c.Frequency, c.FrequencyDuration = in.Frequency, in.FrequencyDuration
	if in.Reusable != nil {
		c.Reusable = *in.Reusable
	}
	c.Expiration, c.ExpirationAt = in.Expiration, in.ExpirationAt
	if err := checkExpirationAt(c, now); err != nil {
		return nil, err
	}
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if c.LimitedPlanIDs, err = couponPlans(r, org).IDs(ctx, in.Plans); err != nil {
			return err
		}
		c.LimitedBillableMetricIDs, err = couponMetrics(r, org).IDs(ctx, in.BillableMetrics)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := s.CreateCoupon(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// UpdateCouponInput is the input of [Service.UpdateCouponFrom]; unset
// fields keep their value.
type UpdateCouponInput struct {
	// Name is the display name.
	Name core.Optional[string]
	// Description is free text.
	Description core.Optional[*string]
	// Expiration is no_expiration or time_limit.
	Expiration core.Optional[coupon.Expiration]
	// ExpirationAt is the expiry instant.
	ExpirationAt core.Optional[*time.Time]
	// Code is the unique code; locked once applied.
	Code core.Optional[string]
	// Type is locked once applied.
	Type core.Optional[coupon.Type]
	// Amount is locked once applied.
	Amount core.Optional[*int64]
	// Currency is locked once applied.
	Currency core.Optional[money.Currency]
	// PercentageRate is locked once applied.
	PercentageRate core.Optional[*money.Decimal]
	// Frequency is locked once applied.
	Frequency core.Optional[coupon.Frequency]
	// FrequencyDuration is locked once applied.
	FrequencyDuration core.Optional[*int]
	// Reusable is locked once applied.
	Reusable core.Optional[bool]
	// Plans replace the plan limitations; locked once applied.
	Plans core.Optional[[]refs.Ref]
	// BillableMetrics replace the billable metric limitations; locked once
	// applied.
	BillableMetrics core.Optional[[]refs.Ref]
}

// UpdateCouponFrom changes coupon id with in ([Service.UpdateCoupon]).
// Until the coupon is applied, the limitations given are resolved by ID or
// code as on creation, and limiting it to plans removes its billable
// metrics unless they are given too, and the other way round
// (coupons/update_service.rb:44-54).
func (s *Service) UpdateCouponFrom(ctx context.Context, org core.OrganizationID, id core.CouponID, in UpdateCouponInput) (*coupon.Coupon, error) {
	var (
		plans   []core.PlanID
		metrics []core.BillableMetricID
	)
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := keptCoupon(ctx, r, org, id)
		if err != nil {
			return err
		}
		// Coupons::ValidateService checks any expiration date given.
		if in.ExpirationAt.Set {
			next := *c
			next.ExpirationAt = in.ExpirationAt.Value
			if err := checkExpirationAt(&next, s.d.Now()); err != nil {
				return err
			}
		}
		page, err := r.AppliedCoupons.List(ctx, org, coupon.AppliedFilter{CouponID: id}, core.PageRequest{Size: 1})
		if err != nil {
			return fmt.Errorf("list applications of coupon %s: %w", id, err)
		}
		if len(page.Items) > 0 {
			// Locked: the limitations given are not looked up.
			return nil
		}
		if in.Plans.Set {
			if plans, err = couponPlans(r, org).IDs(ctx, in.Plans.Value); err != nil {
				return err
			}
		}
		if in.BillableMetrics.Set {
			metrics, err = couponMetrics(r, org).IDs(ctx, in.BillableMetrics.Value)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.UpdateCoupon(ctx, org, id, func(c *coupon.Coupon) {
		in.Name.Apply(&c.Name)
		in.Description.Apply(&c.Description)
		in.Expiration.Apply(&c.Expiration)
		in.ExpirationAt.Apply(&c.ExpirationAt)
		in.Code.Apply(&c.Code)
		in.Type.Apply(&c.Type)
		in.Amount.Apply(&c.Amount)
		in.Currency.Apply(&c.Currency)
		in.PercentageRate.Apply(&c.PercentageRate)
		in.Frequency.Apply(&c.Frequency)
		in.FrequencyDuration.Apply(&c.FrequencyDuration)
		in.Reusable.Apply(&c.Reusable)
		switch {
		case in.Plans.Set:
			c.LimitedPlanIDs = plans
		case len(metrics) > 0:
			c.LimitedPlanIDs = nil
		}
		switch {
		case in.BillableMetrics.Set:
			c.LimitedBillableMetricIDs = metrics
		case len(plans) > 0:
			c.LimitedBillableMetricIDs = nil
		}
	})
}

// GetCoupon returns the coupon id, terminated or not; a deleted coupon is
// not found.
func (s *Service) GetCoupon(ctx context.Context, org core.OrganizationID, id core.CouponID) (*coupon.Coupon, error) {
	return couponRead(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*coupon.Coupon, error) {
		return keptCoupon(ctx, r, org, id)
	})
}

// GetCouponByCode returns the coupon with code that is not deleted,
// terminated or not (coupons_controller.rb:47).
func (s *Service) GetCouponByCode(ctx context.Context, org core.OrganizationID, code string) (*coupon.Coupon, error) {
	return couponRead(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*coupon.Coupon, error) {
		c, err := r.Coupons.GetByCode(ctx, org, code)
		if errors.Is(err, core.ErrNotFound) {
			return nil, core.NotFound("coupon")
		}
		return c, err
	})
}

// ListCoupons lists the coupons that are not deleted, filtered on status
// and searched by name and code, in the order of the API (lago
// CouponsQuery).
func (s *Service) ListCoupons(ctx context.Context, org core.OrganizationID, f coupon.Filter, page core.PageRequest) (core.Page[*coupon.Coupon], error) {
	f.Order = core.OrderRecent
	return couponRead(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (core.Page[*coupon.Coupon], error) {
		return r.Coupons.List(ctx, org, f, page)
	})
}

// GetAppliedCoupon returns the applied coupon id.
func (s *Service) GetAppliedCoupon(ctx context.Context, org core.OrganizationID, id core.AppliedCouponID) (*coupon.AppliedCoupon, error) {
	return couponRead(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*coupon.AppliedCoupon, error) {
		a, err := r.AppliedCoupons.Get(ctx, org, id)
		if errors.Is(err, core.ErrNotFound) {
			return nil, core.NotFound("applied_coupon")
		}
		return a, err
	})
}

// ListAppliedCoupons lists the applied coupons of customers that are not
// deleted, newest first (lago AppliedCouponsQuery); f.CouponCodes match
// deleted coupons too.
func (s *Service) ListAppliedCoupons(ctx context.Context, org core.OrganizationID, f coupon.AppliedFilter, page core.PageRequest) (core.Page[*coupon.AppliedCoupon], error) {
	f.KeptCustomers, f.Order = true, core.OrderRecent
	return couponRead(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (core.Page[*coupon.AppliedCoupon], error) {
		return r.AppliedCoupons.List(ctx, org, f, page)
	})
}

// GetCouponCustomer returns the customer id, or the customer with
// externalID when id is zero; a missing or deleted customer is
// core.NotFound("customer").
func (s *Service) GetCouponCustomer(ctx context.Context, org core.OrganizationID, id core.CustomerID, externalID string) (*customer.Customer, error) {
	return couponRead(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*customer.Customer, error) {
		var (
			c   *customer.Customer
			err error
		)
		if id.IsZero() {
			c, err = r.Customers.GetByExternalID(ctx, org, externalID)
		} else {
			c, err = r.Customers.Get(ctx, org, id)
		}
		if errors.Is(err, core.ErrNotFound) || (err == nil && c.Deleted()) {
			return nil, core.NotFound("customer")
		}
		return c, err
	})
}

// keptCoupon returns the coupon id of org; a missing or deleted coupon is
// core.NotFound("coupon") (lago's default scope, coupon.rb:67).
func keptCoupon(ctx context.Context, r port.Repos, org core.OrganizationID, id core.CouponID) (*coupon.Coupon, error) {
	c, err := r.Coupons.Get(ctx, org, id)
	if errors.Is(err, core.ErrNotFound) || (err == nil && c.Deleted()) {
		return nil, core.NotFound("coupon")
	}
	if err != nil {
		return nil, fmt.Errorf("get coupon %s: %w", id, err)
	}
	return c, nil
}

// checkExpirationAt checks that an expiration date is in the future (lago
// Coupons::ValidateService, which runs before the other checks).
func checkExpirationAt(c *coupon.Coupon, now time.Time) error {
	if c.ExpirationAt == nil {
		return nil
	}
	var v core.Validator
	v.Future("expiration_at", *c.ExpirationAt, now)
	return v.Err()
}

// couponCodeConflict turns a code uniqueness violation into the code
// validation error value_already_exist (app/models/coupon.rb:53).
func couponCodeConflict(err error) error {
	if errors.Is(err, core.ErrConflict) {
		return &core.ValidationError{Errors: []core.FieldError{{Field: "code", Code: core.CodeAlreadyExists}}}
	}
	return err
}

func equalTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// couponPlans finds the plans of coupon limitations; the resource of a
// missing one is lago's "plans".
func couponPlans(r port.Repos, org core.OrganizationID) refs.Finder[core.PlanID, *catalog.Plan] {
	f := refs.Plans(r, org)
	f.Resource = "plans"
	return f
}

// couponMetrics finds the billable metrics of coupon limitations; the
// resource of a missing one is lago's "billable_metrics".
func couponMetrics(r port.Repos, org core.OrganizationID) refs.Finder[core.BillableMetricID, *catalog.BillableMetric] {
	f := refs.Metrics(r, org)
	f.Resource = "billable_metrics"
	return f
}

// couponRead runs fn in a transaction and returns its result.
func couponRead[T any](ctx context.Context, tx port.Transactor, fn func(context.Context, port.Repos) (T, error)) (T, error) {
	var out T
	err := tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = fn(ctx, r)
		return err
	})
	return out, err
}
