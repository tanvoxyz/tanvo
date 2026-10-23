package fees

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
	"github.com/tanvoxyz/tanvo/libs/pricing"
)

// Service is the fees service. Its methods compute fees in memory: the
// caller stores them, applies their taxes and emits their webhooks.
type Service struct {
	d *kernel.Deps
}

// New returns the service over the checked dependencies d.
func New(d *kernel.Deps) *Service { return &Service{d: d} }

// Scope is the subscription, the invoice and the periods fees are computed
// for.
type Scope struct {
	// Env is the subscription with its environment.
	Env *kernel.SubscriptionEnv
	// Invoice is the invoice the fees go on; nil for current usage and for
	// fees not attached to an invoice.
	Invoice *invoice.Invoice
	// InvoiceSubscription links Invoice to the subscription; nil when
	// Invoice is.
	InvoiceSubscription *invoice.Subscription
	// Boundaries are the periods of the run.
	Boundaries period.Boundaries
	// Timestamp is the billing instant; zero means the invoice
	// subscription's, else the clock's.
	Timestamp time.Time
}

// Options tune the computation of subscription, charge and fixed-charge
// fees.
type Options struct {
	// CurrentUsage computes the usage of the period so far: nothing is
	// checked as already billed, true-ups are skipped, and the units of
	// prorated and pay-in-advance charges are the current usage units.
	CurrentUsage bool
	// Preview computes the fees of an invoice that is not stored: adjusted
	// fees are ignored.
	Preview bool
	// Recurring keeps every fee, zero ones included (the fees of a
	// recurring invoice).
	Recurring bool
	// SkipZeroUnitsFilters drops, for current usage, the fees of charge
	// filters without units.
	SkipZeroUnitsFilters bool
	// SkipAdjustedFees ignores the adjustments of a draft invoice.
	SkipAdjustedFees bool
	// Cache returns the cached aggregations of a scope; nil means none are
	// kept.
	Cache func(sc event.CachedAggregationScope) metering.CachedAggregations
}

// CachedAggregation is a running aggregation state to store after a fee
// computation, for the next pay-in-advance event or the next period of a
// recurring metric.
type CachedAggregation struct {
	// ChargeID is the charge.
	ChargeID core.ChargeID
	// ChargeFilterID is the charge filter; zero for the default bucket.
	ChargeFilterID core.ChargeFilterID
	// ExternalSubscriptionID is the subscription's external ID.
	ExternalSubscriptionID string
	// Aggregation is the state.
	Aggregation metering.CachedAggregation
}

// ChargeResult is the outcome of a charge fee computation.
type ChargeResult struct {
	// Fees are the fees to store, true-up fees included.
	Fees []*invoice.Fee
	// Aggregations are the aggregation states to cache.
	Aggregations []CachedAggregation
}

// timestamp returns the billing instant of sc.
func (s *Service) timestamp(sc Scope) time.Time {
	switch {
	case !sc.Timestamp.IsZero():
		return sc.Timestamp
	case sc.InvoiceSubscription != nil && !sc.InvoiceSubscription.Timestamp.IsZero():
		return sc.InvoiceSubscription.Timestamp
	default:
		return s.d.Now()
	}
}

