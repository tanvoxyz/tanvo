package contracts

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// InAdvanceSegments returns the billing segments that price an event of
// metric m at ts on the contract ct in advance (lago
// Events::PayInAdvanceBillingSegmentResolver), not stored: for each of
// the contract's products and product filters of the metric, the card
// effective on the event's day in the customer's time zone, when it bills
// in advance (a newer arrears card supersedes an older advance one), with
// the segment of its schedule covering ts. A contract that is not active
// has none. The segments are pending, built from the card's schedule
// without resuming at its stored cycles; a card without rates has none.
//
// source: app/services/events/pay_in_advance_billing_segment_resolver.rb:12-77, app/services/billing_segments/build_event_segment_service.rb:15-34
func (s *Service) InAdvanceSegments(ctx context.Context, r port.Repos, ct *subscription.Subscription, m *catalog.BillableMetric, ts time.Time) ([]fees.ProductSegment, error) {
	if !ct.Contract || ct.Status != subscription.StatusActive {
		return nil, nil
	}
	org := ct.OrganizationID
	cards, err := r.ContractRateCards.ListBySubscription(ctx, org, ct.ID)
	if err != nil {
		return nil, fmt.Errorf("list rate cards of %s: %w", ct.ID, err)
	}
	type entry struct {
		card    *subscription.RateCard
		rc      *catalog.RateCard
		product *catalog.Product
	}
	type groupKey struct {
		product core.ProductID
		filter  core.ProductFilterID
	}
	products := map[core.ProductID]*catalog.Product{}
	var entries []entry
	for _, card := range cards {
		rc, err := r.RateCards.Get(ctx, org, card.RateCardID)
		if err != nil {
			return nil, fmt.Errorf("load rate card %s: %w", card.RateCardID, err)
		}
		p, ok := products[rc.ProductID]
		if !ok {
			if p, err = r.Products.Get(ctx, org, rc.ProductID); err != nil {
				return nil, fmt.Errorf("load product %s: %w", rc.ProductID, err)
			}
			products[rc.ProductID] = p
		}
		if p.ProductType != catalog.ProductTypeMetered || p.BillableMetricID != m.ID {
			continue
		}
		entries = append(entries, entry{card: card, rc: rc, product: p})
	}
	if len(entries) == 0 {
		return nil, nil
	}
	slices.SortStableFunc(entries, func(a, b entry) int {
		return cmp.Or(a.card.EffectiveDate.Compare(b.card.EffectiveDate),
			a.card.CreatedAt.Compare(b.card.CreatedAt),
			cmp.Compare(a.card.ID.String(), b.card.ID.String()))
	})
	cenv, err := customerEnv(ctx, r, ct)
	if err != nil {
		return nil, err
	}
	day := core.DateOf(ts, cenv.Location)
	// The latest version of each product and filter effective on the day.
	selected := map[groupKey]int{}
	var order []groupKey
	for i, e := range entries {
		k := groupKey{product: e.rc.ProductID, filter: e.rc.ProductFilterID}
		if e.card.EffectiveDate.After(day) {
			continue
		}
		if _, ok := selected[k]; !ok {
			order = append(order, k)
		}
		selected[k] = i
	}
	var out []fees.ProductSegment
	for _, k := range order {
		e := entries[selected[k]]
		if e.rc.BillingTiming != catalog.BillingTimingAdvance {
			continue
		}
		sch, err := buildScheduleFrom(ctx, r, ct, cenv.Location, e.card, false)
		if nf := (*core.NotFoundError)(nil); errors.As(err, &nf) && nf.Resource == ResourceRate {
			// A card without rates prices no event.
			continue
		}
		if err != nil {
			return nil, err
		}
		seg := sch.SegmentCovering(ts)
		if seg == nil {
			continue
		}
		row, err := s.segmentRow(ct, e.card, sch, *seg, subscription.SegmentPending, s.d.Now())
		if err != nil {
			return nil, err
		}
		out = append(out, fees.ProductSegment{
			Segment: row, Contract: ct, Card: e.card, RateCard: e.rc, Product: e.product, Location: cenv.Location,
		})
	}
	return out, nil
}
