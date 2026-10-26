package kafka

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel/metric"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/ingest"
)

// Disposition is what became of a record.
type Disposition uint8

// Dispositions.
const (
	// Done: the record was processed.
	Done Disposition = iota
	// Retry: the record failed and may succeed later.
	Retry
	// Dead: the record will never succeed; it goes to the dead letter
	// topic.
	Dead
)

// Result is a handler's outcome for one record.
type Result struct {
	// Disposition is what became of the record.
	Disposition Disposition
	// Code classifies a failure (ingest.ErrorDecode, …).
	Code string
	// Err is the failure.
	Err error
	// IngestedAt is the record's event ingestion time, which bounds its
	// retries; the record's timestamp when zero.
	IngestedAt time.Time
}

// Handler processes a polled batch and returns one result per record, in
// the order of recs. It must not keep recs.
type Handler func(ctx context.Context, recs []*kgo.Record) []Result

// MaxRetryAge is the age past which a retried record goes to the dead
// letter topic.
const MaxRetryAge = 12 * time.Hour

// DefaultBackoff is the pause after a batch with retried records.
const DefaultBackoff = time.Second

// DefaultMaxPollRecords bounds the records of one batch.
const DefaultMaxPollRecords = 1000

// ConsumerConfig configures a [Consumer].
type ConsumerConfig struct {
	// Client consumes the topics in a group. It must be created with
	// kgo.BlockRebalanceOnPoll and kgo.DisableAutoCommit. Required.
	Client *kgo.Client
	// DeadLetters produces dead letters; Client when nil.
	DeadLetters *kgo.Client
	// DeadLetterTopic is the dead letter topic. Required.
	DeadLetterTopic string
	// Handler processes batches. Required.
	Handler Handler
	// MaxPollRecords bounds a batch; DefaultMaxPollRecords when zero.
	MaxPollRecords int
	// MaxRetryAge overrides [MaxRetryAge] when positive.
	MaxRetryAge time.Duration
	// Backoff overrides [DefaultBackoff] when positive.
	Backoff time.Duration
	// Clock tells ages; the system clock when nil.
	Clock core.Clock
	// Logger logs failures; slog.Default() when nil.
	Logger *slog.Logger
	// MeterProvider receives the consumer's metrics (the records by
	// outcome, the group's lag and the age of the retried records); none
	// are reported when nil.
	MeterProvider metric.MeterProvider
	// Group names the consumer group on the metrics; the client's group
	// when empty.
	Group string
	// LagInterval overrides [DefaultLagInterval] when positive; a negative
	// one turns the lag reports off.
	LagInterval time.Duration
}

// Consumer runs a consumer group loop. See the package documentation.
type Consumer struct {
	cfg ConsumerConfig
	m   meters
}

