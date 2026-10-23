package deduct

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// CreateCoupon validates and stores a new coupon (lago
// Coupons::CreateService): its limitations must name existing plans or
// billable metrics of its organization, and not both. A zero ID is
// assigned; the status starts active.
func (s *Service) CreateCoupon(ctx context.Context, c *coupon.Coupon) error {
	now := s.d.Now()
	if c.ID.IsZero() {
		c.ID = core.NewID[core.CouponID](s.d.IDs)
	}
	c.Status, c.TerminatedAt = coupon.StatusActive, nil
	c.CreatedAt, c.UpdatedAt = now, now
	if err := c.Validate(now); err != nil {
		return err
	}
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := checkLimitations(ctx, r, c); err != nil {
			return err
		}
		if err := r.Coupons.Create(ctx, c); err != nil {
			return fmt.Errorf("create coupon: %w", err)
		}
		return nil
	})
}

// checkLimitations verifies that the plans and billable metrics a coupon
// is limited to exist.
func checkLimitations(ctx context.Context, r port.Repos, c *coupon.Coupon) error {
	if c.LimitedToPlans() {
		plans, err := r.Plans.ListByIDs(ctx, c.OrganizationID, c.LimitedPlanIDs)
		if err != nil {
			return fmt.Errorf("list plans: %w", err)
		}
		if len(plans) != len(c.LimitedPlanIDs) {
			return fmt.Errorf("coupon plans: %w", core.ErrNotFound)
		}
	}
	if c.LimitedToBillableMetrics() {
		metrics, err := r.Metrics.ListByIDs(ctx, c.OrganizationID, c.LimitedBillableMetricIDs)
		if err != nil {
			return fmt.Errorf("list billable metrics: %w", err)
		}
		if len(metrics) != len(c.LimitedBillableMetricIDs) {
			return fmt.Errorf("coupon billable metrics: %w", core.ErrNotFound)
		}
	}
	return nil
}

// UpdateCoupon changes a coupon with fn (lago Coupons::UpdateService). Once
// the coupon has been applied, only its name, description and expiration
// may change ([coupon.CheckUpdate]); otherwise its limitations are checked
// as on creation.
func (s *Service) UpdateCoupon(ctx context.Context, org core.OrganizationID, id core.CouponID, fn func(*coupon.Coupon)) (*coupon.Coupon, error) {
	var out *coupon.Coupon
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		old, err := r.Coupons.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("get coupon %s: %w", id, err)
		}
		next := *old
		next.LimitedPlanIDs = slices.Clone(old.LimitedPlanIDs)
		next.LimitedBillableMetricIDs = slices.Clone(old.LimitedBillableMetricIDs)
		fn(&next)
		next.ID, next.OrganizationID, next.Status, next.TerminatedAt, next.CreatedAt = old.ID, old.OrganizationID, old.Status, old.TerminatedAt, old.CreatedAt
		page, err := r.AppliedCoupons.List(ctx, org, coupon.AppliedFilter{CouponID: id}, core.PageRequest{Size: 1})
		if err != nil {
			return fmt.Errorf("list applications of coupon %s: %w", id, err)
		}
		applied := len(page.Items) > 0
		if err := coupon.CheckUpdate(old, &next, applied); err != nil {
			return err
		}
		now := s.d.Now()
		if err := next.Validate(now); err != nil {
			return err
		}
		if !applied {
			if err := checkLimitations(ctx, r, &next); err != nil {
				return err
			}
		}
		next.UpdatedAt = now
		if err := r.Coupons.Update(ctx, &next); err != nil {
			return fmt.Errorf("update coupon %s: %w", id, err)
		}
		out = &next
		return nil
	})
	return out, err
}

