package rpc

import (
	"context"
	"fmt"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/libs/billing/entitlements"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	catalogv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1/catalogv1connect"
)

// Entitlements serves tanvo.catalog.v1.EntitlementService over the
// billing library's entitlement service (lago
// Api::V1::Plans::EntitlementsController and
// Api::V1::Subscriptions::EntitlementsController with their privileges
// controllers).
type Entitlements struct {
	catalogv1connect.UnimplementedEntitlementServiceHandler

	svc *entitlements.Service
}

var _ catalogv1connect.EntitlementServiceHandler = (*Entitlements)(nil)

// NewEntitlements returns the service over svc.
func NewEntitlements(svc *entitlements.Service) *Entitlements { return &Entitlements{svc: svc} }

var entitlementSources = NewEnum(map[entitlements.Source]catalogv1.EntitlementSource{
	entitlements.SourcePlan:         catalogv1.EntitlementSource_ENTITLEMENT_SOURCE_PLAN,
	entitlements.SourceSubscription: catalogv1.EntitlementSource_ENTITLEMENT_SOURCE_SUBSCRIPTION,
})

// ListEntitlements implements EntitlementService.ListEntitlements: the
// plan's entitlements, or the subscription's resolved ones.
func (s *Entitlements) ListEntitlements(ctx context.Context, req *catalogv1.ListEntitlementsRequest) (*catalogv1.ListEntitlementsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	parent, err := entitlementParentOf(req)
	if err != nil {
		return nil, err
	}
	var ents []entitlements.Entitlement
	if parent.sub != nil {
		ents, err = s.svc.ListSubscriptionEntitlements(ctx, p.OrganizationID, *parent.sub)
	} else {
		ents, err = s.svc.ListPlanEntitlements(ctx, p.OrganizationID, parent.plan)
	}
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.ListEntitlementsResponse_builder{Entitlements: entitlementProtos(ents)}.Build(), nil
}

// GetEntitlement implements EntitlementService.GetEntitlement for plans;
// subscriptions have no single entitlement read (lago routes only index
// on subscription entitlements).
func (s *Entitlements) GetEntitlement(ctx context.Context, req *catalogv1.GetEntitlementRequest) (*catalogv1.GetEntitlementResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	parent, err := entitlementParentOf(req)
	if err != nil {
		return nil, err
	}
	if parent.sub != nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "subscription entitlements are read with ListEntitlements")
	}
	feature, err := CatalogRef("feature_id", req.GetFeatureId())
	if err != nil {
		return nil, err
	}
	e, err := s.svc.GetPlanEntitlement(ctx, p.OrganizationID, parent.plan, feature)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.GetEntitlementResponse_builder{Entitlement: entitlementProto(e)}.Build(), nil
}

// ReplaceEntitlements implements EntitlementService.ReplaceEntitlements
// for plans: the plan grants exactly the given features (lago
// Entitlement::PlanEntitlementsUpdateService with partial: false).
// Subscriptions are only upserted (lago calls
// SubscriptionEntitlementsUpdateService with partial: true only).
func (s *Entitlements) ReplaceEntitlements(ctx context.Context, req *catalogv1.ReplaceEntitlementsRequest) (*catalogv1.ReplaceEntitlementsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	parent, err := entitlementParentOf(req)
	if err != nil {
		return nil, err
	}
	if parent.sub != nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "subscription entitlements are changed with UpsertEntitlement and DeleteEntitlement")
	}
	in := make([]entitlements.EntitlementInput, len(req.GetEntitlements()))
	for i, e := range req.GetEntitlements() {
		if in[i], err = entitlementInput(fmt.Sprintf("entitlements[%d]", i), e); err != nil {
			return nil, err
		}
	}
	ents, err := s.svc.ReplacePlanEntitlements(ctx, p.OrganizationID, parent.plan, in)
	if err != nil {
		return nil, entitlementError(ctx, "entitlements.", err)
	}
	return catalogv1.ReplaceEntitlementsResponse_builder{Entitlements: entitlementProtos(ents)}.Build(), nil
}

// UpsertEntitlement implements EntitlementService.UpsertEntitlement: the
// given privilege values are merged into the plan's entitlement, or into
// the subscription's override (lago PlanEntitlementsUpdateService and
// SubscriptionEntitlementsUpdateService with partial: true).
func (s *Entitlements) UpsertEntitlement(ctx context.Context, req *catalogv1.UpsertEntitlementRequest) (*catalogv1.UpsertEntitlementResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	parent, err := entitlementParentOf(req)
	if err != nil {
		return nil, err
	}
	in, err := entitlementInput("entitlement", req.GetEntitlement())
	if err != nil {
		return nil, err
	}
	var e entitlements.Entitlement
	if parent.sub != nil {
		e, err = s.svc.UpsertSubscriptionEntitlement(ctx, p.OrganizationID, *parent.sub, in)
	} else {
		e, err = s.svc.UpsertPlanEntitlement(ctx, p.OrganizationID, parent.plan, in)
	}
	if err != nil {
		return nil, entitlementError(ctx, "entitlement.", err)
	}
	return catalogv1.UpsertEntitlementResponse_builder{Entitlement: entitlementProto(e)}.Build(), nil
}

