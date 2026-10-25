package streaming_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/streaming"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	sd "github.com/tanvoxyz/tanvo/libs/core/streaming"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

var t0 = billingtest.Date(2026, time.September, 15, 10)

// produced is one record a fake producer received.
type produced struct {
	dest *sd.Destination
	env  streaming.Envelope
	key  string
}

// fakeProducer records the records; fail, when set, fails the n-th call
// (from 1); noCredentials makes CredentialsAvailable false, and
// loseCredentials makes it false once a record was produced.
type fakeProducer struct {
	mu              sync.Mutex
	calls           []produced
	attempts        int
	fail            func(n int) error
	noCredentials   bool
	loseCredentials bool
}

func (p *fakeProducer) Produce(_ context.Context, d *sd.Destination, data []byte, key string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.attempts++
	if p.fail != nil {
		if err := p.fail(p.attempts); err != nil {
			return err
		}
	}
	var env streaming.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return err
	}
	p.calls = append(p.calls, produced{dest: d, env: env, key: key})
	return nil
}

func (p *fakeProducer) CredentialsAvailable(*sd.Destination) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.noCredentials && (!p.loseCredentials || len(p.calls) == 0)
}

func (p *fakeProducer) records() []produced {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.calls)
}

// flakyTx fails its next fail transactions.
type flakyTx struct {
	port.Transactor
	fail int
}

func (t *flakyTx) InTx(ctx context.Context, fn func(ctx context.Context, r port.Repos) error) error {
	if t.fail > 0 {
		t.fail--
		return errors.New("stream is gone")
	}
	return t.Transactor.InTx(ctx, fn)
}

// fixture is a harness with a customer, a monthly plan coded
// enterprise_monthly with a standard charge on a count metric, an active
// subscription started six months ago, and the streaming service over a
// fake producer, its delivery logs captured.
type fixture struct {
	*billingtest.Harness
	svc      *streaming.Service
	usage    *usage.Service
	producer *fakeProducer
	flaky    *flakyTx
	logs     *bytes.Buffer
	cust     *customer.Customer
	plan     *catalog.Plan
	metric   *catalog.BillableMetric
	charge   *catalog.Charge
	sub      *subscription.Subscription
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	h := billingtest.New(t, t0)
	f := &fixture{Harness: h, producer: &fakeProducer{}, logs: &bytes.Buffer{}}
	h.Deps.Logger = slog.New(slog.NewTextHandler(f.logs, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return a
	}}))
	f.usage = usage.New(h.Deps)
	f.flaky = &flakyTx{Transactor: h.Deps.Tx}
	d := *h.Deps
	d.Tx = f.flaky
	f.svc = streaming.New(&d, f.usage, f.producer)
	f.cust = f.Customer()
	f.plan = f.Plan(period.Monthly, 0, func(p *catalog.Plan) { p.Code = "enterprise_monthly" })
	f.metric = f.Metric("api_calls", catalog.AggregationTypeCount, "")
	f.charge = f.Charge(f.plan, f.metric, catalog.ChargeModelStandard, `{"amount":"1"}`)
	f.sub = f.Subscription(f.cust, f.plan, billingtest.SubscribedAt(t0.AddDate(0, -6, 0)))
	return f
}

// destination stores the kinesis destination of the factory, claiming
// both event types.
func (f *fixture) destination(opts ...func(*sd.Destination)) *sd.Destination {
	f.T.Helper()
	d := &sd.Destination{
		ID: core.NewID[core.StreamingDestinationID](f.IDs), OrganizationID: f.OrgID(), Type: sd.TypeKinesis, Active: true,
		EventTypes: []sd.EventType{sd.EventCustomerUsageRefreshed, sd.EventCustomerFullUsageRefreshed},
		Kinesis: sd.KinesisSettings{
			StreamARN: "arn:aws:kinesis:eu-west-1:123456789012:stream/tanvo-streaming-sandbox", Region: "eu-west-1",
			RoleARN: "arn:aws:iam::123456789012:role/tanvo-streaming-sandbox-writer", PartitionKey: sd.PartitionKeyCustomerExternalID,
		},
		CreatedAt: f.Now(), UpdatedAt: f.Now(),
	}
	for _, o := range opts {
		o(d)
	}
	f.Do(func(ctx context.Context, r port.Repos) error { return r.StreamingDestinations.Create(ctx, d) })
	return d
}

