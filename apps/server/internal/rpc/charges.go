package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/libs/billing/plans"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/money"
	catalogv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1/catalogv1connect"
)

// Charges serves tanvo.catalog.v1.ChargeService over the billing
// library's plan service: the charges of a plan (lago
// Api::V1::Plans::ChargesController and Plans::Charges::FiltersController)
// and of a subscription, which override the plan's (lago
// Api::V1::Subscriptions::ChargesController and
// Subscriptions::Charges::FiltersController).
type Charges struct {
	catalogv1connect.UnimplementedChargeServiceHandler

	svc *plans.Service
}

var _ catalogv1connect.ChargeServiceHandler = (*Charges)(nil)

// NewCharges returns the service over svc.
func NewCharges(svc *plans.Service) *Charges { return &Charges{svc: svc} }

// chargeUpdateFields are the update_mask paths of UpdateCharge; a
// subscription's charge takes only properties, filters, min_amount,
// invoice_display_name, applied_pricing_unit and tax_ids.
var chargeUpdateFields = []string{
	"code", "charge_model", "properties", "filters", "pay_in_advance", "invoiceable", "regroup_paid_fees",
	"prorated", "accepts_target_wallet", "min_amount", "invoice_display_name", "applied_pricing_unit", "tax_ids",
}

// chargeFilterUpdateFields are the update_mask paths of
// UpdateChargeFilter.
var chargeFilterUpdateFields = []string{"properties", "invoice_display_name"}

var chargeModels = NewEnum(map[catalog.ChargeModel]catalogv1.ChargeModel{
	catalog.ChargeModelStandard:            catalogv1.ChargeModel_CHARGE_MODEL_STANDARD,
	catalog.ChargeModelGraduated:           catalogv1.ChargeModel_CHARGE_MODEL_GRADUATED,
	catalog.ChargeModelGraduatedPercentage: catalogv1.ChargeModel_CHARGE_MODEL_GRADUATED_PERCENTAGE,
	catalog.ChargeModelPackage:             catalogv1.ChargeModel_CHARGE_MODEL_PACKAGE,
	catalog.ChargeModelPercentage:          catalogv1.ChargeModel_CHARGE_MODEL_PERCENTAGE,
	catalog.ChargeModelVolume:              catalogv1.ChargeModel_CHARGE_MODEL_VOLUME,
	catalog.ChargeModelDynamic:             catalogv1.ChargeModel_CHARGE_MODEL_DYNAMIC,
})

var chargeRegroups = NewEnum(map[catalog.RegroupPaidFees]catalogv1.RegroupPaidFees{
	catalog.RegroupPaidFeesInvoice: catalogv1.RegroupPaidFees_REGROUP_PAID_FEES_INVOICE,
})

// chargeParentRequest is a request naming a plan or a subscription.
type chargeParentRequest interface {
	GetPlanId() string
	GetSubscriptionId() string
}

// chargeParent is the plan or the subscription of a request.
type chargeParent struct {
	plan refs.Ref
	sub  *refs.SubscriptionRef
}

// chargeParentOf parses the parent of req: plan_id is an ID or
// `code:<code>`, subscription_id an ID or `external_id:<id>`.
func chargeParentOf(req chargeParentRequest) (chargeParent, error) {
	if s := req.GetSubscriptionId(); s != "" {
		r, err := subRef("subscription_id", s)
		if err != nil {
			return chargeParent{}, err
		}
		return chargeParent{sub: &refs.SubscriptionRef{ID: r.ID, ExternalID: r.ExternalID}}, nil
	}
	plan, err := CatalogRef("plan_id", req.GetPlanId())
	return chargeParent{plan: plan}, err
}

// CreateCharge implements ChargeService.CreateCharge on a plan (lago
// Api::V1::Plans::ChargesController#create). A subscription's charges
// are its plan's: lago v1.55.0 has no create there.
func (s *Charges) CreateCharge(ctx context.Context, req *catalogv1.CreateChargeRequest) (*catalogv1.CreateChargeResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	parent, err := chargeParentOf(req)
	if err != nil {
		return nil, err
	}
	if parent.sub != nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "charges are created on plans")
	}
	in, err := chargeInput("charge", req.GetCharge())
	if err != nil {
		return nil, err
	}
	c, err := s.svc.CreateCharge(ctx, p.OrganizationID, parent.plan, in, req.GetCascadeUpdates())
	if err != nil {
		return nil, chargeError(ctx, err)
	}
	pb, err := s.proto(ctx, p.OrganizationID, c)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.CreateChargeResponse_builder{Charge: pb}.Build(), nil
}

