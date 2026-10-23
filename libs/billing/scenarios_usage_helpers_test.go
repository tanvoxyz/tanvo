package billing_test

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// usgCharge is one entry of lago's charges_usage: the fees of a charge
// summed over its filters and groups.
type usgCharge struct {
	charge core.ChargeID
	units  money.Decimal
	events int64
	amount int64
	fees   []*invoice.Fee
}

// group returns the summed fees of the charge whose grouped_by is kv
// (key, value pairs); ok is false when none matches.
func (c usgCharge) group(kv ...string) (usgCharge, bool) {
	want := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		want[kv[i]] = kv[i+1]
	}
	return c.where(func(f *invoice.Fee) bool { return maps.Equal(f.GroupedBy, want) })
}

// filter returns the summed fees of the charge filter id (zero for the
// default bucket).
func (c usgCharge) filter(id core.ChargeFilterID) (usgCharge, bool) {
	return c.where(func(f *invoice.Fee) bool { return f.ChargeFilterID == id })
}

// where sums the fees of c matching keep.
func (c usgCharge) where(keep func(*invoice.Fee) bool) (usgCharge, bool) {
	out := usgCharge{charge: c.charge, units: money.Zero}
	for _, f := range c.fees {
		if keep(f) {
			out.add(f)
		}
	}
	return out, len(out.fees) > 0
}

func (c *usgCharge) add(f *invoice.Fee) {
	c.units = c.units.Add(f.Units)
	c.events += f.EventsCount
	c.amount += f.Amount
	c.fees = append(c.fees, f)
}

// usgUsage is lago's customer_usage of one subscription.
type usgUsage struct {
	*usage.Usage
	charges []usgCharge
}

// charge returns the usage of charge id.
func (u *usgUsage) charge(t *testing.T, id core.ChargeID) usgCharge {
	t.Helper()
	for _, c := range u.charges {
		if c.charge == id {
			return c
		}
	}
	t.Fatalf("no usage of charge %s", id)
	return usgCharge{}
}

// first returns the first charge usage (lago charges_usage.first).
func (u *usgUsage) first(t *testing.T) usgCharge {
	t.Helper()
	if len(u.charges) == 0 {
		t.Fatalf("charges_usage is empty")
	}
	return u.charges[0]
}

// usgCurrent fetches the current usage of sub at the clock (lago
// fetch_current_usage).
func (s *scenario) usgCurrent(sub *subscription.Subscription, opts ...func(*usage.Options)) *usgUsage {
	s.T.Helper()
	var o usage.Options
	for _, f := range opts {
		f(&o)
	}
	u := billingtest.Read(s.Harness, func(ctx context.Context, r port.Repos) (*usage.Usage, error) {
		return s.E.Usage.CurrentUsage(ctx, r, s.OrgID(), sub.ID, o)
	})
	return usgWrap(u)
}

// usgWrap groups the fees of u per charge in fee order.
func usgWrap(u *usage.Usage) *usgUsage {
	out := &usgUsage{Usage: u}
	idx := map[core.ChargeID]int{}
	for _, f := range u.Fees {
		i, ok := idx[f.ChargeID]
		if !ok {
			i = len(out.charges)
			idx[f.ChargeID] = i
			out.charges = append(out.charges, usgCharge{charge: f.ChargeID, units: money.Zero})
		}
		out.charges[i].add(f)
	}
	return out
}

// usgFees returns every stored fee of the organization's subscriptions
// subs (lago Fee.count when subs is the only subscription).
func (s *scenario) usgFees(subs ...*subscription.Subscription) []*invoice.Fee {
	s.T.Helper()
	var out []*invoice.Fee
	for _, sub := range subs {
		fs := billingtest.Read(s.Harness, func(ctx context.Context, r port.Repos) ([]*invoice.Fee, error) {
			return kernel.All(func(p core.PageRequest) (core.Page[*invoice.Fee], error) {
				return r.Fees.List(ctx, s.OrgID(), invoice.FeeFilter{SubscriptionID: sub.ID}, p)
			})
		})
		out = append(out, fs...)
	}
	return out
}