// events stores n events of the metric for s at ts.
func (f *fixture) events(s *subscription.Subscription, n int, ts time.Time) {
	f.T.Helper()
	for range n {
		f.Event(s, f.metric.Code, ts)
	}
}

func (f *fixture) deliver(t sd.EventType, given []*usage.Usage) {
	f.T.Helper()
	if err := f.svc.Deliver(f.Ctx, f.OrgID(), f.cust.ID, t, given); err != nil {
		f.T.Fatalf("deliver: %v", err)
	}
}

// eurWallet stores an active EUR wallet with the ongoing usage.
func (f *fixture) eurWallet(priority int, cents int64, credits string) *wallet.Wallet {
	return f.Wallet(f.cust, "100", func(w *wallet.Wallet) {
		w.Currency, w.Priority = money.MustCurrency("EUR"), priority
		w.OngoingUsageBalanceAmount, w.CreditsOngoingUsageBalance = cents, money.MustParse(credits)
	})
}

// deliveryJobs returns the args of the queued delivery jobs and forgets
// every queued job.
func (f *fixture) deliveryJobs() []streaming.DeliverEventArgs {
	f.T.Helper()
	var out []streaming.DeliverEventArgs
	for _, j := range f.Store.TakeJobs() {
		if j.Kind != streaming.JobDeliverEvent {
			continue
		}
		if j.Queue != port.QueueStreaming {
			f.T.Errorf("queue: got = %s, want = %s", j.Queue, port.QueueStreaming)
		}
		var a streaming.DeliverEventArgs
		if err := json.Unmarshal(j.Args, &a); err != nil {
			f.T.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func (f *fixture) wantLog(pattern string) {
	f.T.Helper()
	if !regexp.MustCompile(pattern).MatchString(f.logs.String()) {
		f.T.Errorf("logs: got = %q, want a line matching %q", f.logs.String(), pattern)
	}
}

func eventTypes(as []streaming.DeliverEventArgs) []sd.EventType {
	out := make([]sd.EventType, 0, len(as))
	for _, a := range as {
		out = append(out, a.EventType)
	}
	slices.Sort(out)
	return out
}

// --- EventDestinations::CustomerUsage::RefreshedService

// source: spec/services/event_destinations/customer_usage/refreshed_service_spec.rb:21
// source: spec/services/event_destinations/customer_full_usage/refreshed_service_spec.rb:24
func TestDeliverWithoutDestination(t *testing.T) {
	for _, et := range sd.EventTypes() {
		f := newFixture(t)
		f.deliver(et, nil)
		if got := f.producer.records(); len(got) != 0 {
			t.Errorf("%s: records: got = %d, want = 0", et, len(got))
		}
	}
}

// source: spec/services/event_destinations/customer_usage/refreshed_service_spec.rb:30
// source: spec/services/event_destinations/customer_usage/refreshed_service_spec.rb:36
// source: spec/services/event_destinations/customer_usage/refreshed_service_spec.rb:43
func TestDeliverCurrentUsage(t *testing.T) {
	f := newFixture(t)
	d := f.destination()
	f.deliver(sd.EventCustomerUsageRefreshed, nil)
	got := f.producer.records()
	if len(got) != 1 {
		t.Fatalf("records: got = %d, want = 1", len(got))
	}
	if got[0].dest.ID != d.ID {
		t.Errorf("destination: got = %s, want = %s", got[0].dest.ID, d.ID)
	}
	if got[0].key != f.cust.ExternalID {
		t.Errorf("partition key: got = %q, want = %q", got[0].key, f.cust.ExternalID)
	}
}

// source: spec/services/event_destinations/customer_usage/refreshed_service_spec.rb:54
// source: spec/services/event_destinations/customer_usage/refreshed_service_spec.rb:66
// source: spec/services/event_destinations/customer_usage/refreshed_service_spec.rb:78
// source: spec/services/event_destinations/customer_full_usage/refreshed_service_spec.rb:61
func TestDeliverWallets(t *testing.T) {
	t.Run("every active wallet in application order", func(t *testing.T) {
		f := newFixture(t)
		f.destination()
		second := f.eurWallet(50, 500, "5.0")
		first := f.eurWallet(10, 1500, "15.0")
		for _, et := range sd.EventTypes() {
			f.deliver(et, nil)
		}
		for _, r := range f.producer.records() {
			ws := r.env.CustomerUsage.Wallets
			var ids []string
			for _, w := range ws {
				ids = append(ids, w.LagoID)
			}
			if want := []string{first.ID.String(), second.ID.String()}; !slices.Equal(ids, want) {
				t.Errorf("%s wallets: got = %v, want = %v", r.env.EventType, ids, want)
			}
			if ws[0].Credits != "15.0" || ws[0].AmountCents != 1500 || ws[1].Credits != "5.0" || ws[1].AmountCents != 500 {
				t.Errorf("%s wallets: got = %+v, want = 15.0/1500 then 5.0/500", r.env.EventType, ws)
			}
		}
		if got := len(f.producer.records()); got != 2 {
			t.Errorf("records: got = %d, want = 2", got)
		}
	})
	t.Run("a wallet in another currency", func(t *testing.T) {
		f := newFixture(t)
		f.destination()
		f.Wallet(f.cust, "10", func(w *wallet.Wallet) {
			w.OngoingUsageBalanceAmount, w.CreditsOngoingUsageBalance = 200, money.MustParse("2")
		})
		f.deliver(sd.EventCustomerUsageRefreshed, nil)
		ws := f.producer.records()[0].env.CustomerUsage.Wallets
		if len(ws) != 1 || ws[0].AmountCurrency != "USD" {
			t.Errorf("wallets: got = %+v, want = one USD wallet", ws)
		}
	})
	t.Run("the same customer totals on each subscription's record", func(t *testing.T) {
		f := newFixture(t)
		f.destination()
		f.eurWallet(10, 1500, "15.0")
		f.Subscription(f.cust, f.Plan(period.Monthly, 0), billingtest.SubscribedAt(t0.AddDate(0, -1, 0)))
		f.deliver(sd.EventCustomerUsageRefreshed, nil)
		got := f.producer.records()
		if len(got) != 2 {
			t.Fatalf("records: got = %d, want = 2", len(got))
		}
		a, b := got[0].env.CustomerUsage.Wallets, got[1].env.CustomerUsage.Wallets
		if !slices.Equal(a, b) || len(a) != 1 || a[0].AmountCents != 1500 {
			t.Errorf("wallets: got = %+v and %+v, want = the same 1500", a, b)
		}
	})
}

// source: spec/services/event_destinations/customer_usage/refreshed_service_spec.rb:96
func TestDeliverComputesWithoutTaxes(t *testing.T) {
	f := newFixture(t)
	f.destination()
	tax := f.Tax("vat", "20")
	f.UpdateCustomer(f.cust, func(c *customer.Customer) { c.TaxIDs = []core.TaxID{tax.ID} })
	f.events(f.sub, 3, t0.Add(-time.Hour))
	f.deliver(sd.EventCustomerUsageRefreshed, nil)
	u := f.producer.records()[0].env.CustomerUsage
	if u.AmountCents != 300 {
		t.Errorf("amount_cents: got = %d, want = 300 (no taxes)", u.AmountCents)
	}
}

// source: spec/services/event_destinations/customer_usage/refreshed_service_spec.rb:124
// source: spec/services/event_destinations/customer_usage/refreshed_service_spec.rb:134
// source: spec/services/event_destinations/customer_usage/refreshed_service_spec.rb:142
// source: spec/services/event_destinations/customer_usage/refreshed_service_spec.rb:146
func TestEnvelope(t *testing.T) {
	f := newFixture(t)
	f.destination()
	f.events(f.sub, 2, t0.Add(-time.Hour))
	f.deliver(sd.EventCustomerUsageRefreshed, nil)
	env := f.producer.records()[0].env
	if env.EventType != "customer_usage.refreshed.v1" || env.ObjectType != "customer_usage" || env.OrganizationID != f.OrgID().String() ||
		env.CustomerExternalID != f.cust.ExternalID || env.SubscriptionExternalID != f.sub.ExternalID {
		t.Errorf("identifiers: got = %+v", env)
	}
	u := env.CustomerUsage
	dt := regexp.MustCompile(`\A\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z\z`)
	if !dt.MatchString(u.FromDatetime) || !dt.MatchString(u.ToDatetime) || !regexp.MustCompile(`\A\d{4}-\d{2}-\d{2}\z`).MatchString(u.IssuingDate) {
		t.Errorf("datetimes: got = %s %s %s", u.FromDatetime, u.ToDatetime, u.IssuingDate)
	}
	if u.FromDatetime != "2026-09-01T00:00:00Z" || u.ToDatetime != "2026-10-01T00:00:00Z" || u.IssuingDate != "2026-09-30" {
		t.Errorf("period: got = %s %s %s, want = 2026-09-01T00:00:00Z 2026-10-01T00:00:00Z 2026-09-30", u.FromDatetime, u.ToDatetime, u.IssuingDate)
	}
	if !regexp.MustCompile(`\A[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-`).MatchString(env.EventID) {
		t.Errorf("event_id: got = %s, want = a UUIDv7", env.EventID)
	}
	if want := "2026-09-15T10:00:00.000000Z"; env.Version != want {
		t.Errorf("version: got = %s, want = %s", env.Version, want)
	}
	if len(u.ChargesUsage) != 1 || u.ChargesUsage[0].Units != "2.0" || u.ChargesUsage[0].AmountCents != 200 ||
		u.ChargesUsage[0].Charge.Code != f.charge.Code || u.ChargesUsage[0].BillableMetric.Code != "api_calls" {
		t.Errorf("charges_usage: got = %+v", u.ChargesUsage)
	}
}

// source: spec/services/event_destinations/customer_usage/refreshed_service_spec.rb:161
// source: spec/services/event_destinations/customer_usage/refreshed_service_spec.rb:171
// source: spec/services/event_destinations/customer_usage/refreshed_service_spec.rb:182
func TestDeliverGivenUsage(t *testing.T) {
	f := newFixture(t)
	f.destination()
	f.events(f.sub, 2, t0.Add(-time.Hour))
	precomputed := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*usage.Usage, error) {
		return f.usage.CurrentUsage(ctx, r, f.OrgID(), f.sub.ID, usage.Options{})
	})

	f.deliver(sd.EventCustomerUsageRefreshed, nil)
	computed := f.producer.records()[0].env
	f.producer.calls = nil
	f.deliver(sd.EventCustomerUsageRefreshed, []*usage.Usage{precomputed})
	supplied := f.producer.records()[0].env
	computed.EventID, computed.Version, supplied.EventID, supplied.Version = "", "", "", ""
	if !jsonEqual(t, computed, supplied) {
		t.Errorf("envelope: got = %+v, want = %+v", supplied, computed)
	}

	// The given usage is used as is: one the source would not compute
	// shows it was not computed again.
	altered := *precomputed
	altered.Amount = 12345
	f.producer.calls = nil
	f.deliver(sd.EventCustomerUsageRefreshed, []*usage.Usage{&altered})
	if got := f.producer.records(); len(got) != 1 || got[0].env.CustomerUsage.AmountCents != 12345 {
		t.Errorf("records: got = %+v, want = one record of 12345", got)
	}

	other := f.Subscription(f.cust, f.Plan(period.Monthly, 0), billingtest.SubscribedAt(t0.AddDate(0, -1, 0)))
	f.producer.calls = nil
	f.deliver(sd.EventCustomerUsageRefreshed, []*usage.Usage{precomputed})
	var subs []string
	for _, r := range f.producer.records() {
		subs = append(subs, r.env.SubscriptionExternalID)
	}
	slices.Sort(subs)
	want := []string{f.sub.ExternalID, other.ExternalID}
	slices.Sort(want)
	if !slices.Equal(subs, want) {
		t.Errorf("subscriptions: got = %v, want = %v", subs, want)
	}
}

func jsonEqual(t *testing.T, a, b any) bool {
	t.Helper()
	x, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	y, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Equal(x, y)
}

// source: spec/services/event_destinations/customer_usage/refreshed_service_spec.rb:197
// source: spec/services/event_destinations/customer_usage/refreshed_service_spec.rb:207
func TestDeliverSeveralSubscriptions(t *testing.T) {
	t.Run("one version per delivery", func(t *testing.T) {
		f := newFixture(t)
		f.destination()
		f.Subscription(f.cust, f.Plan(period.Monthly, 0), billingtest.SubscribedAt(t0.AddDate(0, -1, 0)))
		f.deliver(sd.EventCustomerUsageRefreshed, nil)
		got := f.producer.records()
		if len(got) != 2 || got[0].env.Version != got[1].env.Version {
			t.Errorf("versions: got = %+v, want = two records of one version", got)
		}
	})
	t.Run("one failing subscription does not stop the others", func(t *testing.T) {
		f := newFixture(t)
		f.destination()
		f.Subscription(f.cust, f.Plan(period.Monthly, 0), billingtest.SubscribedAt(t0.AddDate(0, -1, 0)))
		f.producer.fail = func(n int) error {
			if n == 1 {
				return errors.New("boom")
			}
			return nil
		}
		f.deliver(sd.EventCustomerUsageRefreshed, nil)
		if f.producer.attempts != 2 || len(f.producer.records()) != 1 {
			t.Errorf("attempts, records: got = %d %d, want = 2 1", f.producer.attempts, len(f.producer.records()))
		}
		f.wantLog(`level=ERROR msg="streaming delivery" outcome=failed destination_id=\S+ organization_id=\S+ event_type=customer_usage\.refreshed\.v1 customer_id=\S+ subscription_id=\S+ error=internal message="produce: boom"`)
	})
}

// source: spec/services/event_destinations/customer_usage/refreshed_service_spec.rb:222
func TestDeliverSkipsUncomputableUsage(t *testing.T) {
	f := newFixture(t)
	f.destination()
	// Without a charge on the plan, the full usage has no charge filter
	// and cannot be computed: a failed usage result.
	f.Do(func(ctx context.Context, r port.Repos) error {
		return r.Charges.Discard(ctx, f.OrgID(), f.charge.ID, f.Now())
	})
	f.deliver(sd.EventCustomerFullUsageRefreshed, nil)
	if got := f.producer.records(); len(got) != 0 {
		t.Errorf("records: got = %d, want = 0", len(got))
	}
	f.wantLog(`level=WARN msg="streaming delivery" outcome=skipped .*subscription_id=` + f.sub.ID.String() + ` error=not_allowed`)
}

// --- EventDestinations::CustomerFullUsage::RefreshedService

// source: spec/services/event_destinations/customer_full_usage/refreshed_service_spec.rb:38
// source: spec/services/event_destinations/customer_full_usage/refreshed_service_spec.rb:54
// source: spec/services/event_destinations/customer_full_usage/refreshed_service_spec.rb:77
func TestDeliverFullUsage(t *testing.T) {
	f := newFixture(t)
	f.destination()
	f.events(f.sub, 2, t0.AddDate(0, -3, 0))
	f.events(f.sub, 1, t0.Add(-time.Hour))
	f.deliver(sd.EventCustomerFullUsageRefreshed, nil)
	got := f.producer.records()
	if len(got) != 1 {
		t.Fatalf("records: got = %d, want = 1", len(got))
	}
	env := got[0].env
	if env.EventType != "customer_full_usage.refreshed.v1" || env.ObjectType != "customer_usage" {
		t.Errorf("types: got = %s %s", env.EventType, env.ObjectType)
	}
	if want := f.sub.StartedAt.UTC().Format(time.RFC3339); env.CustomerUsage.FromDatetime != want {
		t.Errorf("from_datetime: got = %s, want = %s (started_at)", env.CustomerUsage.FromDatetime, want)
	}
	if env.CustomerUsage.AmountCents != 300 {
		t.Errorf("amount_cents: got = %d, want = 300 (since the subscription started)", env.CustomerUsage.AmountCents)
	}
}

// source: spec/services/event_destinations/customer_full_usage/refreshed_service_spec.rb:93
func TestDeliverFullUsageExcludedPlan(t *testing.T) {
	f := newFixture(t)
	f.destination(func(d *sd.Destination) { d.CustomerFullUsageExcludedPlanCodes = []string{"enterprise_monthly"} })
	f.deliver(sd.EventCustomerFullUsageRefreshed, nil)
	if got := f.producer.records(); len(got) != 0 {
		t.Errorf("full usage records: got = %d, want = 0", len(got))
	}
	f.deliver(sd.EventCustomerUsageRefreshed, nil)
	if got := f.producer.records(); len(got) != 1 {
		t.Errorf("current usage records: got = %d, want = 1", len(got))
	}
}

// source: spec/services/event_destinations/customer_full_usage/refreshed_service_spec.rb:106
func TestDeliverFullUsageProratedCharge(t *testing.T) {
	f := newFixture(t)
	f.destination()
	field := "amount"
	m := f.Metric("seats", catalog.AggregationTypeSum, field, func(m *catalog.BillableMetric) { m.Recurring = true })
	f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(c *catalog.Charge) { c.Prorated = true })
	f.deliver(sd.EventCustomerFullUsageRefreshed, nil)
	if got := f.producer.records(); len(got) != 0 {
		t.Errorf("records: got = %d, want = 0", len(got))
	}
	f.wantLog(`level=WARN msg="streaming delivery" outcome=skipped .*error=not_allowed message=".*full_usage_not_allowed`)
}

