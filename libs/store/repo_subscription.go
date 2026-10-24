package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindSubscription binds the repositories of the subscription aggregates.
func (t *txState) bindSubscription(r *port.Repos) {
	r.Subscriptions = subscriptionRepo{t}
	r.ActivationRules = activationRuleRepo{t}
	r.FixedChargeEvents = fixedChargeEventRepo{t}
	r.FixedChargeUnitsOverrides = unitsOverrideRepo{t}
	r.AppliedUsageThresholds = appliedUsageThresholdRepo{t}
	r.LifetimeUsages = lifetimeUsageRepo{t}
	r.DailyUsages = dailyUsageRepo{t}
}

// subscriptionRepo implements subscription.Repository.
type subscriptionRepo struct{ t *txState }

var _ subscription.Repository = subscriptionRepo{}

func subscriptionParams(s *subscription.Subscription) db.CreateSubscriptionParams {
	return db.CreateSubscriptionParams{
		OrganizationID:             s.OrganizationID.UUID(),
		ID:                         s.ID.UUID(),
		ExternalID:                 s.ExternalID,
		CustomerID:                 s.CustomerID.UUID(),
		PlanID:                     optUUID(s.PlanID),
		Contract:                   s.Contract,
		BillingAnchorDate:          optDateCol(s.BillingAnchorDate),
		Name:                       s.Name,
		BillingTime:                s.BillingTime.String(),
		Status:                     string(s.Status),
		SubscriptionAt:             utc(s.SubscriptionAt),
		StartedAt:                  optUTC(s.StartedAt),
		EndingAt:                   optUTC(s.EndingAt),
		TerminatedAt:               optUTC(s.TerminatedAt),
		CanceledAt:                 optUTC(s.CanceledAt),
		TrialEndedAt:               optUTC(s.TrialEndedAt),
		CancellationReason:         optString(string(s.CancellationReason)),
		PreviousSubscriptionID:     optUUID(s.PreviousSubscriptionID),
		OnTerminationCreditNote:    optString(string(s.OnTerminationCreditNote)),
		OnTerminationInvoice:       string(s.OnTerminationInvoice),
		PaymentMethodType:          string(s.PaymentMethodType),
		PaymentMethodID:            optUUID(s.PaymentMethodID),
		ProgressiveBillingDisabled: s.ProgressiveBillingDisabled,
		ConsolidateInvoice:         s.ConsolidateInvoice,
		PurchaseOrderNumber:        s.PurchaseOrderNumber,
		BillingEntityID:            optUUID(s.BillingEntityID),
		SkipInvoiceCustomSections:  s.SkipInvoiceCustomSections,
		NextBillingAt:              optUTC(s.NextBillingAt),
		SkipDailyUsage:             s.SkipDailyUsage,
		CreatedAt:                  utc(s.CreatedAt),
		UpdatedAt:                  utc(s.UpdatedAt),
	}
}

// subscriptionRow is the common shape of the subscription reads; the row
// types sqlc generates for each query convert to it.
type subscriptionRow struct {
	Subscription            db.Subscription
	InvoiceCustomSectionIds []uuid.UUID
}

