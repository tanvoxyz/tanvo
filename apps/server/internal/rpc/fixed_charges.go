package rpc

import (
	"context"
	"slices"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/libs/billing/plans"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	catalogv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1/catalogv1connect"
)

// FixedCharges serves tanvo.catalog.v1.FixedChargeService over the
// billing library's plan service: the fixed charges of a plan (lago
// Api::V1::Plans::FixedChargesController) and of a subscription, which
// override the plan's or keep the subscription's own units (lago
// Api::V1::Subscriptions::FixedChargesController).
type FixedCharges struct {
	catalogv1connect.UnimplementedFixedChargeServiceHandler

	svc *plans.Service
}

var _ catalogv1connect.FixedChargeServiceHandler = (*FixedCharges)(nil)

// NewFixedCharges returns the service over svc.
func NewFixedCharges(svc *plans.Service) *FixedCharges { return &FixedCharges{svc: svc} }

// fixedChargeUpdateFields are the update_mask paths of UpdateFixedCharge
// on a plan.
var fixedChargeUpdateFields = []string{
	"code", "charge_model", "properties", "units", "pay_in_advance", "prorated",
	"apply_units_immediately", "invoice_display_name", "tax_ids",
}

// fixedChargeSubscriptionFields are the update_mask paths of
// UpdateFixedCharge on a subscription.
var fixedChargeSubscriptionFields = []string{"properties", "units", "apply_units_immediately", "invoice_display_name", "tax_ids"}

var fixedChargeModels = NewEnum(map[catalog.FixedChargeModel]catalogv1.FixedChargeModel{
	catalog.FixedChargeModelStandard:  catalogv1.FixedChargeModel_FIXED_CHARGE_MODEL_STANDARD,
	catalog.FixedChargeModelGraduated: catalogv1.FixedChargeModel_FIXED_CHARGE_MODEL_GRADUATED,
	catalog.FixedChargeModelVolume:    catalogv1.FixedChargeModel_FIXED_CHARGE_MODEL_VOLUME,
})

// CreateFixedCharge implements FixedChargeService.CreateFixedCharge on a
// plan (lago Api::V1::Plans::FixedChargesController#create). lago
// v1.55.0 has no create of a subscription's fixed charge.
func (s *FixedCharges) CreateFixedCharge(ctx context.Context, req *catalogv1.CreateFixedChargeRequest) (*catalogv1.CreateFixedChargeResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	parent, err := chargeParentOf(req)
	if err != nil {
		return nil, err
	}
	if parent.sub != nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "fixed charges are created on plans")
	}
	in, err := fixedChargeInput("fixed_charge", req.GetFixedCharge())
	if err != nil {
		return nil, err
	}
	f, err := s.svc.CreateFixedCharge(ctx, p.OrganizationID, parent.plan, in, req.GetCascadeUpdates())
	if err != nil {
		return nil, fixedChargeError(ctx, err)
	}
	return catalogv1.CreateFixedChargeResponse_builder{FixedCharge: fixedChargeProto(f)}.Build(), nil
}

// GetFixedCharge implements FixedChargeService.GetFixedCharge;
// fixed_charge_id is an ID or `code:<code>`. On a subscription, units
// are the subscription's.
func (s *FixedCharges) GetFixedCharge(ctx context.Context, req *catalogv1.GetFixedChargeRequest) (*catalogv1.GetFixedChargeResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	parent, err := chargeParentOf(req)
	if err != nil {
		return nil, err
	}
	ref, err := CatalogRef("fixed_charge_id", req.GetFixedChargeId())
	if err != nil {
		return nil, err
	}
	var f *catalog.FixedCharge
	if parent.sub != nil {
		f, err = s.svc.GetSubscriptionFixedCharge(ctx, p.OrganizationID, *parent.sub, ref)
	} else {
		f, err = s.svc.GetFixedCharge(ctx, p.OrganizationID, parent.plan, ref)
	}
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.GetFixedChargeResponse_builder{FixedCharge: fixedChargeProto(f)}.Build(), nil
}