// GetCharge implements ChargeService.GetCharge; charge_id is an ID or
// `code:<code>`.
func (s *Charges) GetCharge(ctx context.Context, req *catalogv1.GetChargeRequest) (*catalogv1.GetChargeResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	parent, err := chargeParentOf(req)
	if err != nil {
		return nil, err
	}
	ref, err := CatalogRef("charge_id", req.GetChargeId())
	if err != nil {
		return nil, err
	}
	var c *catalog.Charge
	if parent.sub != nil {
		c, err = s.svc.GetSubscriptionCharge(ctx, p.OrganizationID, *parent.sub, ref)
	} else {
		c, err = s.svc.GetCharge(ctx, p.OrganizationID, parent.plan, ref)
	}
	if err != nil {
		return nil, Error(ctx, err)
	}
	pb, err := s.proto(ctx, p.OrganizationID, c)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.GetChargeResponse_builder{Charge: pb}.Build(), nil
}

// ListCharges implements ChargeService.ListCharges, most recent first.
func (s *Charges) ListCharges(ctx context.Context, req *catalogv1.ListChargesRequest) (*catalogv1.ListChargesResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	parent, err := chargeParentOf(req)
	if err != nil {
		return nil, err
	}
	pr := Page(req.GetPageSize(), req.GetPageToken())
	var page core.Page[*catalog.Charge]
	if parent.sub != nil {
		page, err = s.svc.ListSubscriptionCharges(ctx, p.OrganizationID, *parent.sub, pr)
	} else {
		page, err = s.svc.ListCharges(ctx, p.OrganizationID, parent.plan, pr)
	}
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := make([]*catalogv1.Charge, 0, len(page.Items))
	currencies := map[core.PlanID]money.Currency{}
	for _, c := range page.Items {
		cur, ok := currencies[c.PlanID]
		if !ok {
			if cur, err = s.currency(ctx, p.OrganizationID, c); err != nil {
				return nil, Error(ctx, err)
			}
			currencies[c.PlanID] = cur
		}
		out = append(out, chargeProtoIn(c, cur))
	}
	return catalogv1.ListChargesResponse_builder{Charges: out, NextPageToken: new(page.NextToken)}.Build(), nil
}

// UpdateCharge implements ChargeService.UpdateCharge. charge.id is an ID
// or `code:<code>`. The fields updated are the update_mask paths or,
// without one, the fields set. On a subscription, the subscription's
// override of the charge is updated, the subscription moving to an
// override of its plan first (lago
// Subscriptions::UpdateOrOverrideChargeService).
func (s *Charges) UpdateCharge(ctx context.Context, req *catalogv1.UpdateChargeRequest) (*catalogv1.UpdateChargeResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	parent, err := chargeParentOf(req)
	if err != nil {
		return nil, err
	}
	b := req.GetCharge()
	ref, err := CatalogRef("charge.id", b.GetId())
	if err != nil {
		return nil, err
	}
	paths, err := UpdatePaths(req.GetUpdateMask(), chargeUpdateFields, func(path string) bool { return chargeHas(b, path) })
	if err != nil {
		return nil, err
	}
	in, err := chargeFields("charge", b, paths)
	if err != nil {
		return nil, err
	}
	var c *catalog.Charge
	if parent.sub != nil {
		c, err = s.svc.UpdateSubscriptionCharge(ctx, p.OrganizationID, *parent.sub, ref, in)
	} else {
		c, err = s.svc.UpdateCharge(ctx, p.OrganizationID, parent.plan, ref, in, req.GetCascadeUpdates())
	}
	if err != nil {
		return nil, chargeError(ctx, err)
	}
	pb, err := s.proto(ctx, p.OrganizationID, c)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.UpdateChargeResponse_builder{Charge: pb}.Build(), nil
}