// newFee returns a pending fee of type t of the subscription of sc, on its
// invoice, in the plan's currency.
func (s *Service) newFee(sc Scope, t core.FeeType) *invoice.Fee {
	now := s.d.Now()
	f := &invoice.Fee{
		ID:                   core.NewID[core.FeeID](s.d.IDs),
		OrganizationID:       sc.Env.OrganizationID(),
		SubscriptionID:       sc.Env.Subscription.ID,
		Type:                 t,
		PreciseAmount:        money.Zero,
		PreciseUnitAmount:    money.Zero,
		Units:                money.Zero,
		TotalAggregatedUnits: money.Zero,
		TaxesPreciseAmount:   money.Zero,
		TaxesRate:            money.Zero,
		Currency:             sc.Env.Plan.Currency,
		Invoiceable:          true,
		PaymentStatus:        invoice.FeePaymentStatusPending,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if sc.Invoice != nil {
		f.InvoiceID = sc.Invoice.ID
	}
	return f
}

// chargeProperties returns the properties of a charge fee of sc: the
// subscription and charges periods.
func (s *Service) chargeProperties(sc Scope) invoice.FeeProperties {
	b := sc.Boundaries
	return invoice.FeeProperties{
		From:                b.Subscription.From,
		To:                  b.Subscription.To,
		ChargesFrom:         b.Charges.From,
		ChargesTo:           b.Charges.To,
		Timestamp:           s.timestamp(sc),
		ChargesDurationDays: b.ChargesDays,
	}
}

// setAmount copies a computed amount onto f.
func setAmount(f *invoice.Fee, a *pricing.FeeAmount, pu *catalog.PricingUnit) {
	f.Amount = a.AmountCents
	f.PreciseAmount = a.PreciseAmountCents
	f.UnitAmount = a.UnitAmountCents
	f.PreciseUnitAmount = a.PreciseUnitAmount
	f.PricingUnitUsage = nil
	if u := a.PricingUnitUsage; u != nil && pu != nil {
		// Amounts in the pricing unit are recorded in its minor units,
		// like the fee's own.
		f.PricingUnitUsage = &invoice.PricingUnitUsage{
			PricingUnitID:     pu.ID,
			ShortName:         pu.ShortName,
			ConversionRate:    u.ConversionRate,
			Amount:            money.NewFromInt(u.AmountCents),
			PreciseAmount:     u.PreciseAmountCents,
			UnitAmount:        money.NewFromInt(u.UnitAmountCents),
			PreciseUnitAmount: u.PreciseUnitAmount,
		}
	}
}

// details encodes the amount details of a model result.
func details(d pricing.Details) (json.RawMessage, error) {
	if d == nil {
		return nil, nil
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("encode amount details: %w", err)
	}
	return raw, nil
}

// groupedBy returns the fee grouping of pricing groups: a missing value is
// the empty string, as events without the property are grouped together.
func groupedBy(g metering.Groups) map[string]string {
	if len(g) == 0 {
		return nil
	}
	out := make(map[string]string, len(g))
	for k, v := range g {
		if v != nil {
			out[k] = *v
		} else {
			out[k] = ""
		}
	}
	return out
}

// sameGrouping reports whether two fee groupings are equal, nil and empty
// being equal.
func sameGrouping(a, b map[string]string) bool { return maps.Equal(a, b) }

// pricingCurrency is the currency a charge is priced in and its pricing
// unit.
type pricingCurrency struct {
	// fiat is the invoice currency.
	fiat money.Currency
	// unit is the currency the models price in: the pricing unit, or fiat.
	unit money.Currency
	// applied is the charge's pricing unit with its rate, or nil.
	applied *pricing.AppliedPricingUnit
	// pricingUnit is the charge's pricing unit, or nil.
	pricingUnit *catalog.PricingUnit
}

// chargeCurrency resolves the currency a charge is priced in.
func chargeCurrency(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv, c *catalog.Charge) (pricingCurrency, error) {
	pc := pricingCurrency{fiat: env.Plan.Currency, unit: env.Plan.Currency}
	if c.AppliedPricingUnit == nil {
		return pc, nil
	}
	pu, err := r.PricingUnits.Get(ctx, env.OrganizationID(), c.AppliedPricingUnit.PricingUnitID)
	if err != nil {
		return pricingCurrency{}, fmt.Errorf("load pricing unit of charge %s: %w", c.ID, err)
	}
	unit, err := money.NewCustomCurrency(pu.Code, pu.Name, catalog.PricingUnitExponent)
	if err != nil {
		return pricingCurrency{}, fmt.Errorf("pricing unit %s: %w", pu.ID, err)
	}
	pc.unit, pc.pricingUnit = unit, pu
	pc.applied = &pricing.AppliedPricingUnit{Unit: unit, ConversionRate: c.AppliedPricingUnit.ConversionRate}
	return pc, nil
}

// adjustments are the adjusted fees of a draft invoice for one
// subscription.
type adjustments []*invoice.AdjustedFee

// loadAdjustments returns the adjusted fees of the draft invoice of sc for
// its subscription, or none when they do not apply.
func loadAdjustments(ctx context.Context, r port.Repos, sc Scope, opts Options) (adjustments, error) {
	if sc.Invoice == nil || sc.Invoice.Status != invoice.StatusDraft {
		return nil, nil
	}
	return listAdjustments(ctx, r, sc, opts)
}

// listAdjustments returns the adjusted fees of the invoice of sc for its
// subscription, whatever the invoice's status; none for current usage,
// previews or when opts skip them.
func listAdjustments(ctx context.Context, r port.Repos, sc Scope, opts Options) (adjustments, error) {
	inv := sc.Invoice
	if inv == nil || opts.CurrentUsage || opts.Preview || opts.SkipAdjustedFees {
		return nil, nil
	}
	all, err := r.AdjustedFees.ListByInvoice(ctx, inv.OrganizationID, inv.ID)
	if err != nil {
		return nil, fmt.Errorf("load adjusted fees of invoice %s: %w", inv.ID, err)
	}
	var out adjustments
	for _, a := range all {
		if a.SubscriptionID == sc.Env.Subscription.ID {
			out = append(out, a)
		}
	}
	return out, nil
}

// subscription returns the adjustment of the subscription fee.
func (a adjustments) subscription() *invoice.AdjustedFee {
	for _, f := range a {
		if f.FeeType == core.FeeTypeSubscription {
			return f
		}
	}
	return nil
}

// charge returns the adjustment of a charge fee of a charge filter (zero
// for the default bucket) and pricing group for the charges period p.
func (a adjustments) charge(c core.ChargeID, filter core.ChargeFilterID, grouped map[string]string, p period.Period) *invoice.AdjustedFee {
	for _, f := range a {
		if f.FeeType == core.FeeTypeCharge && f.ChargeID == c && f.ChargeFilterID == filter && sameGrouping(f.GroupedBy, grouped) &&
			f.MatchesCharges(p.From, p.To) {
			return f
		}
	}
	return nil
}

// fixedCharge returns the adjustment of a fixed-charge fee for the fixed
// charges period p.
func (a adjustments) fixedCharge(fc core.FixedChargeID, p period.Period) *invoice.AdjustedFee {
	for _, f := range a {
		if f.FeeType == core.FeeTypeFixedCharge && f.FixedChargeID == fc && f.MatchesFixedCharges(p.From, p.To) {
			return f
		}
	}
	return nil
}

// invoiceFees returns the stored fees of the invoice of sc, or none.
func invoiceFees(ctx context.Context, r port.Repos, sc Scope) ([]*invoice.Fee, error) {
	if sc.Invoice == nil {
		return nil, nil
	}
	fs, err := r.Fees.ListByInvoice(ctx, sc.Invoice.OrganizationID, sc.Invoice.ID)
	if err != nil {
		return nil, fmt.Errorf("load fees of invoice %s: %w", sc.Invoice.ID, err)
	}
	return fs, nil
}

// subscriptionFees returns the stored fees of the types of a subscription.
func subscriptionFees(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv, types ...core.FeeType) ([]*invoice.Fee, error) {
	fs, err := kernel.All(func(p core.PageRequest) (core.Page[*invoice.Fee], error) {
		return r.Fees.List(ctx, env.OrganizationID(), invoice.FeeFilter{SubscriptionID: env.Subscription.ID, Types: types}, p)
	})
	if err != nil {
		return nil, fmt.Errorf("load fees of subscription %s: %w", env.Subscription.ID, err)
	}
	return fs, nil
}

// invoiceCount returns the number of invoices of the subscription,
// counting the invoice of sc.
func invoiceCount(ctx context.Context, r port.Repos, sc Scope) (int, error) {
	iss, err := r.InvoiceSubscriptions.ListBySubscription(ctx, sc.Env.OrganizationID(), sc.Env.Subscription.ID)
	if err != nil {
		return 0, fmt.Errorf("load invoices of subscription %s: %w", sc.Env.Subscription.ID, err)
	}
	ids := map[core.InvoiceID]bool{}
	for _, is := range iss {
		ids[is.InvoiceID] = true
	}
	if sc.Invoice != nil {
		ids[sc.Invoice.ID] = true
	}
	return len(ids), nil
}

// billedTo returns the end to count the billed days of a period ending at
// to: a period cut by a termination at local midnight also bills the day
// it ends on (lago Utils::Datetime.date_diff_with_timezone adds a second to
// an end at the beginning of a day). Any other end is kept, so a period
// from a mid-day instant lasting whole days is not billed an extra day.
func billedTo(to time.Time, terminatedAt *time.Time, loc *time.Location) time.Time {
	if terminatedAt != nil && to.Equal(*terminatedAt) && to.Equal(startOfDay(to, loc)) {
		return to.Add(time.Nanosecond)
	}
	return to
}

// startOfDay returns the instant the local day of t begins in loc, in
// UTC; a midnight skipped by DST resolves forward ([period.StartOfDay]).
func startOfDay(t time.Time, loc *time.Location) time.Time { return period.StartOfDay(t, loc) }

// isNotFound reports whether err is a missing record.
func isNotFound(err error) bool { return errors.Is(err, core.ErrNotFound) }

// deref returns *p, or zero when p is nil.
func deref(p *money.Decimal) money.Decimal {
	if p == nil {
		return money.Zero
	}
	return *p
}

// cloneDecimal returns a copy of *p, or nil when p is nil.
func cloneDecimal(p *money.Decimal) *money.Decimal {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// int64Of truncates d to an int64.
func int64Of(d money.Decimal) int64 {
	n, _ := d.Int64()
	return n
}
