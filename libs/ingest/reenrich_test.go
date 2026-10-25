package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/memstore"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

const (
	checkSpec  = "spec/services/events/stores/clickhouse/pre_enrichment_check_service_spec.rb:"
	enrichSpec = "spec/services/events/stores/clickhouse/re_enrich_subscription_events_service_spec.rb:"
)

// checkFixture is the catalog of a re-enrichment check: a plan, its
// subscription and charges.
type checkFixture struct {
	st      *memstore.Store
	plan    core.PlanID
	sub     *subscription.Subscription
	started time.Time
}

func newCheckFixture(t *testing.T, status subscription.Status, started time.Time) *checkFixture {
	t.Helper()
	f := &checkFixture{st: memstore.New(), plan: core.NewID[core.PlanID](testIDs), started: started}
	f.sub = &subscription.Subscription{
		ID: core.NewID[core.SubscriptionID](testIDs), OrganizationID: testOrg, ExternalID: "sub", CustomerID: core.NewID[core.CustomerID](testIDs),
		PlanID: f.plan, Status: status, StartedAt: &started,
	}
	if status == subscription.StatusTerminated {
		f.sub.TerminatedAt = new(started.Add(24 * time.Hour))
	}
	f.tx(t, func(ctx context.Context, r port.Repos) error {
		if err := r.Plans.Create(ctx, &catalog.Plan{ID: f.plan, OrganizationID: testOrg, Code: "plan", Name: "plan"}); err != nil {
			return err
		}
		return r.Subscriptions.Create(ctx, f.sub)
	})
	return f
}

func (f *checkFixture) tx(t *testing.T, fn func(context.Context, port.Repos) error) {
	t.Helper()
	if err := f.st.InTx(t.Context(), fn); err != nil {
		t.Fatal(err)
	}
}

// metric creates a sum metric on the "amount" property.
func (f *checkFixture) metric(t *testing.T, code string) *catalog.BillableMetric {
	t.Helper()
	m := &catalog.BillableMetric{
		ID: core.NewID[core.BillableMetricID](testIDs), OrganizationID: testOrg, Code: code, Name: code,
		AggregationType: catalog.AggregationTypeSum, FieldName: ptr("amount"),
	}
	f.tx(t, func(ctx context.Context, r port.Repos) error { return r.Metrics.Create(ctx, m) })
	return m
}

// charge creates a charge of the plan on m at created, with filters
// created at the given instants.
func (f *checkFixture) charge(t *testing.T, m *catalog.BillableMetric, created time.Time, filters ...time.Time) {
	t.Helper()
	c := &catalog.Charge{
		ID: core.NewID[core.ChargeID](testIDs), OrganizationID: testOrg, PlanID: f.plan, BillableMetricID: m.ID,
		ChargeModel: catalog.ChargeModelStandard, CreatedAt: created, UpdatedAt: created,
	}
	c.Code = c.ID.String()
	for i, at := range filters {
		c.Filters = append(c.Filters, catalog.ChargeFilter{
			ID: core.NewID[core.ChargeFilterID](testIDs), ChargeID: c.ID, Code: string(rune('a' + i)), CreatedAt: at,
		})
	}
	f.tx(t, func(ctx context.Context, r port.Repos) error { return r.Charges.Create(ctx, c) })
}

func (f *checkFixture) check(t *testing.T, opts CheckOptions) []Candidate {
	t.Helper()
	r, err := NewReEnricher(ReEnrichConfig{Transactor: f.st})
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Check(t.Context(), testOrg, opts)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	return got
}