// --- DeliverEventJob

// source: spec/jobs/deliver_event_job_spec.rb:8
// source: spec/jobs/deliver_event_job_spec.rb:12
// source: spec/jobs/deliver_event_job_spec.rb:46
func TestJobDelivers(t *testing.T) {
	f := newFixture(t)
	f.destination()
	if err := f.svc.EnqueueDelivery(f.Ctx, f.OrgID(), f.cust.ID, sd.EventCustomerUsageRefreshed); err != nil {
		t.Fatal(err)
	}
	jobs := f.Store.TakeJobs()
	if len(jobs) != 1 || jobs[0].Queue != port.QueueStreaming || jobs[0].MaxAttempts != 1 {
		t.Fatalf("jobs: got = %+v, want = one job on the streaming queue, run once", jobs)
	}
	ok, err := f.svc.HandleJob(f.Ctx, jobs[0])
	if !ok || err != nil {
		t.Fatalf("handle: got = %v %v, want = true <nil>", ok, err)
	}
	if got := f.producer.records(); len(got) != 1 || got[0].env.EventType != "customer_usage.refreshed.v1" {
		t.Errorf("records: got = %+v, want = one current usage record", got)
	}
	// The runtime lock is released: the next delivery runs.
	if _, err := f.svc.HandleJob(f.Ctx, jobs[0]); err != nil {
		t.Fatal(err)
	}
	if got := len(f.producer.records()); got != 2 {
		t.Errorf("records after a second run: got = %d, want = 2", got)
	}
}

