package memstore

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// contractRepo implements subscription.ContractRepository over the
// subscriptions marked as contracts.
type contractRepo struct{ st *state }

var _ subscription.ContractRepository = contractRepo{}

func (r contractRepo) contracts(pred func(*subscription.Subscription) bool) []*subscription.Subscription {
	return r.st.subscription.subscriptions.find(func(x *subscription.Subscription) bool { return x.Contract && pred(x) })
}

// latestStarted orders by started_at (subscription_at), latest first, then
// by created_at, latest first.
func latestStarted(a, b *subscription.Subscription) int {
	return cmp.Or(b.SubscriptionAt.Compare(a.SubscriptionAt), b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(a.ID.String(), b.ID.String()))
}

func (r contractRepo) GetLive(_ context.Context, org core.OrganizationID, externalID string) (*subscription.Subscription, error) {
	rows := r.contracts(func(x *subscription.Subscription) bool {
		return x.OrganizationID == org && x.ExternalID == externalID &&
			(x.Status == subscription.StatusPending || x.Status == subscription.StatusActive)
	})
	slices.SortStableFunc(rows, func(a, b *subscription.Subscription) int {
		pa, pb := a.Status != subscription.StatusPending, b.Status != subscription.StatusPending
		if pa != pb {
			if pa {
				return 1
			}
			return -1
		}
		return latestStarted(a, b)
	})
	if len(rows) == 0 {
		return nil, notFound("contract", stringer(externalID))
	}
	return rows[0], nil
}

func (r contractRepo) GetByStatus(_ context.Context, org core.OrganizationID, externalID string, status subscription.Status) (*subscription.Subscription, error) {
	rows := r.contracts(func(x *subscription.Subscription) bool {
		return x.OrganizationID == org && x.ExternalID == externalID && x.Status == status
	})
	slices.SortStableFunc(rows, latestStarted)
	if len(rows) == 0 {
		return nil, notFound("contract", stringer(externalID))
	}
	return rows[0], nil
}

func (r contractRepo) GetTerminatable(ctx context.Context, org core.OrganizationID, externalID string) (*subscription.Subscription, error) {
	if s, err := r.GetByStatus(ctx, org, externalID, subscription.StatusActive); err == nil {
		return s, nil
	}
	return r.GetByStatus(ctx, org, externalID, subscription.StatusPending)
}

func (r contractRepo) List(_ context.Context, org core.OrganizationID, f subscription.ContractFilter, page core.PageRequest) (core.Page[*subscription.Subscription], error) {
	if err := f.Validate(); err != nil {
		return core.Page[*subscription.Subscription]{}, fmt.Errorf("list contracts: %w", err)
	}
	search := strings.ToLower(core.SearchTerm(f.Search))
	if f.ExternalID != "" {
		search = ""
	}
	subs := subscriptionRepo{d: r.st.subscription, st: r.st}
	rows := r.contracts(func(x *subscription.Subscription) bool {
		if x.OrganizationID != org ||
			f.ExternalID != "" && x.ExternalID != f.ExternalID ||
			len(f.Statuses) > 0 && !slices.Contains(f.Statuses, x.Status) {
			return false
		}
		c, ok := r.st.customer.customers.get(x.CustomerID)
		if !ok {
			return false
		}
		if f.ExternalCustomerID != "" && c.ExternalID != f.ExternalCustomerID {
			return false
		}
		if f.PlanCode != "" {
			p, ok := r.st.catalog.plans.get(x.PlanID)
			if !ok || p.Code != f.PlanCode {
				return false
			}
		}
		if len(f.BillingEntityIDs) > 0 {
			entity := x.BillingEntityID
			if entity.IsZero() {
				entity = c.BillingEntityID
			}
			if !slices.Contains(f.BillingEntityIDs, entity) {
				return false
			}
		}
		if f.HasRateOverrides != nil && r.hasRateOverrides(x.ID) != *f.HasRateOverrides {
			return false
		}
		return search == "" || subs.searchMatches(x, search, f.ExternalCustomerID == "")
	})
	return paginateRecent(rows, recentKey(
		func(x *subscription.Subscription) time.Time { return x.CreatedAt },
		func(x *subscription.Subscription) uuid.UUID { return x.ID.UUID() }), page)
}

