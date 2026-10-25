package rpc

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/tanvoxyz/tanvo/libs/billing/estimates"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
	eventv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/event/v1"
)

// EventReader reads ingested events (libs/ingest Reader).
type EventReader interface {
	// Get returns the event with the transaction ID, of the external
	// subscription when set, else the first ingested; or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, externalSubscriptionID, transactionID string) (*event.Event, error)
	// List returns the events matching in, newest first.
	List(ctx context.Context, org core.OrganizationID, in ingest.ListInput, page core.PageRequest) (core.Page[*event.Event], error)
	// ListEnriched returns the enriched events matching in, latest
	// enriched first; ingest.ErrEnrichedUnavailable when org does not
	// store them.
	ListEnriched(ctx context.Context, org core.OrganizationID, in ingest.ListInput, page core.PageRequest) (core.Page[*metering.EnrichedEvent], error)
}

// EndpointStatusHeader marks the responses of beta RPCs (lago
// X-Lago-Endpoint-Status).
const EndpointStatusHeader = "X-Tanvo-Endpoint-Status"

// ReasonEndpointNotAvailable is the reason of an RPC the organization's
// setup does not serve (lago endpoint_not_available).
const ReasonEndpointNotAvailable = "endpoint_not_available"

// FeeEstimator estimates the fees of events (libs/billing/estimates
// Service).
type FeeEstimator interface {
	// PayInAdvance estimates the fees of an event on its subscription's
	// pay-in-advance charges, priced with the period's usage and taxed.
	PayInAdvance(ctx context.Context, org core.OrganizationID, ev estimates.Event) ([]*invoice.Fee, error)
	// Instant estimates events on the percentage and standard
	// pay-in-advance charges, each priced on its own.
	Instant(ctx context.Context, org core.OrganizationID, events []estimates.Event) ([]estimates.InstantFee, error)
}

// WithReader serves GetEvent and ListEvents with r.
func (s *Events) WithReader(r EventReader) *Events {
	s.reader = r
	return s
}

// WithEstimator serves EstimateFees and EstimateInstantFees with e.
func (s *Events) WithEstimator(e FeeEstimator) *Events {
	s.estimator = e
	return s
}

// GetEvent implements EventService.GetEvent (lago
// EventsController#show).
func (s *Events) GetEvent(ctx context.Context, req *eventv1.GetEventRequest) (*eventv1.GetEventResponse, error) {
	if s.reader == nil {
		return s.UnimplementedEventServiceHandler.GetEvent(ctx, req)
	}
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	e, err := s.reader.Get(ctx, p.OrganizationID, req.GetExternalSubscriptionId(), req.GetTransactionId())
	if errors.Is(err, core.ErrNotFound) {
		err = core.NotFound("event")
	}
	if err != nil {
		return nil, Error(ctx, err)
	}
	return eventv1.GetEventResponse_builder{Event: toProto(e)}.Build(), nil
}

