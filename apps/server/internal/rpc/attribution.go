package rpc

import (
	"context"
	"errors"

	"github.com/tanvoxyz/tanvo/libs/billing/usageattribution"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/attribution"
	attributionv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/attribution/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/attribution/v1/attributionv1connect"
)

// UsageAttribution serves tanvo.attribution.v1.UsageAttributionService
// over the billing library's usage attribution service (lago
// Api::V1::UsageAttributionTypesController), and the attributed usage
// over the query service (attributed_usage.go).
type UsageAttribution struct {
	attributionv1connect.UnimplementedUsageAttributionServiceHandler

	svc   *usageattribution.Service
	query *usageattribution.QueryService
}

var _ attributionv1connect.UsageAttributionServiceHandler = (*UsageAttribution)(nil)

// NewUsageAttribution returns the service over svc.
func NewUsageAttribution(svc *usageattribution.Service) *UsageAttribution {
	return &UsageAttribution{svc: svc}
}

var attributionRoles = NewEnum(map[attribution.Role]attributionv1.UsageAttributionRole{
	attribution.RoleHierarchical: attributionv1.UsageAttributionRole_USAGE_ATTRIBUTION_ROLE_HIERARCHICAL,
	attribution.RoleFlat:         attributionv1.UsageAttributionRole_USAGE_ATTRIBUTION_ROLE_FLAT,
})

// attributionUpdateFields are the update_mask paths of
// UpdateUsageAttributionType (lago's permitted params,
// usage_attribution_types_controller.rb:105-108).
var attributionUpdateFields = []string{"code", "name", "description", "attribution_keys", "role", "parent_id"}

// attributionFilterFields are the fields of the list filter.
var attributionFilterFields = []string{"role", "root"}

// CreateUsageAttributionType implements
// UsageAttributionService.CreateUsageAttributionType.
func (s *UsageAttribution) CreateUsageAttributionType(ctx context.Context, req *attributionv1.CreateUsageAttributionTypeRequest) (*attributionv1.CreateUsageAttributionTypeResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetUsageAttributionType()
	in := usageattribution.CreateInput{
		Code:            b.GetCode(),
		Name:            Opt(b.HasName(), b.GetName()),
		Description:     Opt(b.HasDescription(), b.GetDescription()),
		AttributionKeys: b.GetAttributionKeys(),
		Role:            attributionRoles.Core(b.GetRole()),
	}
	if in.ParentID, err = s.parent(ctx, p.OrganizationID, b.GetParentId()); err != nil {
		return nil, err
	}
	e, err := s.svc.Create(ctx, p.OrganizationID, in)
	if err != nil {
		return nil, attributionError(ctx, err)
	}
	return attributionv1.CreateUsageAttributionTypeResponse_builder{UsageAttributionType: attributionTypeProto(e)}.Build(), nil
}

