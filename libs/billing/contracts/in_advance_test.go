package contracts_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/contracts"
	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/products"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// advanceFixture is an active contract started on 1 January 2027 and a
// count metric its metered products bill.
type advanceFixture struct {
	h      *billingtest.Harness
	svc    *contracts.Service
	cards  *products.Service
	metric *catalog.BillableMetric
	cust   *customer.Customer
	ct     *subscription.Subscription
}

// jan returns an instant of January 2027 in UTC.
func jan(day int, hm ...int) time.Time { return billingtest.Date(2027, time.January, day, hm...) }

// newAdvanceFixture returns the fixture, its customer in the time zone
// (nil inherits the billing entity's) and its contract in the status.
func newAdvanceFixture(t *testing.T, loc *time.Location, status subscription.Status) *advanceFixture {
	t.Helper()
	h := billingtest.New(t, jan(1))
	svc, err := contracts.New(h.Deps)
	if err != nil {
		t.Fatalf("contracts.New() error = %v", err)
	}
	cards, err := products.New(h.Deps)
	if err != nil {
		t.Fatalf("products.New() error = %v", err)
	}
	f := &advanceFixture{h: h, svc: svc, cards: cards}
	f.metric = h.Metric("calls", catalog.AggregationTypeCount, "", func(m *catalog.BillableMetric) {
		m.Filters = []catalog.MetricFilter{{ID: core.NewID[core.BillableMetricFilterID](h.IDs), Key: "region", Values: []string{"eu", "us"}}}
	})
	f.cust = h.Customer(func(c *customer.Customer) { c.Timezone = loc })
	f.ct = h.Contract(f.cust, nil, billingtest.SubscribedAt(jan(1)))
	if status != subscription.StatusActive {
		f.ct.Status = status
		h.Do(func(ctx context.Context, r port.Repos) error { return r.Subscriptions.Update(ctx, f.ct) })
	}
	return f
}

// product creates a metered product of the metric m.
func (f *advanceFixture) product(t *testing.T, code string, m *catalog.BillableMetric) *catalog.Product {
	t.Helper()
	p, err := f.cards.CreateProduct(t.Context(), f.h.OrgID(), products.ProductInput{Code: code, Name: code, ProductType: catalog.ProductTypeMetered, Metric: refs.ByID(m.ID)})
	if err != nil {
		t.Fatalf("CreateProduct() error = %v", err)
	}
	return p
}

// rate is a monthly standard rate in force from an instant.
type rate struct {
	code   string
	from   time.Time
	amount string
}

// card creates a rate card of the product in the timing with the rates,
// on the product filter when not nil.
func (f *advanceFixture) card(t *testing.T, code string, p *catalog.Product, pf *catalog.ProductFilter, timing catalog.BillingTiming, rates ...rate) *catalog.RateCard {
	t.Helper()
	in := products.RateCardInput{Product: refs.ByID(p.ID), Code: code, Name: code, Currency: billingtest.USD, BillingTiming: timing}
	if pf != nil {
		in.Filter = refs.ByID(pf.ID)
	}
	for _, r := range rates {
		in.Rates = append(in.Rates, products.RateInput{
			Code: r.code, EffectiveFrom: r.from, RateModel: catalog.ChargeModelStandard,
			Properties: catalog.MustProperties(`{"amount":"` + r.amount + `"}`), BillingIntervalUnit: catalog.IntervalUnitMonth,
		})
	}
	c, err := f.cards.CreateRateCard(t.Context(), f.h.OrgID(), in)
	if err != nil {
		t.Fatalf("CreateRateCard() error = %v", err)
	}
	return c
}

// rateOf returns the card's rate of the code.
func (f *advanceFixture) rateOf(t *testing.T, c *catalog.RateCard, code string) *catalog.Rate {
	t.Helper()
	rs, err := f.cards.Rates(t.Context(), f.h.OrgID(), refs.ByID(c.ID))
	if err != nil {
		t.Fatalf("Rates() error = %v", err)
	}
	i := slices.IndexFunc(rs.Rates, func(r *catalog.Rate) bool { return r.Code == code })
	if i < 0 {
		t.Fatalf("rate %s of %s: none", code, c.Code)
	}
	return rs.Rates[i]
}