// ListEvents implements EventService.ListEvents (lago
// EventsController#index and EventsQuery): the filter restricts `code`
// and `external_subscription_id` with =, and `timestamp` with >= and <=,
// joined by AND.
func (s *Events) ListEvents(ctx context.Context, req *eventv1.ListEventsRequest) (*eventv1.ListEventsResponse, error) {
	if s.reader == nil {
		return s.UnimplementedEventServiceHandler.ListEvents(ctx, req)
	}
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	f, err := eventsFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	page, err := s.reader.List(ctx, p.OrganizationID, ingest.ListInput{ListFilter: f, FromStartedAt: req.GetTimestampFromStartedAt()}, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return eventv1.ListEventsResponse_builder{Events: Items(page, toProto), NextPageToken: &page.NextToken}.Build(), nil
}

// ListEnrichedEvents implements EventService.ListEnrichedEvents (lago
// EventsController#index_enriched), a beta RPC: the filter is that of
// ListEvents, and only organizations ingesting through Kafka store
// enriched events (lago ensure_organization_uses_clickhouse).
//
// source: app/controllers/api/v1/events_controller.rb:94-118
func (s *Events) ListEnrichedEvents(ctx context.Context, req *eventv1.ListEnrichedEventsRequest) (*eventv1.ListEnrichedEventsResponse, error) {
	if s.reader == nil {
		return s.UnimplementedEventServiceHandler.ListEnrichedEvents(ctx, req)
	}
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if info, ok := connect.CallInfoForServerContext(ctx); ok {
		info.ResponseHeader().Set(EndpointStatusHeader, "beta")
	}
	f, err := eventsFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	page, err := s.reader.ListEnriched(ctx, p.OrganizationID, ingest.ListInput{ListFilter: f, FromStartedAt: req.GetTimestampFromStartedAt()}, Page(req.GetPageSize(), req.GetPageToken()))
	if errors.Is(err, ingest.ErrEnrichedUnavailable) {
		return nil, newError(connect.CodePermissionDenied, ReasonEndpointNotAvailable, "enriched events are stored for organizations ingesting through Kafka")
	}
	if err != nil {
		return nil, Error(ctx, err)
	}
	return eventv1.ListEnrichedEventsResponse_builder{EnrichedEvents: Items(page, toEnrichedProto), NextPageToken: &page.NextToken}.Build(), nil
}

// toEnrichedProto maps a stored enriched event (lago
// V1::EventEnrichedSerializer).
func toEnrichedProto(e *metering.EnrichedEvent) *eventv1.EnrichedEvent {
	b := eventv1.EnrichedEvent_builder{
		TransactionId:          &e.TransactionID,
		ExternalSubscriptionId: &e.SubscriptionID,
		Code:                   &e.Code,
		Timestamp:              timestamppb.New(e.Timestamp),
		EnrichedAt:             timestamppb.New(e.EnrichedAt),
		Value:                  e.Value,
		Properties:             maps.Clone(map[string]string(e.Properties)),
	}
	if e.DecimalValue != nil {
		b.DecimalValue = new(e.DecimalValue.String())
	}
	if e.PreciseTotalAmountCents != nil {
		b.PreciseTotalAmount = new(e.PreciseTotalAmountCents.String())
	}
	return b.Build()
}

// eventsFilter reads the filter of ListEvents.
func eventsFilter(s string) (event.ListFilter, error) {
	var f event.ListFilter
	e, err := ParseFilter(s)
	if err != nil || e == nil {
		return f, err
	}
	terms := []FilterExpr{e}
	if and, ok := e.(FilterAnd); ok {
		terms = and
	}
	seen := map[string]bool{}
	for _, t := range terms {
		r, ok := t.(Restriction)
		if !ok {
			return f, filterError("only restrictions joined by AND are supported")
		}
		key := r.Field + string(r.Op)
		if seen[key] {
			return f, filterError(fmt.Sprintf("field %q is restricted twice", r.Field))
		}
		seen[key] = true
		switch r.Field {
		case "code", "external_subscription_id":
			if r.Op != CmpEq {
				return f, filterError(fmt.Sprintf("field %q is compared with =", r.Field))
			}
			if r.Field == "code" {
				f.Code = r.Value
			} else {
				f.ExternalSubscriptionID = r.Value
			}
		case "timestamp":
			ts, err := time.Parse(time.RFC3339Nano, r.Value)
			if err != nil {
				return f, filterError(fmt.Sprintf("field \"timestamp\": invalid RFC 3339 time %q", r.Value))
			}
			ts = ts.UTC()
			switch r.Op {
			case CmpGe:
				f.TimestampFrom = &ts
			case CmpLe:
				f.TimestampTo = &ts
			default:
				return f, filterError("field \"timestamp\" is compared with >= or <=")
			}
		default:
			return f, filterError(fmt.Sprintf("unknown field %q; supported: code, external_subscription_id, timestamp", r.Field))
		}
	}
	return f, nil
}

// EstimateFees implements EventService.EstimateFees (lago
// EventsController#estimate_fees).
func (s *Events) EstimateFees(ctx context.Context, req *eventv1.EstimateFeesRequest) (*eventv1.EstimateFeesResponse, error) {
	if s.estimator == nil {
		return s.UnimplementedEventServiceHandler.EstimateFees(ctx, req)
	}
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	ev, v := toEstimate("event.", req.GetEvent())
	if len(v) > 0 {
		return nil, InvalidArgument("invalid event", v)
	}
	fs, err := s.estimator.PayInAdvance(ctx, p.OrganizationID, ev)
	if v, ok := validationViolations("event.", err); ok {
		return nil, InvalidArgument("invalid event", v)
	}
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := make([]*eventv1.EstimatedFee, len(fs))
	for i, f := range fs {
		out[i] = estimatedFee(f, ev.Code)
	}
	return eventv1.EstimateFeesResponse_builder{Fees: out}.Build(), nil
}

// EstimateInstantFees implements EventService.EstimateInstantFees (lago
// EventsController#batch_estimate_instant_fees).
func (s *Events) EstimateInstantFees(ctx context.Context, req *eventv1.EstimateInstantFeesRequest) (*eventv1.EstimateInstantFeesResponse, error) {
	if s.estimator == nil {
		return s.UnimplementedEventServiceHandler.EstimateInstantFees(ctx, req)
	}
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	events := make([]estimates.Event, len(req.GetEvents()))
	var violations []FieldViolation
	for i, e := range req.GetEvents() {
		ev, v := toEstimate(fmt.Sprintf("events[%d].", i), e)
		events[i], violations = ev, append(violations, v...)
	}
	if len(violations) > 0 {
		return nil, InvalidArgument("invalid events", violations)
	}
	fs, err := s.estimator.Instant(ctx, p.OrganizationID, events)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := make([]*eventv1.EstimatedFee, len(fs))
	for i, f := range fs {
		amount := Money(f.Amount, f.Currency)
		units, unit := f.Units.String(), f.UnitAmount.String()
		b := eventv1.EstimatedFee_builder{
			ChargeId:           new(f.Charge.ID.String()),
			BillableMetricCode: &f.Metric.Code,
			TransactionId:      &f.TransactionID,
			Amount:             amount,
			TaxesAmount:        Money(0, f.Currency),
			Units:              &units,
			UnitAmount:         &unit,
			EventsCount:        new(int64(1)),
		}
		if !f.ChargeFilterID.IsZero() {
			b.ChargeFilterId = new(f.ChargeFilterID.String())
		}
		out[i] = b.Build()
	}
	return eventv1.EstimateInstantFeesResponse_builder{Fees: out}.Build(), nil
}

// toEstimate maps an API event to an event to estimate; the properties
// are read as at ingestion.
func toEstimate(prefix string, e *eventv1.Event) (estimates.Event, []FieldViolation) {
	in, v := toInput(prefix, e)
	ev := estimates.Event{TransactionID: in.TransactionID, Code: in.Code, ExternalSubscriptionID: in.ExternalSubscriptionID}
	if len(in.Properties) > 0 {
		props, err := event.NormalizeProperties(in.Properties)
		if err != nil {
			v = append(v, FieldViolation{Field: prefix + "properties", Reason: core.CodeInvalid, Description: err.Error()})
		}
		ev.Properties = props
	}
	if e.HasPreciseTotalAmount() {
		d, err := money.Parse(e.GetPreciseTotalAmount())
		if err != nil {
			v = append(v, FieldViolation{Field: prefix + "precise_total_amount", Reason: core.CodeInvalid, Description: "not a decimal"})
		}
		ev.PreciseTotalAmountCents = &d
	}
	return ev, v
}

// estimatedFee maps an estimated pay-in-advance fee.
func estimatedFee(f *invoice.Fee, code string) *eventv1.EstimatedFee {
	units, unit := f.Units.String(), f.PreciseUnitAmount.String()
	b := eventv1.EstimatedFee_builder{
		ChargeId:           new(f.ChargeID.String()),
		BillableMetricCode: &code,
		TransactionId:      &f.PayInAdvanceEventTransactionID,
		Amount:             Money(f.Amount, f.Currency),
		TaxesAmount:        Money(f.TaxesAmount, f.Currency),
		Units:              &units,
		UnitAmount:         &unit,
		EventsCount:        &f.EventsCount,
		GroupedBy:          maps.Clone(f.GroupedBy),
	}
	if !f.ChargeFilterID.IsZero() {
		b.ChargeFilterId = new(f.ChargeFilterID.String())
	}
	return b.Build()
}