// hasRateOverrides reports whether a kept card of the contract has a
// kept phase with an override.
func (r contractRepo) hasRateOverrides(sub core.SubscriptionID) bool {
	return r.st.subscription.rateCards.any(func(c *subscription.RateCard) bool {
		return c.SubscriptionID == sub && kept(c.DeletedAt) &&
			r.st.productCatalog.phases.any(func(p *catalog.RatePhase) bool {
				return p.SubscriptionRateCardID == c.ID && kept(p.DeletedAt) && p.Override != nil
			})
	})
}

func (r contractRepo) ListDueForActivation(_ context.Context, org core.OrganizationID, t time.Time) ([]*subscription.Subscription, error) {
	return r.contracts(func(x *subscription.Subscription) bool {
		if x.OrganizationID != org || x.Status != subscription.StatusPending || x.SubscriptionAt.After(t) {
			return false
		}
		c, ok := r.st.customer.customers.get(x.CustomerID)
		return ok && !c.Deleted()
	}), nil
}

func (r contractRepo) ExistsForPlan(_ context.Context, org core.OrganizationID, plan core.PlanID) (bool, error) {
	return len(r.contracts(func(x *subscription.Subscription) bool {
		return x.OrganizationID == org && x.PlanID == plan
	})) > 0, nil
}

// contractRateCardRepo implements subscription.RateCardRepository.
type contractRateCardRepo struct{ st *state }

var _ subscription.RateCardRepository = contractRateCardRepo{}

func (r contractRateCardRepo) cards() *table[core.SubscriptionRateCardID, subscription.RateCard] {
	return r.st.subscription.rateCards
}

func (r contractRateCardRepo) Create(_ context.Context, c *subscription.RateCard, phases []*catalog.RatePhase) error {
	if r.cards().has(c.ID) {
		return conflict("contract_rate_card", "id")
	}
	if r.cards().any(func(x *subscription.RateCard) bool {
		return x.OrganizationID == c.OrganizationID && x.SubscriptionID == c.SubscriptionID && x.RateCardID == c.RateCardID && kept(x.DeletedAt)
	}) {
		return conflict("contract_rate_card", "rate_card")
	}
	if err := r.putPhases(c.ID, phases, c.CreatedAt); err != nil {
		return err
	}
	r.cards().put(c.ID, *c)
	return nil
}

// putPhases makes phases the kept phases of the card id, discarding the
// others at at.
func (r contractRateCardRepo) putPhases(id core.SubscriptionRateCardID, phases []*catalog.RatePhase, at time.Time) error {
	codes, positions := map[string]bool{}, map[int]bool{}
	for _, ph := range phases {
		if codes[ph.Code] || positions[ph.Position] {
			return conflict("rate_phase", "code")
		}
		codes[ph.Code], positions[ph.Position] = true, true
	}
	r.discardPhases(id, func(p core.RatePhaseID) bool {
		return slices.ContainsFunc(phases, func(ph *catalog.RatePhase) bool { return ph.ID == p })
	}, at)
	for _, ph := range phases {
		x := *ph
		x.PlanRateCardID = core.PlanRateCardID{}
		x.SubscriptionRateCardID = id
		r.st.productCatalog.phases.put(x.ID, x)
	}
	return nil
}

func (r contractRateCardRepo) discardPhases(id core.SubscriptionRateCardID, keep func(core.RatePhaseID) bool, at time.Time) {
	d := r.st.productCatalog
	for _, ph := range d.phases.find(func(ph *catalog.RatePhase) bool { return ph.SubscriptionRateCardID == id && kept(ph.DeletedAt) }) {
		if keep(ph.ID) {
			continue
		}
		ph.DeletedAt = discardAt(at)
		d.phases.put(ph.ID, *ph)
	}
}

func (r contractRateCardRepo) Update(_ context.Context, c *subscription.RateCard) error {
	cur, ok := r.cards().get(c.ID)
	if !ok || cur.OrganizationID != c.OrganizationID || !kept(cur.DeletedAt) {
		return notFound("contract_rate_card", c.ID)
	}
	r.cards().put(c.ID, *c)
	return nil
}

func (r contractRateCardRepo) Discard(_ context.Context, org core.OrganizationID, id core.SubscriptionRateCardID, at time.Time) error {
	cur, ok := r.cards().get(id)
	if !ok || cur.OrganizationID != org || !kept(cur.DeletedAt) {
		return notFound("contract_rate_card", id)
	}
	r.discardPhases(id, keepNone, at)
	cur.DeletedAt = discardAt(at)
	r.cards().put(id, *cur)
	return nil
}

