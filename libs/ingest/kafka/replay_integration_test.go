package kafka_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/ingest/kafka"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// deadLetterTable stores dead letters in ClickHouse's events_dead_letter
// and reads them back for replays, as apps/server wires it.
type deadLetterTable struct {
	ch *clickhouse.Store
	// failMarks fails MarkReplayed, as a replay stopped after producing.
	failMarks bool
}

func (s deadLetterTable) StoreDeadLetters(ctx context.Context, records []ingest.DeadLetterRecord) error {
	rows := make([]clickhouse.DeadLetter, len(records))
	for i, r := range records {
		rows[i] = clickhouse.DeadLetter{
			OrganizationID: r.OrganizationID, SubscriptionID: r.ExternalSubscriptionID, Code: r.Code, TransactionID: r.TransactionID,
			Timestamp: r.Timestamp, IngestedAt: r.IngestedAt, FailedAt: r.FailedAt, Event: string(r.Event),
			ErrorCode: r.ErrorCode, ErrorMessage: r.ErrorMessage, Topic: r.Topic, Partition: r.Partition, Offset: r.Offset,
		}
	}
	return s.ch.WriteDeadLetters(ctx, rows)
}

func (s deadLetterTable) filter(f kafka.ReplayFilter) clickhouse.DeadLetterFilter {
	return clickhouse.DeadLetterFilter{OrganizationID: f.OrganizationID.String(), FailedFrom: f.From, FailedTo: f.To, ErrorCode: f.ErrorCode}
}

func (s deadLetterTable) PendingDeadLetters(ctx context.Context, f kafka.ReplayFilter, after *ingest.DeadLetterRecord, limit int) ([]ingest.DeadLetterRecord, error) {
	var pos *clickhouse.DeadLetterPosition
	if after != nil {
		pos = &clickhouse.DeadLetterPosition{FailedAt: after.FailedAt, Topic: after.Topic, Partition: after.Partition, Offset: after.Offset}
	}
	rows, err := s.ch.PendingDeadLetters(ctx, s.filter(f), pos, limit)
	out := make([]ingest.DeadLetterRecord, len(rows))
	for i, r := range rows {
		out[i] = ingest.DeadLetterRecord{
			DeadLetter: ingest.DeadLetter{
				Event: []byte(r.Event), Topic: r.Topic, Partition: r.Partition, Offset: r.Offset,
				ErrorCode: r.ErrorCode, ErrorMessage: r.ErrorMessage, FailedAt: r.FailedAt,
			},
			OrganizationID: r.OrganizationID, ExternalSubscriptionID: r.SubscriptionID, Code: r.Code, TransactionID: r.TransactionID,
			Timestamp: r.Timestamp, IngestedAt: r.IngestedAt,
		}
	}
	return out, err
}

func (s deadLetterTable) CountPendingDeadLetters(ctx context.Context, f kafka.ReplayFilter) ([]kafka.DeadLetterCount, error) {
	counts, err := s.ch.CountPendingDeadLetters(ctx, s.filter(f))
	out := make([]kafka.DeadLetterCount, len(counts))
	for i, c := range counts {
		out[i] = kafka.DeadLetterCount(c)
	}
	return out, err
}

func (s deadLetterTable) MarkReplayed(ctx context.Context, dead []ingest.DeadLetterRecord, at time.Time) error {
	if s.failMarks {
		return errors.New("stopped")
	}
	rows := make([]clickhouse.DeadLetter, len(dead))
	for i, d := range dead {
		rows[i] = clickhouse.DeadLetter{OrganizationID: d.OrganizationID, Topic: d.Topic, Partition: d.Partition, Offset: d.Offset}
	}
	return s.ch.MarkDeadLettersReplayed(ctx, rows, at)
}

// gate rejects the events of the closed transaction IDs, as a store
// rejects a value until it is fixed.
type gate struct {
	w      ingest.EventWriter
	closed atomic.Bool
	tx     string
}

func (g *gate) Write(ctx context.Context, events []metering.Event) ([]int, error) {
	if g.closed.Load() {
		for _, e := range events {
			if e.TransactionID == g.tx {
				return nil, fmt.Errorf("gate: %w", core.ErrInvalidValue)
			}
		}
	}
	return g.w.Write(ctx, events)
}