// usgSub creates a calendar subscription of c to p at the clock (lago
// create_subscription).
func (s *scenario) usgSub(c *customer.Customer, p *catalog.Plan, opts ...func(*subscription.Subscription)) *subscription.Subscription {
	s.T.Helper()
	return s.Subscription(c, p, opts...)
}

// usgEvent ingests an event of sub for metric code at the clock with
// properties (key, value pairs), running the in-advance charge fees.
func (s *scenario) usgEvent(sub *subscription.Subscription, code string, props ...string) {
	s.T.Helper()
	s.ingest(sub, code, props...)
}

// usgEventAt is [scenario.usgEvent] with the event timestamp ts.
func (s *scenario) usgEventAt(sub *subscription.Subscription, code string, ts time.Time, props ...string) {
	s.T.Helper()
	s.ingestAt(sub, code, ts, props...)
}

// usgMonthly creates a monthly in-arrears plan of amount.
func (s *scenario) usgMonthly(amount int64, opts ...func(*catalog.Plan)) *catalog.Plan {
	s.T.Helper()
	return s.Plan(period.Monthly, amount, opts...)
}

// usgSubscriptionInvoices returns the invoices of sub, oldest first.
func (s *scenario) usgSubscriptionInvoices(c *customer.Customer, sub *subscription.Subscription) []*invoice.Invoice {
	s.T.Helper()
	var out []*invoice.Invoice
	for _, inv := range s.Invoices(c.ID) {
		links := billingtest.Read(s.Harness, func(ctx context.Context, r port.Repos) ([]*invoice.Subscription, error) {
			return r.InvoiceSubscriptions.ListByInvoice(ctx, s.OrgID(), inv.ID)
		})
		if slices.ContainsFunc(links, func(l *invoice.Subscription) bool { return l.SubscriptionID == sub.ID }) {
			out = append(out, inv)
		}
	}
	return out
}

// usgChargeFees returns the charge fees of an invoice.
func (s *scenario) usgChargeFees(id core.InvoiceID) []*invoice.Fee {
	s.T.Helper()
	var out []*invoice.Fee
	for _, f := range s.Fees(id) {
		if f.Type == core.FeeTypeCharge {
			out = append(out, f)
		}
	}
	return out
}

// usgUnits asserts that d reads want (compared as decimals).
func usgUnits(t *testing.T, what string, d money.Decimal, want string) {
	t.Helper()
	if !d.Equal(money.MustParse(want)) {
		t.Errorf("%s units = %s, want = %s", what, d, want)
	}
}

// usgEq asserts got equals want.
func usgEq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want = %v", what, got, want)
	}
}

// usgRecurring is a metric option making it recurring.
func usgRecurring(m *catalog.BillableMetric) { m.Recurring = true }

// usgInAdvance is a charge option paying it in advance.
func usgInAdvance(c *catalog.Charge) { c.PayInAdvance = true }

// usgProrated is a charge option prorating it.
func usgProrated(c *catalog.Charge) { c.Prorated = true }

// usgFilters is a charge option with filters.
func usgFilters(fs ...catalog.ChargeFilter) func(*catalog.Charge) {
	return func(c *catalog.Charge) { c.Filters = append(c.Filters, fs...) }
}

// usgMetricFilter is a metric option adding a filter key with values.
func usgMetricFilter(key string, values ...string) func(*catalog.BillableMetric) {
	return func(m *catalog.BillableMetric) {
		m.Filters = append(m.Filters, catalog.MetricFilter{Key: key, Values: values})
	}
}

// usgDT is lago DateTime.new(y, m, d, h, mi, s) in UTC.
func usgDT(y int, m time.Month, d int, hms ...int) time.Time {
	v := [3]int{}
	copy(v[:], hms)
	return time.Date(y, m, d, v[0], v[1], v[2], 0, time.UTC)
}