func (r contractRateCardRepo) Get(_ context.Context, org core.OrganizationID, id core.SubscriptionRateCardID) (*subscription.RateCard, error) {
	c, ok := r.cards().get(id)
	if !ok || c.OrganizationID != org {
		return nil, notFound("contract_rate_card", id)
	}
	return c, nil
}

func (r contractRateCardRepo) GetByRateCard(_ context.Context, org core.OrganizationID, sub core.SubscriptionID, rateCard core.RateCardID) (*subscription.RateCard, error) {
	c, ok := r.cards().first(func(x *subscription.RateCard) bool {
		return x.OrganizationID == org && x.SubscriptionID == sub && x.RateCardID == rateCard && kept(x.DeletedAt)
	})
	if !ok {
		return nil, notFound("contract_rate_card", rateCard)
	}
	return c, nil
}

// byEffectiveDate orders cards by effective date, then ID.
func byEffectiveDate(a, b *subscription.RateCard) int {
	return cmp.Or(a.EffectiveDate.Compare(b.EffectiveDate), cmp.Compare(a.ID.String(), b.ID.String()))
}

func (r contractRateCardRepo) ListBySubscription(_ context.Context, org core.OrganizationID, sub core.SubscriptionID) ([]*subscription.RateCard, error) {
	rows := r.cards().find(func(x *subscription.RateCard) bool {
		return x.OrganizationID == org && x.SubscriptionID == sub && kept(x.DeletedAt)
	})
	slices.SortStableFunc(rows, byEffectiveDate)
	return rows, nil
}

func (r contractRateCardRepo) match(org core.OrganizationID, q subscription.RateCardQuery) func(*subscription.RateCard) bool {
	d := r.st.productCatalog
	return func(x *subscription.RateCard) bool {
		if x.OrganizationID != org || !kept(x.DeletedAt) ||
			!q.SubscriptionID.IsZero() && x.SubscriptionID != q.SubscriptionID ||
			!q.RateCardID.IsZero() && x.RateCardID != q.RateCardID {
			return false
		}
		if q.ProductID.IsZero() && q.ProductFilterID.IsZero() && q.ProductCategoryID.IsZero() {
			return true
		}
		c, ok := d.rateCards.get(x.RateCardID)
		if !ok || !kept(c.DeletedAt) || !q.ProductID.IsZero() && c.ProductID != q.ProductID || !q.ProductFilterID.IsZero() && c.ProductFilterID != q.ProductFilterID {
			return false
		}
		if !q.ProductCategoryID.IsZero() {
			p, ok := d.products.get(c.ProductID)
			return ok && p.ProductCategoryID == q.ProductCategoryID
		}
		return true
	}
}

func (r contractRateCardRepo) List(_ context.Context, org core.OrganizationID, q subscription.RateCardQuery, page core.PageRequest) (core.Page[*subscription.RateCard], error) {
	rows := r.cards().find(r.match(org, q))
	return paginateOldest(rows, recentKey(
		func(x *subscription.RateCard) time.Time { return x.EffectiveDate.Midnight(time.UTC) },
		func(x *subscription.RateCard) uuid.UUID { return x.ID.UUID() }), page)
}

func (r contractRateCardRepo) Exists(_ context.Context, org core.OrganizationID, q subscription.RateCardQuery) (bool, error) {
	return r.cards().any(r.match(org, q)), nil
}

// due reports whether the card x is due at t (lago
// ContractRateCard.due_for_billing).
func (r contractRateCardRepo) due(x *subscription.RateCard, t time.Time) (core.CustomerID, bool) {
	if !kept(x.DeletedAt) || x.NextBillingAt == nil || x.NextBillingAt.After(t) {
		return core.CustomerID{}, false
	}
	s, ok := r.st.subscription.subscriptions.get(x.SubscriptionID)
	if !ok || s.Status != subscription.StatusActive || s.SubscriptionAt.After(t) {
		return core.CustomerID{}, false
	}
	c, ok := r.st.customer.customers.get(s.CustomerID)
	if !ok || c.Deleted() {
		return core.CustomerID{}, false
	}
	if !r.st.productCatalog.rates.any(func(rt *catalog.Rate) bool { return rt.RateCardID == x.RateCardID && kept(rt.DeletedAt) }) {
		return core.CustomerID{}, false
	}
	loc := c.Timezone
	if loc == nil {
		if be, ok := r.st.organization.billingEntities.get(c.BillingEntityID); ok && be.Timezone != nil {
			loc = be.Timezone
		} else {
			loc = time.UTC
		}
	}
	if s.EndingAt != nil && !s.EndingAt.After(x.EffectiveDate.Midnight(loc)) {
		return core.CustomerID{}, false
	}
	return s.CustomerID, true
}

