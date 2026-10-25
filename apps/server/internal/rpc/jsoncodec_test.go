package rpc_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2/connectproto"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	eventv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/event/v1"
)

func TestJSONCodecKeepsPropertyNumbersExact(t *testing.T) {
	tests := []struct {
		name string
		json string
		want any // string: the exact text; float64: left to the double
	}{
		{"large integer", `12345678901234567890`, "12345678901234567890"},
		{"long fraction", `0.12345678901234567890123`, "0.12345678901234567890123"},
		{"negative", `-9007199254740993`, "-9007199254740993"},
		{"exponent", `1e3`, "1000"},
		{"negative exponent", `25E-2`, "0.25"},
		{"negative zero", `-0`, "0"},
		{"small integer", `12`, "12"},
		{"exponent beyond the bound", `1e1001`, nil},
		{"string", `"12345678901234567890"`, "12345678901234567890"},
		{"bool", `true`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"event":{"transaction_id":"tx","code":"c","properties":{"v": ` + tt.json + ` ,"region":"eu"}}}`
			var req eventv1.IngestEventRequest
			err := rpc.NewJSONCodec().UnmarshalRead(context.Background(), strings.NewReader(body), &req)
			if tt.want == nil {
				// The double decoding rejects a number it cannot hold.
				if err == nil {
					t.Errorf("UnmarshalRead() error = nil, want = out of range, got = %v", req.GetEvent().GetProperties())
				}
				return
			}
			if err != nil {
				t.Fatalf("UnmarshalRead() error = %v", err)
			}
			props := req.GetEvent().GetProperties().GetFields()
			if got := props["v"].AsInterface(); got != tt.want {
				t.Errorf("properties.v got = %#v, want = %#v", got, tt.want)
			}
			if got := props["region"].GetStringValue(); got != "eu" {
				t.Errorf("properties.region got = %q, want = %q", got, "eu")
			}
		})
	}
}

func TestRESTCodecKeepsPropertyNumbersExact(t *testing.T) {
	codec := rpc.NewRESTCodec(protoregistry.GlobalTypes)
	ctx := context.Background()

	// POST /v1/events maps the body to the event field.
	var one eventv1.IngestEventRequest
	field := one.ProtoReflect().Descriptor().Fields().ByName("event")
	if err := codec.UnmarshalField(ctx, []byte(`{"code":"c","properties":{"bytes":12345678901234567890}}`), &one, field); err != nil {
		t.Fatalf("UnmarshalField() error = %v", err)
	}
	if got := one.GetEvent().GetProperties().GetFields()["bytes"].GetStringValue(); got != "12345678901234567890" {
		t.Errorf("event.properties.bytes got = %q, want = %q", got, "12345678901234567890")
	}

	// POST /v1/events:batch maps the body to the whole request, with JSON
	// names accepted as protojson does.
	var batch eventv1.IngestEventsRequest
	body := `{"events":[{"code":"a","properties":{"n":1}},{"code":"b"},{"code":"c","properties":{"n":0.10000000000000000001}}]}`
	if err := codec.UnmarshalRead(ctx, strings.NewReader(body), &batch); err != nil {
		t.Fatalf("UnmarshalRead() error = %v", err)
	}
	var got []string
	for _, e := range batch.GetEvents() {
		got = append(got, e.GetProperties().GetFields()["n"].GetStringValue())
	}
	if want := []string{"1", "", "0.10000000000000000001"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("events[].properties.n got = %q, want = %q", got, want)
	}
}

func TestJSONCodecLeavesOtherJSON(t *testing.T) {
	ctx := context.Background()
	codec := rpc.NewJSONCodec()
	plain := connectproto.NewJSONCodec()
	tests := []struct {
		name string
		msg  func() proto.Message
		body string
	}{
		{"invalid JSON", func() proto.Message { return &eventv1.IngestEventRequest{} }, `{"event":{"properties":{"n":1}`},
		{"repeated key", func() proto.Message { return &eventv1.IngestEventRequest{} }, `{"event":{"code":"a","code":"b"}}`},
		{"unknown field", func() proto.Message { return &eventv1.IngestEventRequest{} }, `{"evt":{"properties":{"n":1}}}`},
		{"nested property object", func() proto.Message { return &eventv1.IngestEventRequest{} }, `{"event":{"properties":{"o":{"n":12345678901234567890}}}}`},
		{"message without events", func() proto.Message { return &structpb.Struct{} }, `{"n":12345678901234567890}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, want := tt.msg(), tt.msg()
			gotErr := codec.UnmarshalRead(ctx, strings.NewReader(tt.body), got)
			wantErr := plain.UnmarshalRead(ctx, strings.NewReader(tt.body), want)
			if (gotErr == nil) != (wantErr == nil) || !proto.Equal(got, want) {
				t.Errorf("UnmarshalRead() got = %v, %v, want = %v, %v", got, gotErr, want, wantErr)
			}
		})
	}
}

// TestJSONCodecUnixTimestamps decodes event timestamps given as Unix
// seconds, as lago reads them (BigDecimal of the number or its string),
// next to the RFC 3339 form of google.protobuf.Timestamp.
func TestJSONCodecUnixTimestamps(t *testing.T) {
	tests := []struct {
		source, name, json, want string
	}{
		{"spec/requests/api/v1/events_controller_spec.rb:22", "integer seconds", `1693842312`, "2023-09-04T15:45:12Z"},
		{"spec/services/events/create_service_spec.rb:146", "fractional seconds", `1693842312.344`, "2023-09-04T15:45:12.344Z"},
		{"spec/services/events/create_service_spec.rb:135", "seconds as a string", `"1693842312.344"`, "2023-09-04T15:45:12.344Z"},
		{"spec/services/events/create_service_spec.rb:157", "repeating float fraction", `1780586634.1`, "2026-06-04T15:23:54.1Z"},
		{"", "RFC 3339", `"2023-09-04T15:45:12.344Z"`, "2023-09-04T15:45:12.344Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.source != "" {
				t.Log("source: " + tt.source)
			}
			for _, decode := range []func(string, *eventv1.IngestEventRequest) error{
				func(body string, req *eventv1.IngestEventRequest) error {
					return rpc.NewJSONCodec().UnmarshalRead(context.Background(), strings.NewReader(`{"event":`+body+`}`), req)
				},
				func(body string, req *eventv1.IngestEventRequest) error {
					field := req.ProtoReflect().Descriptor().Fields().ByName("event")
					return rpc.NewRESTCodec(protoregistry.GlobalTypes).UnmarshalField(context.Background(), []byte(body), req, field)
				},
			} {
				var req eventv1.IngestEventRequest
				if err := decode(`{"code":"c","timestamp":`+tt.json+`}`, &req); err != nil {
					t.Fatalf("decode error = %v", err)
				}
				if got := req.GetEvent().GetTimestamp().AsTime().Format(time.RFC3339Nano); got != tt.want {
					t.Errorf("timestamp got = %s, want = %s", got, tt.want)
				}
			}
		})
	}

	// source: spec/services/events/create_service_spec.rb:179
	var req eventv1.IngestEventRequest
	body := `{"event":{"code":"c","timestamp":"2023-09-04 15:45:12 UTC"}}`
	if err := rpc.NewJSONCodec().UnmarshalRead(context.Background(), strings.NewReader(body), &req); err == nil {
		t.Errorf("a timestamp in another format: got = %v, want = an error", req.GetEvent().GetTimestamp())
	}
}
