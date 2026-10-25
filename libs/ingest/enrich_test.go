package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	expression "github.com/tanvoxyz/tanvo/libs/expression/go"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func testEvent(code string, props map[string]string) *event.Event {
	return &event.Event{
		ID:                     core.NewID[core.EventID](testIDs),
		OrganizationID:         testOrg,
		TransactionID:          "tx",
		Code:                   code,
		ExternalSubscriptionID: "sub",
		Timestamp:              testNow.Add(-time.Minute),
		Properties:             props,
		IngestedAt:             testNow,
	}
}

func strp(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// source: spec/services/events/enrich_service_spec.rb
func TestEnrich(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		name, source string
		agg          catalog.AggregationType
		props        map[string]string
		chargeProps  string
		filterProps  string
		accepts      bool
		wantValue    string
		wantDecimal  string
		wantOp       event.OperationType
		wantGroups   map[string]string // "<nil>" for nil
		wantWallet   string
	}{
		{name: "creates an enriched event", source: "spec/services/events/enrich_service_spec.rb:32", agg: catalog.AggregationTypeSum, props: map[string]string{"item_id": "12"}, wantValue: "12", wantDecimal: "12"},
		{name: "count aggregation", source: "spec/services/events/enrich_service_spec.rb:121", agg: catalog.AggregationTypeCount, props: map[string]string{"item_id": "12"}, wantValue: "1", wantDecimal: "1"},
		{name: "missing property", source: "spec/services/events/enrich_service_spec.rb:138", agg: catalog.AggregationTypeSum, props: map[string]string{}, wantValue: "0", wantDecimal: "0"},
		{name: "invalid property", source: "spec/services/events/enrich_service_spec.rb:163", agg: catalog.AggregationTypeSum, props: map[string]string{"item_id": "invalid_value"}, wantValue: "invalid_value", wantDecimal: "0"},
		{name: "unique count", source: "spec/services/events/enrich_service_spec.rb:190", agg: catalog.AggregationTypeUniqueCount, props: map[string]string{"item_id": "foo_bar"}, wantValue: "foo_bar", wantDecimal: "0", wantOp: event.OperationTypeAdd},
		{name: "unique count remove", source: "spec/services/events/enrich_service_spec.rb:216", agg: catalog.AggregationTypeUniqueCount, props: map[string]string{"item_id": "foo_bar", "operation_type": "remove"}, wantValue: "foo_bar", wantDecimal: "0", wantOp: event.OperationTypeRemove},
		{name: "unique count unknown operation", source: "spec/services/events/enrich_service_spec.rb:242", agg: catalog.AggregationTypeUniqueCount, props: map[string]string{"item_id": "foo_bar", "operation_type": "invalid"}, wantValue: "foo_bar", wantDecimal: "0", wantOp: ""},
		{
			name: "pricing group keys", source: "spec/services/events/enrich_service_spec.rb:269", agg: catalog.AggregationTypeSum,
			props: map[string]string{"item_id": "12", "cloud": "aws", "provider": "visa"}, chargeProps: `{"amount":"120","pricing_group_keys":["cloud","provider"]}`,
			wantValue: "12", wantDecimal: "12", wantGroups: map[string]string{"cloud": "aws", "provider": "visa"},
		},
		{
			name: "missing group keys", source: "spec/services/events/enrich_service_spec.rb:296", agg: catalog.AggregationTypeSum,
			props: map[string]string{"item_id": "12"}, chargeProps: `{"amount":"120","pricing_group_keys":["cloud","provider"]}`,
			wantValue: "12", wantDecimal: "12", wantGroups: map[string]string{"cloud": "<nil>", "provider": "<nil>"},
		},
		{
			name: "filter pricing group keys", source: "spec/services/events/enrich_service_spec.rb:453", agg: catalog.AggregationTypeSum,
			props: map[string]string{"item_id": "12", "region": "eu", "cloud": "aws"}, filterProps: `{"amount":"120","pricing_group_keys":["cloud","provider"]}`,
			wantValue: "12", wantDecimal: "12", wantGroups: map[string]string{"cloud": "aws", "provider": "<nil>"},
		},
		{
			name: "target wallet code", source: "spec/services/events/enrich_service_spec.rb:339", agg: catalog.AggregationTypeSum,
			props: map[string]string{"item_id": "12", "target_wallet_code": "wallet1234"}, accepts: true,
			wantValue: "12", wantDecimal: "12", wantGroups: map[string]string{"target_wallet_code": "wallet1234"}, wantWallet: "wallet1234",
		},
		{
			name: "no target wallet code", source: "spec/services/events/enrich_service_spec.rb:364", agg: catalog.AggregationTypeSum,
			props: map[string]string{"item_id": "12", "region": "eu"}, accepts: true,
			wantValue: "12", wantDecimal: "12",
		},
		{
			name: "target wallet code and pricing group keys", source: "spec/services/events/enrich_service_spec.rb:402", agg: catalog.AggregationTypeSum,
			props:       map[string]string{"item_id": "12", "cloud": "aws", "provider": "visa", "target_wallet_code": "wallet123"},
			chargeProps: `{"amount":"120","pricing_group_keys":["cloud","provider"]}`, accepts: true,
			wantValue: "12", wantDecimal: "12", wantGroups: map[string]string{"cloud": "aws", "provider": "visa", "target_wallet_code": "wallet123"}, wantWallet: "wallet123",
		},
		{
			name: "target wallet code on a charge not accepting one", source: "tanvo", agg: catalog.AggregationTypeSum,
			props:     map[string]string{"item_id": "12", "target_wallet_code": "wallet1234"},
			wantValue: "12", wantDecimal: "12",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cat := &fakeCatalog{}
			m := cat.metric("bm", tc.agg, "item_id")
			s := cat.subscription("sub", subscription.StatusActive, testNow.Add(-time.Hour))
			ch := cat.charge(s.PlanID, m, false, tc.chargeProps)
			ch.AcceptsTargetWallet = tc.accepts
			if tc.filterProps != "" {
				ch.Filters = []catalog.ChargeFilter{{Values: map[string][]string{"region": {"eu"}}, Properties: catalog.MustProperties(tc.filterProps)}}
			}
			en := NewEnricher(cat, nil, core.FixedClock(testNow))
			got, err := en.Enrich(ctx, testEvent("bm", tc.props), SourceAPI, false)
			if err != nil {
				t.Fatalf("Enrich() error = %v (%s)", err, tc.source)
			}
			if strp(got.Value) != tc.wantValue || got.DecimalValue.Cmp(money.MustParse(tc.wantDecimal)) != 0 {
				t.Errorf("value got = %s / %s, want = %s / %s (%s)", strp(got.Value), got.DecimalValue, tc.wantValue, tc.wantDecimal, tc.source)
			}
			if got.TargetWalletCode != tc.wantWallet {
				t.Errorf("target_wallet_code got = %q, want = %q (%s)", got.TargetWalletCode, tc.wantWallet, tc.source)
			}
			if got.OperationType != tc.wantOp {
				t.Errorf("operation got = %q, want = %q (%s)", got.OperationType, tc.wantOp, tc.source)
			}
			if len(got.GroupedBy) != len(tc.wantGroups) {
				t.Errorf("grouped_by got = %v, want = %v (%s)", got.GroupedBy, tc.wantGroups, tc.source)
			}
			for k, v := range tc.wantGroups {
				if strp(got.GroupedBy[k]) != v {
					t.Errorf("grouped_by[%s] got = %s, want = %s (%s)", k, strp(got.GroupedBy[k]), v, tc.source)
				}
			}
			if got.SubscriptionID != s.ID || got.PlanID != s.PlanID || got.CustomerID != s.CustomerID || got.BillableMetricID != m.ID {
				t.Errorf("resolution got = %+v (%s)", got, tc.source)
			}
			// source: spec/services/events/enrich_service_spec.rb:59
			if !got.EnrichedAt.Equal(testNow.Truncate(time.Microsecond)) {
				t.Errorf("enriched_at got = %v, want = %v (sub-second precision)", got.EnrichedAt, testNow)
			}
		})
	}
}