// source: spec/jobs/deliver_event_job_spec.rb:20
func TestJobUnknownEventType(t *testing.T) {
	f := newFixture(t)
	raw, _ := json.Marshal(map[string]string{"event_type": "customer_usage.imagined.v1", "customer_id": f.cust.ID.String()})
	ok, err := f.svc.HandleJob(f.Ctx, port.Job{Kind: streaming.JobDeliverEvent, OrganizationID: f.OrgID(), Args: raw})
	if !ok || !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("handle: got = %v %v, want = true and an invalid value", ok, err)
	}
	if ok, _ := f.svc.HandleJob(f.Ctx, port.Job{Kind: "other"}); ok {
		t.Errorf("handle other kind: got = true, want = false")
	}
}

// source: spec/jobs/deliver_event_job_spec.rb:36
// source: spec/jobs/deliver_event_job_spec.rb:54
func TestJobSupersededWhileRunning(t *testing.T) {
	f := newFixture(t)
	f.destination()
	f.Do(func(ctx context.Context, r port.Repos) error {
		_, err := r.StreamingDeliveryLocks.ClaimRun(ctx, f.OrgID(), f.cust.ID, sd.EventCustomerUsageRefreshed, f.Now(), f.Now().Add(sd.LockTTL))
		return err
	})
	raw, _ := json.Marshal(streaming.DeliverEventArgs{EventType: sd.EventCustomerUsageRefreshed, CustomerID: f.cust.ID})
	ok, err := f.svc.HandleJob(f.Ctx, port.Job{Kind: streaming.JobDeliverEvent, OrganizationID: f.OrgID(), Args: raw})
	if !ok || err != nil {
		t.Fatalf("handle: got = %v %v, want = true <nil>", ok, err)
	}
	if got := len(f.producer.records()); got != 0 {
		t.Errorf("records: got = %d, want = 0", got)
	}
	f.wantLog(`level=INFO msg="streaming delivery" outcome=superseded event_type=customer_usage\.refreshed\.v1 customer_id=` + f.cust.ID.String() +
		` lock_key=streaming:customer_usage\.refreshed\.v1:` + f.cust.ID.String())
}