// TerminateCoupon terminates a coupon unless it already is (lago
// Coupons::TerminateService); its applied coupons keep applying.
func (s *Service) TerminateCoupon(ctx context.Context, org core.OrganizationID, id core.CouponID) (*coupon.Coupon, error) {
	var out *coupon.Coupon
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := r.Coupons.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("get coupon %s: %w", id, err)
		}
		if c.Status != coupon.StatusTerminated {
			if err := c.Terminate(s.d.Now()); err != nil {
				return err
			}
			if err := r.Coupons.Update(ctx, c); err != nil {
				return fmt.Errorf("update coupon %s: %w", id, err)
			}
		}
		out = c
		return nil
	})
	return out, err
}

// DestroyCoupon removes a coupon from use (lago Coupons::DestroyService):
// the coupon is terminated and so are its active applied coupons. libs/core
// has no deleted state for coupons, so the coupon stays listed as
// terminated.
func (s *Service) DestroyCoupon(ctx context.Context, org core.OrganizationID, id core.CouponID) (*coupon.Coupon, error) {
	var out *coupon.Coupon
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := r.Coupons.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("get coupon %s: %w", id, err)
		}
		now := s.d.Now()
		if c.Status != coupon.StatusTerminated {
			if err := c.Terminate(now); err != nil {
				return err
			}
			if err := r.Coupons.Update(ctx, c); err != nil {
				return fmt.Errorf("update coupon %s: %w", id, err)
			}
		}
		applied, err := r.AppliedCoupons.ListActiveByCoupon(ctx, org, id)
		if err != nil {
			return fmt.Errorf("list applications of coupon %s: %w", id, err)
		}
		for _, a := range applied {
			if err := a.Terminate(now); err != nil {
				return err
			}
			if err := r.AppliedCoupons.Update(ctx, a); err != nil {
				return fmt.Errorf("update applied coupon %s: %w", a.ID, err)
			}
		}
		out = c
		return nil
	})
	return out, err
}

// TerminateExpiredCoupons terminates the active time-limited coupons of
// organization org whose expiration is at or before at (lago
// Coupons::TerminateService.terminate_all_expired) and returns how many.
// at is the instant of the clock tick that dispatched the run, not the
// worker's "now", so that a backlog terminates what had expired at the
// tick; the termination itself is stamped with the clock. Each coupon is
// rechecked in the transaction, so a rerun is a no-op.
func (s *Service) TerminateExpiredCoupons(ctx context.Context, org core.OrganizationID, at time.Time) (int, error) {
	n := 0
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		// libs/core lists the expired coupons of every organization; the
		// run keeps org's.
		expired, err := kernel.All(func(p core.PageRequest) (core.Page[*coupon.Coupon], error) {
			return r.Coupons.ListExpired(ctx, at, p)
		})
		if err != nil {
			return fmt.Errorf("list expired coupons: %w", err)
		}
		now := s.d.Now()
		for _, c := range expired {
			if c.OrganizationID != org || c.Status != coupon.StatusActive || !c.Expired(at) {
				continue
			}
			if err := c.Terminate(now); err != nil {
				return err
			}
			if err := r.Coupons.Update(ctx, c); err != nil {
				return fmt.Errorf("update coupon %s: %w", c.ID, err)
			}
			n++
		}
		return nil
	})
	return n, err
}

// ApplyCouponInput is the input of [Service.ApplyCoupon].
type ApplyCouponInput struct {
	// OrganizationID is the organization.
	OrganizationID core.OrganizationID
	// CouponID is the coupon to apply.
	CouponID core.CouponID
	// CustomerID is the customer.
	CustomerID core.CustomerID
	// Overrides override the coupon's terms for this customer.
	Overrides coupon.Overrides
}