func TestEnrichResolution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// source: spec/services/events/enrich_service_spec.rb:96
	t.Run("precise amount", func(t *testing.T) {
		t.Parallel()
		cat := &fakeCatalog{}
		cat.metric("bm", catalog.AggregationTypeSum, "v")
		e := testEvent("bm", nil)
		d := money.MustParse("12.34")
		e.PreciseTotalAmount = &d
		got, err := NewEnricher(cat, nil, nil).Enrich(ctx, e, SourceAPI, false)
		if err != nil || got.PreciseTotalAmountCents == nil || got.PreciseTotalAmountCents.String() != "12.34" {
			t.Errorf("Enrich() got = %v, %v, want = 12.34", got, err)
		}
		if got.Metering().PreciseTotalAmountCents.String() != "12.34" {
			t.Errorf("Metering() amount got = %v, want = 12.34", got.Metering().PreciseTotalAmountCents)
		}
	})

	// source: events-processor/processors/events_processor/enrichment_service_test.go:64
	// (Tanvo stores the event without a metric; post-validation reports it)
	t.Run("without billable metric", func(t *testing.T) {
		t.Parallel()
		cat := &fakeCatalog{}
		cat.subscription("sub", subscription.StatusActive, testNow.Add(-time.Hour))
		got, err := NewEnricher(cat, nil, nil).Enrich(ctx, testEvent("unknown", nil), SourceAPI, false)
		if err != nil {
			t.Fatalf("Enrich() error = %v", err)
		}
		if !got.BillableMetricID.IsZero() || got.Value != nil || got.SubscriptionID.IsZero() || got.InAdvance {
			t.Errorf("Enrich() got = %+v, want = a subscription and no metric", got)
		}
	})

	// source: events-processor/processors/events_processor/enrichment_service_test.go:118
	t.Run("expression failed to evaluate", func(t *testing.T) {
		t.Parallel()
		cat := &fakeCatalog{}
		m := cat.metric("bm", catalog.AggregationTypeWeightedSum, "api_requests")
		m.Expression = ptr("round(event.properties.value)")
		_, err := NewEnricher(cat, nil, nil).Enrich(ctx, testEvent("bm", nil), "sqs", true)
		var xe *ExpressionError
		if !errors.As(err, &xe) {
			t.Errorf("Enrich() error got = %v, want = *ExpressionError", err)
		}
	})

	recurring := []struct {
		name, source string
		recurring    bool
		want         bool
	}{
		{"recurring falls back on the active subscription", "events-processor/processors/events_processor/enrichment_service_test.go:149", true, true},
		{"non-recurring does not fall back", "events-processor/processors/events_processor/enrichment_service_test.go:197", false, false},
	}
	for _, tc := range recurring {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cat := &fakeCatalog{}
			m := cat.metric("bm", catalog.AggregationTypeCount, "")
			m.Recurring = tc.recurring
			s := cat.subscription("sub", subscription.StatusActive, time.Unix(1751000000, 0).UTC())
			e := testEvent("bm", nil)
			e.Timestamp = time.Unix(1741007009, 0).UTC()
			got, err := NewEnricher(cat, nil, nil).Enrich(ctx, e, SourceAPI, false)
			if err != nil {
				t.Fatalf("Enrich() error = %v", err)
			}
			if matched := got.SubscriptionID == s.ID; matched != tc.want {
				t.Errorf("matched got = %v, want = %v (%s)", matched, tc.want, tc.source)
			}
		})
	}

	inAdvance := []struct {
		name, source string
		sub, charge  bool
		inAdvance    bool
		want         bool
	}{
		{"without plan", "events-processor/processors/events_processor/enrichment_service_test.go:323", false, true, true, false},
		{"with a charge charged in advance", "events-processor/processors/events_processor/enrichment_service_test.go:337", true, true, true, true},
		{"without any charge charged in advance", "events-processor/processors/events_processor/enrichment_service_test.go:362", true, true, false, false},
		{"without charge", "", true, false, false, false},
	}
	for _, tc := range inAdvance {
		t.Run("in advance "+tc.name, func(t *testing.T) {
			t.Parallel()
			cat := &fakeCatalog{}
			m := cat.metric("bm", catalog.AggregationTypeCount, "")
			plan := core.NewID[core.PlanID](testIDs)
			if tc.sub {
				plan = cat.subscription("sub", subscription.StatusActive, testNow.Add(-time.Hour)).PlanID
			}
			if tc.charge {
				cat.charge(plan, m, tc.inAdvance, "")
				cat.charge(plan, m, false, "")
			}
			got, err := NewEnricher(cat, nil, nil).Enrich(ctx, testEvent("bm", nil), SourceAPI, false)
			if err != nil || got.InAdvance != tc.want {
				t.Errorf("in advance got = %v, %v, want = %v (%s)", got.InAdvance, err, tc.want, tc.source)
			}
		})
	}

	t.Run("catalog failure", func(t *testing.T) {
		t.Parallel()
		cat := &fakeCatalog{err: errors.New("down")}
		if _, err := NewEnricher(cat, nil, nil).Enrich(ctx, testEvent("bm", nil), SourceAPI, false); err == nil {
			t.Error("Enrich() error = nil, want = an error")
		}
	})

	t.Run("does not modify the event", func(t *testing.T) {
		t.Parallel()
		cat := &fakeCatalog{}
		m := cat.metric("bm", catalog.AggregationTypeSum, "r")
		m.Expression = ptr("event.properties.a * 2")
		e := testEvent("bm", map[string]string{"a": "2"})
		got, err := NewEnricher(cat, nil, nil).Enrich(ctx, e, "sqs", true)
		if err != nil || got.Properties["r"] != "4" || strp(got.Value) != "4" {
			t.Fatalf("Enrich() got = %v, %v, want = r=4", got, err)
		}
		if _, ok := e.Properties["r"]; ok {
			t.Error("event properties got = modified, want = unchanged")
		}
	})

	t.Run("JSON round trip", func(t *testing.T) {
		t.Parallel()
		cat := &fakeCatalog{}
		m := cat.metric("bm", catalog.AggregationTypeSum, "v")
		s := cat.subscription("sub", subscription.StatusActive, testNow.Add(-time.Hour))
		cat.charge(s.PlanID, m, true, `{"pricing_group_keys":["g"]}`)
		got, err := NewEnricher(cat, nil, nil).Enrich(ctx, testEvent("bm", map[string]string{"v": "1.5"}), SourceAPI, false)
		if err != nil {
			t.Fatalf("Enrich() error = %v", err)
		}
		b, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		back, err := DecodeEnriched(b)
		if err != nil {
			t.Fatalf("DecodeEnriched() error = %v", err)
		}
		b2, _ := json.Marshal(back)
		if string(b) != string(b2) {
			t.Errorf("round trip got = %s, want = %s", b2, b)
		}
	})
}