// attach applies the card to the contract from the day, anchored on it.
func (f *advanceFixture) attach(c *catalog.RateCard, day core.Date) *subscription.RateCard {
	return f.h.ContractRateCard(f.ct, c, func(rc *subscription.RateCard) { rc.EffectiveDate, rc.BillingAnchorDate = day, day })
}

// segments resolves the contract's in-advance segments of an event at ts.
func (f *advanceFixture) segments(t *testing.T, ts time.Time) []fees.ProductSegment {
	t.Helper()
	return billingtest.Read(f.h, func(ctx context.Context, r port.Repos) ([]fees.ProductSegment, error) {
		return f.svc.InAdvanceSegments(ctx, r, f.ct, f.metric, ts)
	})
}

// cardsOf returns the contract rate cards of the segments.
func cardsOf(segs []fees.ProductSegment) []core.SubscriptionRateCardID {
	out := make([]core.SubscriptionRateCardID, len(segs))
	for i, s := range segs {
		out[i] = s.Card.ID
	}
	slices.SortFunc(out, func(a, b core.SubscriptionRateCardID) int { return a.Compare(b) })
	return out
}

// ids sorts the contract rate card IDs of the cards.
func ids(cards ...*subscription.RateCard) []core.SubscriptionRateCardID {
	out := make([]core.SubscriptionRateCardID, len(cards))
	for i, c := range cards {
		out[i] = c.ID
	}
	slices.SortFunc(out, func(a, b core.SubscriptionRateCardID) int { return a.Compare(b) })
	return out
}

// base is the default setup of the spec: an advance card from 1 January
// whose rate becomes 20 on 15 January at noon.
func (f *advanceFixture) base(t *testing.T, p *catalog.Product, timing catalog.BillingTiming, changedAt time.Time) (*catalog.RateCard, *subscription.RateCard) {
	t.Helper()
	c := f.card(t, "base", p, nil, timing, rate{"first", jan(1), "10"}, rate{"second", changedAt, "20"})
	return c, f.attach(c, core.NewDate(2027, time.January, 1))
}