// TestReEnrichCheck ports the cases of the check that apply to Tanvo:
// charges and filters added after the subscription started.
func TestReEnrichCheck(t *testing.T) {
	t.Parallel()
	started := time.Date(2024, 12, 1, 0, 0, 0, 0, time.UTC)
	month := 30 * 24 * time.Hour
	tests := []struct {
		name   string
		source string
		status subscription.Status
		setup  func(t *testing.T, f *checkFixture)
		want   []string
	}{
		{
			name: "no charge", source: checkSpec + "24", status: subscription.StatusActive,
			setup: func(*testing.T, *checkFixture) {},
		},
		{
			name: "charge created after the start", source: checkSpec + "150", status: subscription.StatusActive,
			setup: func(t *testing.T, f *checkFixture) { f.charge(t, f.metric(t, "new_charge_metric"), started.Add(month)) },
			want:  []string{"new_charge_metric"},
		},
		{
			name: "charge created before the start", source: checkSpec + "161", status: subscription.StatusActive,
			setup: func(t *testing.T, f *checkFixture) {
				f.charge(t, f.metric(t, "new_charge_metric"), started.Add(-month))
			},
		},
		{
			name: "terminated subscription", source: checkSpec + "174", status: subscription.StatusTerminated,
			setup: func(t *testing.T, f *checkFixture) { f.charge(t, f.metric(t, "new_charge_metric"), started.Add(month)) },
		},
		{
			name: "filter created after the start", source: checkSpec + "189", status: subscription.StatusActive,
			setup: func(t *testing.T, f *checkFixture) {
				f.charge(t, f.metric(t, "new_charge_metric"), started.Add(-month), started.Add(24*time.Hour))
			},
			want: []string{"new_charge_metric"},
		},
		{
			name: "filter created before the start", source: checkSpec + "204", status: subscription.StatusActive,
			setup: func(t *testing.T, f *checkFixture) {
				f.charge(t, f.metric(t, "new_charge_metric"), started.Add(-month), started.Add(-24*time.Hour))
			},
		},
		{
			name: "codes merged without duplicates", source: checkSpec + "238", status: subscription.StatusActive,
			setup: func(t *testing.T, f *checkFixture) {
				shared := f.metric(t, "shared_metric")
				f.charge(t, f.metric(t, "recurring_metric"), started.Add(month))
				f.charge(t, shared, started.Add(month))
				f.charge(t, shared, started.Add(2*month))
				f.charge(t, f.metric(t, "grouped_metric"), started.Add(month))
			},
			want: []string{"grouped_metric", "recurring_metric", "shared_metric"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_ = tt.source
			f := newCheckFixture(t, tt.status, started)
			tt.setup(t, f)
			got := f.check(t, CheckOptions{})
			var want []Candidate
			if tt.want != nil {
				want = []Candidate{{SubscriptionID: f.sub.ID, Codes: tt.want}}
			}
			if !slices.EqualFunc(got, want, func(a, b Candidate) bool {
				return a.SubscriptionID == b.SubscriptionID && slices.Equal(a.Codes, b.Codes)
			}) {
				t.Errorf("Check() got = %v, want = %v", got, want)
			}
			if jobs := f.st.Jobs(); len(jobs) != 0 {
				t.Errorf("dry run enqueued %d jobs, want = 0", len(jobs))
			}
		})
	}
}

// TestReEnrichCheckReprocess checks that the check enqueues one job per
// subscription found when asked to reprocess.
//
// source: checkSpec + "256"
func TestReEnrichCheckReprocess(t *testing.T) {
	t.Parallel()
	started := time.Date(2025, 11, 1, 0, 0, 0, 0, time.UTC)
	f := newCheckFixture(t, subscription.StatusActive, started)
	f.charge(t, f.metric(t, "recurring_metric"), started.Add(time.Hour))
	f.check(t, CheckOptions{Reprocess: true, BatchSize: 1000})
	jobs := f.st.Jobs()
	if len(jobs) != 1 || jobs[0].Kind != JobReEnrichSubscription || jobs[0].OrganizationID != testOrg || jobs[0].Queue != port.QueueDefault {
		t.Fatalf("jobs got = %+v, want = one %s job of the organization", jobs, JobReEnrichSubscription)
	}
	var args ReEnrichArgs
	if err := json.Unmarshal(jobs[0].Args, &args); err != nil {
		t.Fatal(err)
	}
	want := ReEnrichArgs{SubscriptionID: f.sub.ID, Codes: []string{"recurring_metric"}, BatchSize: 1000}
	if args.SubscriptionID != want.SubscriptionID || !slices.Equal(args.Codes, want.Codes) || args.BatchSize != want.BatchSize || args.Sleep != 0 {
		t.Errorf("job args got = %+v, want = %+v", args, want)
	}

	r, _ := NewReEnricher(ReEnrichConfig{Transactor: f.st})
	if _, err := r.Check(t.Context(), testOrg, CheckOptions{Reprocess: true}); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("Check(batch size 0) error got = %v, want = %v", err, core.ErrInvalidValue)
	}
}

// recordingReplacer records the batches it is given.
type recordingReplacer struct {
	batches [][]metering.Event
}

func (r *recordingReplacer) Replace(_ context.Context, events []metering.Event) error {
	r.batches = append(r.batches, slices.Clone(events))
	return nil
}

func (r *recordingReplacer) events() []metering.Event { return slices.Concat(r.batches...) }

// enrichFixture holds a subscription's stored events in both modes: the
// raw event log of postgres mode and the ledger of kafka mode.
type enrichFixture struct {
	*checkFixture
	ledger *fakeRaw
}

