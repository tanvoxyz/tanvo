package deduct

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// CreateCoupon validates and stores a new coupon (lago
// Coupons::CreateService): its limitations must name existing plans or
// billable metrics of its organization, and not both. A zero ID is
// assigned; the status starts active. The checks run in lago's order: the
// expiration date, the limitations, then the coupon's fields.
func (s *Service) CreateCoupon(ctx context.Context, c *coupon.Coupon) error {
	now := s.d.Now()
	if c.ID.IsZero() {
		c.ID = core.NewID[core.CouponID](s.d.IDs)
	}
	c.Status, c.TerminatedAt, c.DeletedAt = coupon.StatusActive, nil, nil
	c.CreatedAt, c.UpdatedAt = now, now
	if err := checkExpirationAt(c, now); err != nil {
		return err
	}
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := checkLimitations(ctx, r, c); err != nil {
			return err
		}
		if err := c.Validate(now); err != nil {
			return err
		}
		if err := r.Coupons.Create(ctx, c); err != nil {
			return couponCodeConflict(fmt.Errorf("create coupon: %w", err))
		}
		return s.logCoupon(ctx, r, auditlog.ActivityCouponCreated, nil, c)
	})
}

// checkLimitations verifies that the plans and billable metrics a coupon
// is limited to exist (core.NotFound("plans") or
// core.NotFound("billable_metrics")) and that they are not both set (a
// [coupon.CodeOnlyOneLimitationType] rule error).
func checkLimitations(ctx context.Context, r port.Repos, c *coupon.Coupon) error {
	if c.LimitedToPlans() {
		plans, err := r.Plans.ListByIDs(ctx, c.OrganizationID, c.LimitedPlanIDs)
		if err != nil && !errors.Is(err, core.ErrNotFound) {
			return fmt.Errorf("list plans: %w", err)
		}
		if err != nil || len(plans) != len(c.LimitedPlanIDs) {
			return core.NotFound("plans")
		}
	}
	if c.LimitedToBillableMetrics() {
		metrics, err := r.Metrics.ListByIDs(ctx, c.OrganizationID, c.LimitedBillableMetricIDs)
		if err != nil && !errors.Is(err, core.ErrNotFound) {
			return fmt.Errorf("list billable metrics: %w", err)
		}
		if err != nil || len(metrics) != len(c.LimitedBillableMetricIDs) {
			return core.NotFound("billable_metrics")
		}
	}
	if c.LimitedToPlans() && c.LimitedToBillableMetrics() {
		return core.NewRuleError(coupon.CodeOnlyOneLimitationType, "a coupon is limited to plans or billable metrics, not both")
	}
	return nil
}

// UpdateCoupon changes a coupon with fn (lago Coupons::UpdateService). Once
// the coupon has been applied, only its name, description and expiration
// change and changes to the other fields are ignored ([coupon.KeepLocked]);
// otherwise its limitations are checked as on creation. The checks run in
// lago's order: the expiration date, the limitations, then the coupon's
// fields.
func (s *Service) UpdateCoupon(ctx context.Context, org core.OrganizationID, id core.CouponID, fn func(*coupon.Coupon)) (*coupon.Coupon, error) {
	var out *coupon.Coupon
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		old, err := keptCoupon(ctx, r, org, id)
		if err != nil {
			return err
		}
		next := *old
		next.LimitedPlanIDs = slices.Clone(old.LimitedPlanIDs)
		next.LimitedBillableMetricIDs = slices.Clone(old.LimitedBillableMetricIDs)
		fn(&next)
		next.ID, next.OrganizationID, next.Status, next.TerminatedAt, next.DeletedAt, next.CreatedAt = old.ID, old.OrganizationID, old.Status, old.TerminatedAt, old.DeletedAt, old.CreatedAt
		now := s.d.Now()
		if !equalTime(old.ExpirationAt, next.ExpirationAt) {
			if err := checkExpirationAt(&next, now); err != nil {
				return err
			}
		}
		page, err := r.AppliedCoupons.List(ctx, org, coupon.AppliedFilter{CouponID: id}, core.PageRequest{Size: 1})
		if err != nil {
			return fmt.Errorf("list applications of coupon %s: %w", id, err)
		}
		if len(page.Items) > 0 {
			coupon.KeepLocked(old, &next)
		} else if err := checkLimitations(ctx, r, &next); err != nil {
			return err
		}
		// The expiration date was checked when it changed: a stored one is
		// not rechecked.
		if err := next.Validate(time.Time{}); err != nil {
			return err
		}
		next.UpdatedAt = now
		if err := r.Coupons.Update(ctx, &next); err != nil {
			return couponCodeConflict(fmt.Errorf("update coupon %s: %w", id, err))
		}
		out = &next
		return s.logCoupon(ctx, r, auditlog.ActivityCouponUpdated, old, &next)
	})
	return out, err
}

