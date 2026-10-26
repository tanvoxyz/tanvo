package kafka

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
)

// DefaultBarrierGrace is how long after an instant [Barrier] waits before
// it lists the raw offsets at that instant: a record stamped before the
// instant may still be on its way to the broker.
const DefaultBarrierGrace = 30 * time.Second

// barrierKeep is how long a barrier's enriched offsets are kept.
const barrierKeep = 48 * time.Hour

// OffsetAdmin is the part of [kadm.Client] a [Barrier] uses.
type OffsetAdmin interface {
	// ListOffsetsAfterMilli lists the first offsets at or after a
	// millisecond timestamp, or the end offsets.
	ListOffsetsAfterMilli(ctx context.Context, millisecond int64, topics ...string) (kadm.ListedOffsets, error)
	// ListStartOffsets lists the log start offsets.
	ListStartOffsets(ctx context.Context, topics ...string) (kadm.ListedOffsets, error)
	// ListEndOffsets lists the end offsets (high watermarks).
	ListEndOffsets(ctx context.Context, topics ...string) (kadm.ListedOffsets, error)
	// FetchOffsets fetches the committed offsets of a group.
	FetchOffsets(ctx context.Context, group string) (kadm.OffsetResponses, error)
}

var _ OffsetAdmin = (*kadm.Client)(nil)

// BarrierConfig configures a [Barrier].
type BarrierConfig struct {
	// Admin lists the offsets.
	Admin OffsetAdmin
	// Topics are the raw and enriched topics.
	Topics Topics
	// EnrichGroup and WriteGroup are the consumer groups of the
	// enrichment and writer consumers.
	EnrichGroup, WriteGroup string
	// Grace is [DefaultBarrierGrace] when zero.
	Grace time.Duration
	// Now is time.Now when nil.
	Now func() time.Time
}

// Barrier tells whether the events produced to the raw topic before an
// instant are in the event store: the enrichment group has committed
// past them on every raw partition, and the writer group has committed
// past the enriched records that existed once it had.
//
// The enrichment consumer commits a raw record only after its enriched
// record is acknowledged, so once the enrichment group is past the raw
// offsets at the instant, the enriched topic's end offsets of that moment
// cover every enriched record of those events. The barrier keeps those
// end offsets, per instant, in memory: a later snapshot (in another
// process, or after a restart) only covers more records, so it waits
// longer but is never wrong.
type Barrier struct {
	cfg BarrierConfig

	mu       sync.Mutex
	enriched map[time.Time]map[int32]int64
}

// NewBarrier returns a barrier.
func NewBarrier(cfg BarrierConfig) (*Barrier, error) {
	if cfg.Admin == nil || cfg.Topics.Raw == "" || cfg.Topics.Enriched == "" || cfg.EnrichGroup == "" || cfg.WriteGroup == "" {
		return nil, fmt.Errorf("kafka: barrier: admin, topics and groups are required")
	}
	if cfg.Grace == 0 {
		cfg.Grace = DefaultBarrierGrace
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Barrier{cfg: cfg, enriched: map[time.Time]map[int32]int64{}}, nil
}

// Reached reports whether the events produced before at are in the event
// store.
func (b *Barrier) Reached(ctx context.Context, at time.Time) (bool, error) {
	at = at.UTC().Truncate(time.Millisecond)
	if b.cfg.Now().Before(at.Add(b.cfg.Grace)) {
		return false, nil
	}
	enriched, ok := b.snapshot(at)
	if !ok {
		raw, err := b.cfg.Admin.ListOffsetsAfterMilli(ctx, at.UnixMilli(), b.cfg.Topics.Raw)
		if err == nil {
			err = raw.Error()
		}
		if err != nil {
			return false, fmt.Errorf("kafka: barrier: raw offsets at %s: %w", at.Format(time.RFC3339), err)
		}
		past, err := b.past(ctx, b.cfg.EnrichGroup, b.cfg.Topics.Raw, offsets(raw, b.cfg.Topics.Raw))
		if err != nil || !past {
			return false, err
		}
		ends, err := b.cfg.Admin.ListEndOffsets(ctx, b.cfg.Topics.Enriched)
		if err != nil {
			return false, fmt.Errorf("kafka: barrier: enriched end offsets: %w", err)
		}
		if err := ends.Error(); err != nil {
			return false, fmt.Errorf("kafka: barrier: enriched end offsets: %w", err)
		}
		enriched = offsets(ends, b.cfg.Topics.Enriched)
		b.keep(at, enriched)
	}
	return b.past(ctx, b.cfg.WriteGroup, b.cfg.Topics.Enriched, enriched)
}

// past reports whether group has committed at least the offsets of topic,
// a partition counting as past when its target is at or below its log
// start (its earlier records are gone).
func (b *Barrier) past(ctx context.Context, group, topic string, targets map[int32]int64) (bool, error) {
	if len(targets) == 0 {
		return true, nil
	}
	committed, err := b.cfg.Admin.FetchOffsets(ctx, group)
	if err != nil {
		return false, fmt.Errorf("kafka: barrier: offsets of group %s: %w", group, err)
	}
	if err := committed.Error(); err != nil {
		return false, fmt.Errorf("kafka: barrier: offsets of group %s: %w", group, err)
	}
	var starts map[int32]int64
	for p, target := range targets {
		if c, ok := committed.Lookup(topic, p); ok && c.At >= target {
			continue
		}
		if starts == nil {
			s, err := b.cfg.Admin.ListStartOffsets(ctx, topic)
			if err != nil {
				return false, fmt.Errorf("kafka: barrier: start offsets of %s: %w", topic, err)
			}
			starts = offsets(s, topic)
		}
		if s, ok := starts[p]; !ok || target > s {
			return false, nil
		}
	}
	return true, nil
}

func (b *Barrier) snapshot(at time.Time) (map[int32]int64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	o, ok := b.enriched[at]
	return o, ok
}

func (b *Barrier) keep(at time.Time, o map[int32]int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for t := range b.enriched {
		if t.Before(at.Add(-barrierKeep)) {
			delete(b.enriched, t)
		}
	}
	b.enriched[at] = o
}

// offsets returns the listed offsets of topic by partition, leaving out
// the partitions that failed.
func offsets(l kadm.ListedOffsets, topic string) map[int32]int64 {
	out := map[int32]int64{}
	for p, o := range l[topic] {
		if o.Err == nil && p >= 0 {
			out[p] = o.Offset
		}
	}
	return out
}
