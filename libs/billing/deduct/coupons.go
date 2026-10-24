package deduct

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

var hundred = money.NewFromInt(100)

// CouponAmount returns the discount of an applied coupon on base, in minor
// units (lago AppliedCoupons::AmountService, domain §22.7): a percentage
// gives round_half_up(base × rate / 100), at most base; a recurring or
// forever fixed amount min(amount, base); a once fixed amount
// min(remaining amount, base). A fractional base used as the amount is
// truncated to whole minor units. It is 0 when base ≤ 0.
func CouponAmount(a *coupon.AppliedCoupon, base money.Decimal) int64 {
	if !base.IsPositive() {
		return 0
	}
	whole := func(d money.Decimal) int64 {
		n, _ := d.Round(0, money.Floor).Int64()
		return max(n, 0)
	}
	if a.Percentage() {
		d := base.Mul(*a.PercentageRate).Div(hundred)
		if d.GreaterThanOrEqual(base) {
			return whole(base)
		}
		return kernel.RoundCents(d)
	}
	var amount int64
	switch {
	case a.Frequency == coupon.FrequencyOnce && a.RemainingAmount != nil:
		amount = *a.RemainingAmount
	case a.Amount != nil:
		amount = *a.Amount
	}
	if kernel.Cents(amount).GreaterThan(base) {
		return whole(base)
	}
	return max(amount, 0)
}

// couponSpread returns the amount a coupon of amount spreads over the
// targeted fees: the fractional base itself when the coupon is capped at
// it, as lago weights the fees on the amount it computed, before the
// credit's integer column truncates it
// (app/services/applied_coupons/amount_service.rb:28-38,
// app/services/credits/applied_coupon_service.rb:34-37).
func couponSpread(a *coupon.AppliedCoupon, base money.Decimal, amount int64) money.Decimal {
	if a.Percentage() {
		if base.Mul(*a.PercentageRate).Div(hundred).GreaterThanOrEqual(base) {
			return base
		}
		return kernel.Cents(amount)
	}
	var face int64
	switch {
	case a.Frequency == coupon.FrequencyOnce && a.RemainingAmount != nil:
		face = *a.RemainingAmount
	case a.Amount != nil:
		face = *a.Amount
	}
	if kernel.Cents(face).GreaterThan(base) {
		return base
	}
	return kernel.Cents(amount)
}

// couponRank orders applied coupons for application: metric-limited
// coupons first, then plan-limited ones, then the others.
func couponRank(c *coupon.Coupon) int {
	switch {
	case c.LimitedToBillableMetrics():
		return 0
	case c.LimitedToPlans():
		return 1
	default:
		return 2
	}
}

// catalogLookup caches the catalog facts coupon and wallet targeting need.
type catalogLookup struct {
	r       port.Repos
	org     core.OrganizationID
	plans   map[core.SubscriptionID]*catalog.Plan
	metrics map[core.ChargeID]core.BillableMetricID
	// targeted holds the charges accepting a target wallet.
	targeted map[core.ChargeID]bool
}

func newCatalogLookup(r port.Repos, org core.OrganizationID) *catalogLookup {
	return &catalogLookup{
		r: r, org: org, plans: map[core.SubscriptionID]*catalog.Plan{},
		metrics: map[core.ChargeID]core.BillableMetricID{}, targeted: map[core.ChargeID]bool{},
	}
}

// plan returns the plan of a subscription.
func (l *catalogLookup) plan(ctx context.Context, id core.SubscriptionID) (*catalog.Plan, error) {
	if p, ok := l.plans[id]; ok {
		return p, nil
	}
	sub, err := l.r.Subscriptions.Get(ctx, l.org, id)
	if err != nil {
		return nil, fmt.Errorf("get subscription %s: %w", id, err)
	}
	p, err := l.r.Plans.Get(ctx, l.org, sub.PlanID)
	if err != nil {
		return nil, fmt.Errorf("get plan %s: %w", sub.PlanID, err)
	}
	l.plans[id] = p
	return p, nil
}

// metric returns the billable metric of a charge fee, zero for other fees.
func (l *catalogLookup) metric(ctx context.Context, f *invoice.Fee) (core.BillableMetricID, error) {
	if f.ChargeID.IsZero() {
		return core.BillableMetricID{}, nil
	}
	if m, ok := l.metrics[f.ChargeID]; ok {
		return m, nil
	}
	c, err := l.r.Charges.Get(ctx, l.org, f.ChargeID)
	if err != nil {
		return core.BillableMetricID{}, fmt.Errorf("get charge %s: %w", f.ChargeID, err)
	}
	l.metrics[f.ChargeID] = c.BillableMetricID
	l.targeted[f.ChargeID] = c.AcceptsTargetWallet
	return c.BillableMetricID, nil
}

// acceptsTargetWallet reports whether the charge of a charge fee accepts a
// target wallet; [catalogLookup.metric] must have loaded it.
func (l *catalogLookup) acceptsTargetWallet(f *invoice.Fee) bool {
	return !f.ChargeID.IsZero() && l.targeted[f.ChargeID]
}

