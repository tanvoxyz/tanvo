package kafka_test

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/ingest/internal/dockertest"
	"github.com/tanvoxyz/tanvo/libs/ingest/kafka"
)

// env holds the containers of the integration tests, started on first use:
// TANVO_TEST_KAFKA_BROKERS / TANVO_TEST_CLICKHOUSE_DSN, or Redpanda and
// ClickHouse containers on the local Docker engine.
var env struct {
	once    sync.Once
	brokers string
	conn    driver.Conn
	skip    string
	cleanup []func()
}

func TestMain(m *testing.M) {
	flag.Parse()
	code := m.Run()
	for _, f := range env.cleanup {
		f()
	}
	os.Exit(code)
}

func start(ctx context.Context) error {
	brokers, dsn := os.Getenv("TANVO_TEST_KAFKA_BROKERS"), os.Getenv("TANVO_TEST_CLICKHOUSE_DSN")
	if brokers == "" || dsn == "" {
		engine, err := dockertest.Connect(ctx)
		if err != nil {
			return err
		}
		if brokers == "" {
			port, err := dockertest.FreePort()
			if err != nil {
				return err
			}
			c, err := engine.Run(ctx, dockertest.Spec{
				Image: "docker.redpanda.com/redpandadata/redpanda:v25.2.10",
				Cmd: []string{
					"redpanda", "start", "--mode", "dev-container", "--smp", "1", "--overprovisioned",
					"--kafka-addr", "PLAINTEXT://0.0.0.0:" + port,
					"--advertise-kafka-addr", "PLAINTEXT://127.0.0.1:" + port,
				},
				Ports:     []string{port + "/tcp"},
				HostPorts: map[string]string{port + "/tcp": port},
			})
			if err != nil {
				return err
			}
			env.cleanup = append(env.cleanup, func() { _ = c.Close(context.Background()) })
			brokers = "127.0.0.1:" + port
		}
		if dsn == "" {
			c, err := engine.Run(ctx, dockertest.Spec{
				Image: "clickhouse/clickhouse-server:25.8",
				Env:   []string{"CLICKHOUSE_USER=tanvo", "CLICKHOUSE_PASSWORD=tanvo", "CLICKHOUSE_DB=tanvo"},
				Ports: []string{"9000/tcp"},
			})
			if err != nil {
				return err
			}
			env.cleanup = append(env.cleanup, func() { _ = c.Close(context.Background()) })
			dsn = fmt.Sprintf("clickhouse://tanvo:tanvo@127.0.0.1:%s/tanvo", c.Ports["9000/tcp"])
		}
	}
	opts, err := ch.ParseDSN(dsn)
	if err != nil {
		return err
	}
	conn, err := ch.Open(opts)
	if err != nil {
		return err
	}
	env.cleanup = append(env.cleanup, func() { _ = conn.Close() })
	if err := dockertest.Wait(ctx, 2*time.Minute, conn.Ping); err != nil {
		return err
	}
	if err := clickhouse.Migrate(ctx, conn); err != nil {
		return err
	}
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers))
	if err != nil {
		return err
	}
	defer cl.Close()
	if err := dockertest.Wait(ctx, 2*time.Minute, cl.Ping); err != nil {
		return err
	}
	env.brokers, env.conn = brokers, conn
	return nil
}

func setup(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped with -short")
	}
	env.once.Do(func() {
		if err := start(context.Background()); err != nil {
			if errors.Is(err, dockertest.ErrUnavailable) {
				env.skip = err.Error()
			} else {
				env.skip = "containers: " + err.Error()
			}
		}
	})
	if env.conn == nil {
		t.Skip(env.skip)
	}
}

func client(t *testing.T, opts ...kgo.Opt) *kgo.Client {
	t.Helper()
	cl, err := kgo.NewClient(append([]kgo.Opt{kgo.SeedBrokers(env.brokers), kgo.FetchMaxWait(100 * time.Millisecond)}, opts...)...)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(cl.Close)
	return cl
}