// DeleteCharge implements ChargeService.DeleteCharge on a plan (lago
// Api::V1::Plans::ChargesController#destroy); lago v1.55.0 has no delete
// of a subscription's charge.
func (s *Charges) DeleteCharge(ctx context.Context, req *catalogv1.DeleteChargeRequest) (*catalogv1.DeleteChargeResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	parent, err := chargeParentOf(req)
	if err != nil {
		return nil, err
	}
	if parent.sub != nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "charges are deleted from plans")
	}
	ref, err := CatalogRef("charge_id", req.GetChargeId())
	if err != nil {
		return nil, err
	}
	if _, err := s.svc.DeleteCharge(ctx, p.OrganizationID, parent.plan, ref, req.GetCascadeUpdates()); err != nil {
		return nil, Error(ctx, err)
	}
	return &catalogv1.DeleteChargeResponse{}, nil
}

// CreateChargeFilter implements ChargeService.CreateChargeFilter. On a
// subscription, the filter goes on the subscription's override of the
// charge (lago Subscriptions::ChargeFilters::CreateService).
func (s *Charges) CreateChargeFilter(ctx context.Context, req *catalogv1.CreateChargeFilterRequest) (*catalogv1.CreateChargeFilterResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	parent, err := chargeParentOf(req)
	if err != nil {
		return nil, err
	}
	ref, err := CatalogRef("charge_id", req.GetChargeId())
	if err != nil {
		return nil, err
	}
	b := req.GetFilter()
	in, err := chargeFilterInput("filter", b, []string{"properties", "invoice_display_name"})
	if err != nil {
		return nil, err
	}
	c, err := s.charge(ctx, p.OrganizationID, parent, ref)
	if err != nil {
		return nil, Error(ctx, err)
	}
	var f *catalog.ChargeFilter
	if parent.sub != nil {
		f, err = s.svc.CreateSubscriptionChargeFilter(ctx, p.OrganizationID, *parent.sub, ref, in)
	} else {
		f, err = s.svc.CreateChargeFilter(ctx, p.OrganizationID, parent.plan, ref, in, req.GetCascadeUpdates())
	}
	if err != nil {
		return nil, chargeFilterError(ctx, err)
	}
	return catalogv1.CreateChargeFilterResponse_builder{Filter: chargeFilterProto(f, c.ChargeModel)}.Build(), nil
}

