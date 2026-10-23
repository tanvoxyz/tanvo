package clickhouse_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func txs(events []metering.Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.SubscriptionID + "/" + e.TransactionID
	}
	return out
}

// source: spec/requests/api/v1/events_controller_spec.rb:304 (the
// ClickHouse ledger the event API reads).
func TestRawEvents(t *testing.T) {
	ctx := context.Background()
	store := clickhouse.New(database(t))
	ts := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	ingested := time.Date(2026, 3, 6, 0, 0, 0, 0, time.UTC)
	at := func(e metering.Event, sub string, ing time.Duration) metering.Event {
		e.SubscriptionID, e.IngestedAt = sub, ingested.Add(ing)
		return e
	}
	amount := money.MustParse("12.5")
	withAmount := event("amount", ts.Add(2*time.Hour))
	withAmount.PreciseTotalAmountCents = &amount
	withAmount.Metadata = metering.EventMetadata{UserAgent: "curl/8", IPAddress: "192.0.2.1"}
	other := event("x", ts)
	other.OrganizationID = "other"
	storage := event("b", ts.Add(time.Hour))
	storage.Code = "storage"
	if _, err := store.Write(ctx, []metering.Event{
		at(event("a", ts), "sub", 0),
		at(event("a", ts), "sub_2", time.Minute), // the same transaction ID under another subscription
		at(storage, "sub", time.Minute),
		at(withAmount, "sub", 2*time.Minute),
		at(other, "sub", 0),
	}); err != nil {
		t.Fatal(err)
	}
	// A replaced copy is read as replaced.
	replaced := at(event("a", ts), "sub", 3*time.Minute)
	replaced.Properties = metering.Properties{"value": "7"}
	if err := store.Replace(ctx, []metering.Event{replaced}); err != nil {
		t.Fatal(err)
	}

	all, err := store.RawEvents(ctx, metering.RawFilter{OrganizationID: "org"}, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := txs(all), []string{"sub/a", "sub/amount", "sub/b", "sub_2/a"}; !slices.Equal(got, want) {
		t.Errorf("events got = %v, want = %v", got, want)
	}
	if all[0].Properties["value"] != "7" {
		t.Errorf("replaced properties got = %v, want = value 7", all[0].Properties)
	}
	if a := all[1].PreciseTotalAmountCents; a == nil || a.Cmp(amount) != 0 || all[1].Metadata != withAmount.Metadata {
		t.Errorf("amount event got = %+v, want amount 12.5 and the metadata", all[1])
	}
	if all[2].PreciseTotalAmountCents != nil {
		t.Errorf("amount got = %v, want = nil", all[2].PreciseTotalAmountCents)
	}

	tests := []struct {
		name  string
		f     metering.RawFilter
		after *metering.RawPosition
		want  []string
	}{
		{"code", metering.RawFilter{Code: "storage"}, nil, []string{"sub/b"}},
		{"subscription", metering.RawFilter{SubscriptionID: "sub_2"}, nil, []string{"sub_2/a"}},
		{"timestamp range", metering.RawFilter{TimestampFrom: new(ts.Add(time.Hour)), TimestampTo: new(ts.Add(2 * time.Hour))}, nil, []string{"sub/amount", "sub/b"}},
		{"after", metering.RawFilter{}, &metering.RawPosition{IngestedAt: all[1].IngestedAt, SubscriptionID: "sub", TransactionID: "amount"}, []string{"sub/b", "sub_2/a"}},
		{"after a tie", metering.RawFilter{}, &metering.RawPosition{IngestedAt: ingested.Add(time.Minute), SubscriptionID: "sub", TransactionID: "b"}, []string{"sub_2/a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.f.OrganizationID = "org"
			got, err := store.RawEvents(ctx, tt.f, tt.after, 10)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(txs(got), tt.want) {
				t.Errorf("events got = %v, want = %v", txs(got), tt.want)
			}
		})
	}

	e, ok, err := store.RawEvent(ctx, "org", "", "a")
	if err != nil || !ok || e.SubscriptionID != "sub" {
		t.Errorf("first ingested got = %+v, %v, %v; want = sub/a", e, ok, err)
	}
	e, ok, err = store.RawEvent(ctx, "org", "sub_2", "a")
	if err != nil || !ok || e.SubscriptionID != "sub_2" {
		t.Errorf("by subscription got = %+v, %v, %v; want = sub_2/a", e, ok, err)
	}
	if _, ok, err := store.RawEvent(ctx, "org", "", "missing"); err != nil || ok {
		t.Errorf("missing got = %v, %v; want = not found", ok, err)
	}
}
