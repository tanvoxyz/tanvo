package invoicing_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/products"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// segmentFixture is a customer with a fixed product priced by a USD rate
// card whose rate is a standard 30.00 with the minimum of the test.
type segmentFixture struct {
	*fixture
	cards *products.Service
	card  *catalog.RateCard
	rate  *catalog.Rate
}

// newSegmentFixture returns a segment fixture on 2 September 2026, the
// card's rate in force from then with the minimum (cents).
func newSegmentFixture(t *testing.T, minimum int64) *segmentFixture {
	t.Helper()
	f := newFixture(t, billingtest.Date(2026, time.September, 2))
	cards, err := products.New(f.Deps)
	if err != nil {
		t.Fatalf("products.New() error = %v", err)
	}
	sf := &segmentFixture{fixture: f, cards: cards}
	sf.card = sf.rateCard("fixed-usd", billingtest.USD, minimum)
	return sf
}

// rateCard creates a rate card of a new fixed product in the currency.
func (f *segmentFixture) rateCard(code string, cur money.Currency, minimum int64) *catalog.RateCard {
	f.T.Helper()
	ctx, org := f.Ctx, f.OrgID()
	p, err := f.cards.CreateProduct(ctx, org, products.ProductInput{Code: code, Name: code, ProductType: catalog.ProductTypeFixed})
	if err != nil {
		f.T.Fatalf("CreateProduct() error = %v", err)
	}
	in := products.RateInput{
		Code: "base", EffectiveFrom: f.Now(), RateModel: catalog.ChargeModelStandard,
		Properties: catalog.MustProperties(`{"amount":"30.00"}`), BillingIntervalUnit: catalog.IntervalUnitMonth,
	}
	if minimum > 0 {
		in.MinAmount = &minimum
	}
	c, err := f.cards.CreateRateCard(ctx, org, products.RateCardInput{
		Product: refs.ByID(p.ID), Code: code, Name: code, Currency: cur, Rates: []products.RateInput{in},
	})
	if err != nil {
		f.T.Fatalf("CreateRateCard() error = %v", err)
	}
	rates, err := f.cards.Rates(ctx, org, refs.ByID(c.ID))
	if err != nil || len(rates.Rates) != 1 {
		f.T.Fatalf("Rates() = %+v, %v, want one rate", rates, err)
	}
	if f.card == nil {
		f.rate = rates.Rates[0]
	}
	return c
}

// contract creates a contract of the customer started on 1 July.
func (f *segmentFixture) contract(opts ...func(*subscription.Subscription)) *subscription.Subscription {
	f.T.Helper()
	return f.Contract(f.cust, nil, append([]func(*subscription.Subscription){
		billingtest.SubscribedAt(billingtest.Date(2026, time.July, 1)),
	}, opts...)...)
}

// segment stores a pending August segment of the card applied to the
// contract with units, billed in arrears at the start of September and
// priced at the snapshot amount; the options run before it is stored.
func (f *segmentFixture) segment(c *subscription.Subscription, card *catalog.RateCard, units int64, amount string, opts ...func(*subscription.BillingSegment)) *subscription.BillingSegment {
	f.T.Helper()
	n := money.NewFromInt(units)
	applied := f.ContractRateCard(c, card, func(rc *subscription.RateCard) { rc.Units = &n })
	rates, err := f.cards.Rates(f.Ctx, f.OrgID(), refs.ByID(card.ID))
	if err != nil {
		f.T.Fatalf("Rates() error = %v", err)
	}
	aug, sep := billingtest.Date(2026, time.August, 1), billingtest.Date(2026, time.September, 1)
	seg := &subscription.BillingSegment{
		ID:             core.NewID[core.BillingSegmentID](f.IDs),
		OrganizationID: f.OrgID(),
		SubscriptionID: c.ID,
		CustomerID:     c.CustomerID,
		RateCardID:     applied.ID,
		RateCardRateID: rates.Rates[0].ID,
		CycleStartedAt: aug,
		StartedAt:      aug,
		EndedAt:        sep,
		BillingAt:      sep,
		RateModel:      catalog.ChargeModelStandard,
		RateProperties: catalog.MustProperties(`{"amount":"` + amount + `"}`),
		Currency:       card.Currency,
		ProrationRatio: money.NewFromInt(1),
		Status:         subscription.SegmentPending,
		CreatedAt:      f.Now(),
		UpdatedAt:      f.Now(),
	}
	for _, o := range opts {
		o(seg)
	}
	f.Do(func(ctx context.Context, r port.Repos) error {
		return r.BillingSegments.InsertAll(ctx, []*subscription.BillingSegment{seg})
	})
	return seg
}