// ApplyCoupon applies a coupon to a customer (lago
// AppliedCoupons::CreateService) under the customer's coupons lock: the
// coupon must be active, reusable or never applied to the customer, not
// overlap the plans or metrics of the customer's active coupons. A fixed
// amount coupon keeps its own currency, which becomes the customer's
// default currency when the customer has none; the customer's currency is
// a default, not a constraint (multi-currency customers).
func (s *Service) ApplyCoupon(ctx context.Context, in ApplyCouponInput) (*coupon.AppliedCoupon, error) {
	var out *coupon.AppliedCoupon
	org := in.OrganizationID
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		cust, err := r.Customers.Get(ctx, org, in.CustomerID)
		if err != nil {
			return fmt.Errorf("get customer %s: %w", in.CustomerID, err)
		}
		if err := kernel.LockCustomer(ctx, r, org, cust.ID, customer.LockScopeCoupons); err != nil {
			return err
		}
		c, err := r.Coupons.Get(ctx, org, in.CouponID)
		if err != nil {
			return fmt.Errorf("get coupon %s: %w", in.CouponID, err)
		}
		facts, err := applicationFacts(ctx, r, c, cust)
		if err != nil {
			return err
		}
		now := s.d.Now()
		a := coupon.Apply(c, core.NewID[core.AppliedCouponID](s.d.IDs), cust.ID, in.Overrides, now)
		if err := coupon.CheckApplication(c, a, facts); err != nil {
			return err
		}
		if err := a.Validate(); err != nil {
			return err
		}
		if a.Amount != nil && cust.Currency.IsZero() {
			cust.Currency = a.Currency
			cust.UpdatedAt = now
			if err := r.Customers.Update(ctx, cust); err != nil {
				return fmt.Errorf("set currency of customer %s: %w", cust.ID, err)
			}
		}
		if err := r.AppliedCoupons.Create(ctx, a); err != nil {
			return fmt.Errorf("create applied coupon: %w", err)
		}
		out = a
		return nil
	})
	return out, err
}

// applicationFacts gathers what [coupon.CheckApplication] needs.
func applicationFacts(ctx context.Context, r port.Repos, c *coupon.Coupon, cust *customer.Customer) (coupon.ApplicationFacts, error) {
	org := c.OrganizationID
	// CustomerCurrency stays zero: a customer's currency is only a default
	// (lago Customers::UpdateCurrencyService), so no currency check applies.
	f := coupon.ApplicationFacts{PlanMetrics: map[core.PlanID][]core.BillableMetricID{}}
	n, err := r.AppliedCoupons.CountByCouponAndCustomer(ctx, org, c.ID, cust.ID)
	if err != nil {
		return f, fmt.Errorf("count applications of coupon %s: %w", c.ID, err)
	}
	f.AppliedCount = n
	active, err := r.AppliedCoupons.ListActiveByCustomer(ctx, org, cust.ID)
	if err != nil {
		return f, fmt.Errorf("list applied coupons: %w", err)
	}
	plans := slices.Clone(c.LimitedPlanIDs)
	for _, a := range active {
		oc, err := r.Coupons.Get(ctx, org, a.CouponID)
		if err != nil {
			return f, fmt.Errorf("get coupon %s: %w", a.CouponID, err)
		}
		f.Active = append(f.Active, oc)
		plans = append(plans, oc.LimitedPlanIDs...)
	}
	for _, p := range plans {
		if _, ok := f.PlanMetrics[p]; ok {
			continue
		}
		charges, err := r.Charges.ListByPlan(ctx, org, p)
		if err != nil {
			return f, fmt.Errorf("list charges of plan %s: %w", p, err)
		}
		metrics := []core.BillableMetricID{}
		for _, ch := range charges {
			metrics = append(metrics, ch.BillableMetricID)
		}
		f.PlanMetrics[p] = metrics
	}
	return f, nil
}

// TerminateAppliedCoupon terminates an applied coupon unless it already is
// (lago AppliedCoupons::TerminateService).
func (s *Service) TerminateAppliedCoupon(ctx context.Context, org core.OrganizationID, id core.AppliedCouponID) (*coupon.AppliedCoupon, error) {
	var out *coupon.AppliedCoupon
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		a, err := r.AppliedCoupons.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("get applied coupon %s: %w", id, err)
		}
		if a.Active() {
			if err := a.Terminate(s.d.Now()); err != nil {
				return err
			}
			if err := r.AppliedCoupons.Update(ctx, a); err != nil {
				return fmt.Errorf("update applied coupon %s: %w", id, err)
			}
		}
		out = a
		return nil
	})
	return out, err
}
