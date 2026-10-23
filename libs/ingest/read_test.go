package ingest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/memstore"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// fakeRaw is a ledger of events, in the order of RawEvents.
type fakeRaw struct {
	events []metering.Event
	got    []metering.RawFilter
}

func (f *fakeRaw) RawEvent(_ context.Context, org, sub, tx string) (metering.Event, bool, error) {
	for _, e := range f.events {
		if e.OrganizationID == org && e.TransactionID == tx && (sub == "" || e.SubscriptionID == sub) {
			return e, true, nil
		}
	}
	return metering.Event{}, false, nil
}

func (f *fakeRaw) RawEvents(_ context.Context, rf metering.RawFilter, after *metering.RawPosition, limit int) ([]metering.Event, error) {
	f.got = append(f.got, rf)
	start := 0
	if after != nil {
		start = slices.IndexFunc(f.events, func(e metering.Event) bool {
			return e.SubscriptionID == after.SubscriptionID && e.TransactionID == after.TransactionID
		}) + 1
	}
	return f.events[start:min(start+limit, len(f.events))], nil
}

func seedEvents(t *testing.T, st *memstore.Store, subs []*subscription.Subscription, events ...*event.Event) {
	t.Helper()
	err := st.InTx(context.Background(), func(ctx context.Context, r port.Repos) error {
		for _, s := range subs {
			if err := r.Subscriptions.Create(ctx, s); err != nil {
				return err
			}
		}
		_, _, err := r.Events.Append(ctx, testOrg, events)
		return err
	})
	if err != nil {
		t.Fatalf("seeding error = %v", err)
	}
}

func loggedEvent(tx, sub string, ts time.Time) *event.Event {
	return &event.Event{
		ID: core.NewID[core.EventID](testIDs), OrganizationID: testOrg, TransactionID: tx, Code: "bm",
		ExternalSubscriptionID: sub, Timestamp: ts, Properties: map[string]string{}, IngestedAt: ts,
	}
}

func transactions(p core.Page[*event.Event]) []string {
	out := make([]string, len(p.Items))
	for i, e := range p.Items {
		out[i] = e.TransactionID
	}
	return out
}

// source: spec/queries/events_query_spec.rb:134
func TestReaderPostgres(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memstore.New()
	src := &fakeCatalog{}
	old := src.subscription("sub", subscription.StatusTerminated, testNow.Add(-72*time.Hour))
	old.TerminatedAt = new(testNow.Add(-48 * time.Hour))
	current := src.subscription("sub", subscription.StatusActive, testNow.Add(-36*time.Hour))
	seedEvents(t, st, []*subscription.Subscription{old, current},
		loggedEvent("before", "sub", testNow.Add(-60*time.Hour)),
		loggedEvent("after", "sub", testNow.Add(-time.Hour)),
		loggedEvent("other", "sub_2", testNow.Add(-2*time.Hour)),
	)
	r, err := NewReader(ReaderConfig{Transactor: st})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		in   ListInput
		want []string
	}{
		{"all", ListInput{}, []string{"after", "other", "before"}},
		{"subscription", ListInput{ListFilter: event.ListFilter{ExternalSubscriptionID: "sub"}}, []string{"after", "before"}},
		{"from started at", ListInput{ListFilter: event.ListFilter{ExternalSubscriptionID: "sub"}, FromStartedAt: true}, []string{"after"}},
		{"from started at overrides timestamp_from", ListInput{ListFilter: event.ListFilter{ExternalSubscriptionID: "sub", TimestampFrom: new(testNow)}, FromStartedAt: true}, []string{"after"}},
		{"from started at without subscription", ListInput{ListFilter: event.ListFilter{ExternalSubscriptionID: "sub_2", TimestampFrom: new(testNow.Add(-3 * time.Hour))}, FromStartedAt: true}, []string{"other"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := r.List(ctx, testOrg, tt.in, core.PageRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if got := transactions(p); !slices.Equal(got, tt.want) {
				t.Errorf("events got = %v, want = %v", got, tt.want)
			}
		})
	}
	if e, err := r.Get(ctx, testOrg, "", "other"); err != nil || e.ExternalSubscriptionID != "sub_2" {
		t.Errorf("Get() got = %v, %v, want = other of sub_2", e, err)
	}
	if _, err := r.Get(ctx, testOrg, "", "missing"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get(missing) error got = %v, want = %v", err, core.ErrNotFound)
	}
}

func TestReaderKafka(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memstore.New()
	ts := testNow.Add(-time.Hour)
	raw := &fakeRaw{}
	for _, tx := range []string{"c", "b", "a"} {
		raw.events = append(raw.events, metering.Event{OrganizationID: testOrg.String(), SubscriptionID: "sub", TransactionID: tx, Code: "bm", Timestamp: ts, IngestedAt: ts})
	}
	r, err := NewReader(ReaderConfig{Transactor: st, Mode: ModeKafka, Raw: raw})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	page := core.PageRequest{Size: 2}
	for {
		p, err := r.List(ctx, testOrg, ListInput{ListFilter: event.ListFilter{Code: "bm"}}, page)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, transactions(p)...)
		if p.NextToken == "" {
			break
		}
		page.Token = p.NextToken
	}
	if !slices.Equal(got, []string{"c", "b", "a"}) {
		t.Errorf("events got = %v, want = [c b a]", got)
	}
	if raw.got[0].Code != "bm" || raw.got[0].OrganizationID != testOrg.String() {
		t.Errorf("filter got = %+v, want = code bm of the organization", raw.got[0])
	}
	if _, err := r.List(ctx, testOrg, ListInput{}, core.PageRequest{Token: "bad"}); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("bad token error got = %v, want = %v", err, core.ErrInvalidValue)
	}
	e, err := r.Get(ctx, testOrg, "", "b")
	if err != nil || e.ExternalSubscriptionID != "sub" || !e.CreatedAt.Equal(ts) {
		t.Errorf("Get() got = %+v, %v, want = b of sub", e, err)
	}
	if _, err := r.Get(ctx, testOrg, "", "missing"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get(missing) error got = %v, want = %v", err, core.ErrNotFound)
	}
}