// NewConsumer returns a consumer configured by cfg.
func NewConsumer(cfg ConsumerConfig) (*Consumer, error) {
	if cfg.Client == nil || cfg.Handler == nil || cfg.DeadLetterTopic == "" {
		return nil, errors.New("kafka: consumer needs a client, a handler and a dead letter topic")
	}
	if cfg.DeadLetters == nil {
		cfg.DeadLetters = cfg.Client
	}
	if cfg.MaxPollRecords <= 0 {
		cfg.MaxPollRecords = DefaultMaxPollRecords
	}
	if cfg.MaxRetryAge <= 0 {
		cfg.MaxRetryAge = MaxRetryAge
	}
	if cfg.Backoff <= 0 {
		cfg.Backoff = DefaultBackoff
	}
	if cfg.Clock == nil {
		cfg.Clock = core.SystemClock{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Group == "" {
		cfg.Group, _ = cfg.Client.OptValue(kgo.ConsumerGroup).(string)
	}
	if cfg.LagInterval == 0 {
		cfg.LagInterval = DefaultLagInterval
	}
	return &Consumer{cfg: cfg, m: newMeters(cfg.MeterProvider, cfg.Group)}, nil
}

// Run consumes until ctx is done or the client is closed; it returns nil
// then. With a meter provider, it reports the group's lag every
// LagInterval while it runs.
func (c *Consumer) Run(ctx context.Context) error {
	if c.m.enabled && c.cfg.LagInterval > 0 && c.m.group != "" {
		lctx, cancel := context.WithCancel(ctx)
		var wg sync.WaitGroup
		wg.Go(func() { c.reportLag(lctx, c.cfg.LagInterval) })
		defer wg.Wait()
		defer cancel()
	}
	cl := c.cfg.Client
	for {
		fetches := cl.PollRecords(ctx, c.cfg.MaxPollRecords)
		if fetches.IsClientClosed() || ctx.Err() != nil {
			cl.AllowRebalance()
			return nil
		}
		fetches.EachError(func(topic string, partition int32, err error) {
			c.cfg.Logger.WarnContext(ctx, "kafka: fetch", "topic", topic, "partition", partition, "error", err)
		})
		recs := fetches.Records()
		retry := false
		if len(recs) > 0 {
			retry = c.process(ctx, recs)
		}
		cl.AllowRebalance()
		if retry {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(c.cfg.Backoff):
			}
		}
	}
}

type partitionKey struct {
	topic     string
	partition int32
}

// plan is the decision for one batch, computed from the results.
type plan struct {
	// commit holds the last record to commit per partition.
	commit map[partitionKey]*kgo.Record
	// rewind holds the first record to retry per partition.
	rewind map[partitionKey]*kgo.Record
	// dead holds the dead letters to produce.
	dead []ingest.DeadLetter
	// counts counts the records by outcome and error code: the committed
	// records are done or dead, the others retried, with the code of the
	// record their partition stopped at.
	counts map[outcomeKey]int64
	// retryAge is the age of the oldest record retried.
	retryAge time.Duration
}

// decide applies the commit rules to a batch: per partition, in offset
// order, done and dead records advance the commit point; the first retried
// record younger than maxAge stops it and is retried with every record
// after it.
func decide(recs []*kgo.Record, results []Result, now time.Time, maxAge time.Duration) plan {
	p := plan{commit: map[partitionKey]*kgo.Record{}, rewind: map[partitionKey]*kgo.Record{}, counts: map[outcomeKey]int64{}}
	stopCode := map[partitionKey]string{}
	order := make([]int, len(recs))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		ra, rb := recs[a], recs[b]
		return cmp.Or(strings.Compare(ra.Topic, rb.Topic), cmp.Compare(ra.Partition, rb.Partition), cmp.Compare(ra.Offset, rb.Offset))
	})
	for _, i := range order {
		r, res := recs[i], results[i]
		k := partitionKey{r.Topic, r.Partition}
		if _, stopped := p.rewind[k]; stopped {
			p.counts[outcomeKey{outcomeRetry, stopCode[k]}]++
			continue
		}
		switch res.Disposition {
		case Done:
			p.counts[outcomeKey{outcome: outcomeDone}]++
		case Retry:
			at := res.IngestedAt
			if at.IsZero() {
				at = r.Timestamp
			}
			if age := now.Sub(at); age < maxAge {
				p.rewind[k], stopCode[k] = r, res.Code
				p.retryAge = max(p.retryAge, age)
				p.counts[outcomeKey{outcomeRetry, res.Code}]++
				continue
			}
			p.dead = append(p.dead, deadLetter(r, res, now))
			p.counts[outcomeKey{outcomeDead, res.Code}]++
		default:
			p.dead = append(p.dead, deadLetter(r, res, now))
			p.counts[outcomeKey{outcomeDead, res.Code}]++
		}
		p.commit[k] = r
	}
	return p
}

func deadLetter(r *kgo.Record, res Result, now time.Time) ingest.DeadLetter {
	d := ingest.DeadLetter{
		Event:     r.Value,
		Topic:     r.Topic,
		Partition: r.Partition,
		Offset:    r.Offset,
		ErrorCode: res.Code,
		FailedAt:  now.UTC(),
	}
	if res.Err != nil {
		d.ErrorMessage = res.Err.Error()
	}
	return d
}

