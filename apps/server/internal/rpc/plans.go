package rpc

import (
	"context"
	"fmt"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/libs/billing/plans"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
	catalogv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1/catalogv1connect"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
)

// Plans serves tanvo.catalog.v1.PlanService over the billing library's
// plan service (lago Api::V1::PlansController).
type Plans struct {
	catalogv1connect.UnimplementedPlanServiceHandler

	svc *plans.Service
}

var _ catalogv1connect.PlanServiceHandler = (*Plans)(nil)

// NewPlans returns the service over svc.
func NewPlans(svc *plans.Service) *Plans { return &Plans{svc: svc} }

var (
	planIntervals = NewEnum(map[period.Interval]commonv1.Interval{
		period.Weekly:     commonv1.Interval_INTERVAL_WEEKLY,
		period.Monthly:    commonv1.Interval_INTERVAL_MONTHLY,
		period.Quarterly:  commonv1.Interval_INTERVAL_QUARTERLY,
		period.Semiannual: commonv1.Interval_INTERVAL_SEMIANNUAL,
		period.Yearly:     commonv1.Interval_INTERVAL_YEARLY,
	})
	planStatuses = NewEnum(map[catalog.PlanStatus]catalogv1.PlanStatus{
		catalog.PlanStatusActive:          catalogv1.PlanStatus_PLAN_STATUS_ACTIVE,
		catalog.PlanStatusPendingDeletion: catalogv1.PlanStatus_PLAN_STATUS_PENDING_DELETION,
		catalog.PlanStatusDeleted:         catalogv1.PlanStatus_PLAN_STATUS_DELETED,
	})
	commitmentTypes = NewEnum(map[catalog.CommitmentType]catalogv1.CommitmentType{
		catalog.CommitmentTypeMinimumCommitment: catalogv1.CommitmentType_COMMITMENT_TYPE_MINIMUM_COMMITMENT,
	})
)

// planUpdateFields are the update_mask paths of UpdatePlan.
var planUpdateFields = []string{
	"code", "name", "invoice_display_name", "description", "interval", "amount", "pay_in_advance",
	"trial_period_days", "bill_charges_monthly", "bill_fixed_charges_monthly", "tax_ids", "metadata",
	"charges", "fixed_charges", "minimum_commitment", "usage_thresholds",
}

// CreatePlan implements PlanService.CreatePlan.
func (s *Plans) CreatePlan(ctx context.Context, req *catalogv1.CreatePlanRequest) (*catalogv1.CreatePlanResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetPlan()
	amount, currency, err := FromMoney("plan.amount", b.GetAmount())
	if err != nil {
		return nil, err
	}
	taxes, err := CatalogRefs("plan.tax_ids", b.GetTaxIds())
	if err != nil {
		return nil, err
	}
	in := plans.CreateInput{
		Code:                    b.GetCode(),
		Name:                    b.GetName(),
		InvoiceDisplayName:      Opt(b.HasInvoiceDisplayName(), b.GetInvoiceDisplayName()),
		Description:             Opt(b.HasDescription(), b.GetDescription()),
		Interval:                planIntervals.Core(b.GetInterval()),
		Amount:                  amount,
		Currency:                currency,
		PayInAdvance:            b.GetPayInAdvance(),
		TrialPeriodDays:         int(b.GetTrialPeriodDays()),
		BillChargesMonthly:      Opt(b.HasBillChargesMonthly(), b.GetBillChargesMonthly()),
		BillFixedChargesMonthly: Opt(b.HasBillFixedChargesMonthly(), b.GetBillFixedChargesMonthly()),
		Taxes:                   taxes,
		Metadata:                createMetadata(b.GetMetadata()),
	}
	if in.Charges, err = planCharges(b); err != nil {
		return nil, err
	}
	if in.FixedCharges, err = planFixedCharges(b); err != nil {
		return nil, err
	}
	if in.UsageThresholds, err = thresholdInputs(b.GetUsageThresholds(), currency); err != nil {
		return nil, err
	}
	if b.HasMinimumCommitment() {
		if in.MinimumCommitment, err = commitmentInput(b.GetMinimumCommitment(), currency); err != nil {
			return nil, err
		}
	}
	plan, err := s.svc.Create(ctx, p.OrganizationID, in)
	if err != nil {
		return nil, planError(ctx, err)
	}
	pb, err := s.proto(ctx, plan)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.CreatePlanResponse_builder{Plan: pb}.Build(), nil
}

