package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"slices"
	"strconv"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/metering"
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

// Events serves tanvo.event.v1.EventService: the ingestion RPCs. The
// other RPCs answer Unimplemented.
type Events struct {
	eventv1connect.UnimplementedEventServiceHandler

	ingest Ingester
}

var _ eventv1connect.EventServiceHandler = (*Events)(nil)

// NewEvents returns the event service over in.
func NewEvents(in Ingester) *Events { return &Events{ingest: in} }

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
	out, err := s.ingest.Create(ctx, p.OrganizationID, in, metadata(ctx))
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
	out, err := s.ingest.CreateBatch(ctx, p.OrganizationID, ins, metadata(ctx))
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

func metadata(ctx context.Context) ingest.Metadata {
	info, ok := connect.CallInfoForServerContext(ctx)
	if !ok {
		return ingest.Metadata{}
	}
	md := ingest.Metadata{UserAgent: info.RequestHeader().Get("User-Agent"), IPAddress: info.PeerAddr}
	if host, _, err := net.SplitHostPort(info.PeerAddr); err == nil {
		md.IPAddress = host
	}
	return md
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
	if e.HasTargetWalletCode() {
		props[metering.TargetWalletCodeProperty] = e.GetTargetWalletCode()
	}
	if len(props) > 0 {
		in.Properties = props
	}
	if len(e.GetAttributions()) > 0 {
		violations = append(violations, FieldViolation{Field: prefix + "attributions", Reason: core.CodeForbidden, Description: "attribution labels are not accepted by this server"})
	}
	return in, violations
}

func toProto(e *event.Event) *eventv1.Event {
	if e == nil {
		return nil
	}
	id := e.ID.String()
	b := eventv1.Event_builder{
		Id:                     &id,
		TransactionId:          &e.TransactionID,
		ExternalSubscriptionId: &e.ExternalSubscriptionID,
		Code:                   &e.Code,
		Timestamp:              timestamppb.New(e.Timestamp),
		Attributions:           e.AttributionLabels,
	}
	if len(e.Properties) > 0 {
		fields := make(map[string]*structpb.Value, len(e.Properties))
		for k, v := range e.Properties {
			fields[k] = structpb.NewStringValue(v)
		}
		b.Properties = &structpb.Struct{Fields: fields}
	}
	if e.PreciseTotalAmount != nil {
		s := e.PreciseTotalAmount.String()
		b.PreciseTotalAmount = &s
	}
	if !e.IngestedAt.IsZero() {
		b.CreatedAt = timestamppb.New(e.IngestedAt)
	}
	return b.Build()
}