// GetUsageAttributionType implements
// UsageAttributionService.GetUsageAttributionType.
func (s *UsageAttribution) GetUsageAttributionType(ctx context.Context, req *attributionv1.GetUsageAttributionTypeRequest) (*attributionv1.GetUsageAttributionTypeResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	e, err := s.lookup(p.OrganizationID).Find(ctx, "usage_attribution_type_id", req.GetUsageAttributionTypeId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	return attributionv1.GetUsageAttributionTypeResponse_builder{UsageAttributionType: attributionTypeProto(e)}.Build(), nil
}

// ListUsageAttributionTypes implements
// UsageAttributionService.ListUsageAttributionTypes: newest first (lago
// UsageAttributionTypesQuery).
func (s *UsageAttribution) ListUsageAttributionTypes(ctx context.Context, req *attributionv1.ListUsageAttributionTypesRequest) (*attributionv1.ListUsageAttributionTypesResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	f, err := attributionListFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	f.Search = req.GetSearch()
	page, err := s.svc.List(ctx, p.OrganizationID, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return attributionv1.ListUsageAttributionTypesResponse_builder{
		UsageAttributionTypes: Items(page, attributionTypeProto),
		NextPageToken:         new(page.NextToken),
	}.Build(), nil
}

// UpdateUsageAttributionType implements
// UsageAttributionService.UpdateUsageAttributionType.
// usage_attribution_type.id is an ID or code:<code>. The fields updated
// are the update_mask paths or, without one, the fields set; an empty
// parent_id detaches the type.
func (s *UsageAttribution) UpdateUsageAttributionType(ctx context.Context, req *attributionv1.UpdateUsageAttributionTypeRequest) (*attributionv1.UpdateUsageAttributionTypeResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetUsageAttributionType()
	paths, err := UpdatePaths(req.GetUpdateMask(), attributionUpdateFields, func(path string) bool {
		switch path {
		case "code":
			return b.HasCode()
		case "name":
			return b.HasName()
		case "description":
			return b.HasDescription()
		case "attribution_keys":
			return len(b.GetAttributionKeys()) > 0
		case "role":
			return b.HasRole()
		case "parent_id":
			return b.HasParentId()
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	cur, err := s.lookup(p.OrganizationID).Find(ctx, "usage_attribution_type.id", b.GetId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	var in usageattribution.UpdateInput
	for _, path := range paths {
		switch path {
		case "code":
			in.Code = core.Some(b.GetCode())
		case "name":
			in.Name = core.Some(Opt(b.HasName(), b.GetName()))
		case "description":
			in.Description = core.Some(Opt(b.HasDescription(), b.GetDescription()))
		case "attribution_keys":
			in.AttributionKeys = core.Some(b.GetAttributionKeys())
		case "role":
			in.Role = core.Some(attributionRoles.Core(b.GetRole()))
		case "parent_id":
			parent, err := s.parent(ctx, p.OrganizationID, b.GetParentId())
			if err != nil {
				return nil, err
			}
			in.ParentID = core.Some(parent)
		}
	}
	e, err := s.svc.Update(ctx, p.OrganizationID, cur.Type.ID, in)
	if err != nil {
		return nil, attributionError(ctx, err)
	}
	return attributionv1.UpdateUsageAttributionTypeResponse_builder{UsageAttributionType: attributionTypeProto(e)}.Build(), nil
}

// DeleteUsageAttributionType implements
// UsageAttributionService.DeleteUsageAttributionType: the type and its
// values are deleted, its children kept.
func (s *UsageAttribution) DeleteUsageAttributionType(ctx context.Context, req *attributionv1.DeleteUsageAttributionTypeRequest) (*attributionv1.DeleteUsageAttributionTypeResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := s.lookup(p.OrganizationID).Find(ctx, "usage_attribution_type_id", req.GetUsageAttributionTypeId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	if _, err := s.svc.Delete(ctx, p.OrganizationID, cur.Type.ID); err != nil {
		return nil, Error(ctx, err)
	}
	return &attributionv1.DeleteUsageAttributionTypeResponse{}, nil
}

func (s *UsageAttribution) lookup(org core.OrganizationID) Lookup[core.UsageAttributionTypeID, *usageattribution.Entry] {
	return Lookup[core.UsageAttributionTypeID, *usageattribution.Entry]{
		ByID: func(ctx context.Context, id core.UsageAttributionTypeID) (*usageattribution.Entry, error) {
			return s.svc.Get(ctx, org, id)
		},
		ByCode: func(ctx context.Context, code string) (*usageattribution.Entry, error) {
			return s.svc.GetByCode(ctx, org, code)
		},
	}
}

// parent resolves the parent reference ref: "" is no parent, and a kept
// type that cannot be found is NotFound parent_usage_attribution_type
// (usage_attribution_types_controller.rb:93-99).
func (s *UsageAttribution) parent(ctx context.Context, org core.OrganizationID, ref string) (core.UsageAttributionTypeID, error) {
	if ref == "" {
		return core.UsageAttributionTypeID{}, nil
	}
	e, err := s.lookup(org).Find(ctx, "usage_attribution_type.parent_id", ref)
	if err != nil {
		var nf *core.NotFoundError
		if errors.As(err, &nf) {
			err = core.NotFound(usageattribution.ParentResource)
		}
		return core.UsageAttributionTypeID{}, Error(ctx, err)
	}
	return e.Type.ID, nil
}

func attributionListFilter(s string) (attribution.TypeFilter, error) {
	var f attribution.TypeFilter
	e, err := ParseFilter(s)
	if err != nil {
		return f, err
	}
	eq, err := Equalities(e, attributionFilterFields...)
	if err != nil {
		return f, err
	}
	if v, ok := eq["role"]; ok {
		roles, err := FilterEnum[attributionv1.UsageAttributionRole]("role", v, attributionv1.UsageAttributionRole(0).Descriptor())
		if err != nil {
			return f, err
		}
		if len(roles) != 1 {
			return f, filterError(`field "role" takes one value`)
		}
		f.Role = attributionRoles.Core(roles[0])
	}
	if f.Root, err = FilterBool("root", eq["root"]); err != nil {
		return f, err
	}
	return f, nil
}

// attributionError maps the service errors, whose field paths are the
// type's, onto the request's usage_attribution_type message.
func attributionError(ctx context.Context, err error) error {
	if v, ok := validationViolations("usage_attribution_type.", err); ok {
		return withReason(InvalidArgument("invalid usage attribution type", v), ReasonValidationErrors)
	}
	return Error(ctx, err)
}

// attributionTypeProto renders e (lago V1::UsageAttributionTypeSerializer).
func attributionTypeProto(e *usageattribution.Entry) *attributionv1.UsageAttributionType {
	t := e.Type
	b := attributionv1.UsageAttributionType_builder{
		Id:              new(t.ID.String()),
		Code:            new(t.Code),
		Name:            t.Name,
		Description:     t.Description,
		AttributionKeys: t.AttributionKeys,
		Role:            new(attributionRoles.Proto(t.Role)),
		CreatedAt:       Timestamp(t.CreatedAt),
		UpdatedAt:       Timestamp(t.UpdatedAt),
	}
	if !t.ParentID.IsZero() {
		b.ParentId = new(t.ParentID.String())
	}
	if e.Parent != nil {
		b.ParentCode = new(e.Parent.Code)
	}
	return b.Build()
}