// source: spec/services/events/calculate_expression_service_spec.rb
// source: events-processor/processors/events_processor/enrichment_service_test.go:252
func TestProgramsApply(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		name, source string
		expression   string
		props        map[string]string
		ts           time.Time
		want         string // "" for no change
		wantErr      bool
	}{
		{"no expression", "spec/services/events/calculate_expression_service_spec.rb:21", "", map[string]string{"left": "1", "right": "2"}, testNow, "", false},
		{"expression", "spec/services/events/calculate_expression_service_spec.rb:28", "event.properties.left + event.properties.right", map[string]string{"left": "1", "right": "2"}, testNow, "3", false},
		{"without required fields", "events-processor/processors/events_processor/enrichment_service_test.go:265", "round(event.properties.value * event.properties.units)", nil, testNow, "", true},
		{"with required fields", "events-processor/processors/events_processor/enrichment_service_test.go:278", "round(event.properties.value * event.properties.units)", map[string]string{"value": "12.0", "units": "3"}, time.Unix(1741007009, 0), "36", false},
		{"float timestamp", "events-processor/processors/events_processor/enrichment_service_test.go:289", "round(event.properties.value * event.properties.units)", map[string]string{"value": "12.0", "units": "3"}, time.Unix(1741007009, 123e6), "36", false},
		{"syntax error", "", "event.properties.(", map[string]string{}, testNow, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := &catalog.BillableMetric{Code: "bm", FieldName: ptr("result")}
			if tc.expression != "" {
				m.Expression = ptr(tc.expression)
			}
			e := testEvent("bm", tc.props)
			e.Timestamp = tc.ts
			err := NewPrograms(nil).Apply(ctx, m, e)
			var xe *ExpressionError
			if tc.wantErr != errors.As(err, &xe) {
				t.Fatalf("Apply() error got = %v, want error = %v (%s)", err, tc.wantErr, tc.source)
			}
			if got := e.Properties["result"]; got != tc.want {
				t.Errorf("result got = %q, want = %q (%s)", got, tc.want, tc.source)
			}
		})
	}
}