// process runs the customer's segment invoicing.
func (f *segmentFixture) process() []*invoice.Invoice {
	f.T.Helper()
	invs, err := f.svc.ProcessSegments(f.Ctx, f.OrgID(), f.cust.ID)
	if err != nil {
		f.T.Fatalf("ProcessSegments() error = %v", err)
	}
	return invs
}

// segmentsOf returns the stored segments of the contract.
func (f *segmentFixture) segmentsOf(c *subscription.Subscription) []*subscription.BillingSegment {
	f.T.Helper()
	return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*subscription.BillingSegment, error) {
		return r.BillingSegments.ListBySubscription(ctx, f.OrgID(), c.ID)
	})
}

// feeCounts returns the number of fees of each invoice.
func (f *segmentFixture) feeCounts(invs []*invoice.Invoice) []int {
	f.T.Helper()
	out := make([]int, len(invs))
	for i, inv := range invs {
		out[i] = len(f.Fees(inv.ID))
	}
	return out
}

func TestProcessSegments(t *testing.T) {
	t.Parallel()
	// source: spec/services/billing_segments/process_service_spec.rb:638
	t.Run("prices the fee from the segment's snapshot of its rate", func(t *testing.T) {
		t.Parallel()
		f := newSegmentFixture(t, 0)
		c := f.contract()
		f.segment(c, f.card, 5, "15.00")
		invs := f.process()
		if len(invs) != 1 {
			t.Fatalf("invoices = %d, want = 1", len(invs))
		}
		fees := f.Fees(invs[0].ID)
		if len(fees) != 1 {
			t.Fatalf("fees = %d, want = 1", len(fees))
		}
		fee := fees[0]
		if fee.Amount != 7500 || fee.UnitAmount != 1500 || fee.PreciseUnitAmount.Cmp(money.NewFromInt(15)) != 0 || fee.RateCardRateID != f.rate.ID {
			t.Errorf("fee = %d, %d, %s, %s, want = 7500, 1500, 15, %s", fee.Amount, fee.UnitAmount, fee.PreciseUnitAmount, fee.RateCardRateID, f.rate.ID)
		}
	})
	// source: spec/services/billing_segments/process_service_spec.rb:648
	t.Run("finalizes the invoice, links the contract and marks the segment done", func(t *testing.T) {
		t.Parallel()
		f := newSegmentFixture(t, 0)
		c := f.contract()
		f.segment(c, f.card, 5, "15.00")
		invs := f.process()
		if len(invs) != 1 {
			t.Fatalf("invoices = %d, want = 1", len(invs))
		}
		inv := f.Invoice(invs[0].ID)
		if inv.Status != invoice.StatusFinalized {
			t.Errorf("status = %s, want = finalized", inv.Status)
		}
		if got := inv.IssuingDate; got != core.DateOf(billingtest.Date(2026, time.September, 1), time.UTC) {
			t.Errorf("issuing date = %s, want = 2026-09-01", got)
		}
		// Tanvo links a contract like any subscription (lago links it
		// through its segments only).
		subs := f.invoiceSubscriptions(inv.ID)
		if len(subs) != 1 || subs[0].SubscriptionID != c.ID {
			t.Errorf("invoice subscriptions = %+v, want = the contract", subs)
		}
		fee := f.Fees(inv.ID)[0]
		if fee.SubscriptionID != c.ID || fee.ProductID.IsZero() {
			t.Errorf("fee subscription, product = %s, %s, want = the contract, the product", fee.SubscriptionID, fee.ProductID)
		}
		segs := f.segmentsOf(c)
		if len(segs) != 1 || segs[0].Status != subscription.SegmentDone || segs[0].InvoiceID != inv.ID {
			t.Errorf("segment = %+v, want = done on the invoice", segs[0])
		}
	})
	// source: spec/services/billing_segments/process_service_spec.rb:662
	t.Run("does not invoice a segment twice", func(t *testing.T) {
		t.Parallel()
		f := newSegmentFixture(t, 0)
		c := f.contract()
		f.segment(c, f.card, 5, "15.00")
		first := f.process()
		if again := f.process(); len(again) != 0 {
			t.Errorf("rerun invoices = %d, want = 0", len(again))
		}
		if got := f.Invoices(f.cust.ID); len(got) != 1 || got[0].ID != first[0].ID {
			t.Errorf("invoices = %d, want = the first one", len(got))
		}
		if got := f.segmentsOf(c)[0].InvoiceID; got != first[0].ID {
			t.Errorf("segment invoice = %s, want = %s", got, first[0].ID)
		}
	})
	// source: spec/services/billing_segments/process_service_spec.rb:702
	t.Run("leaves another customer's segments pending", func(t *testing.T) {
		t.Parallel()
		f := newSegmentFixture(t, 0)
		other := f.Customer()
		oc := f.Contract(other, nil, billingtest.SubscribedAt(billingtest.Date(2026, time.July, 1)))
		f.segment(oc, f.card, 1, "15.00", func(s *subscription.BillingSegment) { s.CustomerID = other.ID })
		f.segment(f.contract(), f.card, 5, "15.00")
		if invs := f.process(); len(invs) != 1 {
			t.Fatalf("invoices = %d, want = 1", len(invs))
		}
		if s := f.segmentsOf(oc)[0]; s.Status != subscription.SegmentPending || !s.InvoiceID.IsZero() {
			t.Errorf("other segment = %s, %s, want = pending without invoice", s.Status, s.InvoiceID)
		}
	})
	// source: spec/services/billing_segments/process_service_spec.rb:777
	t.Run("completes a fee below the rate's minimum with a true-up fee", func(t *testing.T) {
		t.Parallel()
		f := newSegmentFixture(t, 10_000)
		f.segment(f.contract(), f.card, 5, "10.00")
		invs := f.process()
		inv := f.Invoice(invs[0].ID)
		fees := f.Fees(inv.ID)
		if inv.TotalAmount != 10_000 || len(fees) != 2 {
			t.Fatalf("total, fees = %d, %d, want = 10000, 2", inv.TotalAmount, len(fees))
		}
		var fee, trueUp *invoice.Fee
		for _, x := range fees {
			if x.TrueUpParentFeeID.IsZero() {
				fee = x
			} else {
				trueUp = x
			}
		}
		if fee == nil || trueUp == nil || fee.Amount != 5000 || trueUp.Amount != 5000 || trueUp.TrueUpParentFeeID != fee.ID {
			t.Errorf("fee, true-up = %+v, %+v, want = 5000 and 5000 completing it", fee, trueUp)
		}
	})
	// source: spec/services/billing_segments/process_service_spec.rb:1155
	// source: spec/services/billing_segments/process_service_spec.rb:1167
	t.Run("closes a zero invoice unless the customer finalizes them", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			setting customer.ZeroAmountFinalization
			want    invoice.Status
		}{
			{customer.ZeroAmountFinalizationSkip, invoice.StatusClosed},
			{customer.ZeroAmountFinalizationFinalize, invoice.StatusFinalized},
		} {
			f := newSegmentFixture(t, 0)
			f.UpdateCustomer(f.cust, func(c *customer.Customer) { c.FinalizeZeroAmountInvoice = tc.setting })
			f.segment(f.contract(), f.card, 5, "0.00")
			if got := f.Invoice(f.process()[0].ID).Status; got != tc.want {
				t.Errorf("%s: status = %s, want = %s", tc.setting, got, tc.want)
			}
		}
	})
}

