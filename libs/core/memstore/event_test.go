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
	log := eventLogRepo{d}
	org, other := core.NewID[core.OrganizationID](ids), core.NewID[core.OrganizationID](ids)
	ev := func(org core.OrganizationID, tx, value string) *event.Event {
		return &event.Event{
			ID: core.NewID[core.EventID](ids), OrganizationID: org, TransactionID: tx, Code: "api_calls",
			ExternalSubscriptionID: "sub_1", Timestamp: now, IngestedAt: now,
			Properties: map[string]string{"value": value},
		}
	}

	// domain §24 item 11: the first event of a transaction ID wins.
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
	got, err := log.Get(ctx, org, "tr_1")
	if err != nil || got.Properties["value"] != "1" {
		t.Errorf("Get: got = %v %v, want = value 1", got, err)
	}
	got.Properties["value"] = "mutated"
	if again, _ := log.Get(ctx, org, "tr_1"); again.Properties["value"] != "1" {
		t.Errorf("Get shares storage: got = %s, want = 1", again.Properties["value"])
	}

	snap := eventLogRepo{d.snapshot()}
	fix := ev(org, "tr_1", "10")
	fix.IngestedAt = now.Add(time.Hour)
	if err := log.Replace(ctx, fix); err != nil {
		t.Fatal(err)
	}
	if got, _ := log.Get(ctx, org, "tr_1"); got.Properties["value"] != "10" || got.ID == fix.ID || !got.IngestedAt.Equal(now) {
		t.Errorf("Replace: got = %+v, want = value 10 with the stored ID and ingestion time", got)
	}
	if old, _ := snap.Get(ctx, org, "tr_1"); old.Properties["value"] != "1" {
		t.Errorf("snapshot after Replace: got = %s, want = 1", old.Properties["value"])
	}
	if err := log.Replace(ctx, ev(org, "tr_404", "0")); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Replace(unknown): got = %v, want = ErrNotFound", err)
	}
	if _, err := log.Get(ctx, org, "tr_404"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get(unknown): got = %v, want = ErrNotFound", err)
	}
}