func (r contractRateCardRepo) ListDueForBilling(_ context.Context, org core.OrganizationID, customer core.CustomerID, t time.Time) ([]*subscription.RateCard, error) {
	return r.cards().find(func(x *subscription.RateCard) bool {
		if x.OrganizationID != org {
			return false
		}
		c, ok := r.due(x, t)
		return ok && c == customer
	}), nil
}

func (r contractRateCardRepo) CustomersDueForBilling(_ context.Context, org core.OrganizationID, t time.Time) ([]core.CustomerID, error) {
	var out []core.CustomerID
	for _, x := range r.cards().find(func(x *subscription.RateCard) bool { return x.OrganizationID == org }) {
		if c, ok := r.due(x, t); ok && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b core.CustomerID) int { return a.Compare(b) })
	return out, nil
}

func (r contractRateCardRepo) ListPhases(_ context.Context, org core.OrganizationID, id core.SubscriptionRateCardID) ([]*catalog.RatePhase, error) {
	rows := r.st.productCatalog.phases.find(func(p *catalog.RatePhase) bool {
		return p.OrganizationID == org && p.SubscriptionRateCardID == id && kept(p.DeletedAt)
	})
	slices.SortStableFunc(rows, func(a, b *catalog.RatePhase) int { return cmp.Compare(a.Position, b.Position) })
	return rows, nil
}

func (r contractRateCardRepo) SavePhases(_ context.Context, org core.OrganizationID, id core.SubscriptionRateCardID, phases []*catalog.RatePhase, at time.Time) error {
	cur, ok := r.cards().get(id)
	if !ok || cur.OrganizationID != org || !kept(cur.DeletedAt) {
		return notFound("contract_rate_card", id)
	}
	return r.putPhases(id, phases, at)
}

// billingSegmentRepo implements subscription.BillingSegmentRepository.
type billingSegmentRepo struct{ st *state }

var _ subscription.BillingSegmentRepository = billingSegmentRepo{}

func (r billingSegmentRepo) segments() *table[core.BillingSegmentID, subscription.BillingSegment] {
	return r.st.subscription.segments
}

func (r billingSegmentRepo) InsertAll(_ context.Context, segs []*subscription.BillingSegment) error {
	for i, s := range segs {
		if r.segments().has(s.ID) {
			return conflict("billing_segment", "id")
		}
		overlaps := func(x *subscription.BillingSegment) bool {
			return x.OrganizationID == s.OrganizationID && x.RateCardID == s.RateCardID &&
				(x.StartedAt.Equal(s.StartedAt) || x.Overlaps(s.StartedAt, s.EndedAt))
		}
		if r.segments().any(overlaps) || slices.ContainsFunc(segs[:i], overlaps) {
			return conflict("billing_segment", "overlapping_periods")
		}
	}
	for _, s := range segs {
		r.segments().put(s.ID, *s)
	}
	return nil
}

func (r billingSegmentRepo) Update(_ context.Context, s *subscription.BillingSegment) error {
	cur, ok := r.segments().get(s.ID)
	if !ok || cur.OrganizationID != s.OrganizationID {
		return notFound("billing_segment", s.ID)
	}
	r.segments().put(s.ID, *s)
	return nil
}

func byStart(a, b *subscription.BillingSegment) int {
	return cmp.Or(a.StartedAt.Compare(b.StartedAt), cmp.Compare(a.ID.String(), b.ID.String()))
}

func (r billingSegmentRepo) ListOverlapping(_ context.Context, org core.OrganizationID, card core.SubscriptionRateCardID, from, to time.Time) ([]*subscription.BillingSegment, error) {
	rows := r.segments().find(func(x *subscription.BillingSegment) bool {
		return x.OrganizationID == org && x.RateCardID == card && x.Overlaps(from, to)
	})
	slices.SortStableFunc(rows, byStart)
	return rows, nil
}

