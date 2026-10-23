package rpc

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	webhookv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/webhook/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/webhook/v1/webhookv1connect"
	"github.com/tanvoxyz/tanvo/libs/webhooks"
)

// Webhooks serves tanvo.webhook.v1.WebhookService over the webhook
// endpoints service (lago Api::V1::WebhookEndpointsController and the
// webhooks GraphQL resolvers and retry mutation).
type Webhooks struct {
	webhookv1connect.UnimplementedWebhookServiceHandler

	svc *webhooks.Endpoints
}

var _ webhookv1connect.WebhookServiceHandler = (*Webhooks)(nil)

// NewWebhooks returns the service over svc.
func NewWebhooks(svc *webhooks.Endpoints) *Webhooks { return &Webhooks{svc: svc} }

var deliveryStatuses = NewEnum(map[webhook.Status]webhookv1.DeliveryStatus{
	webhook.StatusPending:   webhookv1.DeliveryStatus_DELIVERY_STATUS_PENDING,
	webhook.StatusRetrying:  webhookv1.DeliveryStatus_DELIVERY_STATUS_RETRYING,
	webhook.StatusSucceeded: webhookv1.DeliveryStatus_DELIVERY_STATUS_SUCCEEDED,
	webhook.StatusFailed:    webhookv1.DeliveryStatus_DELIVERY_STATUS_FAILED,
})

// endpointUpdateFields are the update_mask paths of UpdateWebhookEndpoint.
var endpointUpdateFields = []string{"url", "name", "event_types"}

// deliveryFilterFields are the fields ListWebhookDeliveries filters on
// with equalities (lago WebhooksQuery statuses, event_types and
// http_statuses); updated_at takes >= and <= bounds (from_date, to_date).
var deliveryFilterFields = []string{"status", "event_type", "http_status"}

// WebhookSecurityLog adapts the security log to the webhook endpoints
// service.
func WebhookSecurityLog(l auth.SecurityLogger) webhooks.SecurityLogger {
	return webhookSecurityLog{l}
}

type webhookSecurityLog struct{ l auth.SecurityLogger }

func (w webhookSecurityLog) RecordEndpoint(ctx context.Context, org core.OrganizationID, event string, resources map[string]any) error {
	return w.l.Record(ctx, auth.SecurityEvent{
		OrganizationID: org, Type: webhooks.SecurityLogEndpoint, Event: event, Resources: resources,
	})
}

// CreateWebhookEndpoint implements WebhookService.CreateWebhookEndpoint
// (lago WebhookEndpoints::CreateService). The response carries the signing
// secret.
func (s *Webhooks) CreateWebhookEndpoint(ctx context.Context, req *webhookv1.CreateWebhookEndpointRequest) (*webhookv1.CreateWebhookEndpointResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetWebhookEndpoint()
	e, err := s.svc.Create(ctx, p.OrganizationID, webhooks.EndpointInput{
		URL:        b.GetUrl(),
		Name:       Opt(b.HasName(), b.GetName()),
		EventTypes: eventTypesInput(b.GetEventTypes()),
	})
	if err != nil {
		return nil, endpointError(ctx, err)
	}
	out := webhookEndpoint(e)
	out.SetSecret(e.Secret)
	return webhookv1.CreateWebhookEndpointResponse_builder{WebhookEndpoint: out}.Build(), nil
}

// GetWebhookEndpoint implements WebhookService.GetWebhookEndpoint.
func (s *Webhooks) GetWebhookEndpoint(ctx context.Context, req *webhookv1.GetWebhookEndpointRequest) (*webhookv1.GetWebhookEndpointResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.WebhookEndpointID]("webhook_endpoint_id", req.GetWebhookEndpointId())
	if err != nil {
		return nil, err
	}
	e, err := s.svc.Get(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return webhookv1.GetWebhookEndpointResponse_builder{WebhookEndpoint: webhookEndpoint(e)}.Build(), nil
}

