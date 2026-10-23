package rpc

import (
	"context"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/entitlements"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	catalogv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1/catalogv1connect"
)

// Features serves tanvo.catalog.v1.FeatureService over the billing
// library's entitlement service (lago Api::V1::FeaturesController).
type Features struct {
	catalogv1connect.UnimplementedFeatureServiceHandler

	svc *entitlements.Service
}

var _ catalogv1connect.FeatureServiceHandler = (*Features)(nil)

// NewFeatures returns the service over svc.
func NewFeatures(svc *entitlements.Service) *Features { return &Features{svc: svc} }

var featurePrivilegeValueTypes = NewEnum(map[catalog.PrivilegeValueType]catalogv1.PrivilegeValueType{
	catalog.PrivilegeValueTypeInteger: catalogv1.PrivilegeValueType_PRIVILEGE_VALUE_TYPE_INTEGER,
	catalog.PrivilegeValueTypeString:  catalogv1.PrivilegeValueType_PRIVILEGE_VALUE_TYPE_STRING,
	catalog.PrivilegeValueTypeBoolean: catalogv1.PrivilegeValueType_PRIVILEGE_VALUE_TYPE_BOOLEAN,
	catalog.PrivilegeValueTypeSelect:  catalogv1.PrivilegeValueType_PRIVILEGE_VALUE_TYPE_SELECT,
})

// featureUpdateFields are the update_mask paths of UpdateFeature; the code
// cannot change (lago feature_update_params).
var featureUpdateFields = []string{"name", "description", "privileges"}

// CreateFeature implements FeatureService.CreateFeature.
func (s *Features) CreateFeature(ctx context.Context, req *catalogv1.CreateFeatureRequest) (*catalogv1.CreateFeatureResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetFeature()
	f, err := s.svc.CreateFeature(ctx, p.OrganizationID, entitlements.CreateFeatureInput{
		Code:        b.GetCode(),
		Name:        Opt(b.HasName(), b.GetName()),
		Description: Opt(b.HasDescription(), b.GetDescription()),
		Privileges:  featurePrivileges(b.GetPrivileges()),
	})
	if err != nil {
		return nil, featureError(ctx, err)
	}
	return catalogv1.CreateFeatureResponse_builder{Feature: featureProto(f)}.Build(), nil
}