// --- Customers::RefreshWalletsService#deliver_streaming_events

// refresher returns the wallets service of f streaming through f.svc.
func (f *fixture) refresher() *wallets.Service {
	return wallets.New(f.Deps).WithUsage(f.usage).WithStreamer(f.svc)
}

func (f *fixture) refresh() {
	f.T.Helper()
	if _, err := f.refresher().RefreshOngoingBalances(f.Ctx, f.OrgID(), f.cust.ID); err != nil {
		f.T.Fatalf("refresh: %v", err)
	}
}

func TestRefreshStreams(t *testing.T) {
	both := []sd.EventType{sd.EventCustomerFullUsageRefreshed, sd.EventCustomerUsageRefreshed}
	for _, tc := range []struct {
		name          string
		sources       []string
		destination   func(*fixture)
		noCredentials bool
		lose          bool
		fail          bool
		wantJobs      []sd.EventType
		wantInline    int
		wantLog       string
	}{
		{
			name:     "no destination enqueues nothing",
			sources:  []string{"spec/services/customers/refresh_wallets_service_spec.rb:103"},
			wantJobs: nil,
		},
		{
			name: "a worker without credentials enqueues every type",
			sources: []string{
				"spec/services/customers/refresh_wallets_service_spec.rb:110", "spec/services/customers/refresh_wallets_service_spec.rb:115",
				"spec/services/customers/refresh_wallets_service_spec.rb:204", "spec/services/customers/refresh_wallets_service_spec.rb:209",
			},
			destination:   func(f *fixture) { f.destination() },
			noCredentials: true,
			wantJobs:      both,
		},
		{
			name: "a worker with credentials delivers the current usage inline and enqueues the full usage",
			sources: []string{
				"spec/services/customers/refresh_wallets_service_spec.rb:139", "spec/services/customers/refresh_wallets_service_spec.rb:151",
				"spec/services/customers/refresh_wallets_service_spec.rb:159",
				"spec/services/customers/refresh_wallets_service_spec.rb:164",
			},
			destination: func(f *fixture) { f.destination() },
			wantJobs:    []sd.EventType{sd.EventCustomerFullUsageRefreshed},
			wantInline:  1,
		},
		{
			name:        "credentials lost during the inline delivery hand it to a job",
			sources:     []string{"spec/services/customers/refresh_wallets_service_spec.rb:204"},
			destination: func(f *fixture) { f.destination() },
			lose:        true,
			wantJobs:    both,
			wantInline:  1,
		},
		{
			name: "a failing inline delivery is logged and handed to a job",
			sources: []string{
				"spec/services/customers/refresh_wallets_service_spec.rb:224", "spec/services/customers/refresh_wallets_service_spec.rb:231",
			},
			destination: func(f *fixture) { f.destination() },
			fail:        true,
			wantJobs:    both,
			wantLog:     `level=ERROR msg="streaming delivery" outcome=failed event_type=customer_usage\.refreshed\.v1 customer_id=\S+ error=internal message="stream is gone"`,
		},
		{
			name:        "an inactive destination enqueues nothing",
			sources:     []string{"spec/services/customers/refresh_wallets_service_spec.rb:240"},
			destination: func(f *fixture) { f.destination(func(d *sd.Destination) { d.Active = false }) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.Wallet(f.cust, "10")
			f.events(f.sub, 3, t0.Add(-time.Hour))
			if tc.destination != nil {
				tc.destination(f)
			}
			f.producer.noCredentials, f.producer.loseCredentials = tc.noCredentials, tc.lose
			if tc.fail {
				f.flaky.fail = 1
			}
			f.refresh()
			if got := eventTypes(f.deliveryJobs()); !slices.Equal(got, tc.wantJobs) {
				t.Errorf("jobs: got = %v, want = %v", got, tc.wantJobs)
			}
			got := f.producer.records()
			if len(got) != tc.wantInline {
				t.Fatalf("inline records: got = %d, want = %d", len(got), tc.wantInline)
			}
			if tc.wantInline > 0 && got[0].env.CustomerUsage.AmountCents != 300 {
				t.Errorf("inline amount: got = %d, want = 300", got[0].env.CustomerUsage.AmountCents)
			}
			if tc.wantInline > 0 && got[0].env.CustomerUsage.Wallets[0].AmountCents != 300 {
				t.Errorf("inline wallet usage: got = %d, want = 300 (the refreshed balances)", got[0].env.CustomerUsage.Wallets[0].AmountCents)
			}
			if tc.wantLog != "" {
				f.wantLog(tc.wantLog)
			}
		})
	}
}

