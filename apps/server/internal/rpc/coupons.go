package rpc

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/billing/deduct"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/money"
	couponv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/coupon/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/coupon/v1/couponv1connect"
)

// Coupons serves tanvo.coupon.v1.CouponService over the billing library's
// deduction service (lago Api::V1::CouponsController,
// Api::V1::AppliedCouponsController and
// Api::V1::Customers::AppliedCouponsController).
type Coupons struct {
	couponv1connect.UnimplementedCouponServiceHandler

	svc *deduct.Service
}

var _ couponv1connect.CouponServiceHandler = (*Coupons)(nil)

// NewCoupons returns the service over svc.
func NewCoupons(svc *deduct.Service) *Coupons { return &Coupons{svc: svc} }

var (
	couponTypes = NewEnum(map[coupon.Type]couponv1.CouponType{
		coupon.TypeFixedAmount: couponv1.CouponType_COUPON_TYPE_FIXED_AMOUNT,
		coupon.TypePercentage:  couponv1.CouponType_COUPON_TYPE_PERCENTAGE,
	})
	couponFrequencies = NewEnum(map[coupon.Frequency]couponv1.CouponFrequency{
		coupon.FrequencyOnce:      couponv1.CouponFrequency_COUPON_FREQUENCY_ONCE,
		coupon.FrequencyRecurring: couponv1.CouponFrequency_COUPON_FREQUENCY_RECURRING,
		coupon.FrequencyForever:   couponv1.CouponFrequency_COUPON_FREQUENCY_FOREVER,
	})
	couponExpirations = NewEnum(map[coupon.Expiration]couponv1.CouponExpiration{
		coupon.ExpirationNoExpiration: couponv1.CouponExpiration_COUPON_EXPIRATION_NO_EXPIRATION,
		coupon.ExpirationTimeLimit:    couponv1.CouponExpiration_COUPON_EXPIRATION_TIME_LIMIT,
	})
	couponStatuses = NewEnum(map[coupon.Status]couponv1.CouponStatus{
		coupon.StatusActive:     couponv1.CouponStatus_COUPON_STATUS_ACTIVE,
		coupon.StatusTerminated: couponv1.CouponStatus_COUPON_STATUS_TERMINATED,
	})
)

// couponUpdateFields are the update_mask paths of UpdateCoupon.
var couponUpdateFields = []string{
	"code", "name", "description", "coupon_type", "amount", "percentage_rate", "frequency",
	"frequency_duration", "reusable", "expiration", "expiration_at", "limited_plan_ids",
	"limited_billable_metric_ids",
}

// couponFields renames the domain's field paths to the proto's.
var couponFields = map[string]string{
	"amount_cents":             "amount.amount",
	"amount_currency":          "amount.currency",
	"limited_plans":            "limited_plan_ids",
	"limited_billable_metrics": "limited_billable_metric_ids",
	"coupon":                   "coupon_id",
}

// CreateCoupon implements CouponService.CreateCoupon (lago
// Coupons::CreateService): reusable defaults to true, and the limitations
// are plan and billable metric IDs or codes.
func (s *Coupons) CreateCoupon(ctx context.Context, req *couponv1.CreateCouponRequest) (*couponv1.CreateCouponResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetCoupon()
	in := deduct.CreateCouponInput{
		Code:              b.GetCode(),
		Name:              b.GetName(),
		Description:       Opt(b.HasDescription(), b.GetDescription()),
		Type:              couponTypes.Core(b.GetCouponType()),
		Frequency:         couponFrequencies.Core(b.GetFrequency()),
		FrequencyDuration: OptInt(b.HasFrequencyDuration(), b.GetFrequencyDuration()),
		Reusable:          Opt(b.HasReusable(), b.GetReusable()),
		Expiration:        couponExpirations.Core(b.GetExpiration()),
		ExpirationAt:      TimePtr(b.GetExpirationAt()),
	}
	if in.Amount, in.Currency, err = couponAmount("coupon.amount", b.HasAmount(), b); err != nil {
		return nil, err
	}
	if in.PercentageRate, err = couponRate("coupon.percentage_rate", b.HasPercentageRate(), b.GetPercentageRate()); err != nil {
		return nil, err
	}
	if in.Plans, err = CatalogRefs("coupon.limited_plan_ids", b.GetLimitedPlanIds()); err != nil {
		return nil, err
	}
	if in.BillableMetrics, err = CatalogRefs("coupon.limited_billable_metric_ids", b.GetLimitedBillableMetricIds()); err != nil {
		return nil, err
	}
	c, err := s.svc.CreateCouponFrom(ctx, p.OrganizationID, in)
	if err != nil {
		return nil, couponError(ctx, "coupon.", "invalid coupon", err)
	}
	out, err := s.couponOut(ctx, p.OrganizationID, c)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return couponv1.CreateCouponResponse_builder{Coupon: out}.Build(), nil
}

