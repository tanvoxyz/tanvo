package billingtest

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// USD is the default currency of the fixtures.
var USD = money.MustCurrency("USD")

func apply[T any](v *T, opts []func(*T)) {
	for _, o := range opts {
		o(v)
	}
}

// Customer creates a USD customer of the default billing entity.
func (h *Harness) Customer(opts ...func(*customer.Customer)) *customer.Customer {
	h.T.Helper()
	id := core.NewID[core.CustomerID](h.IDs)
	c := customer.New(h.OrgID(), id, "cust_"+id.String()[30:], h.BillingEntity.ID, h.Now())
	c.Currency = USD
	apply(c, opts)
	h.Do(func(ctx context.Context, r port.Repos) error {
		seq, err := r.Sequences.Next(ctx, h.OrgID(), port.SequenceKey{Name: port.SequenceCustomer, Scope: c.BillingEntityID.UUID()})
		if err != nil {
			return err
		}
		c.AssignSequence(seq, h.BillingEntity.DocumentNumberPrefix)
		return r.Customers.Create(ctx, c)
	})
	return c
}

// UpdateCustomer changes a stored customer.
func (h *Harness) UpdateCustomer(c *customer.Customer, fn func(*customer.Customer)) {
	h.T.Helper()
	fn(c)
	h.Do(func(ctx context.Context, r port.Repos) error { return r.Customers.Update(ctx, c) })
}