func TestProcessSegmentsGrouping(t *testing.T) {
	t.Parallel()
	// second stores the segment of a second card (another product) on c,
	// billed at the instant.
	second := func(f *segmentFixture, c *subscription.Subscription, cur money.Currency, at time.Time) *subscription.BillingSegment {
		card := f.rateCard("second-"+cur.String(), cur, 0)
		return f.segment(c, card, 3, "20.00", func(s *subscription.BillingSegment) { s.BillingAt = at })
	}
	sep := billingtest.Date(2026, time.September, 1)
	po := func(n string) func(*subscription.Subscription) {
		return func(s *subscription.Subscription) { s.PurchaseOrderNumber = &n }
	}
	for _, tc := range []struct {
		name   string
		source []string
		build  func(f *segmentFixture)
		want   []int
		pos    []string
	}{
		{
			name:   "consolidates the segments of one billing day",
			source: []string{"spec/services/billing_segments/process_service_spec.rb:1039"},
			build: func(f *segmentFixture) {
				c := f.contract()
				f.segment(c, f.card, 5, "15.00")
				second(f, c, billingtest.USD, sep.Add(10*time.Hour))
			},
			want: []int{2},
		},
		{
			name:   "invoices each segment of a contract that does not consolidate",
			source: []string{"spec/services/billing_segments/process_service_spec.rb:1049", "spec/services/billing_segments/process_service_spec.rb:561"},
			build: func(f *segmentFixture) {
				c := f.contract(func(s *subscription.Subscription) { s.ConsolidateInvoice = false })
				f.segment(c, f.card, 5, "15.00")
				second(f, c, billingtest.USD, sep)
			},
			want: []int{1, 1},
		},
		{
			name:   "consolidates contracts sharing a purchase order number",
			source: []string{"spec/services/billing_segments/process_service_spec.rb:1066", "spec/services/billing_segments/process_service_spec.rb:1076"},
			build: func(f *segmentFixture) {
				f.segment(f.contract(po("PO-SAME")), f.card, 5, "15.00")
				second(f, f.contract(po("PO-SAME")), billingtest.USD, sep)
			},
			want: []int{2},
			pos:  []string{"PO-SAME"},
		},
		{
			name:   "splits different billing days",
			source: []string{"spec/services/billing_segments/process_service_spec.rb:1094"},
			build: func(f *segmentFixture) {
				f.segment(f.contract(), f.card, 5, "15.00")
				second(f, f.contract(), billingtest.USD, sep.AddDate(0, 0, 1))
			},
			want: []int{1, 1},
		},
		{
			name:   "splits different currencies",
			source: []string{"spec/services/billing_segments/process_service_spec.rb:1100", "spec/services/billing_segments/process_service_spec.rb:567"},
			build: func(f *segmentFixture) {
				f.segment(f.contract(), f.card, 5, "15.00")
				second(f, f.contract(), money.MustCurrency("EUR"), sep)
			},
			want: []int{1, 1},
		},
		{
			name:   "splits different payment methods",
			source: []string{"spec/services/billing_segments/process_service_spec.rb:1112", "spec/services/billing_segments/process_service_spec.rb:619"},
			build: func(f *segmentFixture) {
				f.segment(f.contract(), f.card, 5, "15.00")
				second(f, f.contract(func(s *subscription.Subscription) { s.PaymentMethodType = core.PaymentMethodTypeManual }), billingtest.USD, sep)
			},
			want: []int{1, 1},
		},
		{
			name:   "splits different purchase order numbers",
			source: []string{"spec/services/billing_segments/process_service_spec.rb:1116", "spec/services/billing_segments/process_service_spec.rb:586"},
			build: func(f *segmentFixture) {
				f.segment(f.contract(po("PO-111")), f.card, 5, "15.00")
				second(f, f.contract(po("PO-222")), billingtest.USD, sep)
			},
			want: []int{1, 1},
			pos:  []string{"PO-111", "PO-222"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newSegmentFixture(t, 0)
			tc.build(f)
			invs := f.process()
			if got := f.feeCounts(invs); !slices.Equal(got, tc.want) {
				t.Errorf("fees per invoice = %v, want = %v", got, tc.want)
			}
			if tc.pos != nil {
				var got []string
				for _, inv := range invs {
					if n := f.Invoice(inv.ID).PurchaseOrderNumber; n != nil {
						got = append(got, *n)
					}
				}
				slices.Sort(got)
				if !slices.Equal(got, tc.pos) {
					t.Errorf("purchase orders = %v, want = %v", got, tc.pos)
				}
			}
		})
	}
	// source: spec/services/billing_segments/process_service_spec.rb:549
	t.Run("groups by the customer's local billing day", func(t *testing.T) {
		t.Parallel()
		f := newSegmentFixture(t, 0)
		ny, err := time.LoadLocation("America/New_York")
		if err != nil {
			t.Fatalf("LoadLocation() error = %v", err)
		}
		f.UpdateCustomer(f.cust, func(c *customer.Customer) { c.Timezone = ny })
		c := f.contract()
		f.segment(c, f.card, 5, "15.00", func(s *subscription.BillingSegment) { s.BillingAt = sep.Add(2 * time.Hour) })
		second(f, c, billingtest.USD, sep.Add(-time.Hour))
		invs := f.process()
		if got := f.feeCounts(invs); !slices.Equal(got, []int{2}) {
			t.Fatalf("fees per invoice = %v, want = [2]", got)
		}
		if got := f.Invoice(invs[0].ID).IssuingDate; got != core.DateOf(billingtest.Date(2026, time.August, 31), time.UTC) {
			t.Errorf("issuing date = %s, want = 2026-08-31", got)
		}
	})
}