// usgParse parses a UTC RFC 3339 instant without zone ("2024-02-01T01:00:00").
func usgParse(t *testing.T, v string) time.Time {
	t.Helper()
	at, err := time.Parse("2006-01-02T15:04:05", v)
	if err != nil {
		t.Fatalf("parse %q: %v", v, err)
	}
	return at.UTC()
}

// usgDynamicEvent stores an event of a dynamic charge carrying amount
// (precise cents) at the clock.
func (s *scenario) usgDynamicEvent(sub *subscription.Subscription, code, amount string, props ...string) {
	s.T.Helper()
	d := money.MustParse(amount)
	e := metering.Event{
		OrganizationID: s.OrgID().String(), SubscriptionID: sub.ExternalID, Code: code,
		TransactionID: fmt.Sprintf("usg-dyn-%d", len(s.Events.All())), Timestamp: s.Now(), IngestedAt: s.Now(),
		Properties: metering.Properties{}, PreciseTotalAmountCents: &d,
	}
	for i := 0; i+1 < len(props); i += 2 {
		e.Properties[props[i]] = props[i+1]
	}
	s.Events.Add(e)
}

// usgTaxBE creates a tax of rate applied to the default billing entity
// (lago :applied_to_billing_entity).
func (s *scenario) usgTaxBE(rate string) *catalog.Tax {
	s.T.Helper()
	t := s.Tax("tax_"+rate, rate)
	s.UpdateBillingEntity(func(be *organization.BillingEntity) { be.TaxIDs = append(be.TaxIDs, t.ID) })
	return t
}

// usgPricingUnit creates a pricing unit with code and short name.
func (s *scenario) usgPricingUnit(code, short string) *catalog.PricingUnit {
	s.T.Helper()
	pu := &catalog.PricingUnit{
		ID: core.NewID[core.PricingUnitID](s.IDs), OrganizationID: s.OrgID(),
		Code: code, Name: code, ShortName: short, CreatedAt: s.Now(), UpdatedAt: s.Now(),
	}
	s.Do(func(ctx context.Context, r port.Repos) error { return r.PricingUnits.Create(ctx, pu) })
	return pu
}

// usgPriced is a charge option pricing it in pu at rate (lago
// :applied_pricing_unit).
func usgPriced(pu *catalog.PricingUnit, rate string) func(*catalog.Charge) {
	return func(c *catalog.Charge) {
		c.AppliedPricingUnit = &catalog.AppliedPricingUnit{PricingUnitID: pu.ID, ConversionRate: money.MustParse(rate)}
	}
}

// pricingUnits sums the pricing unit amounts of c's fees; ok is false when
// no fee records one (lago pricing_unit_details nil).
func (c usgCharge) pricingUnits() (short string, rate, amount money.Decimal, ok bool) {
	amount = money.Zero
	for _, f := range c.fees {
		if u := f.PricingUnitUsage; u != nil {
			short, rate, amount, ok = u.ShortName, u.ConversionRate, amount.Add(u.Amount), true
		}
	}
	return short, rate, amount, ok
}

// usgPassTime is lago pass_time(days.days) of the time travel context: it
// bills, then moves to time0 + passed + i days for i < days, billing after
// each move. Like lago it ends one day short of passed + days; *passed is
// advanced by days.
func (s *scenario) usgPassTime(time0 time.Time, passed *int, days int) {
	s.T.Helper()
	for i := range days {
		s.performBilling()
		s.travelTo(time0.AddDate(0, 0, *passed+i))
	}
	s.performBilling()
	*passed += days
}

// usgLifetime returns the lifetime usage of sub.
func (s *scenario) usgLifetime(sub *subscription.Subscription) *subscription.LifetimeUsage {
	s.T.Helper()
	return billingtest.Read(s.Harness, func(ctx context.Context, r port.Repos) (*subscription.LifetimeUsage, error) {
		return r.LifetimeUsages.GetBySubscription(ctx, s.OrgID(), sub.ID)
	})
}
