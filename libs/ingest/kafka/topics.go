package kafka

import (
	"context"
	"errors"
	"fmt"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Topics names the ingestion topics.
type Topics struct {
	// Raw receives validated events (events.raw).
	Raw string
	// Enriched receives enriched events (events.enriched).
	Enriched string
	// DeadLetter receives the messages that failed (events.dead_letter).
	DeadLetter string
	// Signals receives usage refresh signals (usage.refresh).
	Signals string
}

// DefaultTopics are the default topic names.
func DefaultTopics() Topics {
	return Topics{Raw: "events.raw", Enriched: "events.enriched", DeadLetter: "events.dead_letter", Signals: "usage.refresh"}
}

// All returns the topic names.
func (t Topics) All() []string { return []string{t.Raw, t.Enriched, t.DeadLetter, t.Signals} }

// EnsureTopics creates the topics that do not exist, with that many
// partitions and replicas (-1 for the brokers' defaults).
func EnsureTopics(ctx context.Context, cl *kgo.Client, partitions int32, replicas int16, topics ...string) error {
	resp, err := kadm.NewClient(cl).CreateTopics(ctx, partitions, replicas, nil, topics...)
	if err != nil {
		return fmt.Errorf("kafka: create topics: %w", err)
	}
	for _, r := range resp.Sorted() {
		if r.Err != nil && !errors.Is(r.Err, kerr.TopicAlreadyExists) {
			return fmt.Errorf("kafka: create topic %s: %w", r.Topic, r.Err)
		}
	}
	return nil
}