// ListWebhookEndpoints implements WebhookService.ListWebhookEndpoints,
// ordered by URL (lago WebhookEndpointsQuery).
func (s *Webhooks) ListWebhookEndpoints(ctx context.Context, req *webhookv1.ListWebhookEndpointsRequest) (*webhookv1.ListWebhookEndpointsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	page, err := s.svc.List(ctx, p.OrganizationID, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return webhookv1.ListWebhookEndpointsResponse_builder{
		WebhookEndpoints: Items(page, webhookEndpoint),
		NextPageToken:    new(page.NextToken),
	}.Build(), nil
}

// UpdateWebhookEndpoint implements WebhookService.UpdateWebhookEndpoint
// (lago WebhookEndpoints::UpdateService). The fields updated are the
// update_mask paths or, without one, the fields set; event_types is
// replaced only through the mask, since an empty list is unset.
func (s *Webhooks) UpdateWebhookEndpoint(ctx context.Context, req *webhookv1.UpdateWebhookEndpointRequest) (*webhookv1.UpdateWebhookEndpointResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetWebhookEndpoint()
	paths, err := UpdatePaths(req.GetUpdateMask(), endpointUpdateFields, func(path string) bool {
		switch path {
		case "url":
			return b.HasUrl()
		case "name":
			return b.HasName()
		case "event_types":
			return len(b.GetEventTypes()) > 0
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.WebhookEndpointID]("webhook_endpoint.id", b.GetId())
	if err != nil {
		return nil, err
	}
	var in webhooks.EndpointUpdate
	for _, path := range paths {
		switch path {
		case "url":
			in.URL = core.Some(b.GetUrl())
		case "name":
			in.Name = core.Some(Opt(b.HasName(), b.GetName()))
		case "event_types":
			in.EventTypes = core.Some(eventTypesInput(b.GetEventTypes()))
		}
	}
	e, err := s.svc.Update(ctx, p.OrganizationID, id, in)
	if err != nil {
		return nil, endpointError(ctx, err)
	}
	return webhookv1.UpdateWebhookEndpointResponse_builder{WebhookEndpoint: webhookEndpoint(e)}.Build(), nil
}

// DeleteWebhookEndpoint implements WebhookService.DeleteWebhookEndpoint
// (lago WebhookEndpoints::DestroyService).
func (s *Webhooks) DeleteWebhookEndpoint(ctx context.Context, req *webhookv1.DeleteWebhookEndpointRequest) (*webhookv1.DeleteWebhookEndpointResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.WebhookEndpointID]("webhook_endpoint_id", req.GetWebhookEndpointId())
	if err != nil {
		return nil, err
	}
	if _, err := s.svc.Delete(ctx, p.OrganizationID, id); err != nil {
		return nil, Error(ctx, err)
	}
	return webhookv1.DeleteWebhookEndpointResponse_builder{}.Build(), nil
}

// ListWebhookEventTypes implements WebhookService.ListWebhookEventTypes
// (lago Resolvers::EventTypesResolver).
func (s *Webhooks) ListWebhookEventTypes(ctx context.Context, _ *webhookv1.ListWebhookEventTypesRequest) (*webhookv1.ListWebhookEventTypesResponse, error) {
	if _, err := principal(ctx); err != nil {
		return nil, err
	}
	types := webhooks.EventTypes()
	out := make([]*webhookv1.WebhookEventType, len(types))
	for i, t := range types {
		out[i] = webhookv1.WebhookEventType_builder{
			Name:        new(string(t.Type)),
			Category:    new(t.Category),
			Description: new(t.Description),
		}.Build()
	}
	return webhookv1.ListWebhookEventTypesResponse_builder{EventTypes: out}.Build(), nil
}

// GetWebhookDelivery implements WebhookService.GetWebhookDelivery (lago
// Resolvers::WebhookResolver).
func (s *Webhooks) GetWebhookDelivery(ctx context.Context, req *webhookv1.GetWebhookDeliveryRequest) (*webhookv1.GetWebhookDeliveryResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	endpoint, id, err := deliveryIDs(req.GetWebhookEndpointId(), req.GetDeliveryId())
	if err != nil {
		return nil, err
	}
	d, err := s.svc.Delivery(ctx, p.OrganizationID, endpoint, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return webhookv1.GetWebhookDeliveryResponse_builder{Delivery: webhookDelivery(d)}.Build(), nil
}

// ListWebhookDeliveries implements WebhookService.ListWebhookDeliveries:
// the last updated first, filtered on status, event_type, http_status and
// updated_at bounds and searched on the delivery and object IDs (lago
// Resolvers::WebhooksResolver and WebhooksQuery).
func (s *Webhooks) ListWebhookDeliveries(ctx context.Context, req *webhookv1.ListWebhookDeliveriesRequest) (*webhookv1.ListWebhookDeliveriesResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	endpoint, err := ParseID[core.WebhookEndpointID]("webhook_endpoint_id", req.GetWebhookEndpointId())
	if err != nil {
		return nil, err
	}
	f, err := deliveryListFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	f.Search = req.GetSearch()
	page, err := s.svc.Deliveries(ctx, p.OrganizationID, endpoint, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return webhookv1.ListWebhookDeliveriesResponse_builder{
		Deliveries:    Items(page, webhookDelivery),
		NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// RetryWebhookDelivery implements WebhookService.RetryWebhookDelivery
// (lago Webhooks::RetryService).
func (s *Webhooks) RetryWebhookDelivery(ctx context.Context, req *webhookv1.RetryWebhookDeliveryRequest) (*webhookv1.RetryWebhookDeliveryResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	endpoint, id, err := deliveryIDs(req.GetWebhookEndpointId(), req.GetDeliveryId())
	if err != nil {
		return nil, err
	}
	d, err := s.svc.Retry(ctx, p.OrganizationID, endpoint, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return webhookv1.RetryWebhookDeliveryResponse_builder{Delivery: webhookDelivery(d)}.Build(), nil
}

func deliveryIDs(endpoint, delivery string) (core.WebhookEndpointID, core.WebhookID, error) {
	e, err := ParseID[core.WebhookEndpointID]("webhook_endpoint_id", endpoint)
	if err != nil {
		return e, core.WebhookID{}, err
	}
	d, err := ParseID[core.WebhookID]("delivery_id", delivery)
	return e, d, err
}

// eventTypesInput maps the request's event types: none is every type.
func eventTypesInput(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return slices.Clone(in)
}

// endpointError maps the service errors, whose field paths are the
// endpoint's, onto the request's webhook_endpoint message.
func endpointError(ctx context.Context, err error) error {
	if v, ok := validationViolations("webhook_endpoint.", err); ok {
		return withReason(InvalidArgument("invalid webhook endpoint", v), ReasonValidationErrors)
	}
	return Error(ctx, err)
}

func deliveryListFilter(s string) (webhook.DeliveryFilter, error) {
	var f webhook.DeliveryFilter
	e, err := ParseFilter(s)
	if err != nil {
		return f, err
	}
	e, err = deliveryUpdatedBounds(&f, e)
	if err != nil {
		return f, err
	}
	eq, err := Equalities(e, deliveryFilterFields...)
	if err != nil {
		return f, err
	}
	statuses, err := FilterEnum[webhookv1.DeliveryStatus]("status", eq["status"], webhookv1.DeliveryStatus(0).Descriptor())
	if err != nil {
		return f, err
	}
	for _, st := range statuses {
		f.Statuses = append(f.Statuses, deliveryStatuses.Core(st))
	}
	for _, t := range eq["event_type"] {
		f.EventTypes = append(f.EventTypes, webhook.EventType(t))
	}
	if f.HTTPStatus, err = webhook.ParseHTTPStatusFilter(eq["http_status"]); err != nil {
		if errors.Is(err, core.ErrInvalidValue) {
			return f, filterError(fmt.Sprintf("field \"http_status\": %v", err))
		}
		return f, err
	}
	return f, nil
}

// deliveryUpdatedBounds moves the top-level updated_at bounds of e into f
// and returns the rest of e.
func deliveryUpdatedBounds(f *webhook.DeliveryFilter, e FilterExpr) (FilterExpr, error) {
	if e == nil {
		return nil, nil
	}
	terms := []FilterExpr{e}
	if and, ok := e.(FilterAnd); ok {
		terms = and
	}
	rest := make(FilterAnd, 0, len(terms))
	for _, t := range terms {
		r, ok := t.(Restriction)
		if !ok || r.Field != "updated_at" {
			rest = append(rest, t)
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, r.Value)
		if err != nil {
			return nil, filterError(fmt.Sprintf("field \"updated_at\": invalid RFC 3339 time %q", r.Value))
		}
		at = at.UTC()
		var dst **time.Time
		switch r.Op {
		case CmpGe:
			dst = &f.UpdatedFrom
		case CmpLe:
			dst = &f.UpdatedTo
		default:
			return nil, filterError("field \"updated_at\" is compared with >= or <=")
		}
		if *dst != nil {
			return nil, filterError("field \"updated_at\" is bounded twice on one side")
		}
		*dst = &at
	}
	switch len(rest) {
	case 0:
		return nil, nil
	case 1:
		return rest[0], nil
	}
	return rest, nil
}

func webhookEndpoint(e *webhook.Endpoint) *webhookv1.WebhookEndpoint {
	b := webhookv1.WebhookEndpoint_builder{
		Id:        new(e.ID.String()),
		Url:       new(e.URL),
		Name:      e.Name,
		CreatedAt: Timestamp(e.CreatedAt),
		UpdatedAt: Timestamp(e.UpdatedAt),
	}
	for _, t := range e.EventTypes {
		b.EventTypes = append(b.EventTypes, string(t))
	}
	return b.Build()
}

func webhookDelivery(d *webhook.Delivery) *webhookv1.WebhookDelivery {
	b := webhookv1.WebhookDelivery_builder{
		Id:                new(d.ID.String()),
		WebhookEndpointId: new(d.EndpointID.String()),
		EventType:         new(string(d.EventType)),
		ObjectType:        new(d.ObjectType),
		ObjectId:          new(d.ObjectID.String()),
		Payload:           slices.Clone(d.Payload),
		Attempts:          new(int32(d.Attempts)),
		Response:          d.Response,
		LastAttemptAt:     TimestampPtr(d.LastAttemptAt),
		NextAttemptAt:     TimestampPtr(d.NextAttemptAt),
		CreatedAt:         Timestamp(d.CreatedAt),
	}
	if st := deliveryStatuses.Proto(d.Status); st != 0 {
		b.Status = &st
	}
	if d.HTTPStatus != nil {
		b.HttpStatus = new(int32(*d.HTTPStatus))
	}
	return b.Build()
}