func group(t *testing.T, name string, topics ...string) *kgo.Client {
	return client(t, kgo.ConsumerGroup(name), kgo.ConsumeTopics(topics...), kgo.BlockRebalanceOnPoll(), kgo.DisableAutoCommit(),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
}

func run(ctx context.Context, t *testing.T, wg *sync.WaitGroup, cfg kafka.ConsumerConfig) {
	t.Helper()
	c, err := kafka.NewConsumer(cfg)
	if err != nil {
		t.Fatalf("NewConsumer() error = %v", err)
	}
	wg.Go(func() {
		if err := c.Run(ctx); err != nil {
			t.Errorf("Run() error = %v", err)
		}
	})
}

type catalogFake struct {
	metrics map[string]*catalog.BillableMetric
	sub     *subscription.Subscription
	charges map[core.BillableMetricID]*catalog.Charge
}

func (c *catalogFake) Metric(_ context.Context, _ core.OrganizationID, code string) (*catalog.BillableMetric, error) {
	return c.metrics[code], nil
}

func (c *catalogFake) Subscriptions(context.Context, core.OrganizationID, string) ([]*subscription.Subscription, error) {
	return []*subscription.Subscription{c.sub}, nil
}

func (c *catalogFake) Charges(_ context.Context, _ core.OrganizationID, _ core.PlanID, m core.BillableMetricID) ([]*catalog.Charge, error) {
	if ch := c.charges[m]; ch != nil {
		return []*catalog.Charge{ch}, nil
	}
	return nil, nil
}

// flakyBiller fails its first call, then records.
type flakyBiller struct {
	mu     sync.Mutex
	calls  int
	billed map[string]int
}

func (b *flakyBiller) BillInAdvance(_ context.Context, events []*ingest.Enriched) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.calls == 1 {
		return errors.New("billing unavailable")
	}
	for _, e := range events {
		b.billed[e.TransactionID]++
	}
	return nil
}