func subscriptionOf(row subscriptionRow) (*subscription.Subscription, error) {
	r := row.Subscription
	bt, err := period.ParseBillingTime(r.BillingTime)
	if err != nil {
		return nil, fmt.Errorf("subscription %s: %w", r.ID, err)
	}
	return &subscription.Subscription{
		ID:                         idOf[core.SubscriptionID](r.ID),
		OrganizationID:             idOf[core.OrganizationID](r.OrganizationID),
		ExternalID:                 r.ExternalID,
		CustomerID:                 idOf[core.CustomerID](r.CustomerID),
		PlanID:                     optIDOf[core.PlanID](r.PlanID),
		Contract:                   r.Contract,
		BillingAnchorDate:          optDateOf(r.BillingAnchorDate),
		Name:                       r.Name,
		BillingTime:                bt,
		Status:                     subscription.Status(r.Status),
		SubscriptionAt:             utc(r.SubscriptionAt),
		StartedAt:                  optUTC(r.StartedAt),
		EndingAt:                   optUTC(r.EndingAt),
		TerminatedAt:               optUTC(r.TerminatedAt),
		CanceledAt:                 optUTC(r.CanceledAt),
		TrialEndedAt:               optUTC(r.TrialEndedAt),
		CancellationReason:         subscription.CancellationReason(deref(r.CancellationReason)),
		PreviousSubscriptionID:     optIDOf[core.SubscriptionID](r.PreviousSubscriptionID),
		OnTerminationCreditNote:    subscription.TerminationCreditNote(deref(r.OnTerminationCreditNote)),
		OnTerminationInvoice:       subscription.TerminationInvoice(r.OnTerminationInvoice),
		PaymentMethodType:          core.PaymentMethodType(r.PaymentMethodType),
		PaymentMethodID:            optIDOf[core.PaymentMethodID](r.PaymentMethodID),
		ProgressiveBillingDisabled: r.ProgressiveBillingDisabled,
		ConsolidateInvoice:         r.ConsolidateInvoice,
		PurchaseOrderNumber:        r.PurchaseOrderNumber,
		BillingEntityID:            optIDOf[core.BillingEntityID](r.BillingEntityID),
		SkipInvoiceCustomSections:  r.SkipInvoiceCustomSections,
		InvoiceCustomSectionIDs:    idsOf[core.InvoiceCustomSectionID](row.InvoiceCustomSectionIds),
		NextBillingAt:              optUTC(r.NextBillingAt),
		SkipDailyUsage:             r.SkipDailyUsage,
		LastReceivedEventOn:        optDateOf(r.LastReceivedEventOn),
		CreatedAt:                  utc(r.CreatedAt),
		UpdatedAt:                  utc(r.UpdatedAt),
	}, nil
}

// writeSections replaces the invoice custom sections of s.
func (r subscriptionRepo) writeSections(ctx context.Context, q *db.Queries, s *subscription.Subscription, replace bool) error {
	org, id := s.OrganizationID.UUID(), s.ID.UUID()
	if replace {
		if err := q.DeleteSubscriptionSections(ctx, db.DeleteSubscriptionSectionsParams{OrganizationID: org, SubscriptionID: id}); err != nil {
			return err
		}
	}
	if len(s.InvoiceCustomSectionIDs) == 0 {
		return nil
	}
	return q.InsertSubscriptionSections(ctx, db.InsertSubscriptionSectionsParams{
		OrganizationID: org, SubscriptionID: id, SectionIds: uuids(s.InvoiceCustomSectionIDs),
	})
}

func (r subscriptionRepo) Create(ctx context.Context, s *subscription.Subscription) error {
	q, err := r.t.queries(ctx, s.OrganizationID)
	if err != nil {
		return err
	}
	// The sections may reference missing rows: guard the whole write so
	// that a failure leaves no half-written subscription.
	var n int64
	err = r.t.guard(ctx, func() error {
		var err error
		n, err = q.CreateSubscription(ctx, subscriptionParams(s))
		if err != nil || n == 0 {
			return err
		}
		return r.writeSections(ctx, q, s, false)
	})
	return r.t.noteCatalog(insertErr("subscription", n, err), CatalogChange{OrganizationID: s.OrganizationID, Kind: CatalogSubscription, Key: s.ExternalID})
}

func (r subscriptionRepo) Update(ctx context.Context, s *subscription.Subscription) error {
	q, err := r.t.queries(ctx, s.OrganizationID)
	if err != nil {
		return err
	}
	var previous string
	err = r.t.guard(ctx, func() error {
		var err error
		previous, err = q.UpdateSubscription(ctx, db.UpdateSubscriptionParams(subscriptionParams(s)))
		if err != nil {
			return err
		}
		return r.writeSections(ctx, q, s, true)
	})
	if err != nil {
		return getErr("subscription", s.ID, err)
	}
	// A renamed subscription expires the cache entry of its old external
	// ID as well as of the new one.
	if previous != s.ExternalID {
		_ = r.t.noteCatalog(nil, CatalogChange{OrganizationID: s.OrganizationID, Kind: CatalogSubscription, Key: previous})
	}
	return r.t.noteCatalog(nil, CatalogChange{OrganizationID: s.OrganizationID, Kind: CatalogSubscription, Key: s.ExternalID})
}

func (r subscriptionRepo) Get(ctx context.Context, org core.OrganizationID, id core.SubscriptionID) (*subscription.Subscription, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetSubscription(ctx, db.GetSubscriptionParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("subscription", id, err)
	}
	return subscriptionOf(subscriptionRow(row))
}

