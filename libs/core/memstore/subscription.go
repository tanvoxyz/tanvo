package memstore

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// subscriptionData holds the tables of package subscription.
type subscriptionData struct {
	subscriptions          *table[core.SubscriptionID, subscription.Subscription]
	activationRules        *table[core.ActivationRuleID, subscription.ActivationRule]
	fixedChargeEvents      *table[core.FixedChargeEventID, subscription.FixedChargeEvent]
	unitsOverrides         *table[core.FixedChargeUnitsOverrideID, subscription.FixedChargeUnitsOverride]
	appliedUsageThresholds *table[core.AppliedUsageThresholdID, subscription.AppliedUsageThreshold]
	lifetimeUsages         *table[core.LifetimeUsageID, subscription.LifetimeUsage]
	dailyUsages            *table[core.DailyUsageID, subscription.DailyUsage]
	rateCards              *table[core.SubscriptionRateCardID, subscription.RateCard]
	segments               *table[core.BillingSegmentID, subscription.BillingSegment]
}

func newSubscriptionData() *subscriptionData {
	return &subscriptionData{
		subscriptions: newTable[core.SubscriptionID](func(s subscription.Subscription) subscription.Subscription {
			s.InvoiceCustomSectionIDs = slices.Clone(s.InvoiceCustomSectionIDs)
			return s
		}),
		activationRules:        newTable[core.ActivationRuleID, subscription.ActivationRule](nil),
		fixedChargeEvents:      newTable[core.FixedChargeEventID, subscription.FixedChargeEvent](nil),
		unitsOverrides:         newTable[core.FixedChargeUnitsOverrideID](cloneUnitsOverride),
		appliedUsageThresholds: newTable[core.AppliedUsageThresholdID, subscription.AppliedUsageThreshold](nil),
		lifetimeUsages:         newTable[core.LifetimeUsageID, subscription.LifetimeUsage](nil),
		dailyUsages:            newTable[core.DailyUsageID](cloneDailyUsage),
		rateCards: newTable[core.SubscriptionRateCardID](func(c subscription.RateCard) subscription.RateCard {
			c.Units = clonePtr(c.Units)
			c.NextBillingAt = clonePtr(c.NextBillingAt)
			c.DeletedAt = clonePtr(c.DeletedAt)
			return c
		}),
		segments: newTable[core.BillingSegmentID](func(b subscription.BillingSegment) subscription.BillingSegment {
			b.RateProperties = b.RateProperties.Clone()
			return b
		}),
	}
}

func (d *subscriptionData) snapshot() *subscriptionData {
	return &subscriptionData{
		subscriptions:          d.subscriptions.snapshot(),
		activationRules:        d.activationRules.snapshot(),
		fixedChargeEvents:      d.fixedChargeEvents.snapshot(),
		unitsOverrides:         d.unitsOverrides.snapshot(),
		appliedUsageThresholds: d.appliedUsageThresholds.snapshot(),
		lifetimeUsages:         d.lifetimeUsages.snapshot(),
		dailyUsages:            d.dailyUsages.snapshot(),
		rateCards:              d.rateCards.snapshot(),
		segments:               d.segments.snapshot(),
	}
}

// subscriptionRepo implements subscription.Repository.
type subscriptionRepo struct {
	d  *subscriptionData
	st *state // the whole transaction state, for the list filters
}

var _ subscription.Repository = subscriptionRepo{}

func (r subscriptionRepo) checkExternalID(s *subscription.Subscription) error {
	if !s.Live() {
		return nil
	}
	if r.d.subscriptions.any(func(x *subscription.Subscription) bool {
		return x.ID != s.ID && x.OrganizationID == s.OrganizationID && x.ExternalID == s.ExternalID && x.Status == s.Status
	}) {
		return conflict("subscription", "external_id")
	}
	return nil
}

func (r subscriptionRepo) Create(_ context.Context, s *subscription.Subscription) error {
	if r.d.subscriptions.has(s.ID) {
		return conflict("subscription", "id")
	}
	if err := r.checkExternalID(s); err != nil {
		return err
	}
	stored := *s
	stored.LastReceivedEventOn = nil
	r.d.subscriptions.put(s.ID, stored)
	return nil
}

func (r subscriptionRepo) Update(_ context.Context, s *subscription.Subscription) error {
	cur, ok := r.d.subscriptions.get(s.ID)
	if !ok || cur.OrganizationID != s.OrganizationID {
		return notFound("subscription", s.ID)
	}
	if err := r.checkExternalID(s); err != nil {
		return err
	}
	stored := *s
	stored.LastReceivedEventOn = cur.LastReceivedEventOn
	r.d.subscriptions.put(s.ID, stored)
	return nil
}