func newEnrichFixture(t *testing.T, status subscription.Status) *enrichFixture {
	t.Helper()
	started := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	f := &enrichFixture{checkFixture: newCheckFixture(t, status, started), ledger: &fakeRaw{}}
	f.metric(t, "bm")
	f.metric(t, "other")
	return f
}

// add stores an event of the subscription in both modes.
func (f *enrichFixture) add(t *testing.T, code, tx string, ts time.Time, props map[string]string, amount *money.Decimal) {
	t.Helper()
	e := &event.Event{
		ID: core.NewID[core.EventID](testIDs), OrganizationID: testOrg, TransactionID: tx, Code: code, ExternalSubscriptionID: "sub",
		Timestamp: ts, Properties: props, PreciseTotalAmount: amount, IngestedAt: ts.Add(time.Minute),
	}
	f.tx(t, func(ctx context.Context, r port.Repos) error {
		_, _, err := r.Events.Append(ctx, testOrg, []*event.Event{e})
		return err
	})
	f.ledger.events = append(f.ledger.events, metering.Event{
		OrganizationID: testOrg.String(), SubscriptionID: "sub", Code: code, TransactionID: tx, Timestamp: ts,
		Properties: props, PreciseTotalAmountCents: amount, IngestedAt: e.IngestedAt,
	})
}

// run re-enriches the subscription in mode.
func (f *enrichFixture) run(t *testing.T, mode Mode, args ReEnrichArgs) (ReEnrichResult, *recordingReplacer, []time.Duration) {
	t.Helper()
	// The ledger lists the newest ingested first, as ClickHouse does.
	slices.SortFunc(f.ledger.events, func(a, b metering.Event) int { return b.IngestedAt.Compare(a.IngestedAt) })
	reader, err := NewReader(ReaderConfig{Transactor: f.st, Mode: mode, Raw: &filteredRaw{f.ledger}})
	if err != nil {
		t.Fatal(err)
	}
	rep := &recordingReplacer{}
	var sleeps []time.Duration
	r, err := NewReEnricher(ReEnrichConfig{
		Transactor: f.st,
		Enricher:   NewEnricher(RepoCatalog{Transactor: f.st}, nil, nil),
		Events:     reader,
		Replacer:   func(core.OrganizationID) (EventReplacer, error) { return rep, nil },
		Sleep: func(_ context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if args.SubscriptionID.IsZero() {
		args.SubscriptionID = f.sub.ID
	}
	if args.BatchSize == 0 {
		args.BatchSize = 1000
	}
	res, err := r.ReEnrichSubscription(t.Context(), testOrg, args)
	if err != nil {
		t.Fatalf("ReEnrichSubscription() error = %v", err)
	}
	return res, rep, sleeps
}

// filteredRaw applies the code, subscription and timestamp filters to the
// fake ledger.
type filteredRaw struct{ *fakeRaw }

func (f *filteredRaw) RawEvents(ctx context.Context, rf metering.RawFilter, after *metering.RawPosition, limit int) ([]metering.Event, error) {
	keep := &fakeRaw{}
	for _, e := range f.events {
		if (rf.Code == "" || e.Code == rf.Code) && (rf.SubscriptionID == "" || e.SubscriptionID == rf.SubscriptionID) &&
			(rf.TimestampFrom == nil || !e.Timestamp.Before(*rf.TimestampFrom)) && (rf.TimestampTo == nil || !e.Timestamp.After(*rf.TimestampTo)) {
			keep.events = append(keep.events, e)
		}
	}
	return keep.RawEvents(ctx, rf, after, limit)
}

func txIDs(events []metering.Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.TransactionID
	}
	slices.Sort(out)
	return out
}

// TestReEnrichSubscription ports the re-enrichment cases on the stored
// events of both modes.
func TestReEnrichSubscription(t *testing.T) {
	t.Parallel()
	for _, mode := range []Mode{ModePostgres, ModeKafka} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			t.Run("stores the event enriched again", func(t *testing.T) { // source: enrichSpec + "52, 244"
				t.Parallel()
				f := newEnrichFixture(t, subscription.StatusActive)
				ts := f.started.Add(3*24*time.Hour + 299*time.Millisecond)
				amount := money.MustParse("12.5")
				f.add(t, "bm", "tx", ts, map[string]string{"key": "value", "amount": "3"}, &amount)
				res, rep, sleeps := f.run(t, mode, ReEnrichArgs{Sleep: time.Second})
				if res != (ReEnrichResult{Events: 1, Batches: 1}) || len(sleeps) != 1 || sleeps[0] != time.Second {
					t.Fatalf("result got = %+v, sleeps %v, want = 1 event in 1 batch, one sleep of 1s", res, sleeps)
				}
				got := rep.events()[0]
				if got.OrganizationID != testOrg.String() || got.SubscriptionID != "sub" || got.TransactionID != "tx" || got.Code != "bm" ||
					!got.Timestamp.Equal(ts) || !got.IngestedAt.Equal(ts.Add(time.Minute)) || got.Properties["key"] != "value" ||
					got.PreciseTotalAmountCents == nil || got.PreciseTotalAmountCents.String() != "12.5" || got.Value == nil || *got.Value != "3" {
					t.Errorf("replaced event got = %+v, want = the stored event with the value 3", got)
				}
			})
			t.Run("codes", func(t *testing.T) { // source: enrichSpec + "99"
				t.Parallel()
				f := newEnrichFixture(t, subscription.StatusActive)
				f.add(t, "bm", "tx1", f.started.Add(time.Hour), nil, nil)
				f.add(t, "other", "tx2", f.started.Add(time.Hour), nil, nil)
				res, rep, _ := f.run(t, mode, ReEnrichArgs{Codes: []string{"bm"}})
				if got := txIDs(rep.events()); res.Events != 1 || !slices.Equal(got, []string{"tx1"}) {
					t.Errorf("re-enriched got = %v (%+v), want = [tx1]", got, res)
				}
				_, rep, _ = f.run(t, mode, ReEnrichArgs{})
				if got := txIDs(rep.events()); !slices.Equal(got, []string{"tx1", "tx2"}) {
					t.Errorf("re-enriched without codes got = %v, want = [tx1 tx2]", got)
				}
			})
			t.Run("terminated subscription", func(t *testing.T) { // source: enrichSpec + "128"
				t.Parallel()
				f := newEnrichFixture(t, subscription.StatusTerminated)
				f.add(t, "bm", "before", f.started.Add(-time.Hour), nil, nil)
				f.add(t, "bm", "after", f.sub.TerminatedAt.Add(time.Hour), nil, nil)
				f.add(t, "bm", "during", f.started.Add(time.Hour), nil, nil)
				_, rep, _ := f.run(t, mode, ReEnrichArgs{Codes: []string{"bm"}})
				if got := txIDs(rep.events()); !slices.Equal(got, []string{"during"}) {
					t.Errorf("re-enriched got = %v, want = [during]", got)
				}
			})
			t.Run("batches", func(t *testing.T) { // source: enrichSpec + "215"
				t.Parallel()
				f := newEnrichFixture(t, subscription.StatusActive)
				f.add(t, "bm", "tx1", f.started.Add(time.Hour), nil, nil)
				f.add(t, "bm", "tx2", f.started.Add(2*time.Hour), nil, nil)
				res, rep, sleeps := f.run(t, mode, ReEnrichArgs{BatchSize: 1})
				if res != (ReEnrichResult{Events: 2, Batches: 2}) || len(rep.batches) != 2 || len(sleeps) != 2 {
					t.Errorf("result got = %+v, %d batches stored, %d sleeps, want = 2 events in 2 batches", res, len(rep.batches), len(sleeps))
				}
			})
		})
	}
}