// GetPlan implements PlanService.GetPlan: by ID any plan that is not
// deleted, by code the original plan.
func (s *Plans) GetPlan(ctx context.Context, req *catalogv1.GetPlanRequest) (*catalogv1.GetPlanResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	plan, err := s.lookup(p.OrganizationID).Find(ctx, "plan_id", req.GetPlanId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	pb, err := s.proto(ctx, plan)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.GetPlanResponse_builder{Plan: pb}.Build(), nil
}

// ListPlans implements PlanService.ListPlans: the original plans, pending
// deletion included, by name and searched by name or code (lago
// PlansQuery). The filter fields have no counterpart in lago, so a filter
// is refused.
func (s *Plans) ListPlans(ctx context.Context, req *catalogv1.ListPlansRequest) (*catalogv1.ListPlansResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetFilter() != "" {
		return nil, filterError("plans have no filter fields")
	}
	page, err := s.svc.List(ctx, p.OrganizationID, req.GetSearch(), Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := make([]*catalogv1.Plan, len(page.Items))
	for i, plan := range page.Items {
		if out[i], err = s.proto(ctx, plan); err != nil {
			return nil, Error(ctx, err)
		}
	}
	return catalogv1.ListPlansResponse_builder{Plans: out, NextPageToken: new(page.NextToken)}.Build(), nil
}

// UpdatePlan implements PlanService.UpdatePlan. plan.id is an ID or
// code:<code>. The fields updated are the update_mask paths or, without
// one, the fields set; charges, fixed_charges and usage_thresholds replace
// the plan's, and an empty minimum_commitment removes it.
// update_subscriptions_amount is not supported.
func (s *Plans) UpdatePlan(ctx context.Context, req *catalogv1.UpdatePlanRequest) (*catalogv1.UpdatePlanResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetUpdateSubscriptionsAmount() {
		return nil, connect.NewError(connect.CodeUnimplemented, "update_subscriptions_amount is not supported")
	}
	b := req.GetPlan()
	paths, err := UpdatePaths(req.GetUpdateMask(), planUpdateFields, func(path string) bool {
		switch path {
		case "code":
			return b.HasCode()
		case "name":
			return b.HasName()
		case "invoice_display_name":
			return b.HasInvoiceDisplayName()
		case "description":
			return b.HasDescription()
		case "interval":
			return b.HasInterval()
		case "amount":
			return b.HasAmount()
		case "pay_in_advance":
			return b.HasPayInAdvance()
		case "trial_period_days":
			return b.HasTrialPeriodDays()
		case "bill_charges_monthly":
			return b.HasBillChargesMonthly()
		case "bill_fixed_charges_monthly":
			return b.HasBillFixedChargesMonthly()
		case "tax_ids":
			return len(b.GetTaxIds()) > 0
		case "metadata":
			return len(b.GetMetadata()) > 0
		case "charges":
			return len(b.GetCharges()) > 0
		case "fixed_charges":
			return len(b.GetFixedCharges()) > 0
		case "minimum_commitment":
			return b.HasMinimumCommitment()
		case "usage_thresholds":
			return len(b.GetUsageThresholds()) > 0
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	cur, err := s.lookup(p.OrganizationID).Find(ctx, "plan.id", b.GetId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	in := plans.UpdateInput{Cascade: req.GetCascadeUpdates()}
	currency := cur.Currency
	for _, path := range paths {
		switch path {
		case "code":
			in.Code = core.Some(b.GetCode())
		case "name":
			in.Name = core.Some(b.GetName())
		case "invoice_display_name":
			in.InvoiceDisplayName = core.Some(Opt(b.HasInvoiceDisplayName(), b.GetInvoiceDisplayName()))
		case "description":
			in.Description = core.Some(Opt(b.HasDescription(), b.GetDescription()))
		case "interval":
			in.Interval = core.Some(planIntervals.Core(b.GetInterval()))
		case "amount":
			amount, c, err := FromMoney("plan.amount", b.GetAmount())
			if err != nil {
				return nil, err
			}
			in.Amount, in.Currency, currency = core.Some(amount), core.Some(c), c
		case "pay_in_advance":
			in.PayInAdvance = core.Some(b.GetPayInAdvance())
		case "trial_period_days":
			in.TrialPeriodDays = core.Some(int(b.GetTrialPeriodDays()))
		case "bill_charges_monthly":
			in.BillChargesMonthly = core.Some(Opt(b.HasBillChargesMonthly(), b.GetBillChargesMonthly()))
		case "bill_fixed_charges_monthly":
			in.BillFixedChargesMonthly = core.Some(Opt(b.HasBillFixedChargesMonthly(), b.GetBillFixedChargesMonthly()))
		case "tax_ids":
			taxes, err := CatalogRefs("plan.tax_ids", b.GetTaxIds())
			if err != nil {
				return nil, err
			}
			if taxes == nil {
				taxes = []refs.Ref{}
			}
			in.Taxes = core.Some(taxes)
		case "metadata":
			in.Metadata = core.Some(FromMetadata(b.GetMetadata()))
		case "charges":
			charges, err := planCharges(b)
			if err != nil {
				return nil, err
			}
			in.Charges = core.Some(charges)
		case "fixed_charges":
			fixed, err := planFixedCharges(b)
			if err != nil {
				return nil, err
			}
			in.FixedCharges = core.Some(fixed)
		}
	}
	// The amounts of the commitment and thresholds are in the plan's new
	// currency.
	for _, path := range paths {
		switch path {
		case "minimum_commitment":
			c, err := commitmentInput(b.GetMinimumCommitment(), currency)
			if err != nil {
				return nil, err
			}
			in.MinimumCommitment = core.Some(c)
		case "usage_thresholds":
			ts, err := thresholdInputs(b.GetUsageThresholds(), currency)
			if err != nil {
				return nil, err
			}
			if ts == nil {
				ts = []plans.ThresholdInput{}
			}
			in.UsageThresholds = core.Some(ts)
		}
	}
	plan, err := s.svc.Update(ctx, p.OrganizationID, cur.ID, in)
	if err != nil {
		return nil, planError(ctx, err)
	}
	pb, err := s.proto(ctx, plan)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.UpdatePlanResponse_builder{Plan: pb}.Build(), nil
}

// DeletePlan implements PlanService.DeletePlan: the plan is pending
// deletion until a job terminates its subscriptions and deletes it.
func (s *Plans) DeletePlan(ctx context.Context, req *catalogv1.DeletePlanRequest) (*catalogv1.DeletePlanResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := s.lookup(p.OrganizationID).Find(ctx, "plan_id", req.GetPlanId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	plan, err := s.svc.PrepareDestroy(ctx, p.OrganizationID, cur.ID)
	if err != nil {
		return nil, Error(ctx, err)
	}
	pb, err := s.proto(ctx, plan)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.DeletePlanResponse_builder{Plan: pb}.Build(), nil
}

func (s *Plans) lookup(org core.OrganizationID) Lookup[core.PlanID, *catalog.Plan] {
	return Lookup[core.PlanID, *catalog.Plan]{
		ByID:   func(ctx context.Context, id core.PlanID) (*catalog.Plan, error) { return s.svc.Get(ctx, org, id) },
		ByCode: func(ctx context.Context, code string) (*catalog.Plan, error) { return s.svc.GetByCode(ctx, org, code) },
	}
}

// proto converts p with its charges, fixed charges, commitment,
// thresholds and counts.
func (s *Plans) proto(ctx context.Context, p *catalog.Plan) (*catalogv1.Plan, error) {
	v, err := s.svc.View(ctx, p)
	if err != nil {
		return nil, err
	}
	return planProto(v), nil
}

// planError maps the service errors, whose field paths are the plan's,
// onto the request's plan message.
func planError(ctx context.Context, err error) error {
	if v, ok := validationViolations("plan.", err); ok {
		return withReason(InvalidArgument("invalid plan", v), ReasonValidationErrors)
	}
	return Error(ctx, err)
}

func planCharges(b *catalogv1.Plan) ([]plans.ChargeInput, error) {
	var out []plans.ChargeInput
	for i, c := range b.GetCharges() {
		in, err := chargeInput(fmt.Sprintf("plan.charges[%d]", i), c)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, nil
}

func planFixedCharges(b *catalogv1.Plan) ([]plans.FixedChargeInput, error) {
	var out []plans.FixedChargeInput
	for i, f := range b.GetFixedCharges() {
		in, err := fixedChargeInput(fmt.Sprintf("plan.fixed_charges[%d]", i), f)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, nil
}

// minorAmount converts the decimal amount s of field, in major units of c,
// to minor units; more decimals than c has fail with an InvalidArgument
// violation on field.
func minorAmount(field, s string, c money.Currency) (int64, error) {
	d, err := ParseDecimal(field, s)
	if err != nil {
		return 0, err
	}
	minor, err := c.ToMinor(d, money.HalfUp)
	if err != nil || !c.FromMinor(minor).Equal(d) {
		return 0, InvalidArgument("invalid amount", []FieldViolation{{
			Field: field, Reason: core.CodeInvalid, Description: fmt.Sprintf("must have at most %d decimals", c.Exponent()),
		}})
	}
	return minor, nil
}

// thresholdInputs converts the usage thresholds of a plan in currency c;
// a threshold without amount is passed on as missing.
func thresholdInputs(in []*catalogv1.UsageThreshold, c money.Currency) ([]plans.ThresholdInput, error) {
	var out []plans.ThresholdInput
	for i, t := range in {
		ti := plans.ThresholdInput{
			Recurring:            t.GetRecurring(),
			ThresholdDisplayName: Opt(t.HasThresholdDisplayName(), t.GetThresholdDisplayName()),
		}
		if t.GetAmount() != "" {
			amount, err := minorAmount(fmt.Sprintf("plan.usage_thresholds[%d].amount", i), t.GetAmount(), c)
			if err != nil {
				return nil, err
			}
			ti.Amount = &amount
		}
		out = append(out, ti)
	}
	return out, nil
}

// commitmentInput converts the minimum commitment of a plan in currency
// c: only its set fields.
func commitmentInput(m *catalogv1.Commitment, c money.Currency) (*plans.CommitmentInput, error) {
	var in plans.CommitmentInput
	if m.HasAmount() {
		amount, err := minorAmount("plan.minimum_commitment.amount", m.GetAmount(), c)
		if err != nil {
			return nil, err
		}
		in.Amount = core.Some(amount)
	}
	if m.HasInvoiceDisplayName() {
		in.InvoiceDisplayName = core.Some(new(m.GetInvoiceDisplayName()))
	}
	if len(m.GetTaxIds()) > 0 {
		taxes, err := CatalogRefs("plan.minimum_commitment.tax_ids", m.GetTaxIds())
		if err != nil {
			return nil, err
		}
		in.Taxes = core.Some(taxes)
	}
	return &in, nil
}

func planProto(v *plans.View) *catalogv1.Plan {
	p := v.Plan
	b := catalogv1.Plan_builder{
		Id:                       new(p.ID.String()),
		Code:                     new(p.Code),
		Name:                     new(p.Name),
		InvoiceDisplayName:       p.InvoiceDisplayName,
		Description:              p.Description,
		Amount:                   Money(p.Amount, p.Currency),
		PayInAdvance:             new(p.PayInAdvance),
		TrialPeriodDays:          new(int32(p.TrialPeriodDays)),
		BillChargesMonthly:       p.BillChargesMonthly,
		BillFixedChargesMonthly:  p.BillFixedChargesMonthly,
		TaxIds:                   planIDStrings(p.TaxIDs),
		Metadata:                 Metadata(p.Metadata),
		ActiveSubscriptionsCount: new(int32(v.ActiveSubscriptions)),
		DraftInvoicesCount:       new(int32(v.DraftInvoices)),
		CreatedAt:                Timestamp(p.CreatedAt),
		UpdatedAt:                Timestamp(p.UpdatedAt),
	}
	if iv := planIntervals.Proto(p.Interval); iv != 0 {
		b.Interval = &iv
	}
	if st := planStatuses.Proto(p.Status); st != 0 {
		b.Status = &st
	}
	if p.IsChild() {
		b.ParentId = new(p.ParentID.String())
	}
	if yearly, ok := p.YearlyAmount().Int64(); ok {
		b.YearlyAmount = Money(yearly, p.Currency)
	}
	for _, c := range v.Charges {
		b.Charges = append(b.Charges, chargeProtoIn(c, p.Currency))
	}
	for _, f := range v.FixedCharges {
		b.FixedCharges = append(b.FixedCharges, fixedChargeProto(f))
	}
	if c := v.MinimumCommitment; c != nil {
		cb := catalogv1.Commitment_builder{
			Id:                 new(c.ID.String()),
			Amount:             new(p.Currency.FromMinor(c.Amount).StringFixed(p.Currency.Exponent())),
			InvoiceDisplayName: c.InvoiceDisplayName,
			TaxIds:             planIDStrings(c.TaxIDs),
			CreatedAt:          Timestamp(c.CreatedAt),
			UpdatedAt:          Timestamp(c.UpdatedAt),
		}
		if t := commitmentTypes.Proto(c.CommitmentType); t != 0 {
			cb.CommitmentType = &t
		}
		b.MinimumCommitment = cb.Build()
	}
	for _, t := range v.UsageThresholds {
		b.UsageThresholds = append(b.UsageThresholds, catalogv1.UsageThreshold_builder{
			Id:                   new(t.ID.String()),
			Amount:               new(p.Currency.FromMinor(t.Amount).StringFixed(p.Currency.Exponent())),
			Recurring:            new(t.Recurring),
			ThresholdDisplayName: t.ThresholdDisplayName,
			CreatedAt:            Timestamp(t.CreatedAt),
			UpdatedAt:            Timestamp(t.UpdatedAt),
		}.Build())
	}
	return b.Build()
}

// planIDStrings returns the text of ids.
func planIDStrings[I fmt.Stringer](ids []I) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}