// Metric creates a billable metric with the code and aggregation; field is
// the aggregated property ("" for count).
func (h *Harness) Metric(code string, agg catalog.AggregationType, field string, opts ...func(*catalog.BillableMetric)) *catalog.BillableMetric {
	h.T.Helper()
	m := &catalog.BillableMetric{
		ID: core.NewID[core.BillableMetricID](h.IDs), OrganizationID: h.OrgID(),
		Code: code, Name: code, AggregationType: agg, CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	if field != "" {
		m.FieldName = &field
	}
	apply(m, opts)
	h.Do(func(ctx context.Context, r port.Repos) error { return r.Metrics.Create(ctx, m) })
	return m
}

// Plan creates a USD plan of the interval and amount (minor units), paid
// in arrears.
func (h *Harness) Plan(interval period.Interval, amount int64, opts ...func(*catalog.Plan)) *catalog.Plan {
	h.T.Helper()
	id := core.NewID[core.PlanID](h.IDs)
	p := &catalog.Plan{
		ID: id, OrganizationID: h.OrgID(), Code: "plan_" + id.String()[30:], Name: "Plan",
		Interval: interval, Amount: amount, Currency: USD, Status: catalog.PlanStatusActive,
		CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	apply(p, opts)
	h.Do(func(ctx context.Context, r port.Repos) error { return r.Plans.Create(ctx, p) })
	return p
}

// Charge creates an invoiceable charge of the plan on the metric, with the
// model and its JSON properties.
func (h *Harness) Charge(p *catalog.Plan, m *catalog.BillableMetric, model catalog.ChargeModel, properties string, opts ...func(*catalog.Charge)) *catalog.Charge {
	h.T.Helper()
	c := &catalog.Charge{
		ID: core.NewID[core.ChargeID](h.IDs), OrganizationID: h.OrgID(), PlanID: p.ID,
		BillableMetricID: m.ID, ChargeModel: model, Properties: catalog.MustProperties(properties),
		Invoiceable: true, CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	c.Code = "charge_" + c.ID.String()[30:]
	apply(c, opts)
	for i := range c.Filters {
		if c.Filters[i].ID.IsZero() {
			c.Filters[i].ID = core.NewID[core.ChargeFilterID](h.IDs)
		}
		c.Filters[i].ChargeID = c.ID
		if c.Filters[i].CreatedAt.IsZero() {
			c.Filters[i].CreatedAt = h.Now()
		}
	}
	h.Do(func(ctx context.Context, r port.Repos) error { return r.Charges.Create(ctx, c) })
	return c
}

// ChargeFilter returns a charge filter option with the values and the
// JSON properties.
func ChargeFilter(values map[string][]string, properties string) catalog.ChargeFilter {
	return catalog.ChargeFilter{Values: values, Properties: catalog.MustProperties(properties)}
}

// AddOn creates a USD add-on of the amount.
func (h *Harness) AddOn(amount int64, opts ...func(*catalog.AddOn)) *catalog.AddOn {
	h.T.Helper()
	id := core.NewID[core.AddOnID](h.IDs)
	a := &catalog.AddOn{
		ID: id, OrganizationID: h.OrgID(), Code: "addon_" + id.String()[30:], Name: "Add-on",
		Amount: amount, Currency: USD, CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	apply(a, opts)
	h.Do(func(ctx context.Context, r port.Repos) error { return r.AddOns.Create(ctx, a) })
	return a
}

// FixedCharge creates a fixed charge of the plan on the add-on.
func (h *Harness) FixedCharge(p *catalog.Plan, a *catalog.AddOn, model catalog.FixedChargeModel, properties string, units string, opts ...func(*catalog.FixedCharge)) *catalog.FixedCharge {
	h.T.Helper()
	f := &catalog.FixedCharge{
		ID: core.NewID[core.FixedChargeID](h.IDs), OrganizationID: h.OrgID(), PlanID: p.ID, AddOnID: a.ID,
		ChargeModel: model, Properties: catalog.MustProperties(properties), Units: money.MustParse(units),
		CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	f.Code = "fixed_" + f.ID.String()[30:]
	apply(f, opts)
	h.Do(func(ctx context.Context, r port.Repos) error { return r.FixedCharges.Create(ctx, f) })
	return f
}

// Tax creates a tax of the rate (percent).
func (h *Harness) Tax(code string, rate string, opts ...func(*catalog.Tax)) *catalog.Tax {
	h.T.Helper()
	t := &catalog.Tax{
		ID: core.NewID[core.TaxID](h.IDs), OrganizationID: h.OrgID(), Code: code, Name: code,
		Rate: money.MustParse(rate), CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	apply(t, opts)
	h.Do(func(ctx context.Context, r port.Repos) error { return r.Taxes.Create(ctx, t) })
	return t
}

// Commitment creates a minimum commitment of the plan.
func (h *Harness) Commitment(p *catalog.Plan, amount int64, opts ...func(*catalog.Commitment)) *catalog.Commitment {
	h.T.Helper()
	c := &catalog.Commitment{
		ID: core.NewID[core.CommitmentID](h.IDs), OrganizationID: h.OrgID(), PlanID: p.ID,
		CommitmentType: catalog.CommitmentTypeMinimumCommitment, Amount: amount,
		CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	apply(c, opts)
	h.Do(func(ctx context.Context, r port.Repos) error { return r.Commitments.Create(ctx, c) })
	return c
}

// UsageThreshold creates a progressive billing threshold of the plan.
func (h *Harness) UsageThreshold(p *catalog.Plan, amount int64, recurring bool, opts ...func(*catalog.UsageThreshold)) *catalog.UsageThreshold {
	h.T.Helper()
	u := &catalog.UsageThreshold{
		ID: core.NewID[core.UsageThresholdID](h.IDs), OrganizationID: h.OrgID(), PlanID: p.ID,
		Amount: amount, Recurring: recurring, CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	apply(u, opts)
	h.Do(func(ctx context.Context, r port.Repos) error { return r.UsageThresholds.Create(ctx, u) })
	return u
}

// FixedAmountCoupon creates a fixed-amount USD coupon used once.
func (h *Harness) FixedAmountCoupon(amount int64, opts ...func(*coupon.Coupon)) *coupon.Coupon {
	h.T.Helper()
	id := core.NewID[core.CouponID](h.IDs)
	c := coupon.New(h.OrgID(), id, "coupon_"+id.String()[30:], "Coupon", coupon.TypeFixedAmount, h.Now())
	c.Amount, c.Currency, c.Frequency = &amount, USD, coupon.FrequencyOnce
	apply(c, opts)
	h.Do(func(ctx context.Context, r port.Repos) error { return r.Coupons.Create(ctx, c) })
	return c
}

// PercentageCoupon creates a percentage coupon used once.
func (h *Harness) PercentageCoupon(rate string, opts ...func(*coupon.Coupon)) *coupon.Coupon {
	h.T.Helper()
	id := core.NewID[core.CouponID](h.IDs)
	c := coupon.New(h.OrgID(), id, "coupon_"+id.String()[30:], "Coupon", coupon.TypePercentage, h.Now())
	r := money.MustParse(rate)
	c.PercentageRate, c.Frequency = &r, coupon.FrequencyOnce
	apply(c, opts)
	h.Do(func(ctx context.Context, rp port.Repos) error { return rp.Coupons.Create(ctx, c) })
	return c
}

// ApplyCoupon applies a coupon to a customer.
func (h *Harness) ApplyCoupon(c *coupon.Coupon, cust *customer.Customer, opts ...func(*coupon.AppliedCoupon)) *coupon.AppliedCoupon {
	h.T.Helper()
	a := coupon.Apply(c, core.NewID[core.AppliedCouponID](h.IDs), cust.ID, coupon.Overrides{}, h.Now())
	apply(a, opts)
	h.Do(func(ctx context.Context, r port.Repos) error { return r.AppliedCoupons.Create(ctx, a) })
	return a
}

// Subscription creates a subscription of the customer to the plan,
// subscribed at the clock's instant and activated when that instant has
// come (started at subscription_at). Options run before activation.
func (h *Harness) Subscription(c *customer.Customer, p *catalog.Plan, opts ...func(*subscription.Subscription)) *subscription.Subscription {
	h.T.Helper()
	id := core.NewID[core.SubscriptionID](h.IDs)
	s := subscription.New(h.OrgID(), id, c.ID, p.ID, "sub_"+id.String()[30:], h.Now(), subscription.PlanFacts{PayInAdvance: p.PayInAdvance}, h.Now())
	apply(s, opts)
	if !s.SubscriptionAt.After(h.Now()) && s.Status == subscription.StatusPending {
		if err := s.Activate(s.SubscriptionAt, h.Now()); err != nil {
			h.T.Fatalf("activate subscription: %v", err)
		}
	}
	h.Do(func(ctx context.Context, r port.Repos) error { return r.Subscriptions.Create(ctx, s) })
	return s
}

// SubscribedAt is a subscription option setting subscription_at.
func SubscribedAt(t time.Time) func(*subscription.Subscription) {
	return func(s *subscription.Subscription) { s.SubscriptionAt = t.UTC() }
}

// Anniversary is a subscription option for anniversary billing.
func Anniversary(s *subscription.Subscription) { s.BillingTime = period.Anniversary }

// UpdateSubscription changes a stored subscription.
func (h *Harness) UpdateSubscription(s *subscription.Subscription, fn func(*subscription.Subscription)) {
	h.T.Helper()
	fn(s)
	h.Do(func(ctx context.Context, r port.Repos) error { return r.Subscriptions.Update(ctx, s) })
}

// Wallet creates an active USD wallet of the customer at rate 1 with the
// balances of credits (decimal credits, rate 1 so equal to the amount in
// units).
func (h *Harness) Wallet(c *customer.Customer, credits string, opts ...func(*wallet.Wallet)) *wallet.Wallet {
	h.T.Helper()
	w := wallet.New(h.OrgID(), core.NewID[core.WalletID](h.IDs), c.ID, USD, money.One, h.Now())
	cr := money.MustParse(credits)
	minor, err := USD.ToMinor(cr, money.HalfUp)
	if err != nil {
		h.T.Fatalf("wallet credits: %v", err)
	}
	w.CreditsBalance, w.BalanceAmount = cr, minor
	w.CreditsOngoingBalance, w.OngoingBalanceAmount = cr, minor
	apply(w, opts)
	h.Do(func(ctx context.Context, r port.Repos) error { return r.Wallets.Create(ctx, w) })
	return w
}

// Event stores a usage event of the subscription for the metric code at ts
// with properties (key, value pairs).
func (h *Harness) Event(s *subscription.Subscription, code string, ts time.Time, props ...string) metering.Event {
	h.T.Helper()
	if len(props)%2 != 0 {
		h.T.Fatalf("event properties: odd number of values")
	}
	e := metering.Event{
		OrganizationID: h.OrgID().String(), SubscriptionID: s.ExternalID, Code: code,
		TransactionID: h.nextTransactionID(), Timestamp: ts.UTC(), IngestedAt: h.Now(),
	}
	if len(props) > 0 {
		e.Properties = metering.Properties{}
		for i := 0; i < len(props); i += 2 {
			e.Properties[props[i]] = props[i+1]
		}
	}
	h.Events.Add(e)
	return e
}

// Date returns the UTC instant of a date and time (hour, minute).
func Date(year int, month time.Month, day int, hm ...int) time.Time {
	hour, minute := 0, 0
	if len(hm) > 0 {
		hour = hm[0]
	}
	if len(hm) > 1 {
		minute = hm[1]
	}
	return time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
}
