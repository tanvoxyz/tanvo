package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/tanvoxyz/tanvo/apps/server/internal/clientip"
	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	eventv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/event/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/event/v1/eventv1connect"
)

// Ingester ingests events (libs/ingest Service).
type Ingester interface {
	// Create ingests one event.
	Create(ctx context.Context, org core.OrganizationID, in ingest.Input, md ingest.Metadata) (ingest.Outcome, error)
	// CreateBatch ingests events, all or nothing.
	CreateBatch(ctx context.Context, org core.OrganizationID, ins []ingest.Input, md ingest.Metadata) ([]ingest.Outcome, error)
}

// Events serves tanvo.event.v1.EventService: the ingestion RPCs, the
// reads with an [EventReader] and the estimates with a [FeeEstimator].
type Events struct {
	eventv1connect.UnimplementedEventServiceHandler

	ingest    Ingester
	ips       *clientip.Resolver
	reader    EventReader
	estimator FeeEstimator
}

var _ eventv1connect.EventServiceHandler = (*Events)(nil)

// NewEvents returns the event service over in; ips resolves the client
// address recorded in the events' metadata.
func NewEvents(in Ingester, ips *clientip.Resolver) *Events {
	return &Events{ingest: in, ips: ips}
}

// IngestEvent implements EventService.IngestEvent.
func (s *Events) IngestEvent(ctx context.Context, req *eventv1.IngestEventRequest) (*eventv1.IngestEventResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	in, v := toInput("event.", req.GetEvent())
	if len(v) > 0 {
		return nil, InvalidArgument("invalid event", v)
	}
	out, err := s.ingest.Create(ctx, p.OrganizationID, in, s.metadata(ctx))
	if v, ok := validationViolations("event.", err); ok {
		return nil, InvalidArgument("invalid event", v)
	}
	if err != nil {
		return nil, Error(ctx, err)
	}
	return eventv1.IngestEventResponse_builder{Event: toProto(out.Event), Duplicate: &out.Duplicate}.Build(), nil
}

// IngestEvents implements EventService.IngestEvents. The batch is all or
// nothing: an invalid event rejects it with the fields of every invalid
// event ("events[2].code").
func (s *Events) IngestEvents(ctx context.Context, req *eventv1.IngestEventsRequest) (*eventv1.IngestEventsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	events := req.GetEvents()
	ins := make([]ingest.Input, len(events))
	var violations []FieldViolation
	for i, e := range events {
		in, v := toInput(fmt.Sprintf("events[%d].", i), e)
		ins[i], violations = in, append(violations, v...)
	}
	if len(violations) > 0 {
		return nil, InvalidArgument("invalid events", violations)
	}
	out, err := s.ingest.CreateBatch(ctx, p.OrganizationID, ins, s.metadata(ctx))
	var be *ingest.BatchError
	if errors.As(err, &be) {
		for _, i := range slices.Sorted(maps.Keys(be.Errors)) {
			v, ok := validationViolations(fmt.Sprintf("events[%d].", i), be.Errors[i])
			if !ok {
				v = []FieldViolation{{Field: fmt.Sprintf("events[%d]", i), Reason: core.CodeInvalid, Description: be.Errors[i].Error()}}
			}
			violations = append(violations, v...)
		}
		return nil, InvalidArgument("invalid events", violations)
	}
	if err != nil {
		return nil, Error(ctx, err)
	}
	results := make([]*eventv1.EventResult, len(out))
	for i, o := range out {
		results[i] = eventv1.EventResult_builder{
			TransactionId: &o.Event.TransactionID,
			Event:         toProto(o.Event),
			Duplicate:     &o.Duplicate,
		}.Build()
	}
	return eventv1.IngestEventsResponse_builder{Results: results}.Build(), nil
}

func principal(ctx context.Context) (auth.Principal, error) {
	p, ok := Principal(ctx)
	if !ok || p.OrganizationID.IsZero() {
		return auth.Principal{}, connect.NewError(connect.CodeUnauthenticated, "unauthenticated")
	}
	return p, nil
}

// metadata returns the request metadata of an event request: the user
// agent and the client address, resolved like Rails' request.remote_ip
// (events_controller.rb event_metadata).
func (s *Events) metadata(ctx context.Context) ingest.Metadata {
	info, ok := connect.CallInfoForServerContext(ctx)
	if !ok {
		return ingest.Metadata{}
	}
	h := info.RequestHeader()
	return ingest.Metadata{UserAgent: h.Get("User-Agent"), IPAddress: s.ips.Resolve(info.PeerAddr, h)}
}

// toInput maps an API event to the intake's input. The numbers of JSON
// clients' properties arrive as strings of their exact decimal text
// ([JSONCodec]); the Struct numbers left are the doubles of binary
// clients, passed on in their shortest decimal form.
func toInput(prefix string, e *eventv1.Event) (ingest.Input, []FieldViolation) {
	in := ingest.Input{
		TransactionID:          e.GetTransactionId(),
		Code:                   e.GetCode(),
		ExternalSubscriptionID: e.GetExternalSubscriptionId(),
		ExternalContractID:     e.GetExternalContractId(),
	}
	var violations []FieldViolation
	if e.HasTimestamp() {
		in.Timestamp = e.GetTimestamp().AsTime().UTC().Format(time.RFC3339Nano)
	}
	if e.HasPreciseTotalAmount() {
		in.PreciseTotalAmountCents = e.GetPreciseTotalAmount()
	}
	props := map[string]any{}
	for k, v := range e.GetProperties().GetFields() {
		switch x := v.GetKind().(type) {
		case *structpb.Value_NumberValue:
			props[k] = json.Number(strconv.FormatFloat(x.NumberValue, 'f', -1, 64))
		case *structpb.Value_StringValue:
			props[k] = x.StringValue
		case *structpb.Value_BoolValue:
			props[k] = x.BoolValue
		case *structpb.Value_NullValue:
			props[k] = nil
		default:
			violations = append(violations, FieldViolation{Field: prefix + "properties." + k, Reason: core.CodeInvalid, Description: "properties are strings, numbers, booleans or nulls"})
		}
	}
	if len(props) > 0 {
		in.Properties = props
	}
	return in, violations
}

func toProto(e *event.Event) *eventv1.Event {
	if e == nil {
		return nil
	}
	b := eventv1.Event_builder{
		TransactionId:          &e.TransactionID,
		ExternalSubscriptionId: &e.ExternalSubscriptionID,
		Code:                   &e.Code,
		Timestamp:              timestamppb.New(e.Timestamp),
	}
	// The ClickHouse ledger keeps no event ID.
	if !e.ID.IsZero() {
		b.Id = new(e.ID.String())
	}
	if len(e.Properties) > 0 {
		fields := make(map[string]*structpb.Value, len(e.Properties))
		for k, v := range e.Properties {
			fields[k] = structpb.NewStringValue(v)
		}
		b.Properties = &structpb.Struct{Fields: fields}
	}
	if code := e.Properties[catalog.EventTargetWalletCode]; code != "" {
		b.TargetWalletCode = &code
	}
	if e.PreciseTotalAmount != nil {
		s := e.PreciseTotalAmount.String()
		b.PreciseTotalAmount = &s
	}
	if !e.IngestedAt.IsZero() {
		b.CreatedAt = timestamppb.New(e.IngestedAt)
	}
	if md := e.Metadata; md.UserAgent != "" {
		b.UserAgent = &md.UserAgent
	}
	if md := e.Metadata; md.IPAddress != "" {
		b.IpAddress = &md.IPAddress
	}
	return b.Build()
}