// process handles a batch and reports whether records are retried.
func (c *Consumer) process(ctx context.Context, recs []*kgo.Record) bool {
	results := c.cfg.Handler(ctx, recs)
	if len(results) != len(recs) {
		err := fmt.Errorf("kafka: handler returned %d results for %d records", len(results), len(recs))
		c.cfg.Logger.ErrorContext(ctx, "kafka: handler", "error", err)
		results = make([]Result, len(recs))
		for i := range results {
			results[i] = Result{Disposition: Retry, Err: err}
		}
	}
	p := decide(recs, results, c.cfg.Clock.Now(), c.cfg.MaxRetryAge)
	if err := c.produceDead(ctx, p.dead); err != nil {
		// Commit nothing: rewind every partition of the batch.
		c.cfg.Logger.ErrorContext(ctx, "kafka: produce dead letters", "error", err)
		c.rewind(firstPerPartition(recs))
		c.m.count(ctx, map[outcomeKey]int64{{outcomeRetry, codeDeadLetters}: int64(len(recs))})
		c.m.retrying(ctx, p.retryAge)
		return true
	}
	c.m.count(ctx, p.counts)
	c.m.retrying(ctx, p.retryAge)
	if len(p.commit) > 0 {
		last := make([]*kgo.Record, 0, len(p.commit))
		for _, r := range p.commit {
			last = append(last, r)
		}
		if err := c.cfg.Client.CommitRecords(ctx, last...); err != nil {
			// The records are processed; a lost commit only redelivers them.
			c.cfg.Logger.WarnContext(ctx, "kafka: commit", "error", err)
		}
	}
	for k, r := range p.rewind {
		c.cfg.Logger.WarnContext(ctx, "kafka: retry", "topic", k.topic, "partition", k.partition, "offset", r.Offset)
	}
	c.rewind(p.rewind)
	return len(p.rewind) > 0
}

func firstPerPartition(recs []*kgo.Record) map[partitionKey]*kgo.Record {
	out := map[partitionKey]*kgo.Record{}
	for _, r := range recs {
		k := partitionKey{r.Topic, r.Partition}
		if f, ok := out[k]; !ok || r.Offset < f.Offset {
			out[k] = r
		}
	}
	return out
}

// rewind seeks partitions back to their records, so that the next poll
// fetches them again.
func (c *Consumer) rewind(to map[partitionKey]*kgo.Record) {
	if len(to) == 0 {
		return
	}
	offsets := map[string]map[int32]kgo.EpochOffset{}
	for k, r := range to {
		if offsets[k.topic] == nil {
			offsets[k.topic] = map[int32]kgo.EpochOffset{}
		}
		offsets[k.topic][k.partition] = kgo.EpochOffset{Epoch: r.LeaderEpoch, Offset: r.Offset}
	}
	c.cfg.Client.SetOffsets(offsets)
}

func (c *Consumer) produceDead(ctx context.Context, dead []ingest.DeadLetter) error {
	if len(dead) == 0 {
		return nil
	}
	recs := make([]*kgo.Record, len(dead))
	for i, d := range dead {
		b, err := ingest.MarshalDeadLetter(d)
		if err != nil {
			return err
		}
		recs[i] = &kgo.Record{Topic: c.cfg.DeadLetterTopic, Value: b}
		c.cfg.Logger.WarnContext(ctx, "kafka: dead letter", "topic", d.Topic, "partition", d.Partition, "offset", d.Offset, "code", d.ErrorCode, "error", d.ErrorMessage)
	}
	if err := c.cfg.DeadLetters.ProduceSync(ctx, recs...).FirstErr(); err != nil {
		return fmt.Errorf("kafka: produce dead letters: %w", err)
	}
	return nil
}
