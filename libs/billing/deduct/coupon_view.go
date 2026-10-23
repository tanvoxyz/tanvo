package deduct

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// CouponView is what the coupon API shows beside a coupon.
type CouponView struct {
	// PlanCodes are the codes of the kept limited plans that are not
	// override plans.
	PlanCodes []string
	// BillableMetricCodes are the codes of the kept limited metrics.
	BillableMetricCodes []string
}

// CouponViews returns the view of each of cs, by coupon ID.
//
// source: app/serializers/v1/coupon_serializer.rb:18-21
func (s *Service) CouponViews(ctx context.Context, org core.OrganizationID, cs []*coupon.Coupon) (map[core.CouponID]CouponView, error) {
	return couponRead(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (map[core.CouponID]CouponView, error) {
		out := make(map[core.CouponID]CouponView, len(cs))
		for _, c := range cs {
			v := CouponView{PlanCodes: []string{}, BillableMetricCodes: []string{}}
			for _, id := range c.LimitedPlanIDs {
				p, err := r.Plans.Get(ctx, org, id)
				if errors.Is(err, core.ErrNotFound) {
					continue
				}
				if err != nil {
					return nil, fmt.Errorf("load plan %s: %w", id, err)
				}
				if !p.Deleted() && p.ParentID.IsZero() {
					v.PlanCodes = append(v.PlanCodes, p.Code)
				}
			}
			for _, id := range c.LimitedBillableMetricIDs {
				m, err := r.Metrics.Get(ctx, org, id)
				if errors.Is(err, core.ErrNotFound) {
					continue
				}
				if err != nil {
					return nil, fmt.Errorf("load billable metric %s: %w", id, err)
				}
				v.BillableMetricCodes = append(v.BillableMetricCodes, m.Code)
			}
			out[c.ID] = v
		}
		return out, nil
	})
}

// AppliedCouponView is what the coupon API shows beside an applied coupon.
type AppliedCouponView struct {
	// Coupon is the applied coupon's coupon, deleted or not.
	Coupon *coupon.Coupon
	// CustomerExternalID is the customer's external ID.
	CustomerExternalID string
	// Credits are the invoice credits of the applied coupon, in creation
	// order, when asked for.
	Credits []AppliedCouponCredit
}

// AppliedCouponCredit is one invoice credit of an applied coupon.
type AppliedCouponCredit struct {
	// Credit is the credit.
	Credit *invoice.Credit
	// InvoicePaymentStatus is the payment status of the credited invoice.
	InvoicePaymentStatus core.PaymentStatus
}

// AppliedCouponViews returns the view of each of as, by applied coupon ID;
// withCredits also loads their credits (the list of applied coupons).
//
// source: app/serializers/v1/applied_coupon_serializer.rb:5-32, app/serializers/v1/credit_serializer.rb:5-22
func (s *Service) AppliedCouponViews(ctx context.Context, org core.OrganizationID, as []*coupon.AppliedCoupon, withCredits bool) (map[core.AppliedCouponID]AppliedCouponView, error) {
	return couponRead(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (map[core.AppliedCouponID]AppliedCouponView, error) {
		coupons := map[core.CouponID]*coupon.Coupon{}
		externalIDs := map[core.CustomerID]string{}
		invoices := map[core.InvoiceID]core.PaymentStatus{}
		out := make(map[core.AppliedCouponID]AppliedCouponView, len(as))
		for _, a := range as {
			c, ok := coupons[a.CouponID]
			if !ok {
				var err error
				if c, err = r.Coupons.Get(ctx, org, a.CouponID); err != nil {
					return nil, fmt.Errorf("load coupon %s: %w", a.CouponID, err)
				}
				coupons[a.CouponID] = c
			}
			ext, ok := externalIDs[a.CustomerID]
			if !ok {
				cust, err := r.Customers.Get(ctx, org, a.CustomerID)
				if err != nil {
					return nil, fmt.Errorf("load customer %s: %w", a.CustomerID, err)
				}
				ext = cust.ExternalID
				externalIDs[a.CustomerID] = ext
			}
			v := AppliedCouponView{Coupon: c, CustomerExternalID: ext}
			if withCredits {
				credits, err := r.Credits.ListByAppliedCoupon(ctx, org, a.ID)
				if err != nil {
					return nil, fmt.Errorf("load credits of applied coupon %s: %w", a.ID, err)
				}
				v.Credits = make([]AppliedCouponCredit, 0, len(credits))
				for _, cr := range credits {
					st, ok := invoices[cr.InvoiceID]
					if !ok {
						inv, err := r.Invoices.Get(ctx, org, cr.InvoiceID)
						if err != nil {
							return nil, fmt.Errorf("load invoice %s: %w", cr.InvoiceID, err)
						}
						st = inv.PaymentStatus
						invoices[cr.InvoiceID] = st
					}
					v.Credits = append(v.Credits, AppliedCouponCredit{Credit: cr, InvoicePaymentStatus: st})
				}
			}
			out[a.ID] = v
		}
		return out, nil
	})
}