func (r subscriptionRepo) GetByExternalID(ctx context.Context, org core.OrganizationID, externalID string) (*subscription.Subscription, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetSubscriptionByExternalID(ctx, db.GetSubscriptionByExternalIDParams{OrganizationID: org.UUID(), ExternalID: externalID})
	if err != nil {
		return nil, getErr("subscription", externalID, err)
	}
	return subscriptionOf(subscriptionRow(row))
}

func (r subscriptionRepo) List(ctx context.Context, org core.OrganizationID, f subscription.Filter, page core.PageRequest) (core.Page[*subscription.Subscription], error) {
	if err := f.Validate(); err != nil {
		return core.Page[*subscription.Subscription]{}, fmt.Errorf("list subscriptions: %w", err)
	}
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*subscription.Subscription]{}, err
	}
	recent := f.Order == core.OrderRecent
	var (
		after          *uuid.UUID
		afterAt        *time.Time
		afterCreatedAt *time.Time
		lim            int32
	)
	if recent {
		after, afterAt, afterCreatedAt, lim, err = subscriptionCursorArgs(page)
	} else {
		after, lim, err = pageArgs(page)
	}
	if err != nil {
		return core.Page[*subscription.Subscription]{}, err
	}
	search := searchArg(f.Search)
	var searchID *uuid.UUID
	if f.ExternalID != "" {
		search = nil
	} else if t := core.SearchTerm(f.Search); len(t) == 36 {
		if u, err := uuid.Parse(t); err == nil {
			searchID = &u
		}
	}
	var currency *string
	if !f.Currency.IsZero() {
		currency = new(f.Currency.Code())
	}
	rows, err := q.ListSubscriptions(ctx, db.ListSubscriptionsParams{
		OrganizationID:           org.UUID(),
		CustomerID:               optUUID(f.CustomerID),
		ExternalCustomerID:       optString(f.ExternalCustomerID),
		PlanID:                   optUUID(f.PlanID),
		PlanCode:                 optString(f.PlanCode),
		Overridden:               f.Overridden,
		Currency:                 currency,
		ExternalID:               optString(f.ExternalID),
		Statuses:                 enumStrings(f.Statuses),
		BillingEntityIds:         uuids(f.BillingEntityIDs),
		ExcludeNextSubscriptions: f.ExcludeNextSubscriptions,
		Search:                   search,
		SearchID:                 searchID,
		SearchCustomers:          f.ExternalCustomerID == "",
		After:                    after,
		Recent:                   recent,
		AfterAt:                  afterAt,
		AfterCreatedAt:           afterCreatedAt,
		Lim:                      lim,
	})
	if err != nil {
		return core.Page[*subscription.Subscription]{}, fmt.Errorf("list subscriptions: %w", mapErr(err))
	}
	key := func(s *subscription.Subscription) string { return s.ID.String() }
	if recent {
		key = func(s *subscription.Subscription) string { return subscription.RecentCursor(s).Encode() }
	}
	return pageOf(rows, page,
		func(r db.ListSubscriptionsRow) (*subscription.Subscription, error) {
			return subscriptionOf(subscriptionRow(r))
		},
		key)
}

// subscriptionCursorArgs returns the cursor (subscription_at, created_at
// and id, nil on the first page) and the row limit of a page of the
// core.OrderRecent subscription list.
func subscriptionCursorArgs(p core.PageRequest) (after *uuid.UUID, at, createdAt *time.Time, lim int32, err error) {
	lim = int32(p.Limit()) + 1 // Limit is at most core.MaxPageSize
	if p.Token == "" {
		return nil, nil, nil, lim, nil
	}
	c, err := core.DecodeCursor(p.Token)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	a, b, err := subscription.ParseRecentCursor(c)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	return &c.ID, &a, &b, lim, nil
}

// timeCursorArgs returns the cursor (an instant and an id, nil on the
// first page) and the row limit of a page of a list ordered by an instant
// then by id: the core.OrderRecent list of customers.
func timeCursorArgs(p core.PageRequest) (*uuid.UUID, *time.Time, int32, error) {
	lim := int32(p.Limit()) + 1 // Limit is at most core.MaxPageSize
	if p.Token == "" {
		return nil, nil, lim, nil
	}
	c, err := core.DecodeCursor(p.Token)
	if err != nil {
		return nil, nil, 0, err
	}
	at, err := time.Parse(time.RFC3339Nano, c.Key)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("%w: page token", core.ErrInvalidValue)
	}
	return &c.ID, &at, lim, nil
}