func (b *flakyBiller) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.billed)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: timed out", what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestPipeline(t *testing.T) {
	setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ids := core.UUIDv7Generator{}
	org := core.NewID[core.OrganizationID](ids)
	suffix := org.String()[:8]
	topics := kafka.Topics{Raw: "raw-" + suffix, Enriched: "enriched-" + suffix, DeadLetter: "dlq-" + suffix, Signals: "signals-" + suffix}
	admin := client(t)
	if err := kafka.EnsureTopics(ctx, admin, 3, -1, topics.All()...); err != nil {
		t.Fatalf("EnsureTopics() error = %v", err)
	}
	if err := kafka.EnsureTopics(ctx, admin, 3, -1, topics.All()...); err != nil {
		t.Fatalf("EnsureTopics() again error = %v", err)
	}

	started := time.Now().Add(-time.Hour)
	calls := &catalog.BillableMetric{ID: core.NewID[core.BillableMetricID](ids), OrganizationID: org, Code: "calls", AggregationType: catalog.AggregationTypeSum, FieldName: new("n")}
	seats := &catalog.BillableMetric{ID: core.NewID[core.BillableMetricID](ids), OrganizationID: org, Code: "seats", AggregationType: catalog.AggregationTypeSum, FieldName: new("total"), Expression: new("event.properties.a * event.properties.b")}
	sub := &subscription.Subscription{ID: core.NewID[core.SubscriptionID](ids), OrganizationID: org, ExternalID: "sub", PlanID: core.NewID[core.PlanID](ids), CustomerID: core.NewID[core.CustomerID](ids), Status: subscription.StatusActive, StartedAt: &started}
	cat := ingest.NewCache(&catalogFake{
		metrics: map[string]*catalog.BillableMetric{"calls": calls, "seats": seats},
		sub:     sub,
		charges: map[core.BillableMetricID]*catalog.Charge{seats.ID: {PlanID: sub.PlanID, BillableMetricID: seats.ID, PayInAdvance: true}},
	})

	producer := client(t)
	svc, err := ingest.NewService(ingest.Config{Catalog: cat, Mode: ingest.ModeKafka, Producer: kafka.RawProducer{Client: producer, Topic: topics.Raw}})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	run(ctx, t, &wg, kafka.ConsumerConfig{
		Client: group(t, "enrich-"+suffix, topics.Raw), DeadLetters: producer, DeadLetterTopic: topics.DeadLetter,
		Handler: kafka.Enrichment(ingest.NewEnricher(cat, nil, nil), kafka.EnrichedProducer{Client: producer, Topic: topics.Enriched}, 4),
	})
	run(ctx, t, &wg, kafka.ConsumerConfig{
		Client: group(t, "write-"+suffix, topics.Enriched), DeadLetters: producer, DeadLetterTopic: topics.DeadLetter,
		Handler: kafka.Writer(clickhouse.New(env.conn), kafka.SignalProducer{Client: producer, Topic: topics.Signals}, nil),
	})
	b := &flakyBiller{billed: map[string]int{}}
	run(ctx, t, &wg, kafka.ConsumerConfig{
		Client: group(t, "advance-"+suffix, topics.Enriched), DeadLetters: producer, DeadLetterTopic: topics.DeadLetter,
		Handler: kafka.InAdvance(b), Backoff: 100 * time.Millisecond,
	})

	var ins []ingest.Input
	for i := range 10 {
		ins = append(ins, ingest.Input{TransactionID: fmt.Sprintf("c%d", i), Code: "calls", ExternalSubscriptionID: "sub", Properties: map[string]any{"n": json.Number("2")}})
	}
	ins = append(ins,
		ingest.Input{TransactionID: "s1", Code: "seats", ExternalSubscriptionID: "sub", Properties: map[string]any{"a": "3", "b": "4"}},
		ingest.Input{TransactionID: "s2", Code: "seats", ExternalSubscriptionID: "sub", Properties: map[string]any{"a": "1", "b": "1"}},
	)
	if _, err := svc.CreateBatch(ctx, org, ins, ingest.Metadata{}); err != nil {
		t.Fatalf("CreateBatch() error = %v", err)
	}
	// A re-sent transaction ID is dropped by the ClickHouse ledger.
	if _, err := svc.Create(ctx, org, ins[0], ingest.Metadata{}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	// An undecodable message and a connector event whose expression fails
	// go to the dead letter topic.
	bad, _ := json.Marshal(ingest.RawMessage{OrganizationID: org, TransactionID: "x", Code: "seats", ExternalSubscriptionID: "sub", Timestamp: time.Now(), IngestedAt: time.Now(), Source: "connector"})
	if err := producer.ProduceSync(ctx, &kgo.Record{Topic: topics.Raw, Value: []byte("garbage")}, &kgo.Record{Topic: topics.Raw, Key: []byte("x"), Value: bad}).FirstErr(); err != nil {
		t.Fatalf("ProduceSync() error = %v", err)
	}

	stored := func() (n uint64) {
		if err := env.conn.QueryRow(ctx, "SELECT count() FROM events_raw WHERE organization_id = ?", org.String()).Scan(&n); err != nil {
			t.Fatalf("count error = %v", err)
		}
		return n
	}
	eventually(t, "events stored", func() bool { return stored() == 12 })
	eventually(t, "in-advance events billed after a retry", func() bool { return b.count() == 2 })

	var total string
	if err := env.conn.QueryRow(ctx, "SELECT properties['total'] FROM events_raw WHERE organization_id = ? AND transaction_id = 's1'", org.String()).Scan(&total); err != nil || total != "12" {
		t.Errorf("expression result got = %q, %v, want = 12", total, err)
	}

	read := func(topic string, n int) [][]byte {
		cl := client(t, kgo.ConsumeTopics(topic), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
		var out [][]byte
		rctx, rcancel := context.WithTimeout(ctx, 30*time.Second)
		defer rcancel()
		for len(out) < n && rctx.Err() == nil {
			cl.PollFetches(rctx).EachRecord(func(r *kgo.Record) { out = append(out, r.Value) })
		}
		return out
	}
	dead := read(topics.DeadLetter, 2)
	codes := map[string]bool{}
	for _, d := range dead {
		var dl ingest.DeadLetter
		if err := json.Unmarshal(d, &dl); err != nil {
			t.Fatalf("dead letter error = %v", err)
		}
		codes[dl.ErrorCode] = true
	}
	if !codes[ingest.ErrorDecode] || !codes[ingest.ErrorExpression] {
		t.Errorf("dead letters got = %v, want = %s and %s", codes, ingest.ErrorDecode, ingest.ErrorExpression)
	}
	signals := read(topics.Signals, 1)
	var sig ingest.UsageSignal
	if len(signals) == 0 || json.Unmarshal(signals[0], &sig) != nil || sig.CustomerID != sub.CustomerID || len(sig.SubscriptionIDs) != 1 || sig.SubscriptionIDs[0] != sub.ID {
		t.Errorf("signal got = %+v, want = customer %v", sig, sub.CustomerID)
	}
}