// meteredCard creates a USD rate card of a new metered product of the
// metric priced at a standard amount, on the product filter selecting
// values (metric filter key, value pairs) when there are any.
func (f *segmentFixture) meteredCard(code string, m *catalog.BillableMetric, values ...string) (*catalog.RateCard, *catalog.ProductFilter) {
	f.T.Helper()
	ctx, org := f.Ctx, f.OrgID()
	p, err := f.cards.CreateProduct(ctx, org, products.ProductInput{Code: code, Name: code, ProductType: catalog.ProductTypeMetered, Metric: refs.ByID(m.ID)})
	if err != nil {
		f.T.Fatalf("CreateProduct() error = %v", err)
	}
	in := products.RateCardInput{
		Product: refs.ByID(p.ID), Code: code, Name: code, Currency: billingtest.USD,
		Rates: []products.RateInput{{
			Code: "base", EffectiveFrom: f.Now(), RateModel: catalog.ChargeModelStandard,
			Properties: catalog.MustProperties(`{"amount":"1.00"}`), BillingIntervalUnit: catalog.IntervalUnitMonth,
		}},
	}
	var pf *catalog.ProductFilter
	if len(values) > 0 {
		fv := make([]products.FilterValueInput, 0, len(values)/2)
		for i := 0; i < len(values); i += 2 {
			fv = append(fv, products.FilterValueInput{Key: values[i], Value: &values[i+1]})
		}
		if pf, err = f.cards.CreateFilter(ctx, org, p.ID, products.FilterInput{Code: code + "-filter", Name: code, Values: fv}); err != nil {
			f.T.Fatalf("CreateFilter() error = %v", err)
		}
		in.Filter = refs.ByID(pf.ID)
	}
	c, err := f.cards.CreateRateCard(ctx, org, in)
	if err != nil {
		f.T.Fatalf("CreateRateCard() error = %v", err)
	}
	return c, pf
}