// errRollback rolls a transaction back.
var errRollback = errors.New("rollback")

// source: spec/services/customers/refresh_wallets_service_spec.rb:120
// source: spec/services/customers/refresh_wallets_service_spec.rb:169
func TestRefreshRolledBack(t *testing.T) {
	for _, creds := range []bool{false, true} {
		f := newFixture(t)
		f.destination()
		f.producer.noCredentials = !creds
		err := f.Deps.Tx.InTx(f.Ctx, func(ctx context.Context, r port.Repos) error {
			// What runs after commit is dropped with the transaction.
			if _, err := f.svc.WalletsRefreshed(ctx, r, f.OrgID(), f.cust.ID, nil); err != nil {
				return err
			}
			return errRollback
		})
		if !errors.Is(err, errRollback) {
			t.Fatalf("tx: got = %v, want = %v", err, errRollback)
		}
		if got := f.deliveryJobs(); len(got) != 0 {
			t.Errorf("credentials %v: jobs: got = %v, want = none", creds, got)
		}
		if got := len(f.producer.records()); got != 0 {
			t.Errorf("credentials %v: records: got = %d, want = 0", creds, got)
		}
		// The rolled back claims leave the next refresh free to enqueue.
		f.Wallet(f.cust, "10")
		f.refresh()
		if got := len(f.deliveryJobs()); got == 0 {
			t.Errorf("credentials %v: jobs after a committed refresh: got = 0, want > 0", creds)
		}
	}
}