// GetFeature implements FeatureService.GetFeature.
func (s *Features) GetFeature(ctx context.Context, req *catalogv1.GetFeatureRequest) (*catalogv1.GetFeatureResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	f, err := s.lookup(p.OrganizationID).Find(ctx, "feature_id", req.GetFeatureId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.GetFeatureResponse_builder{Feature: featureProto(f)}.Build(), nil
}

// ListFeatures implements FeatureService.ListFeatures: newest first,
// searched on the name, code and description (lago
// Entitlement::FeaturesQuery).
func (s *Features) ListFeatures(ctx context.Context, req *catalogv1.ListFeaturesRequest) (*catalogv1.ListFeaturesResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	page, err := s.svc.ListFeatures(ctx, p.OrganizationID, req.GetSearch(), Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.ListFeaturesResponse_builder{
		Features:      Items(page, featureProto),
		NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// UpdateFeature implements FeatureService.UpdateFeature. feature.id is an
// ID or code:<code>. The fields updated are the update_mask paths or,
// without one, the fields set (privileges when not empty); the given
// privileges are merged by code into the feature's (lago
// Entitlement::FeatureUpdateService with partial: true).
func (s *Features) UpdateFeature(ctx context.Context, req *catalogv1.UpdateFeatureRequest) (*catalogv1.UpdateFeatureResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetFeature()
	paths, err := UpdatePaths(req.GetUpdateMask(), featureUpdateFields, func(path string) bool {
		switch path {
		case "name":
			return b.HasName()
		case "description":
			return b.HasDescription()
		case "privileges":
			return len(b.GetPrivileges()) > 0
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	cur, err := s.lookup(p.OrganizationID).Find(ctx, "feature.id", b.GetId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	var in entitlements.UpdateFeatureInput
	for _, path := range paths {
		switch path {
		case "name":
			in.Name = core.Some(Opt(b.HasName(), b.GetName()))
		case "description":
			in.Description = core.Some(Opt(b.HasDescription(), b.GetDescription()))
		case "privileges":
			in.Privileges = featurePrivileges(b.GetPrivileges())
		}
	}
	f, err := s.svc.UpdateFeature(ctx, p.OrganizationID, cur.ID, in)
	if err != nil {
		return nil, featureError(ctx, err)
	}
	return catalogv1.UpdateFeatureResponse_builder{Feature: featureProto(f)}.Build(), nil
}

// DeleteFeature implements FeatureService.DeleteFeature.
func (s *Features) DeleteFeature(ctx context.Context, req *catalogv1.DeleteFeatureRequest) (*catalogv1.DeleteFeatureResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := s.lookup(p.OrganizationID).Find(ctx, "feature_id", req.GetFeatureId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	f, err := s.svc.DeleteFeature(ctx, p.OrganizationID, cur.ID)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.DeleteFeatureResponse_builder{Feature: featureProto(f)}.Build(), nil
}

// DeleteFeaturePrivilege implements FeatureService.DeleteFeaturePrivilege
// (lago Api::V1::Features::PrivilegesController#destroy).
func (s *Features) DeleteFeaturePrivilege(ctx context.Context, req *catalogv1.DeleteFeaturePrivilegeRequest) (*catalogv1.DeleteFeaturePrivilegeResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := s.lookup(p.OrganizationID).Find(ctx, "feature_id", req.GetFeatureId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	f, err := s.svc.DeleteFeaturePrivilege(ctx, p.OrganizationID, cur.ID, req.GetPrivilegeCode())
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.DeleteFeaturePrivilegeResponse_builder{Feature: featureProto(f)}.Build(), nil
}

func (s *Features) lookup(org core.OrganizationID) Lookup[core.FeatureID, *catalog.Feature] {
	return Lookup[core.FeatureID, *catalog.Feature]{
		ByID: func(ctx context.Context, id core.FeatureID) (*catalog.Feature, error) {
			return s.svc.GetFeature(ctx, org, id)
		},
		ByCode: func(ctx context.Context, code string) (*catalog.Feature, error) {
			return s.svc.GetFeatureByCode(ctx, org, code)
		},
	}
}

// featureError maps the service errors, whose field paths are the
// feature's, onto the request's feature message.
func featureError(ctx context.Context, err error) error {
	if v, ok := validationViolations("feature.", err); ok {
		return withReason(InvalidArgument("invalid feature", v), ReasonValidationErrors)
	}
	return Error(ctx, err)
}

func featurePrivileges(in []*catalogv1.Privilege) []entitlements.PrivilegeInput {
	out := make([]entitlements.PrivilegeInput, len(in))
	for i, p := range in {
		out[i] = entitlements.PrivilegeInput{
			Code:      p.GetCode(),
			Name:      Opt(p.HasName(), p.GetName()),
			ValueType: featurePrivilegeValueTypes.Core(p.GetValueType()),
		}
		if len(p.GetSelectOptions()) > 0 {
			out[i].SelectOptions = slices.Clone(p.GetSelectOptions())
		}
	}
	return out
}

func featurePrivilegeProto(p catalog.Privilege) *catalogv1.Privilege {
	b := catalogv1.Privilege_builder{Code: new(p.Code), Name: p.Name, SelectOptions: slices.Clone(p.SelectOptions)}
	if t := featurePrivilegeValueTypes.Proto(p.ValueType); t != 0 {
		b.ValueType = &t
	}
	return b.Build()
}

func featureProto(f *catalog.Feature) *catalogv1.Feature {
	b := catalogv1.Feature_builder{
		Id:          new(f.ID.String()),
		Code:        new(f.Code),
		Name:        f.Name,
		Description: f.Description,
		CreatedAt:   Timestamp(f.CreatedAt),
		UpdatedAt:   Timestamp(f.UpdatedAt),
	}
	for _, p := range f.Privileges {
		b.Privileges = append(b.Privileges, featurePrivilegeProto(p))
	}
	return b.Build()
}