func (r subscriptionRepo) MatchEvent(ctx context.Context, org core.OrganizationID, externalID string, t time.Time, recurringFallback bool) (*subscription.Subscription, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListSubscriptionsByExternalID(ctx, db.ListSubscriptionsByExternalIDParams{OrganizationID: org.UUID(), ExternalID: externalID})
	if err != nil {
		return nil, fmt.Errorf("match event subscription %q: %w", externalID, mapErr(err))
	}
	candidates, err := convAll(rows, func(r db.ListSubscriptionsByExternalIDRow) (*subscription.Subscription, error) {
		return subscriptionOf(subscriptionRow(r))
	})
	if err != nil {
		return nil, err
	}
	if s := subscription.MatchEvent(candidates, t, recurringFallback); s != nil {
		return s, nil
	}
	return nil, notFound("subscription", externalID)
}

func (r subscriptionRepo) ListByCustomer(ctx context.Context, org core.OrganizationID, customer core.CustomerID, statuses ...subscription.Status) ([]*subscription.Subscription, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListSubscriptionsByCustomer(ctx, db.ListSubscriptionsByCustomerParams{
		OrganizationID: org.UUID(), CustomerID: customer.UUID(), Statuses: enumStrings(statuses),
	})
	if err != nil {
		return nil, fmt.Errorf("list subscriptions of customer %s: %w", customer, mapErr(err))
	}
	return convAll(rows, func(r db.ListSubscriptionsByCustomerRow) (*subscription.Subscription, error) {
		return subscriptionOf(subscriptionRow(r))
	})
}

func (r subscriptionRepo) InitialStartedAt(ctx context.Context, org core.OrganizationID, customer core.CustomerID, externalID string) (*time.Time, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	t, err := q.GetInitialSubscriptionStartedAt(ctx, db.GetInitialSubscriptionStartedAtParams{
		OrganizationID: org.UUID(), CustomerID: customer.UUID(), ExternalID: externalID,
	})
	switch err = mapErr(err); {
	case errors.Is(err, core.ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("initial start of subscription %q of customer %s: %w", externalID, customer, err)
	}
	return optUTC(t), nil
}

func (r subscriptionRepo) ListActiveOrPendingByPlan(ctx context.Context, org core.OrganizationID, plan core.PlanID) ([]*subscription.Subscription, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListActiveOrPendingSubscriptionsByPlan(ctx, db.ListActiveOrPendingSubscriptionsByPlanParams{OrganizationID: org.UUID(), PlanID: optUUID(plan)})
	if err != nil {
		return nil, fmt.Errorf("list subscriptions of plan %s: %w", plan, mapErr(err))
	}
	return convAll(rows, func(r db.ListActiveOrPendingSubscriptionsByPlanRow) (*subscription.Subscription, error) {
		return subscriptionOf(subscriptionRow(r))
	})
}

func (r subscriptionRepo) ListDueForBilling(ctx context.Context, org core.OrganizationID, t time.Time, page core.PageRequest) (core.Page[*subscription.Subscription], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*subscription.Subscription]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*subscription.Subscription]{}, err
	}
	rows, err := q.ListSubscriptionsDueForBilling(ctx, db.ListSubscriptionsDueForBillingParams{
		OrganizationID: org.UUID(), At: utc(t), After: after, Lim: lim,
	})
	if err != nil {
		return core.Page[*subscription.Subscription]{}, fmt.Errorf("list subscriptions due for billing: %w", mapErr(err))
	}
	// The cursor is the ID, so pagination stays in ID order; the page is
	// then ordered by next_billing_at.
	p, err := pageOf(rows, page,
		func(r db.ListSubscriptionsDueForBillingRow) (*subscription.Subscription, error) {
			return subscriptionOf(subscriptionRow(r))
		},
		func(s *subscription.Subscription) string { return s.ID.String() })
	if err != nil {
		return p, err
	}
	slices.SortStableFunc(p.Items, func(a, b *subscription.Subscription) int { return a.NextBillingAt.Compare(*b.NextBillingAt) })
	return p, nil
}

func (r subscriptionRepo) ListPendingToActivate(ctx context.Context, org core.OrganizationID, t time.Time) ([]*subscription.Subscription, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListPendingSubscriptionsToActivate(ctx, db.ListPendingSubscriptionsToActivateParams{OrganizationID: org.UUID(), At: utc(t)})
	if err != nil {
		return nil, fmt.Errorf("list subscriptions to activate: %w", mapErr(err))
	}
	return convAll(rows, func(r db.ListPendingSubscriptionsToActivateRow) (*subscription.Subscription, error) {
		return subscriptionOf(subscriptionRow(r))
	})
}