func TestInAdvanceSegments(t *testing.T) {
	t.Parallel()
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	advance, arrears := catalog.BillingTimingAdvance, catalog.BillingTimingArrears
	sole := func(t *testing.T, segs []fees.ProductSegment) fees.ProductSegment {
		t.Helper()
		if len(segs) != 1 {
			t.Fatalf("segments = %d, want = 1", len(segs))
		}
		return segs[0]
	}
	empty := func(t *testing.T, segs []fees.ProductSegment) {
		t.Helper()
		if len(segs) != 0 {
			t.Fatalf("segments = %d, want = none", len(segs))
		}
	}

	// source: spec/services/events/pay_in_advance_billing_segment_resolver_spec.rb:44
	t.Run("builds the rate's covering slice without storing a segment", func(t *testing.T) {
		t.Parallel()
		f := newAdvanceFixture(t, nil, subscription.StatusActive)
		c, card := f.base(t, f.product(t, "calls", f.metric), advance, jan(15, 12))
		seg := sole(t, f.segments(t, jan(15, 12))).Segment
		second := f.rateOf(t, c, "second")
		if seg.RateCardID != card.ID || seg.RateCardRateID != second.ID || seg.Status != subscription.SegmentPending {
			t.Errorf("segment card, rate, status = %s, %s, %s, want = %s, %s, pending", seg.RateCardID, seg.RateCardRateID, seg.Status, card.ID, second.ID)
		}
		if !seg.CycleStartedAt.Equal(jan(1)) || !seg.StartedAt.Equal(jan(15, 12)) || !seg.EndedAt.Equal(billingtest.Date(2027, time.February, 1)) {
			t.Errorf("segment cycle, start, end = %s, %s, %s, want = 2027-01-01, 2027-01-15T12:00, 2027-02-01", seg.CycleStartedAt, seg.StartedAt, seg.EndedAt)
		}
		if string(seg.RateProperties.Raw()) != string(second.Properties.Raw()) {
			t.Errorf("segment properties = %s, want = %s", seg.RateProperties.Raw(), second.Properties.Raw())
		}
		stored := billingtest.Read(f.h, func(ctx context.Context, r port.Repos) ([]*subscription.BillingSegment, error) {
			return r.BillingSegments.ListBySubscription(ctx, f.h.OrgID(), f.ct.ID)
		})
		if len(stored) != 0 {
			t.Errorf("stored segments = %d, want = 0", len(stored))
		}
	})

	// Two cards of the same product: the base one and another effective
	// on the day of January.
	type pair struct {
		f           *advanceFixture
		p           *catalog.Product
		card, other *subscription.RateCard
	}
	twoCards := func(t *testing.T, loc *time.Location, otherTiming catalog.BillingTiming, otherDay int) pair {
		t.Helper()
		f := newAdvanceFixture(t, loc, subscription.StatusActive)
		p := f.product(t, "calls", f.metric)
		_, card := f.base(t, p, advance, jan(15, 12))
		d := core.NewDate(2027, time.January, otherDay)
		other := f.card(t, "other", p, nil, otherTiming, rate{"base", d.Midnight(orUTC(loc)), "10"})
		return pair{f: f, p: p, card: card, other: f.attach(other, d)}
	}
	// source: spec/services/events/pay_in_advance_billing_segment_resolver_spec.rb:84
	t.Run("selects only the latest effective attachment", func(t *testing.T) {
		t.Parallel()
		x := twoCards(t, nil, advance, 15)
		if got := sole(t, x.f.segments(t, jan(15, 12))).Card.ID; got != x.other.ID {
			t.Errorf("card = %s, want = %s", got, x.other.ID)
		}
	})
	// source: spec/services/events/pay_in_advance_billing_segment_resolver_spec.rb:91
	t.Run("selects the earlier attachment while the later is not effective", func(t *testing.T) {
		t.Parallel()
		x := twoCards(t, nil, advance, 16)
		if got := sole(t, x.f.segments(t, jan(15, 12))).Card.ID; got != x.card.ID {
			t.Errorf("card = %s, want = %s", got, x.card.ID)
		}
	})
	// source: spec/services/events/pay_in_advance_billing_segment_resolver_spec.rb:99
	t.Run("a later arrears attachment supersedes the advance one", func(t *testing.T) {
		t.Parallel()
		x := twoCards(t, nil, arrears, 15)
		empty(t, x.f.segments(t, jan(15, 12)))
	})
	// source: spec/services/events/pay_in_advance_billing_segment_resolver_spec.rb:107
	t.Run("an event at the replacement's local effective date has none", func(t *testing.T) {
		t.Parallel()
		x := twoCards(t, ny, arrears, 15)
		empty(t, x.f.segments(t, core.NewDate(2027, time.January, 15).Midnight(ny)))
	})
	// source: spec/services/events/pay_in_advance_billing_segment_resolver_spec.rb:114
	t.Run("an event just before the replacement keeps the advance attachment", func(t *testing.T) {
		t.Parallel()
		x := twoCards(t, ny, arrears, 15)
		ts := core.NewDate(2027, time.January, 15).Midnight(ny).Add(-time.Second)
		if got := sole(t, x.f.segments(t, ts)).Card.ID; got != x.card.ID {
			t.Errorf("card = %s, want = %s", got, x.card.ID)
		}
	})
	// source: spec/services/events/pay_in_advance_billing_segment_resolver_spec.rb:131
	t.Run("selects the latest eligible version", func(t *testing.T) {
		t.Parallel()
		x := twoCards(t, nil, advance, 10)
		future := x.f.card(t, "future", x.p, nil, advance, rate{"base", jan(16), "10"})
		x.f.attach(future, core.NewDate(2027, time.January, 16))
		if got := sole(t, x.f.segments(t, jan(15, 12))).Card.ID; got != x.other.ID {
			t.Errorf("card = %s, want = %s", got, x.other.ID)
		}
	})
	// source: spec/services/events/pay_in_advance_billing_segment_resolver_spec.rb:145
	t.Run("uses the version in force, not a future one", func(t *testing.T) {
		t.Parallel()
		f := newAdvanceFixture(t, nil, subscription.StatusActive)
		f.h.Clock.Set(billingtest.Date(2026, time.September, 1))
		p := f.product(t, "calls", f.metric)
		sep := func(day int) time.Time { return billingtest.Date(2026, time.September, day) }
		c := f.card(t, "base", p, nil, advance, rate{"first", sep(22), "10"}, rate{"second", sep(25), "20"})
		card := f.attach(c, core.NewDate(2026, time.September, 22))
		other := f.card(t, "other", p, nil, advance, rate{"base", billingtest.Date(2026, time.October, 1), "10"})
		f.attach(other, core.NewDate(2026, time.October, 1))
		seg := sole(t, f.segments(t, billingtest.Date(2026, time.September, 30, 12)))
		if seg.Card.ID != card.ID || seg.Segment.RateCardRateID != f.rateOf(t, c, "second").ID {
			t.Errorf("card, rate = %s, %s, want = %s, the second rate", seg.Card.ID, seg.Segment.RateCardRateID, card.ID)
		}
	})
	// source: spec/services/events/pay_in_advance_billing_segment_resolver_spec.rb:167
	t.Run("returns a segment for each product of the metric", func(t *testing.T) {
		t.Parallel()
		f := newAdvanceFixture(t, nil, subscription.StatusActive)
		_, card := f.base(t, f.product(t, "calls", f.metric), advance, jan(15, 12))
		other := f.card(t, "other", f.product(t, "more-calls", f.metric), nil, advance, rate{"base", jan(1), "10"})
		attached := f.attach(other, core.NewDate(2027, time.January, 1))
		if got, want := cardsOf(f.segments(t, jan(15, 12))), ids(card, attached); !slices.Equal(got, want) {
			t.Errorf("cards = %v, want = %v", got, want)
		}
	})
	// source: spec/services/events/pay_in_advance_billing_segment_resolver_spec.rb:238
	t.Run("returns the latest version of a product alongside the other product", func(t *testing.T) {
		t.Parallel()
		f := newAdvanceFixture(t, nil, subscription.StatusActive)
		p := f.product(t, "calls", f.metric)
		f.base(t, p, advance, jan(15, 12))
		other := f.card(t, "other", f.product(t, "more-calls", f.metric), nil, advance, rate{"base", jan(1), "10"})
		attached := f.attach(other, core.NewDate(2027, time.January, 1))
		newer := f.card(t, "newer", p, nil, advance, rate{"base", jan(10), "10"})
		newerCard := f.attach(newer, core.NewDate(2027, time.January, 10))
		if got, want := cardsOf(f.segments(t, jan(15, 12))), ids(newerCard, attached); !slices.Equal(got, want) {
			t.Errorf("cards = %v, want = %v", got, want)
		}
	})
	// source: spec/services/events/pay_in_advance_billing_segment_resolver_spec.rb:263
	t.Run("keeps a card of each filter bucket", func(t *testing.T) {
		t.Parallel()
		f := newAdvanceFixture(t, nil, subscription.StatusActive)
		p := f.product(t, "calls", f.metric)
		_, card := f.base(t, p, advance, jan(15, 12))
		eu := "eu"
		pf, err := f.cards.CreateFilter(t.Context(), f.h.OrgID(), p.ID, products.FilterInput{Code: "eu", Name: "eu", Values: []products.FilterValueInput{{Key: "region", Value: &eu}}})
		if err != nil {
			t.Fatalf("CreateFilter() error = %v", err)
		}
		filtered := f.attach(f.card(t, "eu", p, pf, advance, rate{"base", jan(1), "10"}), core.NewDate(2027, time.January, 1))
		if got, want := cardsOf(f.segments(t, jan(15, 12))), ids(card, filtered); !slices.Equal(got, want) {
			t.Errorf("cards = %v, want = %v", got, want)
		}
	})
	// source: spec/services/events/pay_in_advance_billing_segment_resolver_spec.rb:278
	t.Run("uses the override's pricing snapshot", func(t *testing.T) {
		t.Parallel()
		f := newAdvanceFixture(t, nil, subscription.StatusActive)
		c, card := f.base(t, f.product(t, "calls", f.metric), advance, jan(15, 12))
		override := &catalog.RateOverride{RateModel: catalog.ChargeModelStandard, Properties: catalog.MustProperties(`{"amount":"30"}`)}
		phase := &catalog.RatePhase{
			ID: core.NewID[core.RatePhaseID](f.h.IDs), OrganizationID: f.h.OrgID(), SubscriptionRateCardID: card.ID,
			Code: "ramp", Position: 1, BillingIntervalCycleCount: new(1), Override: override,
			CreatedAt: f.h.Now(), UpdatedAt: f.h.Now(),
		}
		f.h.Do(func(ctx context.Context, r port.Repos) error {
			return r.ContractRateCards.SavePhases(ctx, f.h.OrgID(), card.ID, []*catalog.RatePhase{phase}, f.h.Now())
		})
		seg := sole(t, f.segments(t, jan(15, 12))).Segment
		if second := f.rateOf(t, c, "second"); seg.RateCardRateID != second.ID || seg.RatePhaseID != phase.ID {
			t.Errorf("segment rate, phase = %s, %s, want = %s, %s", seg.RateCardRateID, seg.RatePhaseID, second.ID, phase.ID)
		}
		if seg.RateModel != catalog.ChargeModelStandard || string(seg.RateProperties.Raw()) != string(override.Properties.Raw()) {
			t.Errorf("segment model, properties = %s, %s, want = standard, %s", seg.RateModel, seg.RateProperties.Raw(), override.Properties.Raw())
		}
	})
	// A segment carries the card's pricing unit; its conversion rate is the
	// segment's rate's (fees read it there, fees/product.go).
	//
	// source: spec/services/events/pay_in_advance_billing_segment_resolver_spec.rb:298
	t.Run("attaches the unit and new conversion rate", func(t *testing.T) {
		t.Parallel()
		f := newAdvanceFixture(t, nil, subscription.StatusActive)
		unit := &catalog.PricingUnit{
			ID: core.NewID[core.PricingUnitID](f.h.IDs), OrganizationID: f.h.OrgID(),
			Code: "credits", Name: "Credits", ShortName: "CR", CreatedAt: f.h.Now(), UpdatedAt: f.h.Now(),
		}
		f.h.Do(func(ctx context.Context, r port.Repos) error { return r.PricingUnits.Create(ctx, unit) })
		in := products.RateCardInput{
			Product: refs.ByID(f.product(t, "calls", f.metric).ID), Code: "base", Name: "base", Currency: billingtest.USD,
			BillingTiming: advance, PricingUnit: refs.ByID(unit.ID),
		}
		for _, r := range []struct {
			code, conversion string
			from             time.Time
		}{{"first", "2", jan(1)}, {"second", "3", jan(15, 12)}} {
			in.Rates = append(in.Rates, products.RateInput{
				Code: r.code, EffectiveFrom: r.from, RateModel: catalog.ChargeModelStandard,
				Properties: catalog.MustProperties(`{"amount":"10"}`), BillingIntervalUnit: catalog.IntervalUnitMonth,
				PricingUnitConversionRate: new(money.MustParse(r.conversion)),
			})
		}
		c, err := f.cards.CreateRateCard(t.Context(), f.h.OrgID(), in)
		if err != nil {
			t.Fatalf("CreateRateCard() error = %v", err)
		}
		f.attach(c, core.NewDate(2027, time.January, 1))
		seg := sole(t, f.segments(t, jan(15, 12))).Segment
		if seg.PricingUnitID != unit.ID {
			t.Errorf("segment pricing unit = %s, want = %s", seg.PricingUnitID, unit.ID)
		}
		second := f.rateOf(t, c, "second")
		if seg.RateCardRateID != second.ID || second.PricingUnitConversionRate == nil || second.PricingUnitConversionRate.Cmp(money.MustParse("3")) != 0 {
			t.Errorf("segment rate = %s (conversion %v), want = %s (conversion 3)", seg.RateCardRateID, second.PricingUnitConversionRate, second.ID)
		}
	})
	// source: spec/services/events/pay_in_advance_billing_segment_resolver_spec.rb:316
	t.Run("selects the new rate over a stored segment of the old one", func(t *testing.T) {
		t.Parallel()
		f := newAdvanceFixture(t, nil, subscription.StatusActive)
		c, card := f.base(t, f.product(t, "calls", f.metric), advance, jan(15, 12))
		first := f.rateOf(t, c, "first")
		f.h.Do(func(ctx context.Context, r port.Repos) error {
			return r.BillingSegments.InsertAll(ctx, []*subscription.BillingSegment{{
				ID: core.NewID[core.BillingSegmentID](f.h.IDs), OrganizationID: f.h.OrgID(), SubscriptionID: f.ct.ID,
				CustomerID: f.cust.ID, RateCardID: card.ID, RateCardRateID: first.ID,
				CycleStartedAt: jan(1), StartedAt: jan(1), EndedAt: billingtest.Date(2027, time.February, 1), BillingAt: jan(1),
				RateModel: catalog.ChargeModelStandard, RateProperties: first.Properties, Currency: billingtest.USD,
				ProrationRatio: money.NewFromInt(1), Status: subscription.SegmentProcessing, CreatedAt: f.h.Now(), UpdatedAt: f.h.Now(),
			}})
		})
		seg := sole(t, f.segments(t, jan(15, 12))).Segment
		if seg.RateCardRateID != f.rateOf(t, c, "second").ID || !seg.StartedAt.Equal(jan(15, 12)) {
			t.Errorf("rate, start = %s, %s, want = the second rate from 2027-01-15T12:00", seg.RateCardRateID, seg.StartedAt)
		}
	})
	// source: spec/services/events/pay_in_advance_billing_segment_resolver_spec.rb:335
	t.Run("an event just before the rate change selects the first rate", func(t *testing.T) {
		t.Parallel()
		f := newAdvanceFixture(t, nil, subscription.StatusActive)
		c, _ := f.base(t, f.product(t, "calls", f.metric), advance, jan(15, 12))
		seg := sole(t, f.segments(t, jan(15, 12).Add(-500*time.Microsecond))).Segment
		if seg.RateCardRateID != f.rateOf(t, c, "first").ID || !seg.EndedAt.Equal(jan(15, 12)) {
			t.Errorf("rate, end = %s, %s, want = the first rate to 2027-01-15T12:00", seg.RateCardRateID, seg.EndedAt)
		}
	})
	// source: spec/services/events/pay_in_advance_billing_segment_resolver_spec.rb:354
	t.Run("a rate change at the cycle boundary starts a fresh cycle", func(t *testing.T) {
		t.Parallel()
		f := newAdvanceFixture(t, nil, subscription.StatusActive)
		feb := billingtest.Date(2027, time.February, 1)
		c, _ := f.base(t, f.product(t, "calls", f.metric), advance, feb)
		seg := sole(t, f.segments(t, feb)).Segment
		if seg.RateCardRateID != f.rateOf(t, c, "second").ID || !seg.CycleStartedAt.Equal(feb) || !seg.StartedAt.Equal(feb) {
			t.Errorf("rate, cycle, start = %s, %s, %s, want = the second rate from 2027-02-01", seg.RateCardRateID, seg.CycleStartedAt, seg.StartedAt)
		}
	})
	// source: spec/services/events/pay_in_advance_billing_segment_resolver_spec.rb:364
	t.Run("selects no card before its local effective date", func(t *testing.T) {
		t.Parallel()
		f := newAdvanceFixture(t, ny, subscription.StatusActive)
		f.base(t, f.product(t, "calls", f.metric), advance, jan(15, 12))
		empty(t, f.segments(t, jan(1, 4, 59).Add(59*time.Second)))
	})
	// source: spec/services/events/pay_in_advance_billing_segment_resolver_spec.rb:375
	t.Run("selects the card at midnight in the billing entity's time zone", func(t *testing.T) {
		t.Parallel()
		f := newAdvanceFixture(t, nil, subscription.StatusActive)
		f.h.UpdateBillingEntity(func(b *organization.BillingEntity) { b.Timezone = ny })
		_, card := f.base(t, f.product(t, "calls", f.metric), advance, jan(15, 12))
		if got := sole(t, f.segments(t, jan(1, 5))).Card.ID; got != card.ID {
			t.Errorf("card = %s, want = %s", got, card.ID)
		}
	})
	for _, c := range []struct {
		name   string
		source string
		end    time.Duration
		want   bool
	}{
		{"an event after the contract ended", "spec/services/events/post_process_service_spec.rb:173", -time.Second, false},
		{"an event before the contract ends", "spec/services/events/post_process_service_spec.rb:182", time.Second, true},
		{"an event at the contract's exclusive end", "spec/services/events/post_process_service_spec.rb:192", 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", c.source)
			f := newAdvanceFixture(t, nil, subscription.StatusActive)
			ts := jan(15, 12)
			end := ts.Add(c.end)
			f.ct.EndingAt = &end
			f.h.Do(func(ctx context.Context, r port.Repos) error { return r.Subscriptions.Update(ctx, f.ct) })
			f.base(t, f.product(t, "calls", f.metric), advance, jan(20))
			if got := len(f.segments(t, ts)); (got == 1) != c.want {
				t.Errorf("segments = %d, want one = %t", got, c.want)
			}
		})
	}
	for _, c := range []struct {
		name   string
		status subscription.Status
		timing catalog.BillingTiming
		day    int
		ts     time.Time
		other  bool
	}{
		// source: spec/services/events/pay_in_advance_billing_segment_resolver_spec.rb:401
		{name: "an attachment effective after the event", status: subscription.StatusActive, timing: advance, day: 16, ts: jan(15, 12)},
		// source: spec/services/events/pay_in_advance_billing_segment_resolver_spec.rb:409
		{name: "an event before any effective rate", status: subscription.StatusActive, timing: advance, day: 1, ts: billingtest.Date(2026, time.December, 31, 23)},
		// source: spec/services/events/pay_in_advance_billing_segment_resolver_spec.rb:425
		{name: "a pending contract", status: subscription.StatusPending, timing: advance, day: 1, ts: jan(15, 12)},
		// source: spec/services/events/pay_in_advance_billing_segment_resolver_spec.rb:433
		{name: "a terminated contract", status: subscription.StatusTerminated, timing: advance, day: 1, ts: jan(15, 12)},
		// source: spec/services/events/pay_in_advance_billing_segment_resolver_spec.rb:449
		{name: "a card billed in arrears", status: subscription.StatusActive, timing: arrears, day: 1, ts: jan(15, 12)},
		// source: spec/services/events/pay_in_advance_billing_segment_resolver_spec.rb:457
		{name: "a product of another metric", status: subscription.StatusActive, timing: advance, day: 1, ts: jan(15, 12), other: true},
	} {
		t.Run("returns none for "+c.name, func(t *testing.T) {
			t.Parallel()
			f := newAdvanceFixture(t, nil, c.status)
			m := f.metric
			if c.other {
				m = f.h.Metric("other", catalog.AggregationTypeCount, "")
			}
			card := f.card(t, "base", f.product(t, "calls", m), nil, c.timing, rate{"first", jan(1), "10"})
			f.attach(card, core.NewDate(2027, time.January, c.day))
			empty(t, f.segments(t, c.ts))
		})
	}
}

// orUTC returns loc, UTC when nil.
func orUTC(loc *time.Location) *time.Location {
	if loc == nil {
		return time.UTC
	}
	return loc
}