// An event no subscription matches is flagged for in-advance billing when
// a rate card of the active contract with its external ID bills its
// metric in advance.
//
// source: spec/services/events/post_process_service_spec.rb:130-227, app/services/events/post_process_service.rb:118-129
func TestEnrichContract(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		source   string
		contract bool
		subs     bool
		want     bool
	}{
		{name: "flags the event of an in-advance contract", source: "spec/services/events/post_process_service_spec.rb:156", contract: true, want: true},
		{name: "leaves the event of a contract billing nothing in advance", source: "spec/services/events/post_process_service_spec.rb:225"},
		{name: "keeps the matched subscription", source: "tanvo", contract: true, subs: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cat := &fakeCatalog{contracts: map[contractKey]core.SubscriptionID{}}
			m := cat.metric("bm", catalog.AggregationTypeCount, "")
			ct := core.NewID[core.SubscriptionID](testIDs)
			if tc.contract {
				cat.contracts[contractKey{testOrg, "sub", m.ID}] = ct
			}
			var s *subscription.Subscription
			if tc.subs {
				s = cat.subscription("sub", subscription.StatusActive, testNow.Add(-time.Hour))
			}
			got, err := NewEnricher(cat, nil, core.FixedClock(testNow)).Enrich(ctx, testEvent("bm", nil), SourceAPI, false)
			if err != nil {
				t.Fatalf("Enrich() error = %v (%s)", err, tc.source)
			}
			wantTarget := core.SubscriptionID{}
			switch {
			case tc.subs:
				wantTarget = s.ID
			case tc.want:
				wantTarget = ct
			}
			if got.InAdvanceTarget() != wantTarget || (tc.want != (got.ContractID == ct)) || (tc.want && !got.InAdvance) {
				t.Errorf("target, contract, in advance got = %s, %s, %t, want = %s, contract %t (%s)", got.InAdvanceTarget(), got.ContractID, got.InAdvance, wantTarget, tc.want, tc.source)
			}
		})
	}
}

// TestBigDecimalText checks that the event API writes an expression's
// number as lago-api's BigDecimal is written in JSON.
//
// source: spec/services/events/create_service_spec.rb:222
// source: spec/requests/api/v1/events_controller_spec.rb:144
func TestBigDecimalText(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		in   expression.Value
		want string
	}{
		{expression.NumberValue(money.MustParse("3")), "3.0"},
		{expression.NumberValue(money.MustParse("1.50")), "1.5"},
		{expression.NumberValue(money.MustParse("12.177")), "12.177"},
		{expression.NumberValue(money.MustParse("-2")), "-2.0"},
		{expression.NumberValue(money.MustParse("100")), "100.0"},
		{expression.NumberValue(money.MustParse("1E+2")), "100.0"},
		{expression.StringValue("3"), "3"},
		{expression.StringValue("1.23test"), "1.23test"},
	} {
		if got := bigDecimalText(tt.in); got != tt.want {
			t.Errorf("bigDecimalText(%v) got = %q, want = %q", tt.in, got, tt.want)
		}
	}
}
