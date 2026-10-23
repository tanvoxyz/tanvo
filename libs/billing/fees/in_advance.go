package fees

import (
	"context"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
	"github.com/tanvoxyz/tanvo/libs/pricing"
)

// InAdvanceEvent is the event a pay-in-advance charge fee is billed for.
type InAdvanceEvent struct {
	// ID is the stored event's ID; zero for an estimate.
	ID core.EventID
	// Event is the event.
	Event metering.Event
	// Estimate prices an event that is not stored (an estimate of its
	// fee); the aggregation then adds the event instead of excluding it.
	Estimate bool
}

// InAdvanceChargeFees returns the fee a pay-in-advance charge bills for one
// event (domain §22.7): the price of the period's usage with the event
// minus the price without it, in the charge filter the event matches (or
// the default bucket) and its pricing group. The fee is attached to no
// invoice; a non-invoiceable charge's fee is final as is, and the caller
// applies its taxes and emits fee.created. The result also carries the
// aggregation state to cache for the next event.
func (s *Service) InAdvanceChargeFees(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv, c *catalog.Charge, ev InAdvanceEvent, opts Options) (*ChargeResult, error) {
	ts := ev.Event.Timestamp.UTC()
	d, err := env.Dates(ts, period.CurrentUsage)
	if err != nil {
		return nil, err
	}
	b, ok := d.Boundaries()
	if !ok {
		return &ChargeResult{}, nil
	}
	cc, err := loadCharge(ctx, r, env, c)
	if err != nil {
		return nil, err
	}
	bk, err := cc.eventBucket(ev.Event.Properties)
	if err != nil {
		return nil, err
	}
	groups := metering.GroupsOf(ev.Event.Properties, bk.groupKeys())

	req := cc.request(env, bk, b, opts)
	req.GroupBy = nil
	req.GroupValues = groups
	req.PayInAdvance = true
	req.CurrentUsage = false
	req.Event = &ev.Event
	req.EventEstimate = ev.Estimate
	req.MaxTimestamp = ts
	m, err := metering.Aggregate(ctx, s.d.Events, req)
	if err != nil {
		return nil, fmt.Errorf("aggregate charge %s for event %s: %w", c.ID, ev.Event.TransactionID, err)
	}
	agg := aggregationOf(m)
	in := pricing.InAdvanceEvent{
		Persisted:               !ev.Estimate,
		Units:                   deref(m.PayInAdvanceAggregation),
		PreciseTotalAmountCents: deref(m.PayInAdvancePreciseTotalAmountCents),
		Value:                   deref(m.PayInAdvanceAggregation),
	}
	if p, ok := bk.props.(pricing.PercentageProperties); ok && (p.PerTransactionMinAmount != nil || p.PerTransactionMaxAmount != nil) {
		pe, err := metering.PerEvent(ctx, s.d.Events, req, metering.PerEventOptions{GroupValues: groups})
		if err != nil {
			return nil, fmt.Errorf("per-event values of charge %s: %w", c.ID, err)
		}
		agg.PerEvent = &pricing.PerEventAggregation{Values: pe.Values}
		if in.Persisted {
			without, err := metering.PerEvent(ctx, s.d.Events, req, metering.PerEventOptions{GroupValues: groups, ExcludeEvent: true})
			if err != nil {
				return nil, fmt.Errorf("per-event values of charge %s: %w", c.ID, err)
			}
			in.ValuesWithoutEvent = without.Values
		}
	}
	ir, err := pricing.ApplyInAdvance(cc.structure(bk.props), agg, in)
	if err != nil {
		return nil, fmt.Errorf("price event %s of charge %s: %w", ev.Event.TransactionID, c.ID, err)
	}
	amounts, err := pricing.ComputeAmounts(pricing.AmountsInput{Currency: cc.cur.fiat, InAdvance: &ir, PricingUnit: cc.cur.applied})
	if err != nil {
		return nil, fmt.Errorf("amounts of charge %s: %w", c.ID, err)
	}

	sc := Scope{Env: env, Boundaries: b, Timestamp: ts}
	f := s.newFee(sc, core.FeeTypeCharge)
	f.ChargeID = c.ID
	f.ChargeFilterID = bk.filterID()
	f.Properties = s.chargeProperties(sc)
	setAmount(f, amounts.Amount, cc.cur.pricingUnit)
	f.Units, f.TotalAggregatedUnits = ir.Units, ir.Units
	f.EventsCount = ir.Count
	f.GroupedBy = groupedBy(groups)
	f.PayInAdvance = true
	f.Invoiceable = c.Invoiceable
	f.PayInAdvanceEventID = ev.ID
	f.PayInAdvanceEventTransactionID = ev.Event.TransactionID
	if f.AmountDetails, err = details(ir.Details); err != nil {
		return nil, err
	}
	if f.AmountDetails == nil {
		f.AmountDetails = []byte("{}")
	}

	out := &ChargeResult{Fees: []*invoice.Fee{f}}
	if m.CurrentAggregation != nil || m.MaxAggregation != nil || m.MaxAggregationWithProration != nil {
		out.Aggregations = append(out.Aggregations, CachedAggregation{
			ChargeID:               c.ID,
			ChargeFilterID:         bk.filterID(),
			ExternalSubscriptionID: env.Subscription.ExternalID,
			Aggregation: metering.CachedAggregation{
				Timestamp:                   ts,
				TransactionID:               ev.Event.TransactionID,
				Groups:                      groups,
				CurrentAggregation:          deref(m.CurrentAggregation),
				MaxAggregation:              deref(m.MaxAggregation),
				MaxAggregationWithProration: deref(m.MaxAggregationWithProration),
			},
		})
	}
	return out, nil
}