// GetCoupon implements CouponService.GetCoupon: a terminated coupon is
// returned, a deleted one is not found.
func (s *Coupons) GetCoupon(ctx context.Context, req *couponv1.GetCouponRequest) (*couponv1.GetCouponResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.couponLookup(p.OrganizationID).Find(ctx, "coupon_id", req.GetCouponId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	out, err := s.couponOut(ctx, p.OrganizationID, c)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return couponv1.GetCouponResponse_builder{Coupon: out}.Build(), nil
}

// ListCoupons implements CouponService.ListCoupons (lago CouponsQuery):
// the coupons that are not deleted, active first, then by expiration,
// filtered on status and searched by name and code.
func (s *Coupons) ListCoupons(ctx context.Context, req *couponv1.ListCouponsRequest) (*couponv1.ListCouponsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	f, err := couponListFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	f.Search = req.GetSearch()
	page, err := s.svc.ListCoupons(ctx, p.OrganizationID, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	views, err := s.svc.CouponViews(ctx, p.OrganizationID, page.Items)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return couponv1.ListCouponsResponse_builder{
		Coupons:       Items(page, func(c *coupon.Coupon) *couponv1.Coupon { return couponProto(c, views[c.ID]) }),
		NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// UpdateCoupon implements CouponService.UpdateCoupon (lago
// Coupons::UpdateService). coupon.id is an ID or code:<code>. The fields
// updated are the update_mask paths or, without one, the fields set (the
// limitations when not empty). Once the coupon is applied, only the name,
// description and expiration change; the other fields are ignored.
func (s *Coupons) UpdateCoupon(ctx context.Context, req *couponv1.UpdateCouponRequest) (*couponv1.UpdateCouponResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetCoupon()
	paths, err := UpdatePaths(req.GetUpdateMask(), couponUpdateFields, func(path string) bool {
		switch path {
		case "code":
			return b.HasCode()
		case "name":
			return b.HasName()
		case "description":
			return b.HasDescription()
		case "coupon_type":
			return b.HasCouponType()
		case "amount":
			return b.HasAmount()
		case "percentage_rate":
			return b.HasPercentageRate()
		case "frequency":
			return b.HasFrequency()
		case "frequency_duration":
			return b.HasFrequencyDuration()
		case "reusable":
			return b.HasReusable()
		case "expiration":
			return b.HasExpiration()
		case "expiration_at":
			return b.HasExpirationAt()
		case "limited_plan_ids":
			return len(b.GetLimitedPlanIds()) > 0
		case "limited_billable_metric_ids":
			return len(b.GetLimitedBillableMetricIds()) > 0
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	var in deduct.UpdateCouponInput
	for _, path := range paths {
		switch path {
		case "code":
			in.Code = core.Some(b.GetCode())
		case "name":
			in.Name = core.Some(b.GetName())
		case "description":
			in.Description = core.Some(Opt(b.HasDescription(), b.GetDescription()))
		case "coupon_type":
			in.Type = core.Some(couponTypes.Core(b.GetCouponType()))
		case "amount":
			amount, currency, err := couponAmount("coupon.amount", b.HasAmount(), b)
			if err != nil {
				return nil, err
			}
			in.Amount, in.Currency = core.Some(amount), core.Some(currency)
		case "percentage_rate":
			rate, err := couponRate("coupon.percentage_rate", b.HasPercentageRate(), b.GetPercentageRate())
			if err != nil {
				return nil, err
			}
			in.PercentageRate = core.Some(rate)
		case "frequency":
			in.Frequency = core.Some(couponFrequencies.Core(b.GetFrequency()))
		case "frequency_duration":
			in.FrequencyDuration = core.Some(OptInt(b.HasFrequencyDuration(), b.GetFrequencyDuration()))
		case "reusable":
			in.Reusable = core.Some(b.GetReusable())
		case "expiration":
			in.Expiration = core.Some(couponExpirations.Core(b.GetExpiration()))
		case "expiration_at":
			in.ExpirationAt = core.Some(TimePtr(b.GetExpirationAt()))
		case "limited_plan_ids":
			plans, err := CatalogRefs("coupon.limited_plan_ids", b.GetLimitedPlanIds())
			if err != nil {
				return nil, err
			}
			in.Plans = core.Some(plans)
		case "limited_billable_metric_ids":
			metrics, err := CatalogRefs("coupon.limited_billable_metric_ids", b.GetLimitedBillableMetricIds())
			if err != nil {
				return nil, err
			}
			in.BillableMetrics = core.Some(metrics)
		}
	}
	cur, err := s.couponLookup(p.OrganizationID).Find(ctx, "coupon.id", b.GetId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	c, err := s.svc.UpdateCouponFrom(ctx, p.OrganizationID, cur.ID, in)
	if err != nil {
		return nil, couponError(ctx, "coupon.", "invalid coupon", err)
	}
	out, err := s.couponOut(ctx, p.OrganizationID, c)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return couponv1.UpdateCouponResponse_builder{Coupon: out}.Build(), nil
}

// TerminateCoupon implements CouponService.TerminateCoupon (lago
// Coupons::TerminateService).
func (s *Coupons) TerminateCoupon(ctx context.Context, req *couponv1.TerminateCouponRequest) (*couponv1.TerminateCouponResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := s.couponLookup(p.OrganizationID).Find(ctx, "coupon_id", req.GetCouponId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	c, err := s.svc.TerminateCoupon(ctx, p.OrganizationID, cur.ID)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out, err := s.couponOut(ctx, p.OrganizationID, c)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return couponv1.TerminateCouponResponse_builder{Coupon: out}.Build(), nil
}

// DeleteCoupon implements CouponService.DeleteCoupon (lago
// Coupons::DestroyService): the coupon is deleted, keeping its status, and
// its active applications are terminated.
func (s *Coupons) DeleteCoupon(ctx context.Context, req *couponv1.DeleteCouponRequest) (*couponv1.DeleteCouponResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := s.couponLookup(p.OrganizationID).Find(ctx, "coupon_id", req.GetCouponId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	c, err := s.svc.DestroyCoupon(ctx, p.OrganizationID, cur.ID)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out, err := s.couponOut(ctx, p.OrganizationID, c)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return couponv1.DeleteCouponResponse_builder{Coupon: out}.Build(), nil
}

// ApplyCoupon implements CouponService.ApplyCoupon (lago
// AppliedCoupons::CreateService). applied_coupon.customer_id is an ID or
// external_id:<id> and applied_coupon.coupon_id an ID or code:<code>; a
// missing customer is reported before a missing coupon.
func (s *Coupons) ApplyCoupon(ctx context.Context, req *couponv1.ApplyCouponRequest) (*couponv1.ApplyCouponResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetAppliedCoupon()
	in := deduct.ApplyCouponInput{OrganizationID: p.OrganizationID}
	in.Overrides.Frequency = couponFrequencies.Core(b.GetFrequency())
	in.Overrides.FrequencyDuration = OptInt(b.HasFrequencyDuration(), b.GetFrequencyDuration())
	if b.HasAmount() {
		amount, currency, err := FromMoney("applied_coupon.amount", b.GetAmount())
		if err != nil {
			return nil, err
		}
		in.Overrides.Amount, in.Overrides.Currency = &amount, currency
	}
	if in.Overrides.PercentageRate, err = couponRate("applied_coupon.percentage_rate", b.HasPercentageRate(), b.GetPercentageRate()); err != nil {
		return nil, err
	}
	cust, err := s.couponCustomerLookup(p.OrganizationID).Find(ctx, "applied_coupon.customer_id", b.GetCustomerId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	c, err := s.couponLookup(p.OrganizationID).Find(ctx, "applied_coupon.coupon_id", b.GetCouponId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	in.CustomerID, in.CouponID = cust.ID, c.ID
	a, err := s.svc.ApplyCoupon(ctx, in)
	if err != nil {
		return nil, couponError(ctx, "applied_coupon.", "invalid applied coupon", err)
	}
	views, err := s.svc.AppliedCouponViews(ctx, p.OrganizationID, []*coupon.AppliedCoupon{a}, false)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return couponv1.ApplyCouponResponse_builder{AppliedCoupon: appliedCouponProto(a, views[a.ID])}.Build(), nil
}

// GetAppliedCoupon is left to the embedded Unimplemented handler: v1.55.0
// has no show route or resolver for one applied coupon.

// ListAppliedCoupons implements CouponService.ListAppliedCoupons (lago
// AppliedCouponsQuery): the applications to customers that are not
// deleted, newest first, filtered on status and coupon_id (IDs or
// code:<code>, deleted coupons included). customer_id, an ID or
// external_id:<id>, names a customer that must exist.
func (s *Coupons) ListAppliedCoupons(ctx context.Context, req *couponv1.ListAppliedCouponsRequest) (*couponv1.ListAppliedCouponsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	f, err := appliedCouponListFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	if req.GetCustomerId() != "" {
		cust, err := s.couponCustomerLookup(p.OrganizationID).Find(ctx, "customer_id", req.GetCustomerId())
		if err != nil {
			return nil, Error(ctx, err)
		}
		f.CustomerID = cust.ID
	}
	page, err := s.svc.ListAppliedCoupons(ctx, p.OrganizationID, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	views, err := s.svc.AppliedCouponViews(ctx, p.OrganizationID, page.Items, true)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return couponv1.ListAppliedCouponsResponse_builder{
		AppliedCoupons: Items(page, func(a *coupon.AppliedCoupon) *couponv1.AppliedCoupon { return appliedCouponProto(a, views[a.ID]) }),
		NextPageToken:  new(page.NextToken),
	}.Build(), nil
}

// TerminateAppliedCoupon implements CouponService.TerminateAppliedCoupon
// (lago AppliedCoupons::TerminateService).
func (s *Coupons) TerminateAppliedCoupon(ctx context.Context, req *couponv1.TerminateAppliedCouponRequest) (*couponv1.TerminateAppliedCouponResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.AppliedCouponID]("applied_coupon_id", req.GetAppliedCouponId())
	if err != nil {
		return nil, err
	}
	a, err := s.svc.TerminateAppliedCoupon(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	views, err := s.svc.AppliedCouponViews(ctx, p.OrganizationID, []*coupon.AppliedCoupon{a}, false)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return couponv1.TerminateAppliedCouponResponse_builder{AppliedCoupon: appliedCouponProto(a, views[a.ID])}.Build(), nil
}

func (s *Coupons) couponLookup(org core.OrganizationID) Lookup[core.CouponID, *coupon.Coupon] {
	return Lookup[core.CouponID, *coupon.Coupon]{
		ByID: func(ctx context.Context, id core.CouponID) (*coupon.Coupon, error) {
			return s.svc.GetCoupon(ctx, org, id)
		},
		ByCode: func(ctx context.Context, code string) (*coupon.Coupon, error) {
			return s.svc.GetCouponByCode(ctx, org, code)
		},
	}
}

func (s *Coupons) couponCustomerLookup(org core.OrganizationID) Lookup[core.CustomerID, *customer.Customer] {
	return Lookup[core.CustomerID, *customer.Customer]{
		ByID: func(ctx context.Context, id core.CustomerID) (*customer.Customer, error) {
			return s.svc.GetCouponCustomer(ctx, org, id, "")
		},
		ByExternalID: func(ctx context.Context, externalID string) (*customer.Customer, error) {
			return s.svc.GetCouponCustomer(ctx, org, core.CustomerID{}, externalID)
		},
	}
}

// couponError maps the service errors, whose field paths are the
// coupon's, onto the request's message at prefix ("coupon.").
func couponError(ctx context.Context, prefix, msg string, err error) error {
	v, ok := validationViolations("", err)
	if !ok {
		return Error(ctx, err)
	}
	for i := range v {
		if f, ok := couponFields[v[i].Field]; ok {
			v[i].Field = f
		}
		v[i].Field = prefix + v[i].Field
	}
	return withReason(InvalidArgument(msg, v), ReasonValidationErrors)
}

// couponAmount converts the amount of b, when set, to minor units.
func couponAmount(field string, has bool, b *couponv1.Coupon) (*int64, money.Currency, error) {
	if !has {
		return nil, money.Currency{}, nil
	}
	amount, currency, err := FromMoney(field, b.GetAmount())
	if err != nil {
		return nil, money.Currency{}, err
	}
	return &amount, currency, nil
}

// couponRate parses a percentage rate, when set.
func couponRate(field string, has bool, s string) (*money.Decimal, error) {
	if !has {
		return nil, nil
	}
	d, err := ParseDecimal(field, s)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func couponListFilter(s string) (coupon.Filter, error) {
	var f coupon.Filter
	e, err := ParseFilter(s)
	if err != nil {
		return f, err
	}
	eq, err := Equalities(e, "status")
	if err != nil {
		return f, err
	}
	f.Status, err = couponFilterStatus(eq["status"])
	return f, err
}

func appliedCouponListFilter(s string) (coupon.AppliedFilter, error) {
	var f coupon.AppliedFilter
	e, err := ParseFilter(s)
	if err != nil {
		return f, err
	}
	eq, err := Equalities(e, "status", "coupon_id")
	if err != nil {
		return f, err
	}
	if f.Status, err = couponFilterStatus(eq["status"]); err != nil {
		return f, err
	}
	for _, v := range eq["coupon_id"] {
		r, err := CatalogRef(FilterField, v)
		if err != nil {
			return f, err
		}
		if r.Code != "" {
			f.CouponCodes = append(f.CouponCodes, r.Code)
			continue
		}
		id, err := ParseID[core.CouponID](FilterField, r.ID.String())
		if err != nil {
			return f, err
		}
		f.CouponIDs = append(f.CouponIDs, id)
	}
	return f, nil
}

func couponFilterStatus(values []string) (coupon.Status, error) {
	v, err := FilterSingle("status", values)
	if err != nil || v == "" {
		return "", err
	}
	st, err := FilterEnum[couponv1.CouponStatus]("status", []string{v}, couponv1.CouponStatus(0).Descriptor())
	if err != nil {
		return "", err
	}
	return couponStatuses.Core(st[0]), nil
}

// couponOut returns c with its view.
func (s *Coupons) couponOut(ctx context.Context, org core.OrganizationID, c *coupon.Coupon) (*couponv1.Coupon, error) {
	views, err := s.svc.CouponViews(ctx, org, []*coupon.Coupon{c})
	if err != nil {
		return nil, err
	}
	return couponProto(c, views[c.ID]), nil
}

func couponProto(c *coupon.Coupon, v deduct.CouponView) *couponv1.Coupon {
	b := couponv1.Coupon_builder{
		LimitedPlans:           new(c.LimitedToPlans()),
		LimitedBillableMetrics: new(c.LimitedToBillableMetrics()),
		PlanCodes:              v.PlanCodes,
		BillableMetricCodes:    v.BillableMetricCodes,
		Id:                     new(c.ID.String()),
		Code:                   new(c.Code),
		Name:                   new(c.Name),
		Description:            c.Description,
		PercentageRate:         DecimalPtr(c.PercentageRate),
		Reusable:               new(c.Reusable),
		ExpirationAt:           TimestampPtr(c.ExpirationAt),
		TerminatedAt:           TimestampPtr(c.TerminatedAt),
		CreatedAt:              Timestamp(c.CreatedAt),
		UpdatedAt:              Timestamp(c.UpdatedAt),
	}
	if t := couponTypes.Proto(c.Type); t != 0 {
		b.CouponType = &t
	}
	if c.Amount != nil {
		b.Amount = Money(*c.Amount, c.Currency)
	}
	if f := couponFrequencies.Proto(c.Frequency); f != 0 {
		b.Frequency = &f
	}
	if c.FrequencyDuration != nil {
		b.FrequencyDuration = new(int32(*c.FrequencyDuration))
	}
	if e := couponExpirations.Proto(c.Expiration); e != 0 {
		b.Expiration = &e
	}
	if st := couponStatuses.Proto(c.Status); st != 0 {
		b.Status = &st
	}
	for _, id := range c.LimitedPlanIDs {
		b.LimitedPlanIds = append(b.LimitedPlanIds, id.String())
	}
	for _, id := range c.LimitedBillableMetricIDs {
		b.LimitedBillableMetricIds = append(b.LimitedBillableMetricIds, id.String())
	}
	return b.Build()
}

func appliedCouponProto(a *coupon.AppliedCoupon, v deduct.AppliedCouponView) *couponv1.AppliedCoupon {
	b := couponv1.AppliedCoupon_builder{
		ExternalCustomerId: new(v.CustomerExternalID),
		Id:                 new(a.ID.String()),
		CouponId:           new(a.CouponID.String()),
		CustomerId:         new(a.CustomerID.String()),
		PercentageRate:     DecimalPtr(a.PercentageRate),
		TerminatedAt:       TimestampPtr(a.TerminatedAt),
		CreatedAt:          Timestamp(a.CreatedAt),
		UpdatedAt:          Timestamp(a.UpdatedAt),
	}
	if a.Amount != nil {
		b.Amount = Money(*a.Amount, a.Currency)
	}
	if a.RemainingAmount != nil {
		b.RemainingAmount = Money(*a.RemainingAmount, a.Currency)
	}
	if f := couponFrequencies.Proto(a.Frequency); f != 0 {
		b.Frequency = &f
	}
	if a.FrequencyDuration != nil {
		b.FrequencyDuration = new(int32(*a.FrequencyDuration))
	}
	if a.FrequencyDurationRemaining != nil {
		b.FrequencyDurationRemaining = new(int32(*a.FrequencyDurationRemaining))
	}
	if st := couponStatuses.Proto(a.Status); st != 0 {
		b.Status = &st
	}
	if c := v.Coupon; c != nil {
		b.CouponCode, b.CouponName, b.CouponDescription = new(c.Code), new(c.Name), c.Description
		if st := couponStatuses.Proto(c.Status); st != 0 {
			b.CouponStatus = &st
		}
		b.CouponDeletedAt, b.ExpirationAt = TimestampPtr(c.DeletedAt), TimestampPtr(c.ExpirationAt)
	}
	for _, cr := range v.Credits {
		cb := couponv1.AppliedCouponCredit_builder{
			Id:          new(cr.Credit.ID.String()),
			Amount:      Money(cr.Credit.Amount, cr.Credit.Currency),
			BeforeTaxes: new(cr.Credit.BeforeTaxes),
			InvoiceId:   new(cr.Credit.InvoiceID.String()),
		}
		if st := payStatuses.Proto(cr.InvoicePaymentStatus); st != 0 {
			cb.InvoicePaymentStatus = &st
		}
		b.Credits = append(b.Credits, cb.Build())
	}
	return b.Build()
}