// TerminateCoupon terminates a coupon unless it already is (lago
// Coupons::TerminateService); its applied coupons keep applying.
func (s *Service) TerminateCoupon(ctx context.Context, org core.OrganizationID, id core.CouponID) (*coupon.Coupon, error) {
	var out *coupon.Coupon
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := keptCoupon(ctx, r, org, id)
		if err != nil {
			return err
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

// DestroyCoupon deletes a coupon (lago Coupons::DestroyService): the
// coupon keeps its status, leaves the lists and frees its code, and its
// active applied coupons are terminated.
func (s *Service) DestroyCoupon(ctx context.Context, org core.OrganizationID, id core.CouponID) (*coupon.Coupon, error) {
	var out *coupon.Coupon
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := keptCoupon(ctx, r, org, id)
		if err != nil {
			return err
		}
		now := s.d.Now()
		c.Delete(now)
		if err := r.Coupons.Update(ctx, c); err != nil {
			return fmt.Errorf("update coupon %s: %w", id, err)
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
			// coupons/destroy_service.rb:24-26 terminates them through
			// AppliedCoupons::TerminateService.
			if err := s.logAppliedCoupon(ctx, r, auditlog.ActivityAppliedCouponDeleted, a); err != nil {
				return err
			}
		}
		out = c
		return s.logCoupon(ctx, r, auditlog.ActivityCouponDeleted, nil, c)
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
// coupon must be active and not deleted, not overlap the plans or metrics
// of the customer's active coupons, and be reusable or never applied to
// the customer ([coupon.CheckApplication]). A fixed
// amount coupon keeps its own currency, which becomes the customer's
// default currency when the customer has none; the customer's currency is
// a default, not a constraint (multi-currency customers).
func (s *Service) ApplyCoupon(ctx context.Context, in ApplyCouponInput) (*coupon.AppliedCoupon, error) {
	var out *coupon.AppliedCoupon
	org := in.OrganizationID
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		cust, err := r.Customers.Get(ctx, org, in.CustomerID)
		if errors.Is(err, core.ErrNotFound) || (err == nil && cust.Deleted()) {
			return core.NotFound("customer")
		}
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
		if err := coupon.CheckApplication(c, facts); err != nil {
			return err
		}
		now := s.d.Now()
		a := coupon.Apply(c, core.NewID[core.AppliedCouponID](s.d.IDs), cust.ID, in.Overrides, now)
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
		return s.logAppliedCoupon(ctx, r, auditlog.ActivityAppliedCouponCreated, a)
	})
	return out, err
}

// applicationFacts gathers what [coupon.CheckApplication] needs.
func applicationFacts(ctx context.Context, r port.Repos, c *coupon.Coupon, cust *customer.Customer) (coupon.ApplicationFacts, error) {
	org := c.OrganizationID
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
		if errors.Is(err, core.ErrNotFound) {
			return core.NotFound("applied_coupon")
		}
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
		// Logged even when already terminated (terminate_service.rb:15-17).
		return s.logAppliedCoupon(ctx, r, auditlog.ActivityAppliedCouponDeleted, a)
	})
	return out, err
}

// logCoupon records the activity log t of c, whose attributes before an
// update are before (lago activity_loggable of coupons/create_service.rb:12,
// update_service.rb:14 and destroy_service.rb:12).
func (s *Service) logCoupon(ctx context.Context, r port.Repos, t auditlog.ActivityType, before, c *coupon.Coupon) error {
	return s.d.LogChange(ctx, r, kernel.Change{
		Type: t, OrganizationID: c.OrganizationID, ResourceType: auditlog.ResourceCoupon, ResourceID: c.ID.UUID(),
		Object: c, Before: before,
	})
}

// logAppliedCoupon records the activity log t of a, anchored on its coupon
// (applied_coupons/create_service.rb:15, terminate_service.rb:17,
// activity_log.rb:177-178).
func (s *Service) logAppliedCoupon(ctx context.Context, r port.Repos, t auditlog.ActivityType, a *coupon.AppliedCoupon) error {
	return s.d.LogChange(ctx, r, kernel.Change{
		Type: t, OrganizationID: a.OrganizationID, ResourceType: auditlog.ResourceCoupon, ResourceID: a.CouponID.UUID(),
		Object: a, CustomerID: a.CustomerID,
	})
}