// DeleteEntitlement implements EntitlementService.DeleteEntitlement (lago
// Entitlement::PlanEntitlementDestroyService and
// SubscriptionFeatureRemoveService).
func (s *Entitlements) DeleteEntitlement(ctx context.Context, req *catalogv1.DeleteEntitlementRequest) (*catalogv1.DeleteEntitlementResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	parent, err := entitlementParentOf(req)
	if err != nil {
		return nil, err
	}
	feature, err := CatalogRef("feature_id", req.GetFeatureId())
	if err != nil {
		return nil, err
	}
	if parent.sub != nil {
		err = s.svc.DeleteSubscriptionEntitlement(ctx, p.OrganizationID, *parent.sub, feature)
	} else {
		err = s.svc.DeletePlanEntitlement(ctx, p.OrganizationID, parent.plan, feature)
	}
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.DeleteEntitlementResponse_builder{}.Build(), nil
}

// DeleteEntitlementPrivilege implements
// EntitlementService.DeleteEntitlementPrivilege (lago
// Entitlement::PlanEntitlementPrivilegeDestroyService and
// SubscriptionFeaturePrivilegeRemoveService).
func (s *Entitlements) DeleteEntitlementPrivilege(ctx context.Context, req *catalogv1.DeleteEntitlementPrivilegeRequest) (*catalogv1.DeleteEntitlementPrivilegeResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	parent, err := entitlementParentOf(req)
	if err != nil {
		return nil, err
	}
	feature, err := CatalogRef("feature_id", req.GetFeatureId())
	if err != nil {
		return nil, err
	}
	var e entitlements.Entitlement
	if parent.sub != nil {
		e, err = s.svc.DeleteSubscriptionEntitlementPrivilege(ctx, p.OrganizationID, *parent.sub, feature, req.GetPrivilegeCode())
	} else {
		e, err = s.svc.DeletePlanEntitlementPrivilege(ctx, p.OrganizationID, parent.plan, feature, req.GetPrivilegeCode())
	}
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.DeleteEntitlementPrivilegeResponse_builder{Entitlement: entitlementProto(e)}.Build(), nil
}

// entitlementParent is the plan or, when sub is set, the subscription of
// a request.
type entitlementParent struct {
	plan refs.Ref
	sub  *refs.SubscriptionRef
}

type entitlementParentRequest interface {
	GetPlanId() string
	GetSubscriptionId() string
}

// entitlementParentOf parses the parent of req: plan_id is an ID or
// code:<code>, subscription_id an ID or external_id:<external_id>.
func entitlementParentOf(req entitlementParentRequest) (entitlementParent, error) {
	if req.GetSubscriptionId() == "" {
		plan, err := CatalogRef("plan_id", req.GetPlanId())
		return entitlementParent{plan: plan}, err
	}
	r, err := ParseRef("subscription_id", req.GetSubscriptionId())
	if err != nil {
		return entitlementParent{}, err
	}
	if r.Code != "" {
		return entitlementParent{}, InvalidArgument("invalid reference", []FieldViolation{{
			Field: "subscription_id", Reason: core.CodeInvalid, Description: "this resource has no code",
		}})
	}
	if r.ExternalID != "" {
		return entitlementParent{sub: &refs.SubscriptionRef{ExternalID: r.ExternalID}}, nil
	}
	id, err := ParseID[core.SubscriptionID]("subscription_id", r.ID.String())
	return entitlementParent{sub: &refs.SubscriptionRef{ID: id}}, err
}

// entitlementInput converts the entitlement at field.
func entitlementInput(field string, e *catalogv1.Entitlement) (entitlements.EntitlementInput, error) {
	feature, err := CatalogRef(field+".feature_id", e.GetFeatureId())
	if err != nil {
		return entitlements.EntitlementInput{}, err
	}
	in := entitlements.EntitlementInput{Feature: feature}
	for _, v := range e.GetPrivileges() {
		in.Values = append(in.Values, entitlements.ValueInput{Code: v.GetPrivilegeCode(), Value: v.GetValue()})
	}
	return in, nil
}

// entitlementError maps the service's validation errors onto the
// request's entitlement message at prefix.
func entitlementError(ctx context.Context, prefix string, err error) error {
	if v, ok := validationViolations(prefix, err); ok {
		return withReason(InvalidArgument("invalid entitlement", v), ReasonValidationErrors)
	}
	return Error(ctx, err)
}

func entitlementProtos(in []entitlements.Entitlement) []*catalogv1.Entitlement {
	out := make([]*catalogv1.Entitlement, len(in))
	for i, e := range in {
		out[i] = entitlementProto(e)
	}
	return out
}

func entitlementProto(e entitlements.Entitlement) *catalogv1.Entitlement {
	b := catalogv1.Entitlement_builder{
		FeatureId:   new(e.Feature.ID.String()),
		FeatureCode: new(e.Feature.Code),
		FeatureName: e.Feature.Name,
		Source:      entitlementSource(e.Source),
	}
	for _, v := range e.Values {
		b.Privileges = append(b.Privileges, catalogv1.PrivilegeValue_builder{
			PrivilegeCode: new(v.Privilege.Code),
			Value:         new(v.Value),
			Source:        entitlementSource(v.Source),
		}.Build())
	}
	return b.Build()
}

// entitlementSource is the proto source, nil on plan views.
func entitlementSource(s entitlements.Source) *catalogv1.EntitlementSource {
	if s == "" {
		return nil
	}
	return new(entitlementSources.Proto(s))
}