// A batch with events the store rejects stores the others and
// dead-letters those; once the cause is fixed, the replay stores them
// once, even when it is run again after it stopped between producing and
// recording the replay.
func TestRejectedEventsReplay(t *testing.T) {
	setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ids := core.UUIDv7Generator{}
	org := core.NewID[core.OrganizationID](ids)
	suffix := org.String()[:8]
	topics := kafka.Topics{Raw: "raw-" + suffix, Enriched: "enriched-" + suffix, DeadLetter: "dlq-" + suffix, Signals: "signals-" + suffix}
	admin := client(t)
	if err := kafka.EnsureTopics(ctx, admin, 2, -1, topics.All()...); err != nil {
		t.Fatalf("EnsureTopics() error = %v", err)
	}
	store := clickhouse.New(env.conn)
	table := deadLetterTable{ch: store}
	g := &gate{w: store, tx: "late"}
	g.closed.Store(true)
	producer := client(t)

	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	reader := sdkmetric.NewManualReader()
	run(ctx, t, &wg, kafka.ConsumerConfig{
		Client: group(t, "write-"+suffix, topics.Enriched), DeadLetters: producer, DeadLetterTopic: topics.DeadLetter,
		Handler:       kafka.Writer(g, nil, nil),
		MeterProvider: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)), LagInterval: 100 * time.Millisecond,
	})
	run(ctx, t, &wg, kafka.ConsumerConfig{
		Client: group(t, "dead-"+suffix, topics.DeadLetter), DeadLetters: producer, DeadLetterTopic: topics.DeadLetter,
		Handler: kafka.DeadLetters(table, nil, nil),
	})

	started := time.Now().UTC()
	huge := money.MustParse("1" + strings.Repeat("0", 60)) // more than events_raw's Decimal(76, 26) holds
	var events []*ingest.Enriched
	for _, tx := range []string{"good1", "late", "poison", "good2"} {
		e := &ingest.Enriched{
			OrganizationID: org, TransactionID: tx, Code: "calls", ExternalSubscriptionID: "sub",
			Timestamp: started, IngestedAt: started, Properties: map[string]string{"n": "1"},
		}
		if tx == "poison" {
			e.PreciseTotalAmountCents = &huge
		}
		events = append(events, e)
	}
	if err := (kafka.EnrichedProducer{Client: producer, Topic: topics.Enriched}).ProduceEnriched(ctx, events); err != nil {
		t.Fatalf("ProduceEnriched() error = %v", err)
	}

	count := func(sql string, args ...any) (n uint64) {
		t.Helper()
		if err := env.conn.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("count error = %v", err)
		}
		return n
	}
	stored := func(tx string) uint64 {
		return count("SELECT count() FROM events_raw WHERE organization_id = ? AND transaction_id = ?", org.String(), tx)
	}
	deadLetters := func(tx string) uint64 {
		return count("SELECT count() FROM events_dead_letter WHERE organization_id = ? AND transaction_id = ? AND error_code = ?", org.String(), tx, ingest.ErrorReject)
	}
	eventually(t, "good events stored", func() bool { return stored("good1") == 1 && stored("good2") == 1 })
	eventually(t, "rejected events dead-lettered", func() bool { return deadLetters("late") == 1 && deadLetters("poison") == 1 })
	if n := stored("late") + stored("poison"); n != 0 {
		t.Fatalf("rejected events stored got = %d, want = 0", n)
	}

	f := kafka.ReplayFilter{OrganizationID: org, From: started.Add(-time.Minute), To: time.Now().Add(time.Minute), ErrorCode: ingest.ErrorReject}
	replayer := kafka.Replayer{Client: producer, Topics: topics, Store: table, BatchSize: 1}
	counts, err := replayer.Count(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if want := []kafka.DeadLetterCount{{ErrorCode: ingest.ErrorReject, Topic: topics.Enriched, Count: 2}}; len(counts) != 1 || counts[0] != want[0] {
		t.Errorf("dry run got = %+v, want = %+v", counts, want)
	}

	// The store is fixed for "late"; "poison" is still rejected.
	g.closed.Store(false)
	stopped := replayer
	stopped.Store = deadLetterTable{ch: store, failMarks: true}
	if _, err := stopped.Replay(ctx, f); err == nil {
		t.Fatal("stopped replay error got = nil, want = an error")
	}
	res, err := replayer.Replay(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if res.Replayed != 2 || res.Skipped != 0 {
		t.Errorf("replay got = %+v, want = 2 replayed", res)
	}
	// "late" came again twice, as the stopped replay produced its first
	// page; "poison" was dead-lettered again after both, on their
	// partition.
	eventually(t, "replays handled", func() bool { return deadLetters("poison") == 2 })
	if n := stored("late"); n != 1 {
		t.Errorf("replayed event stored got = %d, want = 1", n)
	}
	// What was replayed is no longer pending; the new dead letters are.
	counts, err = replayer.Count(ctx, kafka.ReplayFilter{OrganizationID: org, ErrorCode: ingest.ErrorReject})
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 1 || counts[0].Count != 1 {
		t.Errorf("pending after replay got = %+v, want = the new dead letter of poison", counts)
	}

	// The writer reported its outcomes and, once it caught up, no lag.
	lag, rejected := int64(-1), int64(0)
	eventually(t, "lag reported", func() bool {
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(ctx, &rm); err != nil {
			t.Fatal(err)
		}
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				switch d := m.Data.(type) {
				case metricdata.Gauge[int64]:
					for _, p := range d.DataPoints {
						if v, _ := p.Attributes.Value(kafka.AttrTopic); m.Name == "tanvo.ingest.consumer.lag" && v.AsString() == topics.Enriched {
							lag = p.Value
						}
					}
				case metricdata.Sum[int64]:
					for _, p := range d.DataPoints {
						if v, _ := p.Attributes.Value(kafka.AttrErrorType); m.Name == "tanvo.ingest.records" && v.AsString() == ingest.ErrorReject {
							rejected = p.Value
						}
					}
				}
			}
		}
		return lag == 0
	})
	if rejected != 3 {
		t.Errorf("dead records got = %d, want = 3", rejected)
	}
}
