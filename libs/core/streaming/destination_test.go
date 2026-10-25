package streaming_test

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/streaming"
)

func kinesis() *streaming.Destination {
	return &streaming.Destination{
		ID:             core.MustParseID[core.StreamingDestinationID]("0190f0a0-0000-7000-8000-000000000001"),
		OrganizationID: core.MustParseID[core.OrganizationID]("0190f0a0-0000-7000-8000-000000000002"),
		Type:           streaming.TypeKinesis,
		Active:         true,
		EventTypes:     streaming.EventTypes(),
		Kinesis: streaming.KinesisSettings{
			StreamARN: "arn:aws:kinesis:eu-west-1:123456789012:stream/tanvo-streaming-sandbox",
			Region:    "eu-west-1",
			RoleARN:   "arn:aws:iam::123456789012:role/tanvo-streaming-sandbox-writer",
		},
	}
}

// source: spec/models/streaming_destinations/base_destination_spec.rb:11
func TestEventTypesAreVersioned(t *testing.T) {
	re := regexp.MustCompile(`\.v\d+$`)
	for _, et := range streaming.EventTypes() {
		if !re.MatchString(string(et)) {
			t.Errorf("event type %q: got = unversioned, want = a .vN suffix", et)
		}
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		source string
		edit   func(d *streaming.Destination)
		field  string
		code   string
	}{
		{name: "valid", edit: func(*streaming.Destination) {}},
		{
			name: "event types are required", source: "spec/models/streaming_destinations/base_destination_spec.rb:23",
			edit: func(d *streaming.Destination) { d.EventTypes = nil }, field: "event_types", code: core.CodeRequired,
		},
		{
			name: "an empty array is rejected", source: "spec/models/streaming_destinations/base_destination_spec.rb:28",
			edit: func(d *streaming.Destination) { d.EventTypes = []streaming.EventType{} }, field: "event_types", code: core.CodeRequired,
		},
		{
			name: "an unknown event type is rejected", source: "spec/models/streaming_destinations/base_destination_spec.rb:35",
			edit:  func(d *streaming.Destination) { d.EventTypes = []streaming.EventType{"customer_usage.refresed.v1"} },
			field: "event_types", code: core.CodeInvalid,
		},
		{
			name: "the stream ARN is required", source: "spec/models/streaming_destinations/kinesis_destination_spec.rb:9",
			edit: func(d *streaming.Destination) { d.Kinesis.StreamARN = "" }, field: "stream_arn", code: core.CodeRequired,
		},
		{
			name: "the region is required", source: "spec/models/streaming_destinations/kinesis_destination_spec.rb:9",
			edit: func(d *streaming.Destination) { d.Kinesis.Region = "" }, field: "region", code: core.CodeRequired,
		},
		{
			name: "the role ARN is required", source: "spec/models/streaming_destinations/kinesis_destination_spec.rb:9",
			edit: func(d *streaming.Destination) { d.Kinesis.RoleARN = "" }, field: "role_arn", code: core.CodeRequired,
		},
		{
			name: "an unsupported partition key is rejected", source: "spec/models/streaming_destinations/kinesis_destination_spec.rb:15",
			edit: func(d *streaming.Destination) { d.Kinesis.PartitionKey = "subscription_external_id" }, field: "partition_key", code: core.CodeInvalid,
		},
		{
			name: "the type is required",
			edit: func(d *streaming.Destination) { d.Type = "" }, field: "type", code: core.CodeRequired,
		},
		{
			name: "an unknown type is rejected",
			edit: func(d *streaming.Destination) { d.Type = "kafka" }, field: "type", code: core.CodeInvalid,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := kinesis()
			tt.edit(d)
			err := d.Validate()
			if tt.field == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want = nil", err)
				}
				return
			}
			var ve *core.ValidationError
			if !errors.As(err, &ve) || !ve.Has(tt.field, tt.code) {
				t.Fatalf("Validate() = %v, want = %s: %s", err, tt.field, tt.code)
			}
		})
	}
}

// source: spec/models/streaming_destinations/kinesis_destination_spec.rb:70
func TestDefaultPartitionKey(t *testing.T) {
	d := kinesis()
	if got := d.Kinesis.EffectivePartitionKey(); got != streaming.DefaultPartitionKey {
		t.Errorf("EffectivePartitionKey() = %q, want = %q", got, streaming.DefaultPartitionKey)
	}
	d.Normalize()
	if got := d.Kinesis.PartitionKey; got != streaming.PartitionKeyCustomerExternalID {
		t.Errorf("after Normalize: got = %q, want = %q", got, streaming.PartitionKeyCustomerExternalID)
	}
}

func TestEventTypesFor(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		excluded []string
		want     []streaming.EventType
	}{
		{
			name: "every subscribed type when no plan is excluded", source: "spec/models/streaming_destinations/base_destination_spec.rb:89",
			want: streaming.EventTypes(),
		},
		{
			name: "drops the full usage type for an excluded plan", source: "spec/models/streaming_destinations/base_destination_spec.rb:93",
			excluded: []string{"self_serve_monthly"}, want: []streaming.EventType{streaming.EventCustomerUsageRefreshed},
		},
		{
			name: "keeps the full usage type for a plan that is not excluded", source: "spec/models/streaming_destinations/base_destination_spec.rb:99",
			excluded: []string{"another_plan"}, want: streaming.EventTypes(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := kinesis()
			d.CustomerFullUsageExcludedPlanCodes = tt.excluded
			if got := d.EventTypesFor("self_serve_monthly"); !slices.Equal(got, tt.want) {
				t.Errorf("EventTypesFor() = %v, want = %v", got, tt.want)
			}
			if !slices.Equal(d.EventTypes, streaming.EventTypes()) {
				t.Errorf("EventTypes mutated: got = %v, want = %v", d.EventTypes, streaming.EventTypes())
			}
		})
	}
}

func TestPartitionKeyFor(t *testing.T) {
	long := strings.Repeat("x", 400)
	tests := []struct {
		name     string
		source   string
		strategy streaming.PartitionKey
		external string
		want     string
		wantErr  bool
	}{
		{
			name: "resolves the configured strategy", source: "spec/models/streaming_destinations/kinesis_destination_spec.rb:33",
			external: "cust_1", want: "cust_1",
		},
		{
			name: "bounds the key to what Kinesis accepts", source: "spec/models/streaming_destinations/kinesis_destination_spec.rb:37",
			external: long, want: long[:streaming.PartitionKeyMaxLength],
		},
		{
			name:     "counts characters, not bytes",
			external: strings.Repeat("é", 300), want: strings.Repeat("é", streaming.PartitionKeyMaxLength),
		},
		{
			name: "fails on a strategy written past the validation", source: "spec/models/streaming_destinations/kinesis_destination_spec.rb:46",
			strategy: "galaxy", external: "cust_1", wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := kinesis()
			d.Kinesis.PartitionKey = tt.strategy
			got, err := d.PartitionKeyFor(tt.external)
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "galaxy") || !errors.Is(err, core.ErrInvalidValue) {
					t.Fatalf("PartitionKeyFor() error = %v, want = an invalid value naming galaxy", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("PartitionKeyFor() = %q, %v, want = %q, nil", got, err, tt.want)
			}
		})
	}
}
