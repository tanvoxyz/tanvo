package memstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/event"
)

func TestEventLog(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	ids := core.UUIDv7Generator{}
	d := newEventData()
	log := eventLogRepo{d: d}
	org, other := core.NewID[core.OrganizationID](ids), core.NewID[core.OrganizationID](ids)
	ev := func(org core.OrganizationID, tx, value string) *event.Event {
		return &event.Event{
			ID: core.NewID[core.EventID](ids), OrganizationID: org, TransactionID: tx, Code: "api_calls",
			ExternalSubscriptionID: "sub_1", Timestamp: now, IngestedAt: now,
			Properties: map[string]string{"value": value},
		}
	}

	// domain §24 item 11: the first event of a deduplication key wins.
	accepted, dupes, err := log.Append(ctx, org, []*event.Event{ev(org, "tr_1", "1"), ev(org, "tr_2", "2"), ev(org, "tr_1", "3")})
	if err != nil || len(accepted) != 2 || len(dupes) != 1 {
		t.Fatalf("Append: got = %d accepted %d duplicates %v, want = 2 1 nil", len(accepted), len(dupes), err)
	}
	accepted, dupes, err = log.Append(ctx, org, []*event.Event{ev(org, "tr_2", "4")})
	if err != nil || len(accepted) != 0 || len(dupes) != 1 {
		t.Errorf("Append(re-sent): got = %d accepted %d duplicates %v, want = 0 1 nil", len(accepted), len(dupes), err)
	}
	if accepted, _, err := log.Append(ctx, other, []*event.Event{ev(other, "tr_1", "5")}); err != nil || len(accepted) != 1 {
		t.Errorf("Append(other organization): got = %d %v, want = 1 nil", len(accepted), err)
	}
	if _, _, err := log.Append(ctx, org, []*event.Event{ev(other, "tr_9", "6")}); err == nil {
		t.Errorf("Append(foreign event): got = nil, want = an error")
	}
	got, err := log.Get(ctx, org, "sub_1", "tr_1")
	if err != nil || got.Properties["value"] != "1" {
		t.Errorf("Get: got = %v %v, want = value 1", got, err)
	}
	got.Properties["value"] = "mutated"
	if again, _ := log.Get(ctx, org, "sub_1", "tr_1"); again.Properties["value"] != "1" {
		t.Errorf("Get shares storage: got = %s, want = 1", again.Properties["value"])
	}

	if _, err := log.Get(ctx, org, "sub_1", "tr_404"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get(unknown): got = %v, want = ErrNotFound", err)
	}
}

// TestEventLogDeduplicationKey ports the deduplication key of the event
// log: (organization, external subscription, transaction ID).
func TestEventLogDeduplicationKey(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	ids := core.UUIDv7Generator{}
	org := core.NewID[core.OrganizationID](ids)
	ev := func(sub, tx string, ts time.Time, ingested time.Time) *event.Event {
		return &event.Event{
			ID: core.NewID[core.EventID](ids), OrganizationID: org, TransactionID: tx, Code: "api_calls",
			ExternalSubscriptionID: sub, Timestamp: ts, IngestedAt: ingested,
		}
	}
	tests := []struct {
		name   string
		source string
		stored []*event.Event
		event  *event.Event
		dup    bool
	}{
		{
			name: "transaction ID already used by the subscription", source: "spec/services/events/validate_creation_service_spec.rb:153",
			stored: []*event.Event{ev("sub_1", "tr_1", now, now)}, event: ev("sub_1", "tr_1", now, now), dup: true,
		},
		{
			name: "transaction ID used by another subscription", source: "db/structure.sql:12094 index_unique_transaction_id",
			stored: []*event.Event{ev("sub_1", "tr_1", now, now)}, event: ev("sub_2", "tr_1", now, now), dup: false,
		},
		{
			name: "same subscription and transaction ID at another timestamp", source: "db/structure.sql:12094 index_unique_transaction_id",
			stored: []*event.Event{ev("sub_1", "tr_1", now, now)}, event: ev("sub_1", "tr_1", now.Add(time.Hour), now), dup: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := eventLogRepo{d: newEventData()}
			if _, _, err := log.Append(ctx, org, tt.stored); err != nil {
				t.Fatal(err)
			}
			_, dupes, err := log.Append(ctx, org, []*event.Event{tt.event})
			if err != nil {
				t.Fatal(err)
			}
			if got := len(dupes) == 1; got != tt.dup {
				t.Errorf("duplicate got = %v, want = %v (%s)", got, tt.dup, tt.source)
			}
		})
	}

	t.Run("lookup", func(t *testing.T) {
		log := eventLogRepo{d: newEventData()}
		a, b := ev("sub_2", "tr_1", now, now.Add(time.Minute)), ev("sub_1", "tr_1", now, now)
		if accepted, _, err := log.Append(ctx, org, []*event.Event{a, b}); err != nil || len(accepted) != 2 {
			t.Fatalf("Append got = %d %v, want = 2 <nil>", len(accepted), err)
		}
		for _, tt := range []struct{ sub, want string }{{"sub_1", "sub_1"}, {"sub_2", "sub_2"}, {"", "sub_1"}} {
			got, err := log.Get(ctx, org, tt.sub, "tr_1")
			if err != nil || got.ExternalSubscriptionID != tt.want {
				t.Errorf("Get(%q) got = %v %v, want = %s", tt.sub, got, err, tt.want)
			}
		}
	})
}