func (r subscriptionRepo) GetNext(ctx context.Context, org core.OrganizationID, id core.SubscriptionID) (*subscription.Subscription, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	u := id.UUID()
	row, err := q.GetNextSubscription(ctx, db.GetNextSubscriptionParams{OrganizationID: org.UUID(), PreviousSubscriptionID: &u})
	if err != nil {
		return nil, getErr("next subscription of", id, err)
	}
	return subscriptionOf(subscriptionRow(row))
}

func (r subscriptionRepo) Chain(ctx context.Context, org core.OrganizationID, id core.SubscriptionID) ([]*subscription.Subscription, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.SubscriptionChain(ctx, db.SubscriptionChainParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, fmt.Errorf("subscription chain of %s: %w", id, mapErr(err))
	}
	if len(rows) == 0 {
		return nil, notFound("subscription", id)
	}
	return convAll(rows, func(r db.SubscriptionChainRow) (*subscription.Subscription, error) {
		return subscriptionOf(subscriptionRow(r))
	})
}

func (r subscriptionRepo) ListEndingBetween(ctx context.Context, org core.OrganizationID, from, to time.Time) ([]*subscription.Subscription, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListSubscriptionsEndingBetween(ctx, db.ListSubscriptionsEndingBetweenParams{
		OrganizationID: org.UUID(), FromAt: utc(from), ToAt: utc(to),
	})
	if err != nil {
		return nil, fmt.Errorf("list subscriptions ending: %w", mapErr(err))
	}
	return convAll(rows, func(r db.ListSubscriptionsEndingBetweenRow) (*subscription.Subscription, error) {
		return subscriptionOf(subscriptionRow(r))
	})
}

func (r subscriptionRepo) ListIncompleteWithExpiredRules(ctx context.Context, org core.OrganizationID, now time.Time) ([]*subscription.Subscription, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListIncompleteSubscriptionsWithExpiredRules(ctx, db.ListIncompleteSubscriptionsWithExpiredRulesParams{
		OrganizationID: org.UUID(), Now: utc(now),
	})
	if err != nil {
		return nil, fmt.Errorf("list incomplete subscriptions: %w", mapErr(err))
	}
	return convAll(rows, func(r db.ListIncompleteSubscriptionsWithExpiredRulesRow) (*subscription.Subscription, error) {
		return subscriptionOf(subscriptionRow(r))
	})
}

// activationRuleRepo implements subscription.ActivationRuleRepository.
type activationRuleRepo struct{ t *txState }

var _ subscription.ActivationRuleRepository = activationRuleRepo{}

func activationRuleParams(a *subscription.ActivationRule) db.CreateActivationRuleParams {
	return db.CreateActivationRuleParams{
		OrganizationID: a.OrganizationID.UUID(),
		ID:             a.ID.UUID(),
		SubscriptionID: a.SubscriptionID.UUID(),
		RuleType:       string(a.RuleType),
		TimeoutHours:   int32(a.TimeoutHours), // validated small
		Status:         string(a.Status),
		ExpiresAt:      optUTC(a.ExpiresAt),
		CreatedAt:      utc(a.CreatedAt),
		UpdatedAt:      utc(a.UpdatedAt),
	}
}

func activationRuleOf(r db.SubscriptionActivationRule) (*subscription.ActivationRule, error) {
	return &subscription.ActivationRule{
		ID:             idOf[core.ActivationRuleID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		SubscriptionID: idOf[core.SubscriptionID](r.SubscriptionID),
		RuleType:       subscription.ActivationRuleType(r.RuleType),
		TimeoutHours:   int(r.TimeoutHours),
		Status:         subscription.ActivationRuleStatus(r.Status),
		ExpiresAt:      optUTC(r.ExpiresAt),
		CreatedAt:      utc(r.CreatedAt),
		UpdatedAt:      utc(r.UpdatedAt),
	}, nil
}

func (r activationRuleRepo) Create(ctx context.Context, a *subscription.ActivationRule) error {
	q, err := r.t.queries(ctx, a.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateActivationRule(ctx, activationRuleParams(a))
	return insertErr("activation_rule", n, err)
}

func (r activationRuleRepo) Update(ctx context.Context, a *subscription.ActivationRule) error {
	q, err := r.t.queries(ctx, a.OrganizationID)
	if err != nil {
		return err
	}
	// The subscription may change to a missing one: guard the update.
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateActivationRule(ctx, db.UpdateActivationRuleParams(activationRuleParams(a)))
		return err
	})
	return updateErr("activation_rule", a.ID, n, err)
}