// countingDestinations counts the reads of the destinations.
type countingDestinations struct {
	sd.Repository
	n int
}

func (c *countingDestinations) ForEvent(ctx context.Context, org core.OrganizationID, t sd.EventType) ([]*sd.Destination, error) {
	c.n++
	return c.Repository.ForEvent(ctx, org, t)
}

func (c *countingDestinations) ListActive(ctx context.Context, org core.OrganizationID) ([]*sd.Destination, error) {
	c.n++
	return c.Repository.ListActive(ctx, org)
}

// source: spec/services/customers/refresh_wallets_service_spec.rb:180
func TestRefreshReadsDestinationsOnce(t *testing.T) {
	f := newFixture(t)
	f.destination()
	var n int
	f.Do(func(ctx context.Context, r port.Repos) error {
		c := &countingDestinations{Repository: r.StreamingDestinations}
		r.StreamingDestinations = c
		_, err := f.svc.WalletsRefreshed(ctx, r, f.OrgID(), f.cust.ID, nil)
		n = c.n
		return err
	})
	if n != 1 {
		t.Errorf("destination reads: got = %d, want = 1", n)
	}
}

// A refresh landing while a delivery waits is dropped and logged.
func TestRefreshSupersededWhileWaiting(t *testing.T) {
	f := newFixture(t)
	f.Wallet(f.cust, "10")
	f.destination()
	f.producer.noCredentials = true
	f.refresh()
	f.refresh()
	if got := eventTypes(f.deliveryJobs()); len(got) != 2 {
		t.Errorf("jobs: got = %v, want = one per type", got)
	}
	if got := strings.Count(f.logs.String(), "outcome=superseded"); got != 2 {
		t.Errorf("superseded logs: got = %d, want = 2", got)
	}
}