func (r billingSegmentRepo) MaxCycleStartedAt(_ context.Context, org core.OrganizationID, card core.SubscriptionRateCardID) (*time.Time, error) {
	var out *time.Time
	for _, x := range r.segments().find(func(x *subscription.BillingSegment) bool {
		return x.OrganizationID == org && x.RateCardID == card
	}) {
		if out == nil || x.CycleStartedAt.After(*out) {
			t := x.CycleStartedAt
			out = &t
		}
	}
	return out, nil
}

// awaiting reports whether x awaits invoicing: pending or processing, and
// not of a metered in-advance rate card.
func (r billingSegmentRepo) awaiting(x *subscription.BillingSegment) bool {
	if x.Status != subscription.SegmentPending && x.Status != subscription.SegmentProcessing {
		return false
	}
	card, ok := r.st.subscription.rateCards.get(x.RateCardID)
	if !ok {
		return false
	}
	rc, ok := r.st.productCatalog.rateCards.get(card.RateCardID)
	if !ok {
		return false
	}
	p, ok := r.st.productCatalog.products.get(rc.ProductID)
	return ok && (p.ProductType != catalog.ProductTypeMetered || rc.BillingTiming != catalog.BillingTimingAdvance)
}

func (r billingSegmentRepo) ListAwaitingInvoicing(_ context.Context, org core.OrganizationID, customer core.CustomerID) ([]*subscription.BillingSegment, error) {
	rows := r.segments().find(func(x *subscription.BillingSegment) bool {
		return x.OrganizationID == org && x.CustomerID == customer && r.awaiting(x)
	})
	slices.SortStableFunc(rows, func(a, b *subscription.BillingSegment) int {
		return cmp.Or(a.BillingAt.Compare(b.BillingAt), cmp.Compare(a.ID.String(), b.ID.String()))
	})
	return rows, nil
}

func (r billingSegmentRepo) CustomersAwaitingInvoicing(_ context.Context, org core.OrganizationID) ([]core.CustomerID, error) {
	var out []core.CustomerID
	for _, x := range r.segments().find(func(x *subscription.BillingSegment) bool {
		return x.OrganizationID == org && r.awaiting(x)
	}) {
		c, ok := r.st.customer.customers.get(x.CustomerID)
		if ok && !c.Deleted() && !slices.Contains(out, x.CustomerID) {
			out = append(out, x.CustomerID)
		}
	}
	slices.SortFunc(out, func(a, b core.CustomerID) int { return a.Compare(b) })
	return out, nil
}

func (r billingSegmentRepo) GeneratingInvoiceIDs(_ context.Context, org core.OrganizationID, customer core.CustomerID) ([]core.InvoiceID, error) {
	var out []core.InvoiceID
	for _, x := range r.segments().find(func(x *subscription.BillingSegment) bool {
		return x.OrganizationID == org && x.CustomerID == customer && x.Status == subscription.SegmentDone && !x.InvoiceID.IsZero()
	}) {
		inv, ok := r.st.invoice.invoices.get(x.InvoiceID)
		if ok && inv.Status == invoice.StatusGenerating && !slices.Contains(out, x.InvoiceID) {
			out = append(out, x.InvoiceID)
		}
	}
	slices.SortFunc(out, func(a, b core.InvoiceID) int { return a.Compare(b) })
	return out, nil
}

func (r billingSegmentRepo) ListByInvoice(_ context.Context, org core.OrganizationID, inv core.InvoiceID) ([]*subscription.BillingSegment, error) {
	rows := r.segments().find(func(x *subscription.BillingSegment) bool {
		return x.OrganizationID == org && x.InvoiceID == inv
	})
	slices.SortStableFunc(rows, byStart)
	return rows, nil
}

func (r billingSegmentRepo) ListBySubscription(_ context.Context, org core.OrganizationID, sub core.SubscriptionID) ([]*subscription.BillingSegment, error) {
	rows := r.segments().find(func(x *subscription.BillingSegment) bool {
		return x.OrganizationID == org && x.SubscriptionID == sub
	})
	slices.SortStableFunc(rows, byStart)
	return rows, nil
}