// ListChargeFilters implements ChargeService.ListChargeFilters, in the
// charge's order.
func (s *Charges) ListChargeFilters(ctx context.Context, req *catalogv1.ListChargeFiltersRequest) (*catalogv1.ListChargeFiltersResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	parent, err := chargeParentOf(req)
	if err != nil {
		return nil, err
	}
	ref, err := CatalogRef("charge_id", req.GetChargeId())
	if err != nil {
		return nil, err
	}
	c, err := s.charge(ctx, p.OrganizationID, parent, ref)
	if err != nil {
		return nil, Error(ctx, err)
	}
	pr := Page(req.GetPageSize(), req.GetPageToken())
	var page core.Page[*catalog.ChargeFilter]
	if parent.sub != nil {
		page, err = s.svc.ListSubscriptionChargeFilters(ctx, p.OrganizationID, *parent.sub, ref, pr)
	} else {
		page, err = s.svc.ListChargeFilters(ctx, p.OrganizationID, parent.plan, ref, pr)
	}
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.ListChargeFiltersResponse_builder{
		Filters:       Items(page, func(f *catalog.ChargeFilter) *catalogv1.ChargeFilter { return chargeFilterProto(f, c.ChargeModel) }),
		NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// GetChargeFilter implements ChargeService.GetChargeFilter: the filter
// of the charge of the plan, or of the subscription's plan.
func (s *Charges) GetChargeFilter(ctx context.Context, req *catalogv1.GetChargeFilterRequest) (*catalogv1.GetChargeFilterResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	parent, err := chargeParentOf(req)
	if err != nil {
		return nil, err
	}
	ref, err := CatalogRef("charge_id", req.GetChargeId())
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.ChargeFilterID]("filter_id", req.GetFilterId())
	if err != nil {
		return nil, err
	}
	c, err := s.charge(ctx, p.OrganizationID, parent, ref)
	if err != nil {
		return nil, Error(ctx, err)
	}
	var f *catalog.ChargeFilter
	if parent.sub != nil {
		f, err = s.svc.GetSubscriptionChargeFilter(ctx, p.OrganizationID, *parent.sub, ref, id)
	} else {
		f, err = s.svc.GetChargeFilter(ctx, p.OrganizationID, parent.plan, ref, id)
	}
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.GetChargeFilterResponse_builder{Filter: chargeFilterProto(f, c.ChargeModel)}.Build(), nil
}

// UpdateChargeFilter implements ChargeService.UpdateChargeFilter; the
// fields updated are the update_mask paths or, without one, the fields
// set. On a subscription, the filter with the same values on the
// subscription's override of the charge is updated or created (lago
// Subscriptions::ChargeFilters::UpdateOrOverrideService).
func (s *Charges) UpdateChargeFilter(ctx context.Context, req *catalogv1.UpdateChargeFilterRequest) (*catalogv1.UpdateChargeFilterResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	parent, err := chargeParentOf(req)
	if err != nil {
		return nil, err
	}
	ref, err := CatalogRef("charge_id", req.GetChargeId())
	if err != nil {
		return nil, err
	}
	b := req.GetFilter()
	id, err := ParseID[core.ChargeFilterID]("filter.id", b.GetId())
	if err != nil {
		return nil, err
	}
	paths, err := UpdatePaths(req.GetUpdateMask(), chargeFilterUpdateFields, func(path string) bool {
		switch path {
		case "properties":
			return b.HasProperties()
		case "invoice_display_name":
			return b.HasInvoiceDisplayName()
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	in, err := chargeFilterInput("filter", b, paths)
	if err != nil {
		return nil, err
	}
	in.Values = nil
	c, err := s.charge(ctx, p.OrganizationID, parent, ref)
	if err != nil {
		return nil, Error(ctx, err)
	}
	var f *catalog.ChargeFilter
	if parent.sub != nil {
		f, err = s.svc.UpdateSubscriptionChargeFilter(ctx, p.OrganizationID, *parent.sub, ref, id, in)
	} else {
		f, err = s.svc.UpdateChargeFilter(ctx, p.OrganizationID, parent.plan, ref, id, in, req.GetCascadeUpdates())
	}
	if err != nil {
		return nil, chargeFilterError(ctx, err)
	}
	return catalogv1.UpdateChargeFilterResponse_builder{Filter: chargeFilterProto(f, c.ChargeModel)}.Build(), nil
}

// DeleteChargeFilter implements ChargeService.DeleteChargeFilter. On a
// subscription, the filter with the same values is removed from the
// subscription's override of the charge (lago
// Subscriptions::ChargeFilters::DestroyService).
func (s *Charges) DeleteChargeFilter(ctx context.Context, req *catalogv1.DeleteChargeFilterRequest) (*catalogv1.DeleteChargeFilterResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	parent, err := chargeParentOf(req)
	if err != nil {
		return nil, err
	}
	ref, err := CatalogRef("charge_id", req.GetChargeId())
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.ChargeFilterID]("filter_id", req.GetFilterId())
	if err != nil {
		return nil, err
	}
	if parent.sub != nil {
		_, err = s.svc.DeleteSubscriptionChargeFilter(ctx, p.OrganizationID, *parent.sub, ref, id)
	} else {
		_, err = s.svc.DeleteChargeFilter(ctx, p.OrganizationID, parent.plan, ref, id, req.GetCascadeUpdates())
	}
	if err != nil {
		return nil, Error(ctx, err)
	}
	return &catalogv1.DeleteChargeFilterResponse{}, nil
}

// charge returns the charge ref of the parent; an override of it has
// its charge model, which prices its filters.
func (s *Charges) charge(ctx context.Context, org core.OrganizationID, parent chargeParent, ref refs.Ref) (*catalog.Charge, error) {
	if parent.sub != nil {
		return s.svc.GetSubscriptionCharge(ctx, org, *parent.sub, ref)
	}
	return s.svc.GetCharge(ctx, org, parent.plan, ref)
}

// currency returns the currency of the plan of c, in which its minimum
// amount is.
func (s *Charges) currency(ctx context.Context, org core.OrganizationID, c *catalog.Charge) (money.Currency, error) {
	p, err := s.svc.Get(ctx, org, c.PlanID)
	if err != nil {
		return money.Currency{}, fmt.Errorf("load plan %s: %w", c.PlanID, err)
	}
	return p.Currency, nil
}

func (s *Charges) proto(ctx context.Context, org core.OrganizationID, c *catalog.Charge) (*catalogv1.Charge, error) {
	cur, err := s.currency(ctx, org, c)
	if err != nil {
		return nil, err
	}
	return chargeProtoIn(c, cur), nil
}

// chargeError maps the service errors, whose field paths are the
// charge's, onto the request's charge message.
func chargeError(ctx context.Context, err error) error {
	if v, ok := validationViolations("charge.", err); ok {
		return withReason(InvalidArgument("invalid charge", v), ReasonValidationErrors)
	}
	return Error(ctx, err)
}

// chargeFilterError maps the service errors, whose field paths are the
// filter's, onto the request's filter message.
func chargeFilterError(ctx context.Context, err error) error {
	if v, ok := validationViolations("filter.", err); ok {
		return withReason(InvalidArgument("invalid charge filter", v), ReasonValidationErrors)
	}
	return Error(ctx, err)
}

// chargeHas reports whether the field path of the charge b is set.
func chargeHas(b *catalogv1.Charge, path string) bool {
	switch path {
	case "code":
		return b.HasCode()
	case "charge_model":
		return b.HasChargeModel()
	case "properties":
		return b.HasProperties()
	case "filters":
		return len(b.GetFilters()) > 0
	case "pay_in_advance":
		return b.HasPayInAdvance()
	case "invoiceable":
		return b.HasInvoiceable()
	case "regroup_paid_fees":
		return b.HasRegroupPaidFees()
	case "prorated":
		return b.HasProrated()
	case "accepts_target_wallet":
		return b.HasAcceptsTargetWallet()
	case "min_amount":
		return b.HasMinAmount()
	case "invoice_display_name":
		return b.HasInvoiceDisplayName()
	case "applied_pricing_unit":
		return b.HasAppliedPricingUnit()
	case "tax_ids":
		return len(b.GetTaxIds()) > 0
	}
	return false
}

// chargeInput converts the charge pb of the request field field: every
// field set, the billable metric and, for a plan update, its ID.
func chargeInput(field string, pb *catalogv1.Charge) (plans.ChargeInput, error) {
	paths := slices.DeleteFunc(slices.Clone(chargeUpdateFields), func(path string) bool { return !chargeHas(pb, path) })
	in, err := chargeFields(field, pb, paths)
	if err != nil {
		return plans.ChargeInput{}, err
	}
	if in.BillableMetric, err = CatalogRef(field+".billable_metric_id", pb.GetBillableMetricId()); err != nil {
		return plans.ChargeInput{}, err
	}
	if pb.HasId() {
		if in.ID, err = ParseID[core.ChargeID](field+".id", pb.GetId()); err != nil {
			return plans.ChargeInput{}, err
		}
	}
	return in, nil
}

// chargeFields converts the paths of the charge pb at field.
func chargeFields(field string, pb *catalogv1.Charge, paths []string) (plans.ChargeInput, error) {
	var in plans.ChargeInput
	model := chargeModels.Core(pb.GetChargeModel())
	for _, path := range paths {
		switch path {
		case "code":
			in.Code = core.Some(pb.GetCode())
		case "charge_model":
			in.ChargeModel = core.Some(model)
		case "properties":
			p, err := chargePropertiesInput(field+".properties", pb.GetProperties())
			if err != nil {
				return plans.ChargeInput{}, err
			}
			in.Properties = core.Some(p)
		case "filters":
			filters := []plans.ChargeFilterInput{}
			for i, f := range pb.GetFilters() {
				fi, err := chargeFilterInput(fmt.Sprintf("%s.filters[%d]", field, i), f, []string{"properties", "invoice_display_name"})
				if err != nil {
					return plans.ChargeInput{}, err
				}
				filters = append(filters, fi)
			}
			in.Filters = core.Some(filters)
		case "pay_in_advance":
			in.PayInAdvance = core.Some(pb.GetPayInAdvance())
		case "invoiceable":
			in.Invoiceable = core.Some(pb.GetInvoiceable())
		case "regroup_paid_fees":
			in.RegroupPaidFees = core.Some(chargeRegroups.Core(pb.GetRegroupPaidFees()))
		case "prorated":
			in.Prorated = core.Some(pb.GetProrated())
		case "accepts_target_wallet":
			in.AcceptsTargetWallet = core.Some(pb.GetAcceptsTargetWallet())
		case "min_amount":
			d := money.Zero
			if pb.GetMinAmount() != "" {
				var err error
				if d, err = ParseDecimal(field+".min_amount", pb.GetMinAmount()); err != nil {
					return plans.ChargeInput{}, err
				}
			}
			in.MinAmount = core.Some(d)
		case "invoice_display_name":
			in.InvoiceDisplayName = core.Some(Opt(pb.HasInvoiceDisplayName(), pb.GetInvoiceDisplayName()))
		case "applied_pricing_unit":
			if !pb.HasAppliedPricingUnit() {
				in.AppliedPricingUnit = core.Some[*plans.PricingUnitInput](nil)
				continue
			}
			a := pb.GetAppliedPricingUnit()
			unit, err := CatalogRef(field+".applied_pricing_unit.pricing_unit_id", a.GetPricingUnitId())
			if err != nil {
				return plans.ChargeInput{}, err
			}
			rate, err := ParseDecimal(field+".applied_pricing_unit.conversion_rate", a.GetConversionRate())
			if err != nil {
				return plans.ChargeInput{}, err
			}
			in.AppliedPricingUnit = core.Some(&plans.PricingUnitInput{PricingUnit: unit, ConversionRate: rate})
		case "tax_ids":
			taxes, err := CatalogRefs(field+".tax_ids", pb.GetTaxIds())
			if err != nil {
				return plans.ChargeInput{}, err
			}
			if taxes == nil {
				taxes = []refs.Ref{}
			}
			in.Taxes = core.Some(taxes)
		}
	}
	return in, nil
}

// chargeFilterInput converts the filter pb at field: its values and the
// paths of properties and invoice_display_name.
func chargeFilterInput(field string, pb *catalogv1.ChargeFilter, paths []string) (plans.ChargeFilterInput, error) {
	in := plans.ChargeFilterInput{Values: map[string][]string{}}
	for k, v := range pb.GetValues() {
		in.Values[k] = slices.Clone(v.GetValues())
	}
	for _, path := range paths {
		switch path {
		case "properties":
			if !pb.HasProperties() {
				continue
			}
			p, err := chargePropertiesInput(field+".properties", pb.GetProperties())
			if err != nil {
				return plans.ChargeFilterInput{}, err
			}
			in.Properties = core.Some(p)
		case "invoice_display_name":
			if pb.HasInvoiceDisplayName() {
				in.InvoiceDisplayName = core.Some(new(pb.GetInvoiceDisplayName()))
			}
		}
	}
	return in, nil
}

// chargePropertiesInput converts the properties pb to the stored JSON of
// the charge models (lago's properties hash): amounts are strings and
// range bounds numbers.
func chargePropertiesInput(field string, pb *catalogv1.ChargeProperties) (catalog.Properties, error) {
	m := map[string]any{}
	putDecimal := func(key, v string) {
		if v != "" {
			m[key] = v
		}
	}
	switch {
	case pb.HasStandard():
		putDecimal("amount", pb.GetStandard().GetAmount())
	case pb.HasPackage():
		p := pb.GetPackage()
		putDecimal("amount", p.GetAmount())
		m["package_size"] = p.GetPackageSize()
		m["free_units"] = p.GetFreeUnits()
	case pb.HasPercentage():
		p := pb.GetPercentage()
		putDecimal("rate", p.GetRate())
		putDecimal("fixed_amount", p.GetFixedAmount())
		if p.HasFreeUnitsPerEvents() {
			m["free_units_per_events"] = p.GetFreeUnitsPerEvents()
		}
		putDecimal("free_units_per_total_aggregation", p.GetFreeUnitsPerTotalAggregation())
		putDecimal("per_transaction_min_amount", p.GetPerTransactionMinAmount())
		putDecimal("per_transaction_max_amount", p.GetPerTransactionMaxAmount())
	case pb.HasGraduated():
		m["graduated_ranges"] = chargeRangesInput(pb.GetGraduated().GetRanges())
	case pb.HasVolume():
		m["volume_ranges"] = chargeRangesInput(pb.GetVolume().GetRanges())
	case pb.HasGraduatedPercentage():
		ranges := []map[string]any{}
		for _, r := range pb.GetGraduatedPercentage().GetRanges() {
			ranges = append(ranges, map[string]any{
				"from_value":  chargeBound(r.GetFromValue()),
				"to_value":    chargeBound(r.GetToValue()),
				"rate":        r.GetRate(),
				"flat_amount": r.GetFlatAmount(),
			})
		}
		m["graduated_percentage_ranges"] = ranges
	}
	if g := pb.GetGrouping(); g != nil {
		if keys := g.GetPricingGroupKeys(); len(keys) > 0 {
			m["pricing_group_keys"] = keys
		}
		if keys := g.GetPresentationGroupKeys(); len(keys) > 0 {
			out := make([]map[string]any, len(keys))
			for i, k := range keys {
				out[i] = map[string]any{"value": k.GetValue(), "options": map[string]any{"display_in_invoice": k.GetDisplayInInvoice()}}
			}
			m["presentation_group_keys"] = out
		}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return catalog.Properties{}, fmt.Errorf("%s: %w", field, err)
	}
	return catalog.NewProperties(raw)
}

func chargeRangesInput(in []*catalogv1.Range) []map[string]any {
	out := []map[string]any{}
	for _, r := range in {
		out = append(out, map[string]any{
			"from_value":      chargeBound(r.GetFromValue()),
			"to_value":        chargeBound(r.GetToValue()),
			"per_unit_amount": r.GetPerUnitAmount(),
			"flat_amount":     r.GetFlatAmount(),
		})
	}
	return out
}

// chargeBound is a range bound as a JSON number; "" is the open end.
func chargeBound(s string) any {
	if s == "" {
		return nil
	}
	return json.Number(s)
}

// chargeProtoIn converts the charge c of a plan in currency cur.
func chargeProtoIn(c *catalog.Charge, cur money.Currency) *catalogv1.Charge {
	taxIDs := make([]string, len(c.TaxIDs))
	for i, id := range c.TaxIDs {
		taxIDs[i] = id.String()
	}
	b := catalogv1.Charge_builder{
		Id:                  new(c.ID.String()),
		Code:                new(c.Code),
		PlanId:              new(c.PlanID.String()),
		BillableMetricId:    new(c.BillableMetricID.String()),
		Properties:          chargePropertiesProto(c.ChargeModel, c.Properties),
		PayInAdvance:        new(c.PayInAdvance),
		Invoiceable:         new(c.Invoiceable),
		Prorated:            new(c.Prorated),
		AcceptsTargetWallet: new(c.AcceptsTargetWallet),
		MinAmount:           new(cur.FromMinor(c.MinAmount).StringFixed(cur.Exponent())),
		InvoiceDisplayName:  c.InvoiceDisplayName,
		TaxIds:              taxIDs,
		CreatedAt:           Timestamp(c.CreatedAt),
		UpdatedAt:           Timestamp(c.UpdatedAt),
	}
	if c.IsChild() {
		b.ParentId = new(c.ParentID.String())
	}
	if m := chargeModels.Proto(c.ChargeModel); m != 0 {
		b.ChargeModel = &m
	}
	if r := chargeRegroups.Proto(c.RegroupPaidFees); r != 0 {
		b.RegroupPaidFees = &r
	}
	if a := c.AppliedPricingUnit; a != nil {
		b.AppliedPricingUnit = catalogv1.AppliedPricingUnit_builder{
			PricingUnitId:  new(a.PricingUnitID.String()),
			ConversionRate: new(a.ConversionRate.String()),
		}.Build()
	}
	for i := range c.Filters {
		b.Filters = append(b.Filters, chargeFilterProto(&c.Filters[i], c.ChargeModel))
	}
	return b.Build()
}

// chargeFilterProto converts the filter f of a charge of the model.
func chargeFilterProto(f *catalog.ChargeFilter, model catalog.ChargeModel) *catalogv1.ChargeFilter {
	values := make(map[string]*catalogv1.FilterValues, len(f.Values))
	for _, k := range slices.Sorted(maps.Keys(f.Values)) {
		values[k] = catalogv1.FilterValues_builder{Values: slices.Clone(f.Values[k])}.Build()
	}
	return catalogv1.ChargeFilter_builder{
		Id:                 new(f.ID.String()),
		Code:               new(f.Code),
		Values:             values,
		Properties:         chargePropertiesProto(model, f.Properties),
		InvoiceDisplayName: f.InvoiceDisplayName,
		CreatedAt:          Timestamp(f.CreatedAt),
	}.Build()
}

// chargePropertiesProto converts the stored properties of the model.
func chargePropertiesProto(model catalog.ChargeModel, p catalog.Properties) *catalogv1.ChargeProperties {
	m := map[string]any{}
	if !p.IsZero() {
		d := json.NewDecoder(bytes.NewReader(p.Raw()))
		d.UseNumber()
		_ = d.Decode(&m) // stored properties are objects; anything else reads as empty
	}
	str := func(key string) *string {
		v, ok := m[key]
		if !ok || v == nil {
			return nil
		}
		return new(chargeString(v))
	}
	b := catalogv1.ChargeProperties_builder{}
	switch model {
	case catalog.ChargeModelStandard:
		b.Standard = catalogv1.StandardProperties_builder{Amount: str("amount")}.Build()
	case catalog.ChargeModelPackage:
		b.Package = catalogv1.PackageProperties_builder{
			Amount:      str("amount"),
			PackageSize: chargeInt(m["package_size"]),
			FreeUnits:   chargeInt(m["free_units"]),
		}.Build()
	case catalog.ChargeModelPercentage:
		b.Percentage = catalogv1.PercentageProperties_builder{
			Rate:                         str("rate"),
			FixedAmount:                  str("fixed_amount"),
			FreeUnitsPerEvents:           chargeInt(m["free_units_per_events"]),
			FreeUnitsPerTotalAggregation: str("free_units_per_total_aggregation"),
			PerTransactionMinAmount:      str("per_transaction_min_amount"),
			PerTransactionMaxAmount:      str("per_transaction_max_amount"),
		}.Build()
	case catalog.ChargeModelGraduated:
		b.Graduated = catalogv1.GraduatedProperties_builder{Ranges: chargeRangesProto(m["graduated_ranges"])}.Build()
	case catalog.ChargeModelVolume:
		b.Volume = catalogv1.VolumeProperties_builder{Ranges: chargeRangesProto(m["volume_ranges"])}.Build()
	case catalog.ChargeModelGraduatedPercentage:
		var ranges []*catalogv1.PercentageRange
		for _, r := range chargeObjects(m["graduated_percentage_ranges"]) {
			ranges = append(ranges, catalogv1.PercentageRange_builder{
				FromValue:  chargeStringPtr(r["from_value"]),
				ToValue:    chargeStringPtr(r["to_value"]),
				Rate:       chargeStringPtr(r["rate"]),
				FlatAmount: chargeStringPtr(r["flat_amount"]),
			}.Build())
		}
		b.GraduatedPercentage = catalogv1.GraduatedPercentageProperties_builder{Ranges: ranges}.Build()
	case catalog.ChargeModelDynamic:
		b.Dynamic = &catalogv1.DynamicProperties{}
	}
	var g catalogv1.Grouping_builder
	if keys, ok := m["pricing_group_keys"].([]any); ok {
		for _, k := range keys {
			g.PricingGroupKeys = append(g.PricingGroupKeys, chargeString(k))
		}
	}
	for _, k := range chargeObjects(m["presentation_group_keys"]) {
		opts, _ := k["options"].(map[string]any)
		show, _ := opts["display_in_invoice"].(bool)
		g.PresentationGroupKeys = append(g.PresentationGroupKeys, catalogv1.PresentationGroupKey_builder{
			Value: new(chargeString(k["value"])), DisplayInInvoice: new(show),
		}.Build())
	}
	if len(g.PricingGroupKeys) > 0 || len(g.PresentationGroupKeys) > 0 {
		b.Grouping = g.Build()
	}
	return b.Build()
}

func chargeRangesProto(v any) []*catalogv1.Range {
	var out []*catalogv1.Range
	for _, r := range chargeObjects(v) {
		out = append(out, catalogv1.Range_builder{
			FromValue:     chargeStringPtr(r["from_value"]),
			ToValue:       chargeStringPtr(r["to_value"]),
			PerUnitAmount: chargeStringPtr(r["per_unit_amount"]),
			FlatAmount:    chargeStringPtr(r["flat_amount"]),
		}.Build())
	}
	return out
}

// chargeObjects returns the objects of the JSON array v.
func chargeObjects(v any) []map[string]any {
	arr, _ := v.([]any)
	out := make([]map[string]any, 0, len(arr))
	for _, x := range arr {
		if o, ok := x.(map[string]any); ok {
			out = append(out, o)
		}
	}
	return out
}

// chargeString renders the JSON scalar v.
func chargeString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

func chargeStringPtr(v any) *string {
	if v == nil {
		return nil
	}
	return new(chargeString(v))
}

// chargeInt reads the JSON integer v, written as a number or a string.
func chargeInt(v any) *int64 {
	if v == nil {
		return nil
	}
	d, err := money.Parse(chargeString(v))
	if err != nil {
		return nil
	}
	n, ok := d.Int64()
	if !ok {
		return nil
	}
	return &n
}