// eventBucket returns the bucket an event falls in: the filter it matches,
// else the default bucket of a charge with filters, else the whole charge.
func (cc *chargeContext) eventBucket(props metering.Properties) (bucket, error) {
	if len(cc.charge.Filters) == 0 {
		p, err := cc.properties(cc.charge.Properties)
		if err != nil {
			return bucket{}, err
		}
		return bucket{props: p}, nil
	}
	matching, selected := metering.MatchChargeFilter(cc.filters, cc.metricF, props)
	if selected >= 0 {
		for i := range cc.charge.Filters {
			if f := &cc.charge.Filters[i]; f.ID.String() == matching[selected].ID {
				return cc.filterBucket(f)
			}
		}
	}
	return cc.defaultBucket()
}

// InAdvanceFixedChargeFee returns the fee a pay-in-advance fixed charge
// bills when its units change (lago
// Fees::BuildPayInAdvanceFixedChargeService): the units added over those
// already billed in the fixed charges period, prorated on the days left
// when the fixed charge is prorated. A decrease bills a zero fee, which
// documents the change; nothing is refunded. The fee is attached to no
// invoice.
func (s *Service) InAdvanceFixedChargeFee(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv, fc *catalog.FixedCharge, ev *subscription.FixedChargeEvent) (*invoice.Fee, error) {
	ts := ev.Timestamp.UTC()
	d, err := env.Dates(ts, period.CurrentUsage)
	if err != nil {
		return nil, err
	}
	p, ok := d.FixedChargesPeriod()
	if !ok {
		return nil, nil
	}
	days := d.FixedChargesDays()

	existing, err := subscriptionFees(ctx, r, env, core.FeeTypeFixedCharge)
	if err != nil {
		return nil, err
	}
	paid := money.Zero
	for _, f := range existing {
		if (f.FixedChargeID == fc.ID || (!fc.ParentID.IsZero() && f.FixedChargeID == fc.ParentID)) &&
			f.Properties.ChargesFrom.Equal(p.From) && f.Properties.ChargesTo.Equal(p.To) {
			paid = paid.Add(f.Units)
		}
	}
	delta := ev.Units.Sub(paid)

	sc := Scope{Env: env, Timestamp: ts}
	f := s.newFee(sc, core.FeeTypeFixedCharge)
	f.FixedChargeID = fc.ID
	f.PayInAdvance = true
	f.Properties = invoice.FeeProperties{
		From: p.From, To: p.To, ChargesFrom: p.From, ChargesTo: p.To,
		Timestamp: ts, ChargesDurationDays: days,
	}
	f.AmountDetails = []byte("{}")
	if !delta.IsPositive() {
		return f, nil
	}

	units := delta
	if fc.Prorated && days > 0 {
		left := period.DaysBetween(dateOf(ts), p.To, time.UTC)
		units = delta.Mul(money.NewFromInt(int64(left))).Div(money.NewFromInt(int64(days)))
	}
	props, err := pricing.DecodeProperties(pricing.ChargeModel(fc.ChargeModel), fc.Properties.Raw())
	if err != nil {
		return nil, fmt.Errorf("properties of fixed charge %s: %w", fc.ID, err)
	}
	cur := env.Plan.Currency
	res, err := pricing.Apply(pricing.PricingStructure{Properties: props, Prorated: fc.Prorated, Currency: cur},
		pricing.AggregationResult{Units: units, FullUnits: &units, Count: 1}, pricing.ApplyOptions{})
	if err != nil {
		return nil, fmt.Errorf("price fixed charge %s: %w", fc.ID, err)
	}
	sub := cur.SubunitToUnit()
	amountCents := cur.Round(res.Amount, money.HalfUp).Mul(sub)
	f.Amount = int64Of(amountCents)
	f.PreciseAmount = res.Amount.Mul(sub)
	f.UnitAmount = kernel.RoundCents(amountCents.Div(delta))
	f.PreciseUnitAmount = res.Amount.Div(delta)
	f.Units, f.TotalAggregatedUnits = delta, delta
	return f, nil
}
