package clickhouse_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

func enrichedTxs(events []metering.EnrichedEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.SubscriptionID + "/" + e.TransactionID
	}
	return out
}

// source: spec/requests/api/v1/events_controller_spec.rb:520 (the
// enriched events the enriched events API reads).
func TestEnrichedEvents(t *testing.T) {
	ctx := context.Background()
	store := clickhouse.New(database(t))
	ts := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	valued := func(e metering.Event, sub, value string) metering.Event {
		e.SubscriptionID = sub
		if value != "" {
			e.Value = &value
		}
		return e
	}
	storage := event("b", ts.Add(time.Hour))
	storage.Code = "storage"
	other := event("x", ts)
	other.OrganizationID = "other"
	// Each write is enriched after the previous one.
	for _, batch := range [][]metering.Event{
		{valued(event("a", ts), "sub", "12.5"), valued(other, "sub", "1")},
		{valued(storage, "sub", "abc")},
		{valued(event("c", ts.Add(2*time.Hour)), "sub_2", "")},
	} {
		if _, err := store.Write(ctx, batch); err != nil {
			t.Fatal(err)
		}
	}

	all, err := store.EnrichedEvents(ctx, metering.RawFilter{OrganizationID: "org"}, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := enrichedTxs(all), []string{"sub_2/c", "sub/b", "sub/a"}; !slices.Equal(got, want) {
		t.Fatalf("events got = %v, want = %v", got, want)
	}
	tests := []struct {
		name          string
		e             metering.EnrichedEvent
		value, value2 string // "" for nil
	}{
		{"numeric value", all[2], "12.5", "12.5"},
		{"text value", all[1], "abc", "0"},
		{"no value", all[0], "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deref(tt.e.Value); got != tt.value {
				t.Errorf("value got = %q, want = %q", got, tt.value)
			}
			got := ""
			if tt.e.DecimalValue != nil {
				got = tt.e.DecimalValue.String()
			}
			if got != tt.value2 {
				t.Errorf("decimal value got = %q, want = %q", got, tt.value2)
			}
			if tt.e.EnrichedAt.IsZero() || tt.e.EnrichedAt.Location() != time.UTC {
				t.Errorf("enriched_at got = %v, want = a UTC instant", tt.e.EnrichedAt)
			}
		})
	}

	// A replaced copy is read once, with its new value, enriched last.
	replaced := valued(event("a", ts), "sub", "7")
	if err := store.Replace(ctx, []metering.Event{replaced}); err != nil {
		t.Fatal(err)
	}
	all, err = store.EnrichedEvents(ctx, metering.RawFilter{OrganizationID: "org"}, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := enrichedTxs(all), []string{"sub/a", "sub_2/c", "sub/b"}; !slices.Equal(got, want) || deref(all[0].Value) != "7" {
		t.Errorf("after replace got = %v (value %q), want = %v (value 7)", got, deref(all[0].Value), want)
	}

	from, to := ts.Add(30*time.Minute), ts.Add(90*time.Minute)
	page := []string{}
	var after *metering.EnrichedPosition
	for {
		es, err := store.EnrichedEvents(ctx, metering.RawFilter{OrganizationID: "org"}, after, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(es) == 0 {
			break
		}
		page = append(page, enrichedTxs(es)...)
		last := es[len(es)-1]
		after = &metering.EnrichedPosition{EnrichedAt: last.EnrichedAt, SubscriptionID: last.SubscriptionID, TransactionID: last.TransactionID}
	}
	filters := []struct {
		name string
		f    metering.RawFilter
		want []string
	}{
		{"code", metering.RawFilter{Code: "storage"}, []string{"sub/b"}},
		{"subscription", metering.RawFilter{SubscriptionID: "sub_2"}, []string{"sub_2/c"}},
		{"timestamp range", metering.RawFilter{TimestampFrom: &from, TimestampTo: &to}, []string{"sub/b"}},
	}
	for _, tt := range filters {
		t.Run(tt.name, func(t *testing.T) {
			tt.f.OrganizationID = "org"
			es, err := store.EnrichedEvents(ctx, tt.f, nil, 10)
			if err != nil {
				t.Fatal(err)
			}
			if got := enrichedTxs(es); !slices.Equal(got, tt.want) {
				t.Errorf("events got = %v, want = %v", got, tt.want)
			}
		})
	}
	if want := []string{"sub/a", "sub_2/c", "sub/b"}; !slices.Equal(page, want) {
		t.Errorf("pages got = %v, want = %v", page, want)
	}
	if _, err := store.EnrichedEvents(ctx, metering.RawFilter{}, nil, 1); err == nil {
		t.Error("error got = nil, want = missing organization")
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