// ListFixedCharges implements FixedChargeService.ListFixedCharges, most
// recent first.
func (s *FixedCharges) ListFixedCharges(ctx context.Context, req *catalogv1.ListFixedChargesRequest) (*catalogv1.ListFixedChargesResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	parent, err := chargeParentOf(req)
	if err != nil {
		return nil, err
	}
	pr := Page(req.GetPageSize(), req.GetPageToken())
	var page core.Page[*catalog.FixedCharge]
	if parent.sub != nil {
		page, err = s.svc.ListSubscriptionFixedCharges(ctx, p.OrganizationID, *parent.sub, pr)
	} else {
		page, err = s.svc.ListFixedCharges(ctx, p.OrganizationID, parent.plan, pr)
	}
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.ListFixedChargesResponse_builder{
		FixedCharges:  Items(page, fixedChargeProto),
		NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// UpdateFixedCharge implements FixedChargeService.UpdateFixedCharge.
// fixed_charge.id is an ID or `code:<code>`. The fields updated are the
// update_mask paths or, without one, the fields set. On a subscription,
// a change of units alone is kept as the subscription's units; any other
// change overrides the fixed charge for the subscription (lago
// Subscriptions::UpdateOrOverrideFixedChargeService).
func (s *FixedCharges) UpdateFixedCharge(ctx context.Context, req *catalogv1.UpdateFixedChargeRequest) (*catalogv1.UpdateFixedChargeResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	parent, err := chargeParentOf(req)
	if err != nil {
		return nil, err
	}
	b := req.GetFixedCharge()
	ref, err := CatalogRef("fixed_charge.id", b.GetId())
	if err != nil {
		return nil, err
	}
	allowed := fixedChargeUpdateFields
	if parent.sub != nil {
		allowed = fixedChargeSubscriptionFields
	}
	paths, err := UpdatePaths(req.GetUpdateMask(), allowed, func(path string) bool { return fixedChargeHas(b, path) })
	if err != nil {
		return nil, err
	}
	in, err := fixedChargeFields("fixed_charge", b, paths)
	if err != nil {
		return nil, err
	}
	var f *catalog.FixedCharge
	if parent.sub != nil {
		f, err = s.svc.UpdateSubscriptionFixedCharge(ctx, p.OrganizationID, *parent.sub, ref, in)
	} else {
		f, err = s.svc.UpdateFixedCharge(ctx, p.OrganizationID, parent.plan, ref, in, req.GetCascadeUpdates())
	}
	if err != nil {
		return nil, fixedChargeError(ctx, err)
	}
	return catalogv1.UpdateFixedChargeResponse_builder{FixedCharge: fixedChargeProto(f)}.Build(), nil
}

// DeleteFixedCharge implements FixedChargeService.DeleteFixedCharge on a
// plan (lago Api::V1::Plans::FixedChargesController#destroy); lago
// v1.55.0 has no delete of a subscription's fixed charge.
func (s *FixedCharges) DeleteFixedCharge(ctx context.Context, req *catalogv1.DeleteFixedChargeRequest) (*catalogv1.DeleteFixedChargeResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	parent, err := chargeParentOf(req)
	if err != nil {
		return nil, err
	}
	if parent.sub != nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "fixed charges are deleted from plans")
	}
	ref, err := CatalogRef("fixed_charge_id", req.GetFixedChargeId())
	if err != nil {
		return nil, err
	}
	if _, err := s.svc.DeleteFixedCharge(ctx, p.OrganizationID, parent.plan, ref, req.GetCascadeUpdates()); err != nil {
		return nil, Error(ctx, err)
	}
	return &catalogv1.DeleteFixedChargeResponse{}, nil
}

// fixedChargeError maps the service errors, whose field paths are the
// fixed charge's, onto the request's fixed_charge message.
func fixedChargeError(ctx context.Context, err error) error {
	if v, ok := validationViolations("fixed_charge.", err); ok {
		return withReason(InvalidArgument("invalid fixed charge", v), ReasonValidationErrors)
	}
	return Error(ctx, err)
}

// fixedChargeHas reports whether the field path of the fixed charge b is
// set.
func fixedChargeHas(b *catalogv1.FixedCharge, path string) bool {
	switch path {
	case "code":
		return b.HasCode()
	case "charge_model":
		return b.HasChargeModel()
	case "properties":
		return b.HasProperties()
	case "units":
		return b.HasUnits()
	case "pay_in_advance":
		return b.HasPayInAdvance()
	case "prorated":
		return b.HasProrated()
	case "apply_units_immediately":
		return b.HasApplyUnitsImmediately()
	case "invoice_display_name":
		return b.HasInvoiceDisplayName()
	case "tax_ids":
		return len(b.GetTaxIds()) > 0
	}
	return false
}

// fixedChargeInput converts the fixed charge pb of the request field
// field: every field set, the add-on and, for a plan update, its ID.
func fixedChargeInput(field string, pb *catalogv1.FixedCharge) (plans.FixedChargeInput, error) {
	paths := slices.DeleteFunc(slices.Clone(fixedChargeUpdateFields), func(path string) bool { return !fixedChargeHas(pb, path) })
	in, err := fixedChargeFields(field, pb, paths)
	if err != nil {
		return plans.FixedChargeInput{}, err
	}
	if pb.HasAddOnId() {
		if in.AddOn, err = CatalogRef(field+".add_on_id", pb.GetAddOnId()); err != nil {
			return plans.FixedChargeInput{}, err
		}
	}
	if pb.HasId() {
		if in.ID, err = ParseID[core.FixedChargeID](field+".id", pb.GetId()); err != nil {
			return plans.FixedChargeInput{}, err
		}
	}
	return in, nil
}

