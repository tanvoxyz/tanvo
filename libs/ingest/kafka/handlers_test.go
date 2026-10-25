package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

var (
	ids = core.UUIDv7Generator{}
	org = core.NewID[core.OrganizationID](ids)
)

type catalogFake struct {
	metric  *catalog.BillableMetric
	sub     *subscription.Subscription
	charges []*catalog.Charge
	err     error
}

func (c *catalogFake) Metric(context.Context, core.OrganizationID, string) (*catalog.BillableMetric, error) {
	return c.metric, c.err
}

func (c *catalogFake) Subscriptions(context.Context, core.OrganizationID, string) ([]*subscription.Subscription, error) {
	if c.sub == nil {
		return nil, c.err
	}
	return []*subscription.Subscription{c.sub}, c.err
}

func (c *catalogFake) Charges(context.Context, core.OrganizationID, core.PlanID, core.BillableMetricID) ([]*catalog.Charge, error) {
	return c.charges, c.err
}

type sinkFake struct {
	mu      sync.Mutex
	events  []*ingest.Enriched
	signals []ingest.UsageSignal
	rows    []metering.Event
	err     error
}

func (s *sinkFake) ProduceEnriched(_ context.Context, e []*ingest.Enriched) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.events = append(s.events, e...)
	return nil
}

func (s *sinkFake) BillInAdvance(ctx context.Context, e []*ingest.Enriched) error {
	return s.ProduceEnriched(ctx, e)
}

func (s *sinkFake) SignalUsage(_ context.Context, sig []ingest.UsageSignal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.signals = append(s.signals, sig...)
	return s.err
}

func (s *sinkFake) Write(_ context.Context, rows []metering.Event) ([]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	s.rows = append(s.rows, rows...)
	return nil, nil
}

func rawRecord(t *testing.T, m ingest.RawMessage) *kgo.Record {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	return &kgo.Record{Topic: "events.raw", Value: b}
}

func raw(source string, evaluated bool, props map[string]string) ingest.RawMessage {
	ts := time.Unix(1741007009, 0).UTC()
	return ingest.RawMessage{
		OrganizationID: org, TransactionID: "tx", Code: "api_calls", ExternalSubscriptionID: "sub_id",
		Timestamp: ts, Properties: props, IngestedAt: ts, Source: source, ExpressionEvaluated: evaluated,
	}
}

func fixture(inAdvance bool, expression string) *catalogFake {
	started := time.Unix(1700000000, 0).UTC()
	m := &catalog.BillableMetric{ID: core.NewID[core.BillableMetricID](ids), OrganizationID: org, Code: "api_calls", AggregationType: catalog.AggregationTypeSum, FieldName: new("api_requests")}
	if expression != "" {
		m.Expression = &expression
	}
	sub := &subscription.Subscription{ID: core.NewID[core.SubscriptionID](ids), OrganizationID: org, ExternalID: "sub_id", PlanID: core.NewID[core.PlanID](ids), CustomerID: core.NewID[core.CustomerID](ids), Status: subscription.StatusActive, StartedAt: &started}
	ch := &catalog.Charge{PlanID: sub.PlanID, BillableMetricID: m.ID, PayInAdvance: inAdvance}
	return &catalogFake{metric: m, sub: sub, charges: []*catalog.Charge{ch}}
}