func (r subscriptionRepo) Get(_ context.Context, org core.OrganizationID, id core.SubscriptionID) (*subscription.Subscription, error) {
	s, ok := r.d.subscriptions.get(id)
	if !ok || s.OrganizationID != org {
		return nil, notFound("subscription", id)
	}
	return s, nil
}

func (r subscriptionRepo) GetByExternalID(_ context.Context, org core.OrganizationID, externalID string) (*subscription.Subscription, error) {
	for _, st := range []subscription.Status{subscription.StatusActive, subscription.StatusIncomplete, subscription.StatusPending} {
		if s, ok := r.d.subscriptions.first(func(x *subscription.Subscription) bool {
			return x.OrganizationID == org && !x.Contract && x.ExternalID == externalID && x.Status == st
		}); ok {
			return s, nil
		}
	}
	return nil, notFound("subscription", stringer(externalID))
}

func (r subscriptionRepo) List(_ context.Context, org core.OrganizationID, f subscription.Filter, page core.PageRequest) (core.Page[*subscription.Subscription], error) {
	if err := f.Validate(); err != nil {
		return core.Page[*subscription.Subscription]{}, fmt.Errorf("list subscriptions: %w", err)
	}
	search := strings.ToLower(core.SearchTerm(f.Search))
	if f.ExternalID != "" {
		search = ""
	}
	byPlan := f.PlanCode != "" || f.Overridden != nil || !f.Currency.IsZero()
	byCustomer := f.ExternalCustomerID != "" || len(f.BillingEntityIDs) > 0
	rows := r.d.subscriptions.find(func(x *subscription.Subscription) bool {
		if x.OrganizationID != org || x.Contract ||
			!f.CustomerID.IsZero() && x.CustomerID != f.CustomerID ||
			!f.PlanID.IsZero() && x.PlanID != f.PlanID ||
			f.ExternalID != "" && x.ExternalID != f.ExternalID ||
			len(f.Statuses) > 0 && !slices.Contains(f.Statuses, x.Status) {
			return false
		}
		if f.ExcludeNextSubscriptions && !r.listedInChain(x, f.Statuses) {
			return false
		}
		if byPlan {
			p, ok := r.st.catalog.plans.get(x.PlanID)
			if !ok || p.Deleted() || f.PlanCode != "" && p.Code != f.PlanCode ||
				f.Overridden != nil && p.IsChild() != *f.Overridden ||
				!f.Currency.IsZero() && p.Currency.Code() != f.Currency.Code() {
				return false
			}
		}
		if byCustomer {
			c, ok := r.st.customer.customers.get(x.CustomerID)
			if !ok || f.ExternalCustomerID != "" && (c.Deleted() || c.ExternalID != f.ExternalCustomerID) {
				return false
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
		}
		return search == "" || r.searchMatches(x, search, f.ExternalCustomerID == "")
	})
	if f.Order == core.OrderRecent {
		if page.Token != "" {
			c, err := core.DecodeCursor(page.Token)
			if err != nil {
				return core.Page[*subscription.Subscription]{}, err
			}
			if _, _, err := subscription.ParseRecentCursor(c); err != nil {
				return core.Page[*subscription.Subscription]{}, err
			}
		}
		slices.SortStableFunc(rows, func(a, b *subscription.Subscription) int {
			if subscription.RecentBefore(a, b.SubscriptionAt, b.CreatedAt, b.ID) {
				return -1
			}
			if subscription.RecentBefore(b, a.SubscriptionAt, a.CreatedAt, a.ID) {
				return 1
			}
			return 0
		})
		return paginateSorted(rows, subscription.RecentCursor, func(x *subscription.Subscription, c core.Cursor) bool {
			at, created, _ := subscription.ParseRecentCursor(c)
			var id core.SubscriptionID
			_ = id.Scan(c.ID[:])
			// x comes after the cursor when the cursor comes before x.
			return x.ID != id && !subscription.RecentBefore(x, at, created, id)
		}, page)
	}
	return paginate(rows, func(x *subscription.Subscription) core.SubscriptionID { return x.ID }, page)
}

// listedInChain reports whether x passes the ExcludeNextSubscriptions
// filter given the status filter.
func (r subscriptionRepo) listedInChain(x *subscription.Subscription, statuses []subscription.Status) bool {
	if x.PreviousSubscriptionID.IsZero() || x.Status == subscription.StatusCanceled {
		return true
	}
	prev, ok := r.d.subscriptions.get(x.PreviousSubscriptionID)
	if !ok {
		return false
	}
	return prev.Status == subscription.StatusTerminated ||
		len(statuses) > 0 && !slices.Contains(statuses, prev.Status) && slices.Contains(statuses, x.Status)
}

// searchMatches reports whether the lower-cased term matches x, its plan,
// its ID or (when customers is true) its non-deleted customer.
func (r subscriptionRepo) searchMatches(x *subscription.Subscription, term string, customers bool) bool {
	contains := func(ps ...*string) bool {
		return slices.ContainsFunc(ps, func(p *string) bool { return p != nil && strings.Contains(strings.ToLower(*p), term) })
	}
	if contains(x.Name, &x.ExternalID) {
		return true
	}
	if u, err := uuid.Parse(term); err == nil && len(term) == 36 && u == x.ID.UUID() {
		return true
	}
	if p, ok := r.st.catalog.plans.get(x.PlanID); ok && !p.Deleted() && contains(&p.Name, &p.Code) {
		return true
	}
	if !customers {
		return false
	}
	c, ok := r.st.customer.customers.get(x.CustomerID)
	return ok && !c.Deleted() && contains(c.Name, c.Firstname, c.Lastname, &c.ExternalID, c.Email)
}

func (r subscriptionRepo) MatchEvent(_ context.Context, org core.OrganizationID, externalID string, t time.Time, recurringFallback bool) (*subscription.Subscription, error) {
	candidates := r.d.subscriptions.find(func(x *subscription.Subscription) bool {
		return x.OrganizationID == org && x.ExternalID == externalID
	})
	if s := subscription.MatchEvent(candidates, t, recurringFallback); s != nil {
		return s, nil
	}
	return nil, notFound("subscription", stringer(externalID))
}

func (r subscriptionRepo) ListByCustomer(_ context.Context, org core.OrganizationID, customer core.CustomerID, statuses ...subscription.Status) ([]*subscription.Subscription, error) {
	return r.d.subscriptions.find(func(x *subscription.Subscription) bool {
		return x.OrganizationID == org && !x.Contract && x.CustomerID == customer && (len(statuses) == 0 || slices.Contains(statuses, x.Status))
	}), nil
}

func (r subscriptionRepo) InitialStartedAt(_ context.Context, org core.OrganizationID, customer core.CustomerID, externalID string) (*time.Time, error) {
	var first *time.Time
	for _, x := range r.d.subscriptions.find(func(x *subscription.Subscription) bool {
		return x.OrganizationID == org && x.CustomerID == customer && x.ExternalID == externalID && x.StartedAt != nil
	}) {
		if first == nil || x.StartedAt.Before(*first) {
			t := *x.StartedAt
			first = &t
		}
	}
	return first, nil
}

func (r subscriptionRepo) ListActiveOrPendingByPlan(_ context.Context, org core.OrganizationID, plan core.PlanID) ([]*subscription.Subscription, error) {
	return r.d.subscriptions.find(func(x *subscription.Subscription) bool {
		return x.OrganizationID == org && !x.Contract && x.PlanID == plan &&
			(x.Status == subscription.StatusActive || x.Status == subscription.StatusPending)
	}), nil
}

func (r subscriptionRepo) ListDueForBilling(_ context.Context, org core.OrganizationID, t time.Time, page core.PageRequest) (core.Page[*subscription.Subscription], error) {
	rows := r.d.subscriptions.find(func(x *subscription.Subscription) bool {
		return x.OrganizationID == org && !x.Contract && x.Status == subscription.StatusActive &&
			x.NextBillingAt != nil && !x.NextBillingAt.After(t)
	})
	// The cursor is the ID, so pagination stays in ID order; the page is
	// then ordered by next_billing_at.
	p, err := paginate(rows, func(x *subscription.Subscription) core.SubscriptionID { return x.ID }, page)
	if err != nil {
		return p, err
	}
	slices.SortStableFunc(p.Items, func(a, b *subscription.Subscription) int { return a.NextBillingAt.Compare(*b.NextBillingAt) })
	return p, nil
}

func (r subscriptionRepo) ListPendingToActivate(_ context.Context, org core.OrganizationID, t time.Time) ([]*subscription.Subscription, error) {
	return r.d.subscriptions.find(func(x *subscription.Subscription) bool {
		return x.OrganizationID == org && !x.Contract && x.Status == subscription.StatusPending &&
			x.PreviousSubscriptionID.IsZero() && !x.SubscriptionAt.After(t)
	}), nil
}

func (r subscriptionRepo) GetNext(_ context.Context, org core.OrganizationID, id core.SubscriptionID) (*subscription.Subscription, error) {
	s, ok := r.d.subscriptions.first(func(x *subscription.Subscription) bool {
		return x.OrganizationID == org && x.PreviousSubscriptionID == id && x.Status == subscription.StatusPending
	})
	if !ok {
		return nil, notFound("next subscription of", id)
	}
	return s, nil
}

func (r subscriptionRepo) Chain(_ context.Context, org core.OrganizationID, id core.SubscriptionID) ([]*subscription.Subscription, error) {
	var chain []*subscription.Subscription
	seen := map[core.SubscriptionID]bool{}
	for cur := id; !cur.IsZero() && !seen[cur]; {
		s, ok := r.d.subscriptions.get(cur)
		if !ok || s.OrganizationID != org {
			if len(chain) == 0 {
				return nil, notFound("subscription", id)
			}
			break
		}
		seen[cur] = true
		chain = append(chain, s)
		cur = s.PreviousSubscriptionID
	}
	slices.Reverse(chain)
	return chain, nil
}

func (r subscriptionRepo) ListEndingBetween(_ context.Context, org core.OrganizationID, from, to time.Time) ([]*subscription.Subscription, error) {
	return r.d.subscriptions.find(func(x *subscription.Subscription) bool {
		return x.OrganizationID == org && !x.Contract && x.Status == subscription.StatusActive && x.EndingAt != nil &&
			!x.EndingAt.Before(from) && x.EndingAt.Before(to)
	}), nil
}

func (r subscriptionRepo) ListIncompleteWithExpiredRules(_ context.Context, org core.OrganizationID, now time.Time) ([]*subscription.Subscription, error) {
	return r.d.subscriptions.find(func(x *subscription.Subscription) bool {
		return x.OrganizationID == org && x.Status == subscription.StatusIncomplete &&
			r.d.activationRules.any(func(a *subscription.ActivationRule) bool {
				return a.OrganizationID == org && a.SubscriptionID == x.ID && a.Expirable(now)
			})
	}), nil
}

// activationRuleRepo implements subscription.ActivationRuleRepository.
type activationRuleRepo struct{ d *subscriptionData }

var _ subscription.ActivationRuleRepository = activationRuleRepo{}

func (r activationRuleRepo) Create(_ context.Context, a *subscription.ActivationRule) error {
	if r.d.activationRules.has(a.ID) {
		return conflict("activation_rule", "id")
	}
	r.d.activationRules.put(a.ID, *a)
	return nil
}

func (r activationRuleRepo) Update(_ context.Context, a *subscription.ActivationRule) error {
	cur, ok := r.d.activationRules.get(a.ID)
	if !ok || cur.OrganizationID != a.OrganizationID {
		return notFound("activation_rule", a.ID)
	}
	r.d.activationRules.put(a.ID, *a)
	return nil
}

func (r activationRuleRepo) ListBySubscription(_ context.Context, org core.OrganizationID, sub core.SubscriptionID) ([]*subscription.ActivationRule, error) {
	return r.d.activationRules.find(func(a *subscription.ActivationRule) bool {
		return a.OrganizationID == org && a.SubscriptionID == sub
	}), nil
}

func (r activationRuleRepo) ReplaceForSubscription(_ context.Context, org core.OrganizationID, sub core.SubscriptionID, rules []*subscription.ActivationRule) error {
	for i, a := range rules {
		if a.OrganizationID != org || a.SubscriptionID != sub {
			return notFound("activation_rule", a.ID)
		}
		if slices.ContainsFunc(rules[:i], func(b *subscription.ActivationRule) bool { return b.ID == a.ID }) {
			return conflict("activation_rule", "id")
		}
	}
	r.d.activationRules.deleteWhere(func(a *subscription.ActivationRule) bool {
		return a.OrganizationID == org && a.SubscriptionID == sub
	})
	for _, a := range rules {
		r.d.activationRules.put(a.ID, *a)
	}
	return nil
}

// fixedChargeEventRepo implements subscription.FixedChargeEventRepository.
type fixedChargeEventRepo struct{ d *subscriptionData }

var _ subscription.FixedChargeEventRepository = fixedChargeEventRepo{}

func (r fixedChargeEventRepo) Record(_ context.Context, e *subscription.FixedChargeEvent, loc *time.Location) error {
	if r.d.fixedChargeEvents.has(e.ID) {
		return conflict("fixed_charge_event", "id")
	}
	r.d.fixedChargeEvents.deleteWhere(func(x *subscription.FixedChargeEvent) bool {
		return x.OrganizationID == e.OrganizationID && x.SameDay(e, loc)
	})
	r.d.fixedChargeEvents.put(e.ID, *e)
	return nil
}

func (r fixedChargeEventRepo) ListBySubscription(_ context.Context, org core.OrganizationID, sub core.SubscriptionID, fixedCharge core.FixedChargeID) ([]*subscription.FixedChargeEvent, error) {
	rows := r.d.fixedChargeEvents.find(func(x *subscription.FixedChargeEvent) bool {
		return x.OrganizationID == org && x.SubscriptionID == sub && (fixedCharge.IsZero() || x.FixedChargeID == fixedCharge)
	})
	slices.SortStableFunc(rows, func(a, b *subscription.FixedChargeEvent) int { return a.Timestamp.Compare(b.Timestamp) })
	return rows, nil
}

// appliedUsageThresholdRepo implements
// subscription.AppliedUsageThresholdRepository.
type appliedUsageThresholdRepo struct{ d *subscriptionData }

var _ subscription.AppliedUsageThresholdRepository = appliedUsageThresholdRepo{}

func (r appliedUsageThresholdRepo) Create(_ context.Context, a *subscription.AppliedUsageThreshold) error {
	if r.d.appliedUsageThresholds.has(a.ID) {
		return conflict("applied_usage_threshold", "id")
	}
	if r.d.appliedUsageThresholds.any(func(x *subscription.AppliedUsageThreshold) bool {
		return x.OrganizationID == a.OrganizationID && x.InvoiceID == a.InvoiceID && x.UsageThresholdID == a.UsageThresholdID
	}) {
		return conflict("applied_usage_threshold", "usage_threshold_id")
	}
	r.d.appliedUsageThresholds.put(a.ID, *a)
	return nil
}

func (r appliedUsageThresholdRepo) ListBySubscription(_ context.Context, org core.OrganizationID, sub core.SubscriptionID) ([]*subscription.AppliedUsageThreshold, error) {
	return r.d.appliedUsageThresholds.find(func(x *subscription.AppliedUsageThreshold) bool {
		return x.OrganizationID == org && x.SubscriptionID == sub
	}), nil
}

func (r appliedUsageThresholdRepo) ListByInvoice(_ context.Context, org core.OrganizationID, invoice core.InvoiceID) ([]*subscription.AppliedUsageThreshold, error) {
	return r.d.appliedUsageThresholds.find(func(x *subscription.AppliedUsageThreshold) bool {
		return x.OrganizationID == org && x.InvoiceID == invoice
	}), nil
}

// lifetimeUsageRepo implements subscription.LifetimeUsageRepository.
type lifetimeUsageRepo struct{ d *subscriptionData }

var _ subscription.LifetimeUsageRepository = lifetimeUsageRepo{}

func (r lifetimeUsageRepo) GetByExternalID(_ context.Context, org core.OrganizationID, externalID string) (*subscription.LifetimeUsage, error) {
	l, ok := r.d.lifetimeUsages.first(func(x *subscription.LifetimeUsage) bool {
		return x.OrganizationID == org && x.ExternalSubscriptionID == externalID
	})
	if !ok {
		return nil, notFound("lifetime_usage", stringer(externalID))
	}
	return l, nil
}

func (r lifetimeUsageRepo) GetBySubscription(_ context.Context, org core.OrganizationID, sub core.SubscriptionID) (*subscription.LifetimeUsage, error) {
	l, ok := r.d.lifetimeUsages.first(func(x *subscription.LifetimeUsage) bool {
		return x.OrganizationID == org && x.SubscriptionID == sub
	})
	if !ok {
		return nil, notFound("lifetime_usage", sub)
	}
	return l, nil
}

func (r lifetimeUsageRepo) Upsert(_ context.Context, l *subscription.LifetimeUsage) error {
	if cur, ok := r.d.lifetimeUsages.first(func(x *subscription.LifetimeUsage) bool {
		return x.OrganizationID == l.OrganizationID && x.ExternalSubscriptionID == l.ExternalSubscriptionID
	}); ok {
		l.ID = cur.ID
		l.CreatedAt = cur.CreatedAt
	} else if r.d.lifetimeUsages.has(l.ID) {
		return conflict("lifetime_usage", "id")
	}
	r.d.lifetimeUsages.put(l.ID, *l)
	return nil
}

func (r lifetimeUsageRepo) ListToRecalculate(_ context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*subscription.LifetimeUsage], error) {
	rows := r.d.lifetimeUsages.find(func(x *subscription.LifetimeUsage) bool {
		return x.OrganizationID == org && x.RecalculateInvoicedUsage
	})
	return paginate(rows, func(x *subscription.LifetimeUsage) core.LifetimeUsageID { return x.ID }, page)
}
