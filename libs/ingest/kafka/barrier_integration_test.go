package kafka_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/ingest/kafka"
)

// TestBarrierPipeline checks that the barrier at an instant is reached
// only once every event produced before it is stored, and that events
// produced after it do not hold it.
func TestBarrierPipeline(t *testing.T) {
	setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ids := core.UUIDv7Generator{}
	org := core.NewID[core.OrganizationID](ids)
	suffix := org.String()[:8]
	topics := kafka.Topics{Raw: "raw-b" + suffix, Enriched: "enriched-b" + suffix, DeadLetter: "dlq-b" + suffix, Signals: "signals-b" + suffix}
	admin := client(t)
	if err := kafka.EnsureTopics(ctx, admin, 3, -1, topics.All()...); err != nil {
		t.Fatalf("EnsureTopics() error = %v", err)
	}
	started := time.Now().Add(-time.Hour)
	calls := &catalog.BillableMetric{ID: core.NewID[core.BillableMetricID](ids), OrganizationID: org, Code: "calls", AggregationType: catalog.AggregationTypeSum, FieldName: new("n")}
	sub := &subscription.Subscription{ID: core.NewID[core.SubscriptionID](ids), OrganizationID: org, ExternalID: "sub", PlanID: core.NewID[core.PlanID](ids), CustomerID: core.NewID[core.CustomerID](ids), Status: subscription.StatusActive, StartedAt: &started}
	cat := ingest.NewCache(&catalogFake{metrics: map[string]*catalog.BillableMetric{"calls": calls}, sub: sub})
	producer := client(t)
	svc, err := ingest.NewService(ingest.Config{Catalog: cat, Mode: ingest.ModeKafka, Producer: kafka.RawProducer{Client: producer, Topic: topics.Raw}})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	send := func(prefix string, n int) {
		t.Helper()
		var ins []ingest.Input
		for i := range n {
			ins = append(ins, ingest.Input{TransactionID: fmt.Sprintf("%s%d", prefix, i), Code: "calls", ExternalSubscriptionID: fmt.Sprintf("sub%d", i%5), Properties: map[string]any{"n": json.Number("1")}})
		}
		if _, err := svc.CreateBatch(ctx, org, ins, ingest.Metadata{}); err != nil {
			t.Fatalf("CreateBatch() error = %v", err)
		}
	}
	stored := func(prefix string) (n uint64) {
		if err := env.conn.QueryRow(ctx, "SELECT count() FROM events_raw WHERE organization_id = ? AND startsWith(transaction_id, ?)", org.String(), prefix).Scan(&n); err != nil {
			t.Fatalf("count error = %v", err)
		}
		return n
	}

	send("before", 40)
	time.Sleep(10 * time.Millisecond)
	at := time.Now()
	time.Sleep(10 * time.Millisecond)
	b, err := kafka.NewBarrier(kafka.BarrierConfig{
		Admin: kadm.NewClient(admin), Topics: topics, EnrichGroup: "enrich-b" + suffix, WriteGroup: "write-b" + suffix, Grace: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewBarrier() error = %v", err)
	}
	if got, err := b.Reached(ctx, at); err != nil || got {
		t.Fatalf("Reached() before the consumers got = %v, %v, want = false, nil", got, err)
	}

	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	run(ctx, t, &wg, kafka.ConsumerConfig{
		Client: group(t, "enrich-b"+suffix, topics.Raw), DeadLetters: producer, DeadLetterTopic: topics.DeadLetter,
		Handler: kafka.Enrichment(ingest.NewEnricher(cat, nil, nil), kafka.EnrichedProducer{Client: producer, Topic: topics.Enriched}, 4),
	})
	run(ctx, t, &wg, kafka.ConsumerConfig{
		Client: group(t, "write-b"+suffix, topics.Enriched), DeadLetters: producer, DeadLetterTopic: topics.DeadLetter,
		Handler: kafka.Writer(clickhouse.New(env.conn), kafka.SignalProducer{Client: producer, Topic: topics.Signals}, nil),
	})
	// Events after the instant keep coming while the barrier is checked.
	send("after", 40)
	eventually(t, "barrier reached", func() bool {
		got, err := b.Reached(ctx, at)
		if err != nil {
			t.Fatalf("Reached() error = %v", err)
		}
		if n := stored("before"); got && n < 40 {
			t.Fatalf("barrier reached with %d of the 40 events before it stored", n)
		}
		return got
	})
	eventually(t, "every event stored", func() bool { return stored("") == 80 })
}