// source: events-processor/processors/events_processor/processor_test.go
func TestEnrichmentHandler(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		name, source  string
		cat           *catalogFake
		msg           ingest.RawMessage
		body          string
		sinkErr       error
		want          Disposition
		wantCode      string
		wantValue     string
		wantInAdvance bool
		wantSub       bool
	}{
		{
			name: "post processed on API", source: "events-processor/processors/events_processor/processor_test.go:204",
			cat: fixture(false, "event.properties.x"), msg: raw(ingest.SourceAPI, true, map[string]string{"api_requests": "12.0"}),
			want: Done, wantValue: "12.0", wantSub: true,
		},
		{
			name: "invalid event", source: "events-processor/processors/events_processor/processor_test.go:261",
			cat: fixture(false, ""), body: `{"organization_id":"` + org.String() + `","code":"x","timestamp":"2025-03-06 12:00:00"}`,
			want: Dead, wantCode: ingest.ErrorDecode,
		},
		{
			name: "no subscription found", source: "events-processor/processors/events_processor/processor_test.go:293",
			cat: func() *catalogFake { c := fixture(false, ""); c.sub = nil; return c }(), msg: raw("sqs", false, map[string]string{"api_requests": "12"}),
			want: Done, wantValue: "12",
		},
		{
			name: "expression failed to evaluate", source: "events-processor/processors/events_processor/processor_test.go:322",
			cat: fixture(false, "round(event.properties.value)"), msg: raw("sqs", false, nil),
			want: Dead, wantCode: ingest.ErrorExpression,
		},
		{
			name: "in advance charge", source: "events-processor/processors/events_processor/processor_test.go:362",
			cat: fixture(true, "round(event.properties.value * event.properties.units)"), msg: raw("sqs", false, map[string]string{"value": "4", "units": "3"}),
			want: Done, wantValue: "12", wantInAdvance: true, wantSub: true,
		},
		{
			name: "no in advance charge", source: "events-processor/processors/events_processor/processor_test.go:423",
			cat: fixture(false, ""), msg: raw("sqs", false, map[string]string{"api_requests": "12.0"}),
			want: Done, wantValue: "12.0", wantSub: true,
		},
		{
			name: "catalog failure", cat: &catalogFake{err: errors.New("db down")}, msg: raw("sqs", false, nil),
			want: Retry, wantCode: ingest.ErrorCatalog,
		},
		{
			name: "produce failure", source: "events-processor/processors/events_processor/event_producer_service_test.go:35",
			cat: fixture(false, ""), msg: raw("sqs", false, nil), sinkErr: errors.New("broker down"),
			want: Retry, wantCode: ingest.ErrorProduce,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &sinkFake{err: tc.sinkErr}
			h := Enrichment(ingest.NewEnricher(tc.cat, nil, nil), sink, 2)
			r := &kgo.Record{Topic: "events.raw", Value: []byte(tc.body)}
			if tc.body == "" {
				r = rawRecord(t, tc.msg)
			}
			res := h(ctx, []*kgo.Record{r})
			if len(res) != 1 || res[0].Disposition != tc.want || res[0].Code != tc.wantCode {
				t.Fatalf("result got = %+v, want = %v %q (%s)", res, tc.want, tc.wantCode, tc.source)
			}
			if tc.want != Done {
				return
			}
			if len(sink.events) != 1 {
				t.Fatalf("produced got = %d, want = 1", len(sink.events))
			}
			e := sink.events[0]
			if e.Value == nil || *e.Value != tc.wantValue || e.InAdvance != tc.wantInAdvance || !e.SubscriptionID.IsZero() != tc.wantSub {
				t.Errorf("enriched got = %+v, want value %s in advance %v (%s)", e, tc.wantValue, tc.wantInAdvance, tc.source)
			}
			if tc.wantSub && e.SubscriptionID != tc.cat.sub.ID {
				t.Errorf("subscription got = %v, want = %v", e.SubscriptionID, tc.cat.sub.ID)
			}
		})
	}
}

func enrichedRecord(t *testing.T, e *ingest.Enriched) *kgo.Record {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	return &kgo.Record{Topic: "events.enriched", Value: b}
}

func enriched(tx string, inAdvance bool) *ingest.Enriched {
	return &ingest.Enriched{
		OrganizationID: org, TransactionID: tx, Code: "c", ExternalSubscriptionID: "s",
		Timestamp: time.Unix(1741007009, 0).UTC(), IngestedAt: time.Unix(1741007010, 0).UTC(),
		SubscriptionID: core.NewID[core.SubscriptionID](ids), CustomerID: core.NewID[core.CustomerID](ids), InAdvance: inAdvance,
	}
}