func (r activationRuleRepo) ListBySubscription(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID) ([]*subscription.ActivationRule, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListActivationRulesBySubscription(ctx, db.ListActivationRulesBySubscriptionParams{OrganizationID: org.UUID(), SubscriptionID: sub.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list activation rules of %s: %w", sub, mapErr(err))
	}
	return convAll(rows, activationRuleOf)
}

func (r activationRuleRepo) ReplaceForSubscription(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID, rules []*subscription.ActivationRule) error {
	for i, a := range rules {
		if a.OrganizationID != org || a.SubscriptionID != sub {
			return notFound("activation_rule", a.ID)
		}
		if slices.ContainsFunc(rules[:i], func(b *subscription.ActivationRule) bool { return b.ID == a.ID }) {
			return conflict("activation_rule", "id")
		}
	}
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	// A rule ID taken by another subscription's rule violates the primary
	// key: the savepoint undoes the deletion.
	err = r.t.guard(ctx, func() error {
		if err := q.DeleteActivationRulesBySubscription(ctx, db.DeleteActivationRulesBySubscriptionParams{OrganizationID: org.UUID(), SubscriptionID: sub.UUID()}); err != nil {
			return err
		}
		for _, a := range rules {
			if err := q.InsertActivationRule(ctx, db.InsertActivationRuleParams(activationRuleParams(a))); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("replace activation rules of %s: %w", sub, err)
	}
	return nil
}

// fixedChargeEventRepo implements subscription.FixedChargeEventRepository.
type fixedChargeEventRepo struct{ t *txState }

var _ subscription.FixedChargeEventRepository = fixedChargeEventRepo{}

func fixedChargeEventOf(r db.FixedChargeEvent) (*subscription.FixedChargeEvent, error) {
	return &subscription.FixedChargeEvent{
		ID:                    idOf[core.FixedChargeEventID](r.ID),
		OrganizationID:        idOf[core.OrganizationID](r.OrganizationID),
		SubscriptionID:        idOf[core.SubscriptionID](r.SubscriptionID),
		FixedChargeID:         idOf[core.FixedChargeID](r.FixedChargeID),
		Units:                 r.Units,
		Timestamp:             utc(r.Timestamp),
		ApplyUnitsImmediately: r.ApplyUnitsImmediately,
		CreatedAt:             utc(r.CreatedAt),
		UpdatedAt:             utc(r.UpdatedAt),
	}, nil
}

func (r fixedChargeEventRepo) Record(ctx context.Context, e *subscription.FixedChargeEvent, loc *time.Location) error {
	q, err := r.t.queries(ctx, e.OrganizationID)
	if err != nil {
		return err
	}
	org := e.OrganizationID.UUID()
	// Insert first, so that a taken ID removes nothing; then drop the
	// other events of the same day in loc.
	p := db.CreateFixedChargeEventParams{
		OrganizationID:        org,
		ID:                    e.ID.UUID(),
		SubscriptionID:        e.SubscriptionID.UUID(),
		FixedChargeID:         e.FixedChargeID.UUID(),
		Units:                 e.Units,
		Timestamp:             utc(e.Timestamp),
		ApplyUnitsImmediately: e.ApplyUnitsImmediately,
		CreatedAt:             utc(e.CreatedAt),
		UpdatedAt:             utc(e.UpdatedAt),
	}
	n, err := q.CreateFixedChargeEvent(ctx, p)
	if err := insertErr("fixed_charge_event", n, err); err != nil {
		return err
	}
	fc := e.FixedChargeID.UUID()
	rows, err := q.ListFixedChargeEventsBySubscription(ctx, db.ListFixedChargeEventsBySubscriptionParams{
		OrganizationID: org, SubscriptionID: e.SubscriptionID.UUID(), FixedChargeID: &fc,
	})
	if err != nil {
		return fmt.Errorf("record fixed_charge_event %s: %w", e.ID, mapErr(err))
	}
	var drop []uuid.UUID
	for _, row := range rows {
		x := &subscription.FixedChargeEvent{
			ID: idOf[core.FixedChargeEventID](row.ID), SubscriptionID: e.SubscriptionID, FixedChargeID: e.FixedChargeID,
			Timestamp: utc(row.Timestamp),
		}
		if x.ID != e.ID && x.SameDay(e, loc) {
			drop = append(drop, row.ID)
		}
	}
	if len(drop) == 0 {
		return nil
	}
	if err := q.DeleteFixedChargeEvents(ctx, db.DeleteFixedChargeEventsParams{OrganizationID: org, Ids: drop}); err != nil {
		return fmt.Errorf("record fixed_charge_event %s: %w", e.ID, mapErr(err))
	}
	return nil
}

func (r fixedChargeEventRepo) ListBySubscription(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID, fixedCharge core.FixedChargeID) ([]*subscription.FixedChargeEvent, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListFixedChargeEventsBySubscription(ctx, db.ListFixedChargeEventsBySubscriptionParams{
		OrganizationID: org.UUID(), SubscriptionID: sub.UUID(), FixedChargeID: optUUID(fixedCharge),
	})
	if err != nil {
		return nil, fmt.Errorf("list fixed charge events of %s: %w", sub, mapErr(err))
	}
	return convAll(rows, fixedChargeEventOf)
}

// appliedUsageThresholdRepo implements
// subscription.AppliedUsageThresholdRepository.
type appliedUsageThresholdRepo struct{ t *txState }

var _ subscription.AppliedUsageThresholdRepository = appliedUsageThresholdRepo{}

func appliedUsageThresholdOf(r db.AppliedUsageThreshold) (*subscription.AppliedUsageThreshold, error) {
	return &subscription.AppliedUsageThreshold{
		ID:                  idOf[core.AppliedUsageThresholdID](r.ID),
		OrganizationID:      idOf[core.OrganizationID](r.OrganizationID),
		UsageThresholdID:    idOf[core.UsageThresholdID](r.UsageThresholdID),
		SubscriptionID:      idOf[core.SubscriptionID](r.SubscriptionID),
		InvoiceID:           idOf[core.InvoiceID](r.InvoiceID),
		LifetimeUsageAmount: r.LifetimeUsageAmount,
		CreatedAt:           utc(r.CreatedAt),
		UpdatedAt:           utc(r.UpdatedAt),
	}, nil
}

func (r appliedUsageThresholdRepo) Create(ctx context.Context, a *subscription.AppliedUsageThreshold) error {
	q, err := r.t.queries(ctx, a.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateAppliedUsageThreshold(ctx, db.CreateAppliedUsageThresholdParams{
		OrganizationID:      a.OrganizationID.UUID(),
		ID:                  a.ID.UUID(),
		UsageThresholdID:    a.UsageThresholdID.UUID(),
		SubscriptionID:      a.SubscriptionID.UUID(),
		InvoiceID:           a.InvoiceID.UUID(),
		LifetimeUsageAmount: a.LifetimeUsageAmount,
		CreatedAt:           utc(a.CreatedAt),
		UpdatedAt:           utc(a.UpdatedAt),
	})
	return insertErr("applied_usage_threshold", n, err)
}

func (r appliedUsageThresholdRepo) ListBySubscription(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID) ([]*subscription.AppliedUsageThreshold, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListAppliedUsageThresholdsBySubscription(ctx, db.ListAppliedUsageThresholdsBySubscriptionParams{OrganizationID: org.UUID(), SubscriptionID: sub.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list applied usage thresholds of %s: %w", sub, mapErr(err))
	}
	return convAll(rows, appliedUsageThresholdOf)
}

func (r appliedUsageThresholdRepo) ListByInvoice(ctx context.Context, org core.OrganizationID, inv core.InvoiceID) ([]*subscription.AppliedUsageThreshold, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListAppliedUsageThresholdsByInvoice(ctx, db.ListAppliedUsageThresholdsByInvoiceParams{OrganizationID: org.UUID(), InvoiceID: inv.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list applied usage thresholds of invoice %s: %w", inv, mapErr(err))
	}
	return convAll(rows, appliedUsageThresholdOf)
}

// lifetimeUsageRepo implements subscription.LifetimeUsageRepository.
type lifetimeUsageRepo struct{ t *txState }

var _ subscription.LifetimeUsageRepository = lifetimeUsageRepo{}

func lifetimeUsageOf(r db.LifetimeUsage) (*subscription.LifetimeUsage, error) {
	cur, err := mustCurrency(r.Currency)
	if err != nil {
		return nil, fmt.Errorf("lifetime usage %s: %w", r.ID, err)
	}
	return &subscription.LifetimeUsage{
		ID:                             idOf[core.LifetimeUsageID](r.ID),
		OrganizationID:                 idOf[core.OrganizationID](r.OrganizationID),
		SubscriptionID:                 idOf[core.SubscriptionID](r.SubscriptionID),
		ExternalSubscriptionID:         r.ExternalSubscriptionID,
		Currency:                       cur,
		HistoricalUsageAmount:          r.HistoricalUsageAmount,
		InvoicedUsageAmount:            r.InvoicedUsageAmount,
		CurrentUsageAmount:             r.CurrentUsageAmount,
		RecalculateInvoicedUsage:       r.RecalculateInvoicedUsage,
		RecalculateCurrentUsage:        r.RecalculateCurrentUsage,
		InvoicedUsageAmountRefreshedAt: optUTC(r.InvoicedUsageAmountRefreshedAt),
		CurrentUsageAmountRefreshedAt:  optUTC(r.CurrentUsageAmountRefreshedAt),
		CreatedAt:                      utc(r.CreatedAt),
		UpdatedAt:                      utc(r.UpdatedAt),
	}, nil
}

func (r lifetimeUsageRepo) GetByExternalID(ctx context.Context, org core.OrganizationID, externalID string) (*subscription.LifetimeUsage, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetLifetimeUsageByExternalID(ctx, db.GetLifetimeUsageByExternalIDParams{OrganizationID: org.UUID(), ExternalSubscriptionID: externalID})
	if err != nil {
		return nil, getErr("lifetime_usage", externalID, err)
	}
	return lifetimeUsageOf(row)
}

func (r lifetimeUsageRepo) GetBySubscription(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID) (*subscription.LifetimeUsage, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetLifetimeUsageBySubscription(ctx, db.GetLifetimeUsageBySubscriptionParams{OrganizationID: org.UUID(), SubscriptionID: sub.UUID()})
	if err != nil {
		return nil, getErr("lifetime_usage", sub, err)
	}
	return lifetimeUsageOf(row)
}

func (r lifetimeUsageRepo) Upsert(ctx context.Context, l *subscription.LifetimeUsage) error {
	q, err := r.t.queries(ctx, l.OrganizationID)
	if err != nil {
		return err
	}
	// An ID taken by another chain's record violates the primary key,
	// which the upsert's conflict target does not cover: guard it.
	var row db.UpsertLifetimeUsageRow
	err = r.t.guard(ctx, func() (err error) {
		row, err = q.UpsertLifetimeUsage(ctx, db.UpsertLifetimeUsageParams{
			OrganizationID:                 l.OrganizationID.UUID(),
			ID:                             l.ID.UUID(),
			SubscriptionID:                 l.SubscriptionID.UUID(),
			ExternalSubscriptionID:         l.ExternalSubscriptionID,
			Currency:                       l.Currency.Code(),
			HistoricalUsageAmount:          l.HistoricalUsageAmount,
			InvoicedUsageAmount:            l.InvoicedUsageAmount,
			CurrentUsageAmount:             l.CurrentUsageAmount,
			RecalculateInvoicedUsage:       l.RecalculateInvoicedUsage,
			RecalculateCurrentUsage:        l.RecalculateCurrentUsage,
			InvoicedUsageAmountRefreshedAt: optUTC(l.InvoicedUsageAmountRefreshedAt),
			CurrentUsageAmountRefreshedAt:  optUTC(l.CurrentUsageAmountRefreshedAt),
			CreatedAt:                      utc(l.CreatedAt),
			UpdatedAt:                      utc(l.UpdatedAt),
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("upsert lifetime_usage %s: %w", l.ExternalSubscriptionID, err)
	}
	l.ID = idOf[core.LifetimeUsageID](row.ID)
	l.CreatedAt = utc(row.CreatedAt)
	return nil
}

func (r lifetimeUsageRepo) ListToRecalculate(ctx context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*subscription.LifetimeUsage], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*subscription.LifetimeUsage]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*subscription.LifetimeUsage]{}, err
	}
	rows, err := q.ListLifetimeUsagesToRecalculate(ctx, db.ListLifetimeUsagesToRecalculateParams{OrganizationID: org.UUID(), After: after, Lim: lim})
	if err != nil {
		return core.Page[*subscription.LifetimeUsage]{}, fmt.Errorf("list lifetime usages to recalculate: %w", mapErr(err))
	}
	return pageOf(rows, page, lifetimeUsageOf, func(l *subscription.LifetimeUsage) string { return l.ID.String() })
}