// TestReEnrichSubscriptionMissing checks that a missing subscription
// has nothing to re-enrich (PreEnrichmentCheckJob returns) and that the
// arguments are validated.
//
// source: app/jobs/events/stores/clickhouse/pre_enrichment_check_job.rb:10-11
func TestReEnrichSubscriptionMissing(t *testing.T) {
	t.Parallel()
	f := newEnrichFixture(t, subscription.StatusActive)
	f.add(t, "bm", "tx", f.started.Add(time.Hour), nil, nil)
	res, rep, _ := f.run(t, ModePostgres, ReEnrichArgs{SubscriptionID: core.NewID[core.SubscriptionID](testIDs)})
	if res != (ReEnrichResult{}) || len(rep.batches) != 0 {
		t.Errorf("result got = %+v, want = nothing", res)
	}
	r, _ := NewReEnricher(ReEnrichConfig{Transactor: f.st})
	for _, args := range []ReEnrichArgs{{BatchSize: 0}, {BatchSize: core.MaxPageSize + 1}, {BatchSize: 1, Sleep: -1}} {
		if _, err := r.ReEnrichSubscription(t.Context(), testOrg, args); !errors.Is(err, core.ErrInvalidValue) {
			t.Errorf("ReEnrichSubscription(%+v) error got = %v, want = %v", args, err, core.ErrInvalidValue)
		}
	}
}