func TestWriterHandler(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	recs := []*kgo.Record{enrichedRecord(t, enriched("a", false)), {Value: []byte("garbage")}, enrichedRecord(t, enriched("b", true))}

	sink := &sinkFake{}
	res := Writer(sink, sink, nil)(ctx, recs)
	if res[0].Disposition != Done || res[1].Disposition != Dead || res[2].Disposition != Done {
		t.Errorf("results got = %+v, want = done, dead, done", res)
	}
	if len(sink.rows) != 2 || sink.rows[0].TransactionID != "a" || sink.rows[0].SubscriptionID != "s" {
		t.Errorf("rows got = %+v, want = a and b", sink.rows)
	}
	// source: events-processor/processors/events_processor/subscription_refresh_service_test.go:24
	if len(sink.signals) != 2 {
		t.Errorf("signals got = %d, want = 2", len(sink.signals))
	}

	failing := &sinkFake{err: errors.New("clickhouse down")}
	res = Writer(failing, nil, nil)(ctx, recs)
	if res[0].Disposition != Retry || res[0].Code != ingest.ErrorWrite || res[1].Disposition != Dead || res[2].Disposition != Retry {
		t.Errorf("failed write results got = %+v, want = retry, dead, retry", res)
	}
	if res[0].IngestedAt.IsZero() {
		t.Error("IngestedAt got = zero, want = the event's")
	}
}

// source: events-processor/processors/events_processor/event_producer_service_test.go:58
func TestInAdvanceHandler(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	noSub := enriched("c", true)
	noSub.SubscriptionID = core.SubscriptionID{}
	recs := []*kgo.Record{enrichedRecord(t, enriched("a", false)), enrichedRecord(t, enriched("b", true)), enrichedRecord(t, noSub)}

	sink := &sinkFake{}
	res := InAdvance(sink)(ctx, recs)
	for i, r := range res {
		if r.Disposition != Done {
			t.Errorf("result %d got = %+v, want = done", i, r)
		}
	}
	if len(sink.events) != 1 || sink.events[0].TransactionID != "b" {
		t.Errorf("billed got = %+v, want = b", sink.events)
	}

	res = InAdvance(&sinkFake{err: errors.New("billing down")})(ctx, recs)
	if res[0].Disposition != Done || res[1].Disposition != Retry || res[1].Code != ingest.ErrorInAdvance || res[2].Disposition != Done {
		t.Errorf("failed billing results got = %+v, want = done, retry, done", res)
	}
}

// deadSinkFake records the dead letters stored.
type deadSinkFake struct {
	records []ingest.DeadLetterRecord
	err     error
}

func (s *deadSinkFake) StoreDeadLetters(_ context.Context, r []ingest.DeadLetterRecord) error {
	if s.err != nil {
		return s.err
	}
	s.records = append(s.records, r...)
	return nil
}

func TestDeadLetters(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 3, 6, 12, 0, 0, 0, time.UTC)
	letter, err := ingest.MarshalDeadLetter(ingest.DeadLetter{
		Event: json.RawMessage(`{"organization_id":"o","transaction_id":"t"}`), Topic: "events.raw", Offset: 7, ErrorCode: ingest.ErrorDecode, FailedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	recs := []*kgo.Record{{Value: letter}, {Value: []byte("not a dead letter")}}

	t.Run("stores", func(t *testing.T) {
		t.Parallel()
		sink := &deadSinkFake{}
		results := DeadLetters(sink, core.FixedClock(now), slog.New(slog.DiscardHandler))(context.Background(), recs)
		if results[0].Disposition != Done || results[1].Disposition != Done {
			t.Errorf("dispositions got = %v, %v, want = done, done", results[0].Disposition, results[1].Disposition)
		}
		if len(sink.records) != 1 || sink.records[0].TransactionID != "t" || sink.records[0].Offset != 7 || sink.records[0].Topic != "events.raw" {
			t.Errorf("records got = %+v, want = t from events.raw at 7", sink.records)
		}
	})
	t.Run("retries a failed store without aging", func(t *testing.T) {
		t.Parallel()
		sink := &deadSinkFake{err: errors.New("down")}
		results := DeadLetters(sink, core.FixedClock(now), slog.New(slog.DiscardHandler))(context.Background(), recs)
		if r := results[0]; r.Disposition != Retry || r.Code != ingest.ErrorWrite || !r.IngestedAt.Equal(now) {
			t.Errorf("result got = %+v, want = retry, %s, ingested now", r, ingest.ErrorWrite)
		}
		if results[1].Disposition != Done {
			t.Errorf("skipped message got = %v, want = done", results[1].Disposition)
		}
	})
}
