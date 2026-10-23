package rpc

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/billing/pricingunits"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	catalogv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1/catalogv1connect"
)

// PricingUnits serves tanvo.catalog.v1.PricingUnitService over the
// billing library's pricing unit service (lago's GraphQL pricing unit
// mutations and resolvers; the REST API has none).
type PricingUnits struct {
	catalogv1connect.UnimplementedPricingUnitServiceHandler

	svc *pricingunits.Service
}

var _ catalogv1connect.PricingUnitServiceHandler = (*PricingUnits)(nil)

// NewPricingUnits returns the service over svc.
func NewPricingUnits(svc *pricingunits.Service) *PricingUnits { return &PricingUnits{svc: svc} }

// pricingUnitUpdateFields are the update_mask paths of UpdatePricingUnit;
// the code is immutable (lago UpdatePricingUnitInput).
var pricingUnitUpdateFields = []string{"name", "short_name", "description"}

// CreatePricingUnit implements PricingUnitService.CreatePricingUnit.
func (s *PricingUnits) CreatePricingUnit(ctx context.Context, req *catalogv1.CreatePricingUnitRequest) (*catalogv1.CreatePricingUnitResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetPricingUnit()
	u, err := s.svc.Create(ctx, p.OrganizationID, pricingunits.CreateInput{
		Code:        b.GetCode(),
		Name:        b.GetName(),
		ShortName:   b.GetShortName(),
		Description: Opt(b.HasDescription(), b.GetDescription()),
	})
	if err != nil {
		return nil, pricingUnitError(ctx, err)
	}
	return catalogv1.CreatePricingUnitResponse_builder{PricingUnit: pricingUnitProto(u)}.Build(), nil
}

// GetPricingUnit implements PricingUnitService.GetPricingUnit.
func (s *PricingUnits) GetPricingUnit(ctx context.Context, req *catalogv1.GetPricingUnitRequest) (*catalogv1.GetPricingUnitResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	u, err := s.lookup(p.OrganizationID).Find(ctx, "pricing_unit_id", req.GetPricingUnitId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.GetPricingUnitResponse_builder{PricingUnit: pricingUnitProto(u)}.Build(), nil
}

// ListPricingUnits implements PricingUnitService.ListPricingUnits: by
// name, then newest first, searched by name or code (lago
// PricingUnitsQuery).
func (s *PricingUnits) ListPricingUnits(ctx context.Context, req *catalogv1.ListPricingUnitsRequest) (*catalogv1.ListPricingUnitsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	page, err := s.svc.List(ctx, p.OrganizationID, req.GetSearch(), Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.ListPricingUnitsResponse_builder{
		PricingUnits:  Items(page, pricingUnitProto),
		NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// UpdatePricingUnit implements PricingUnitService.UpdatePricingUnit.
// pricing_unit.id is an ID or code:<code>. The fields updated are the
// update_mask paths or, without one, the fields set.
func (s *PricingUnits) UpdatePricingUnit(ctx context.Context, req *catalogv1.UpdatePricingUnitRequest) (*catalogv1.UpdatePricingUnitResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetPricingUnit()
	paths, err := UpdatePaths(req.GetUpdateMask(), pricingUnitUpdateFields, func(path string) bool {
		switch path {
		case "name":
			return b.HasName()
		case "short_name":
			return b.HasShortName()
		case "description":
			return b.HasDescription()
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	cur, err := s.lookup(p.OrganizationID).Find(ctx, "pricing_unit.id", b.GetId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	var in pricingunits.UpdateInput
	for _, path := range paths {
		switch path {
		case "name":
			in.Name = core.Some(b.GetName())
		case "short_name":
			in.ShortName = core.Some(b.GetShortName())
		case "description":
			in.Description = core.Some(Opt(b.HasDescription(), b.GetDescription()))
		}
	}
	u, err := s.svc.Update(ctx, p.OrganizationID, cur.ID, in)
	if err != nil {
		return nil, pricingUnitError(ctx, err)
	}
	return catalogv1.UpdatePricingUnitResponse_builder{PricingUnit: pricingUnitProto(u)}.Build(), nil
}

func (s *PricingUnits) lookup(org core.OrganizationID) Lookup[core.PricingUnitID, *catalog.PricingUnit] {
	return Lookup[core.PricingUnitID, *catalog.PricingUnit]{
		ByID: func(ctx context.Context, id core.PricingUnitID) (*catalog.PricingUnit, error) {
			return s.svc.Get(ctx, org, id)
		},
		ByCode: func(ctx context.Context, code string) (*catalog.PricingUnit, error) {
			return s.svc.GetByCode(ctx, org, code)
		},
	}
}

// pricingUnitError maps the service errors, whose field paths are the
// pricing unit's, onto the request's pricing_unit message.
func pricingUnitError(ctx context.Context, err error) error {
	if v, ok := validationViolations("pricing_unit.", err); ok {
		return withReason(InvalidArgument("invalid pricing unit", v), ReasonValidationErrors)
	}
	return Error(ctx, err)
}

func pricingUnitProto(u *catalog.PricingUnit) *catalogv1.PricingUnit {
	return catalogv1.PricingUnit_builder{
		Id:          new(u.ID.String()),
		Code:        new(u.Code),
		Name:        new(u.Name),
		ShortName:   new(u.ShortName),
		Description: u.Description,
		CreatedAt:   Timestamp(u.CreatedAt),
		UpdatedAt:   Timestamp(u.UpdatedAt),
	}.Build()
}