// filtered returns a metric option adding a filter of the key and values.
func (f *segmentFixture) filtered(key string, values ...string) func(*catalog.BillableMetric) {
	return func(m *catalog.BillableMetric) {
		m.Filters = append(m.Filters, catalog.MetricFilter{ID: core.NewID[core.BillableMetricFilterID](f.IDs), Key: key, Values: values})
	}
}

// productFee returns the only fee of the invoice priced by the card.
func (f *segmentFixture) productFee(inv *invoice.Invoice, card *catalog.RateCard) *invoice.Fee {
	f.T.Helper()
	var out *invoice.Fee
	for _, fee := range f.Fees(inv.ID) {
		if fee.ProductID == card.ProductID && fee.TrueUpParentFeeID.IsZero() {
			if out != nil {
				f.T.Fatalf("fees of card %s: more than one", card.Code)
			}
			out = fee
		}
	}
	if out == nil {
		f.T.Fatalf("fees of card %s: none", card.Code)
	}
	return out
}

func TestProcessMeteredSegments(t *testing.T) {
	t.Parallel()
	aug := func(day int) time.Time { return billingtest.Date(2026, time.August, day) }
	type check struct {
		units, events, amount int64
	}
	assert := func(t *testing.T, fee *invoice.Fee, want check) {
		t.Helper()
		if fee.Units.Cmp(money.NewFromInt(want.units)) != 0 || fee.EventsCount != want.events || fee.Amount != want.amount {
			t.Errorf("fee units, events, amount = %s, %d, %d, want = %d, %d, %d", fee.Units, fee.EventsCount, fee.Amount, want.units, want.events, want.amount)
		}
	}
	// source: spec/services/billing_segments/process_service_spec.rb:148
	t.Run("prices a count metric by the events of the segment", func(t *testing.T) {
		t.Parallel()
		f := newSegmentFixture(t, 0)
		m := f.Metric("calls", catalog.AggregationTypeCount, "")
		card, _ := f.meteredCard("calls", m)
		c := f.contract()
		f.segment(c, card, 0, "15.00")
		f.Event(c, m.Code, aug(10))
		f.Event(c, m.Code, aug(20))
		invs := f.process()
		if len(invs) != 1 {
			t.Fatalf("invoices = %d, want = 1", len(invs))
		}
		assert(t, f.productFee(invs[0], card), check{2, 2, 3000})
	})
	// source: spec/services/billing_segments/process_service_spec.rb:188
	t.Run("prices a sum metric by the summed values", func(t *testing.T) {
		t.Parallel()
		f := newSegmentFixture(t, 0)
		m := f.Metric("storage", catalog.AggregationTypeSum, "quantity")
		card, _ := f.meteredCard("storage", m)
		c := f.contract()
		f.segment(c, card, 0, "2.50")
		f.Event(c, m.Code, aug(10), "quantity", "10")
		f.Event(c, m.Code, aug(20), "quantity", "10")
		invs := f.process()
		if len(invs) != 1 {
			t.Fatalf("invoices = %d, want = 1", len(invs))
		}
		assert(t, f.productFee(invs[0], card), check{20, 2, 5000})
	})
	// source: spec/services/billing_segments/process_service_spec.rb:431
	t.Run("counts only the events of each contract's segment", func(t *testing.T) {
		t.Parallel()
		f := newSegmentFixture(t, 0)
		m1 := f.Metric("first", catalog.AggregationTypeCount, "")
		m2 := f.Metric("second", catalog.AggregationTypeCount, "")
		card1, _ := f.meteredCard("first", m1)
		card2, _ := f.meteredCard("second", m2)
		c1 := f.contract()
		c2 := f.contract()
		f.segment(c1, card1, 0, "10.00", func(s *subscription.BillingSegment) { s.StartedAt, s.CycleStartedAt = aug(15), aug(15) })
		f.segment(c2, card2, 0, "20.00", func(s *subscription.BillingSegment) { s.EndedAt = aug(15) })
		for _, d := range []int{20, 25, 5, 10} {
			f.Event(c1, m1.Code, aug(d))
		}
		for _, d := range []int{5, 10, 12, 20, 28} {
			f.Event(c2, m2.Code, aug(d))
		}
		invs := f.process()
		if len(invs) != 1 {
			t.Fatalf("invoices = %d, want = 1", len(invs))
		}
		assert(t, f.productFee(invs[0], card1), check{2, 2, 2000})
		assert(t, f.productFee(invs[0], card2), check{3, 3, 6000})
		if got := f.Invoice(invs[0].ID).TotalAmount; got != 8000 {
			t.Errorf("total = %d, want = 8000", got)
		}
	})
	// source: spec/services/billing_segments/process_service_spec.rb:511
	t.Run("resolves each contract's events by its own filter keys", func(t *testing.T) {
		t.Parallel()
		f := newSegmentFixture(t, 0)
		m1 := f.Metric("regional", catalog.AggregationTypeCount, "", f.filtered("region", "eu", "us"))
		m2 := f.Metric("national", catalog.AggregationTypeCount, "", f.filtered("country", "fr", "de"))
		card1, pf1 := f.meteredCard("regional", m1, "region", "eu")
		card2, pf2 := f.meteredCard("national", m2, "country", "fr")
		c1 := f.contract()
		c2 := f.contract()
		f.segment(c1, card1, 0, "10.00")
		f.segment(c2, card2, 0, "20.00")
		f.Event(c1, m1.Code, aug(10), "region", "eu")
		f.Event(c1, m1.Code, aug(20), "region", "eu")
		for _, d := range []int{12, 18, 25} {
			f.Event(c2, m2.Code, aug(d), "country", "fr")
		}
		invs := f.process()
		if len(invs) != 1 {
			t.Fatalf("invoices = %d, want = 1", len(invs))
		}
		fee1, fee2 := f.productFee(invs[0], card1), f.productFee(invs[0], card2)
		assert(t, fee1, check{2, 2, 2000})
		assert(t, fee2, check{3, 3, 6000})
		if fee1.ProductFilterID != pf1.ID || fee2.ProductFilterID != pf2.ID {
			t.Errorf("fee filters = %s, %s, want = %s, %s", fee1.ProductFilterID, fee2.ProductFilterID, pf1.ID, pf2.ID)
		}
	})
	// A card whose filter no event reaches is priced without reading the
	// events (filtered_aggregations), and so is the default bucket when
	// every event matches a filter.
	//
	// source: app/services/billing_segments/process_service.rb:138-144, app/services/fees/charge_service.rb:492-505
	t.Run("prices nothing for a filter no event reaches", func(t *testing.T) {
		t.Parallel()
		f := newSegmentFixture(t, 0)
		m := f.Metric("regional", catalog.AggregationTypeCount, "", f.filtered("region", "eu", "us"))
		eu, _ := f.meteredCard("regional", m, "region", "eu")
		c := f.contract()
		f.segment(c, eu, 0, "10.00")
		f.Event(c, m.Code, aug(10), "region", "us")
		for _, inv := range f.process() {
			for _, fee := range f.Fees(inv.ID) {
				if fee.Amount != 0 || !fee.Units.IsZero() {
					t.Errorf("fee units, amount = %s, %d, want = 0, 0", fee.Units, fee.Amount)
				}
			}
		}
	})
}
