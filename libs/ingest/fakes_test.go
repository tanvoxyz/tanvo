package ingest

import (
	"context"
	"sync"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/memstore"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

var (
	testIDs = core.UUIDv7Generator{}
	testOrg = core.NewID[core.OrganizationID](testIDs)
)

func ptr[T any](v T) *T { return &v }

// fakeCatalog is an in-memory Catalog.
type fakeCatalog struct {
	mu      sync.Mutex
	metrics []*catalog.BillableMetric
	subs    []*subscription.Subscription
	charges []*catalog.Charge
	err     error
	calls   int
}

func (c *fakeCatalog) Metric(_ context.Context, org core.OrganizationID, code string) (*catalog.BillableMetric, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	for _, m := range c.metrics {
		if m.OrganizationID == org && m.Code == code {
			return m, nil
		}
	}
	return nil, nil
}

func (c *fakeCatalog) Subscriptions(_ context.Context, org core.OrganizationID, externalID string) ([]*subscription.Subscription, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	var out []*subscription.Subscription
	for _, s := range c.subs {
		if s.OrganizationID == org && s.ExternalID == externalID {
			out = append(out, s)
		}
	}
	return out, nil
}

func (c *fakeCatalog) Charges(_ context.Context, org core.OrganizationID, plan core.PlanID, metric core.BillableMetricID) ([]*catalog.Charge, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	var out []*catalog.Charge
	for _, ch := range c.charges {
		if ch.OrganizationID == org && ch.PlanID == plan && ch.BillableMetricID == metric {
			out = append(out, ch)
		}
	}
	return out, nil
}

func (c *fakeCatalog) metric(code string, agg catalog.AggregationType, field string) *catalog.BillableMetric {
	m := &catalog.BillableMetric{ID: core.NewID[core.BillableMetricID](testIDs), OrganizationID: testOrg, Code: code, Name: code, AggregationType: agg}
	if field != "" {
		m.FieldName = ptr(field)
	}
	c.metrics = append(c.metrics, m)
	return m
}

func (c *fakeCatalog) subscription(externalID string, status subscription.Status, startedAt time.Time) *subscription.Subscription {
	s := &subscription.Subscription{
		ID:             core.NewID[core.SubscriptionID](testIDs),
		OrganizationID: testOrg,
		ExternalID:     externalID,
		CustomerID:     core.NewID[core.CustomerID](testIDs),
		PlanID:         core.NewID[core.PlanID](testIDs),
		Status:         status,
		StartedAt:      &startedAt,
	}
	c.subs = append(c.subs, s)
	return s
}

func (c *fakeCatalog) charge(plan core.PlanID, m *catalog.BillableMetric, inAdvance bool, props string) *catalog.Charge {
	ch := &catalog.Charge{
		ID:               core.NewID[core.ChargeID](testIDs),
		OrganizationID:   testOrg,
		PlanID:           plan,
		BillableMetricID: m.ID,
		ChargeModel:      catalog.ChargeModelStandard,
		PayInAdvance:     inAdvance,
	}
	ch.Code = ch.ID.String()
	if props != "" {
		ch.Properties = catalog.MustProperties(props)
	}
	c.charges = append(c.charges, ch)
	return ch
}

// memWriter is an eventstore.Writer with a ledger, applied on commit.
type memWriter struct {
	mu      sync.Mutex
	stored  map[string]metering.Event
	pending []metering.Event
	err     error
}

func newMemWriter() *memWriter { return &memWriter{stored: map[string]metering.Event{}} }

func (w *memWriter) Write(_ context.Context, events []metering.Event) ([]int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return nil, w.err
	}
	var dups []int
	seen := map[string]bool{}
	for i, e := range events {
		k := e.OrganizationID + "/" + e.SubscriptionID + "/" + e.TransactionID
		if _, ok := w.stored[k]; ok || seen[k] {
			dups = append(dups, i)
			continue
		}
		seen[k] = true
		w.pending = append(w.pending, e)
	}
	return dups, nil
}

func (w *memWriter) commit(ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ok {
		for _, e := range w.pending {
			w.stored[e.OrganizationID+"/"+e.SubscriptionID+"/"+e.TransactionID] = e
		}
	}
	w.pending = nil
}

func (w *memWriter) events() []metering.Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]metering.Event, 0, len(w.stored))
	for _, e := range w.stored {
		out = append(out, e)
	}
	return out
}

// memTx is the TxStore over memstore and a memWriter.
type memTx struct {
	store  *memstore.Store
	writer *memWriter
}

func newMemTx() *memTx { return &memTx{store: memstore.New(), writer: newMemWriter()} }

func (m *memTx) InTx(ctx context.Context, fn func(context.Context, event.Log, EventWriter) error) error {
	err := m.store.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		return fn(ctx, r.Events, m.writer)
	})
	m.writer.commit(err == nil)
	return err
}

// recorder records in-advance events, signals and raw messages.
type recorder struct {
	mu        sync.Mutex
	inAdvance []*Enriched
	signals   []UsageSignal
	raw       []RawMessage
	err       error
}

func (r *recorder) BillInAdvance(_ context.Context, events []*Enriched) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.inAdvance = append(r.inAdvance, events...)
	return nil
}

func (r *recorder) SignalUsage(_ context.Context, s []UsageSignal) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.signals = append(r.signals, s...)
	return nil
}

func (r *recorder) ProduceRaw(_ context.Context, msgs []RawMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.raw = append(r.raw, msgs...)
	return nil
}