// fixedChargeFields converts the paths of the fixed charge pb at field.
func fixedChargeFields(field string, pb *catalogv1.FixedCharge, paths []string) (plans.FixedChargeInput, error) {
	var in plans.FixedChargeInput
	for _, path := range paths {
		switch path {
		case "code":
			in.Code = core.Some(pb.GetCode())
		case "charge_model":
			in.ChargeModel = core.Some(fixedChargeModels.Core(pb.GetChargeModel()))
		case "properties":
			p, err := fixedChargePropertiesInput(field+".properties", pb.GetProperties())
			if err != nil {
				return plans.FixedChargeInput{}, err
			}
			in.Properties = core.Some(p)
		case "units":
			d, err := ParseDecimal(field+".units", pb.GetUnits())
			if err != nil {
				return plans.FixedChargeInput{}, err
			}
			in.Units = core.Some(d)
		case "pay_in_advance":
			in.PayInAdvance = core.Some(pb.GetPayInAdvance())
		case "prorated":
			in.Prorated = core.Some(pb.GetProrated())
		case "apply_units_immediately":
			in.ApplyUnitsImmediately = pb.GetApplyUnitsImmediately()
		case "invoice_display_name":
			in.InvoiceDisplayName = core.Some(Opt(pb.HasInvoiceDisplayName(), pb.GetInvoiceDisplayName()))
		case "tax_ids":
			taxes, err := CatalogRefs(field+".tax_ids", pb.GetTaxIds())
			if err != nil {
				return plans.FixedChargeInput{}, err
			}
			if taxes == nil {
				taxes = []refs.Ref{}
			}
			in.Taxes = core.Some(taxes)
		}
	}
	return in, nil
}

// fixedChargePropertiesInput converts the properties pb to the stored
// JSON of the fixed charge models, those of the charge models of the
// same names.
func fixedChargePropertiesInput(field string, pb *catalogv1.FixedChargeProperties) (catalog.Properties, error) {
	var b catalogv1.ChargeProperties_builder
	switch {
	case pb.HasStandard():
		b.Standard = pb.GetStandard()
	case pb.HasGraduated():
		b.Graduated = pb.GetGraduated()
	case pb.HasVolume():
		b.Volume = pb.GetVolume()
	}
	return chargePropertiesInput(field, b.Build())
}

// fixedChargeProto converts the fixed charge f.
func fixedChargeProto(f *catalog.FixedCharge) *catalogv1.FixedCharge {
	taxIDs := make([]string, len(f.TaxIDs))
	for i, id := range f.TaxIDs {
		taxIDs[i] = id.String()
	}
	b := catalogv1.FixedCharge_builder{
		Id:                 new(f.ID.String()),
		Code:               new(f.Code),
		PlanId:             new(f.PlanID.String()),
		AddOnId:            new(f.AddOnID.String()),
		Properties:         fixedChargePropertiesProto(f.ChargeModel, f.Properties),
		Units:              new(f.Units.String()),
		PayInAdvance:       new(f.PayInAdvance),
		Prorated:           new(f.Prorated),
		InvoiceDisplayName: f.InvoiceDisplayName,
		TaxIds:             taxIDs,
		CreatedAt:          Timestamp(f.CreatedAt),
		UpdatedAt:          Timestamp(f.UpdatedAt),
	}
	if f.IsChild() {
		b.ParentId = new(f.ParentID.String())
	}
	if m := fixedChargeModels.Proto(f.ChargeModel); m != 0 {
		b.ChargeModel = &m
	}
	return b.Build()
}

// fixedChargePropertiesProto converts the stored properties of the
// model.
func fixedChargePropertiesProto(model catalog.FixedChargeModel, p catalog.Properties) *catalogv1.FixedChargeProperties {
	c := chargePropertiesProto(catalog.ChargeModel(model), p)
	var b catalogv1.FixedChargeProperties_builder
	switch model {
	case catalog.FixedChargeModelStandard:
		b.Standard = c.GetStandard()
	case catalog.FixedChargeModelGraduated:
		b.Graduated = c.GetGraduated()
	case catalog.FixedChargeModelVolume:
		b.Volume = c.GetVolume()
	}
	return b.Build()
}