// targetedFees returns the fees a coupon applies to: the charge fees of
// its metrics, the fees of subscriptions on its plans (or their override
// plans), or every fee.
func (l *catalogLookup) targetedFees(ctx context.Context, c *coupon.Coupon, fees []*invoice.Fee) ([]*invoice.Fee, error) {
	if !c.Limited() {
		return fees, nil
	}
	var out []*invoice.Fee
	for _, f := range fees {
		switch {
		case c.LimitedToBillableMetrics():
			m, err := l.metric(ctx, f)
			if err != nil {
				return nil, err
			}
			if !m.IsZero() && slices.Contains(c.LimitedBillableMetricIDs, m) {
				out = append(out, f)
			}
		case !f.SubscriptionID.IsZero():
			p, err := l.plan(ctx, f.SubscriptionID)
			if err != nil {
				return nil, err
			}
			if slices.Contains(c.LimitedPlanIDs, p.ID) || (p.IsChild() && slices.Contains(c.LimitedPlanIDs, p.ParentID)) {
				out = append(out, f)
			}
		}
	}
	return out, nil
}

// applyCoupons runs step 2 (lago Credits::AppliedCouponsService) under the
// customer's coupons lock.
func (s *Service) applyCoupons(ctx context.Context, r port.Repos, inv *invoice.Invoice, fees []*invoice.Fee, opts Options, out *BeforeTaxes) error {
	if inv.FeesAmount == 0 {
		return nil
	}
	org := inv.OrganizationID
	if !opts.Preview {
		if err := kernel.LockCustomer(ctx, r, org, inv.CustomerID, customer.LockScopeCoupons); err != nil {
			return err
		}
	}
	applied, err := r.AppliedCoupons.ListActiveByCustomer(ctx, org, inv.CustomerID)
	if err != nil {
		return fmt.Errorf("list applied coupons: %w", err)
	}
	if len(applied) == 0 {
		return nil
	}
	coupons := make(map[core.CouponID]*coupon.Coupon, len(applied))
	for _, a := range applied {
		if _, ok := coupons[a.CouponID]; ok {
			continue
		}
		c, err := r.Coupons.Get(ctx, org, a.CouponID)
		if err != nil {
			return fmt.Errorf("get coupon %s: %w", a.CouponID, err)
		}
		coupons[a.CouponID] = c
	}
	// lago orders by limited_billable_metrics DESC, limited_plans DESC,
	// applied_coupons.created_at ASC; the ID breaks ties so that the order
	// never depends on the repository's.
	slices.SortStableFunc(applied, func(a, b *coupon.AppliedCoupon) int {
		if c := couponRank(coupons[a.CouponID]) - couponRank(coupons[b.CouponID]); c != 0 {
			return c
		}
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID.String(), b.ID.String())
	})
	existing, err := r.Credits.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return fmt.Errorf("list credits of invoice %s: %w", inv.ID, err)
	}
	lookup := newCatalogLookup(r, org)
	maps.Copy(lookup.plans, opts.Plans)
	for _, a := range applied {
		if inv.SubTotalExcludingTaxesAmount <= 0 {
			break
		}
		if slices.ContainsFunc(existing, func(c *invoice.Credit) bool { return c.AppliedCouponID == a.ID }) {
			continue
		}
		c, err := s.applyCoupon(ctx, r, lookup, inv, fees, a, coupons[a.CouponID], opts)
		if err != nil {
			return err
		}
		if c == nil {
			continue
		}
		out.Coupons += c.Amount
		out.Credits = append(out.Credits, c)
	}
	return nil
}

// applyCoupon applies one coupon (lago Credits::AppliedCouponService) and
// returns its credit, or nil when the coupon does not apply.
func (s *Service) applyCoupon(ctx context.Context, r port.Repos, lookup *catalogLookup, inv *invoice.Invoice, fees []*invoice.Fee, a *coupon.AppliedCoupon, c *coupon.Coupon, opts Options) (*invoice.Credit, error) {
	if !a.Percentage() && a.Currency.Code() != inv.Currency.Code() {
		return nil, nil
	}
	targeted, err := lookup.targetedFees(ctx, c, fees)
	if err != nil {
		return nil, err
	}
	if len(targeted) == 0 {
		return nil, nil
	}
	base := kernel.Cents(inv.SubTotalExcludingTaxesAmount)
	if c.Limited() {
		base = money.Zero
		for _, f := range targeted {
			base = base.Add(f.SubTotalExcludingTaxesPreciseAmount())
		}
	}
	if !base.IsPositive() {
		return nil, nil
	}
	amount := CouponAmount(a, base)
	now := s.d.Now()
	credit := &invoice.Credit{
		ID: core.NewID[core.InvoiceCreditID](s.d.IDs), OrganizationID: inv.OrganizationID, InvoiceID: inv.ID,
		AppliedCouponID: a.ID, Amount: amount, Currency: inv.Currency, BeforeTaxes: true, CreatedAt: now,
	}
	spread := couponSpread(a, base, amount)
	for _, f := range targeted {
		share := money.Prorate(spread, f.SubTotalExcludingTaxesPreciseAmount(), base)
		f.PreciseCouponsAmount = money.Min(f.PreciseCouponsAmount.Add(share), kernel.Cents(f.Amount))
	}
	if err := a.Consume(amount, now); err != nil {
		return nil, fmt.Errorf("consume applied coupon %s: %w", a.ID, err)
	}
	if !opts.Preview {
		if err := r.Credits.Create(ctx, credit); err != nil {
			return nil, fmt.Errorf("create coupon credit: %w", err)
		}
		if err := r.AppliedCoupons.Update(ctx, a); err != nil {
			return nil, fmt.Errorf("update applied coupon %s: %w", a.ID, err)
		}
	}
	inv.CouponsAmount += amount
	inv.SubTotalExcludingTaxesAmount -= amount
	return credit, nil
}
