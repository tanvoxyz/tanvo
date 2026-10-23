package porttest

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// subscriptionParents are the rows a subscription references.
type subscriptionParents struct {
	org      core.OrganizationID
	entity   *organization.BillingEntity
	customer *customer.Customer
	other    *customer.Customer
	plan     *catalog.Plan
	plan2    *catalog.Plan
}

// subscriptionFixtureCustomer creates a customer of be.
func subscriptionFixtureCustomer(ctx context.Context, e *env, r port.Repos, be *organization.BillingEntity, externalID string) *customer.Customer {
	e.t.Helper()
	c := customer.New(be.OrganizationID, core.NewID[core.CustomerID](e.ids), externalID, be.ID, now)
	noErr(e.t, "Customers.Create() fixture", r.Customers.Create(ctx, c))
	return c
}

// subscriptionFixturePlan creates a monthly EUR plan.
func subscriptionFixturePlan(ctx context.Context, e *env, r port.Repos, org core.OrganizationID, code string) *catalog.Plan {
	e.t.Helper()
	p := &catalog.Plan{
		ID: core.NewID[core.PlanID](e.ids), OrganizationID: org, Code: code, Name: "Plan " + code,
		Interval: period.Monthly, Amount: 1000, Currency: money.MustCurrency("EUR"), Status: catalog.PlanStatusActive,
		CreatedAt: now, UpdatedAt: now,
	}
	noErr(e.t, "Plans.Create() fixture", r.Plans.Create(ctx, p))
	return p
}

// subscriptionFixtureInvoice creates a draft invoice of c.
func subscriptionFixtureInvoice(ctx context.Context, e *env, r port.Repos, c *customer.Customer) *invoice.Invoice {
	e.t.Helper()
	inv := &invoice.Invoice{
		ID: core.NewID[core.InvoiceID](e.ids), OrganizationID: c.OrganizationID, CustomerID: c.ID,
		BillingEntityID: c.BillingEntityID, Type: invoice.TypeProgressiveBilling, Status: invoice.StatusDraft,
		PaymentStatus: core.PaymentStatusPending, Currency: money.MustCurrency("EUR"),
		IssuingDate: core.NewDate(2025, time.March, 14), PaymentDueDate: core.NewDate(2025, time.March, 14),
		ExpectedFinalizationDate: core.NewDate(2025, time.March, 14),
		PaymentMethodType:        core.PaymentMethodTypeProvider, CreatedAt: now, UpdatedAt: now,
	}
	noErr(e.t, "Invoices.Create() fixture", r.Invoices.Create(ctx, inv))
	return inv
}

// subscriptionFixtures creates an organization (and a second one) with two
// customers and two plans.
func subscriptionFixtures(e *env) (p, other subscriptionParents) {
	e.t.Helper()
	a, b := e.twoOrgs()
	e.tx(func(ctx context.Context, r port.Repos) error {
		p = subscriptionParents{org: a.OrganizationID, entity: a}
		p.customer = subscriptionFixtureCustomer(ctx, e, r, a, "cus_1")
		p.other = subscriptionFixtureCustomer(ctx, e, r, a, "cus_2")
		p.plan = subscriptionFixturePlan(ctx, e, r, a.OrganizationID, "basic")
		p.plan2 = subscriptionFixturePlan(ctx, e, r, a.OrganizationID, "premium")
		other = subscriptionParents{org: b.OrganizationID, entity: b}
		other.customer = subscriptionFixtureCustomer(ctx, e, r, b, "cus_1")
		other.plan = subscriptionFixturePlan(ctx, e, r, b.OrganizationID, "basic")
		return nil
	})
	return p, other
}

// newSub returns an active subscription of p's customer to p's plan.
func (p subscriptionParents) newSub(e *env, externalID string) *subscription.Subscription {
	s := subscription.New(p.org, core.NewID[core.SubscriptionID](e.ids), p.customer.ID, p.plan.ID, externalID,
		at(-24*time.Hour), subscription.PlanFacts{}, now)
	s.Status = subscription.StatusActive
	s.StartedAt = ptr(at(-24 * time.Hour))
	return s
}

func subID(s *subscription.Subscription) core.SubscriptionID { return s.ID }

func testSubscription(t *testing.T, h Harness) {
	t.Run("subscriptions", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		p, other := subscriptionFixtures(e)
		org := p.org
		full := p.newSub(e, "sub_full")
		e.tx(func(ctx context.Context, r port.Repos) error {
			s1 := &invoice.CustomSection{
				ID: core.NewID[core.InvoiceCustomSectionID](e.ids), OrganizationID: org, Code: "bank", Name: "Bank",
				SectionType: invoice.CustomSectionTypeManual, CreatedAt: now, UpdatedAt: now,
			}
			s2 := &invoice.CustomSection{
				ID: core.NewID[core.InvoiceCustomSectionID](e.ids), OrganizationID: org, Code: "legal", Name: "Legal",
				SectionType: invoice.CustomSectionTypeManual, CreatedAt: now, UpdatedAt: now,
			}
			noErr(t, "InvoiceCustomSections.Create()", r.InvoiceCustomSections.Create(ctx, s1))
			noErr(t, "InvoiceCustomSections.Create()", r.InvoiceCustomSections.Create(ctx, s2))

			prev := p.newSub(e, "sub_full")
			prev.Status = subscription.StatusTerminated
			prev.TerminatedAt = ptr(at(-time.Hour))
			noErr(t, "Create() previous", r.Subscriptions.Create(ctx, prev))

			full.Name = ptr("Full")
			full.BillingTime = period.Anniversary
			full.EndingAt = ptr(at(30 * 24 * time.Hour))
			full.TrialEndedAt = ptr(at(-time.Hour))
			full.PreviousSubscriptionID = prev.ID
			full.OnTerminationCreditNote = subscription.TerminationCreditNoteRefund
			full.OnTerminationInvoice = subscription.TerminationInvoiceSkip
			full.ProgressiveBillingDisabled = true
			full.ConsolidateInvoice = false
			full.PurchaseOrderNumber = ptr("PO-1")
			full.BillingEntityID = p.entity.ID
			full.SkipInvoiceCustomSections = true
			full.InvoiceCustomSectionIDs = []core.InvoiceCustomSectionID{s2.ID, s1.ID}
			full.NextBillingAt = ptr(at(time.Hour))
			full.CreatedAt, full.UpdatedAt = at(time.Second), at(time.Second)
			noErr(t, "Create()", r.Subscriptions.Create(ctx, full))
			wantErr(t, "Create() with the same ID", r.Subscriptions.Create(ctx, full), core.ErrConflict)
			got, err := r.Subscriptions.Get(ctx, org, full.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, full)

			// Every optional field cleared.
			full.Name, full.EndingAt, full.TrialEndedAt = nil, nil, nil
			full.PreviousSubscriptionID = core.SubscriptionID{}
			full.OnTerminationCreditNote = ""
			full.PurchaseOrderNumber, full.NextBillingAt = nil, nil
			full.BillingEntityID = core.BillingEntityID{}
			full.InvoiceCustomSectionIDs = nil
			full.Status = subscription.StatusCanceled
			full.CanceledAt = ptr(at(time.Minute))
			full.CancellationReason = subscription.CancellationReasonManual
			full.UpdatedAt = at(time.Minute)
			noErr(t, "Update()", r.Subscriptions.Update(ctx, full))
			got, err = r.Subscriptions.Get(ctx, org, full.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, full)

			// Not found and isolation.
			_, err = r.Subscriptions.Get(ctx, org, core.NewID[core.SubscriptionID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)
			_, err = r.Subscriptions.Get(ctx, other.org, full.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			moved := *full
			moved.OrganizationID = other.org
			wantErr(t, "Update() through another organization", r.Subscriptions.Update(ctx, &moved), core.ErrNotFound)
			wantErr(t, "Update() unknown", r.Subscriptions.Update(ctx, p.newSub(e, "nope")), core.ErrNotFound)
			_, err = r.Subscriptions.GetByExternalID(ctx, other.org, "sub_full")
			wantErr(t, "GetByExternalID() from another organization", err, core.ErrNotFound)
			return nil
		})
	})

	t.Run("external id uniqueness", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		p, other := subscriptionFixtures(e)
		org := p.org
		e.tx(func(ctx context.Context, r port.Repos) error {
			active := p.newSub(e, "sub_1")
			noErr(t, "Create() active", r.Subscriptions.Create(ctx, active))
			wantErr(t, "Create() second active", r.Subscriptions.Create(ctx, p.newSub(e, "sub_1")), core.ErrConflict)

			pending := p.newSub(e, "sub_1")
			pending.Status, pending.StartedAt = subscription.StatusPending, nil
			pending.PreviousSubscriptionID = active.ID
			pending.PlanID = p.plan2.ID
			noErr(t, "Create() pending downgrade", r.Subscriptions.Create(ctx, pending))
			pending2 := p.newSub(e, "sub_1")
			pending2.Status, pending2.StartedAt = subscription.StatusPending, nil
			wantErr(t, "Create() second pending", r.Subscriptions.Create(ctx, pending2), core.ErrConflict)

			incomplete := p.newSub(e, "sub_1")
			incomplete.Status = subscription.StatusIncomplete
			noErr(t, "Create() incomplete", r.Subscriptions.Create(ctx, incomplete))

			for range 2 {
				dead := p.newSub(e, "sub_1")
				dead.Status, dead.TerminatedAt = subscription.StatusTerminated, ptr(at(-time.Hour))
				noErr(t, "Create() terminated", r.Subscriptions.Create(ctx, dead))
			}
			// The same external ID in another organization.
			o := subscription.New(other.org, core.NewID[core.SubscriptionID](e.ids), other.customer.ID, other.plan.ID, "sub_1", now, subscription.PlanFacts{}, now)
			o.Status, o.StartedAt = subscription.StatusActive, ptr(now)
			noErr(t, "Create() in another organization", r.Subscriptions.Create(ctx, o))

			// GetByExternalID prefers active, then incomplete, then pending.
			got, err := r.Subscriptions.GetByExternalID(ctx, org, "sub_1")
			noErr(t, "GetByExternalID()", err)
			equal(t, "GetByExternalID()", got.ID, active.ID)

			// Terminating the active subscription frees its status.
			active.Status, active.TerminatedAt = subscription.StatusTerminated, ptr(now)
			noErr(t, "Update() terminate", r.Subscriptions.Update(ctx, active))
			got, err = r.Subscriptions.GetByExternalID(ctx, org, "sub_1")
			noErr(t, "GetByExternalID() after termination", err)
			equal(t, "GetByExternalID() after termination", got.ID, incomplete.ID)

			// An update into a taken status conflicts and the transaction
			// goes on.
			pending.Status, pending.StartedAt = subscription.StatusIncomplete, ptr(now)
			wantErr(t, "Update() into a taken status", r.Subscriptions.Update(ctx, pending), core.ErrConflict)
			incomplete.Status = subscription.StatusActive
			noErr(t, "Update() after a conflict", r.Subscriptions.Update(ctx, incomplete))
			got, err = r.Subscriptions.GetByExternalID(ctx, org, "sub_1")
			noErr(t, "GetByExternalID() after activation", err)
			equal(t, "GetByExternalID() after activation", got, incomplete)

			next, err := r.Subscriptions.GetNext(ctx, org, active.ID)
			noErr(t, "GetNext()", err)
			equal(t, "GetNext()", next.ID, pending.ID)
			_, err = r.Subscriptions.GetNext(ctx, org, pending.ID)
			wantErr(t, "GetNext() without a next subscription", err, core.ErrNotFound)
			_, err = r.Subscriptions.GetNext(ctx, other.org, active.ID)
			wantErr(t, "GetNext() from another organization", err, core.ErrNotFound)

			_, err = r.Subscriptions.GetByExternalID(ctx, org, "sub_404")
			wantErr(t, "GetByExternalID() unknown", err, core.ErrNotFound)
			return nil
		})
	})

	t.Run("lists", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		p, other := subscriptionFixtures(e)
		org := p.org
		e.tx(func(ctx context.Context, r port.Repos) error {
			// s1: active, due at +2h, ending at +10d.
			s1 := p.newSub(e, "s1")
			s1.NextBillingAt = ptr(at(2 * time.Hour))
			s1.EndingAt = ptr(at(10 * 24 * time.Hour))
			// s2: active on the other customer and plan, due at +1h.
			s2 := p.newSub(e, "s2")
			s2.CustomerID, s2.PlanID = p.other.ID, p.plan2.ID
			s2.NextBillingAt = ptr(at(time.Hour))
			// s3: pending, starting at +1h.
			s3 := p.newSub(e, "s3")
			s3.Status, s3.StartedAt, s3.SubscriptionAt = subscription.StatusPending, nil, at(time.Hour)
			// s4: pending downgrade of s1.
			s4 := p.newSub(e, "s1")
			s4.Status, s4.StartedAt, s4.SubscriptionAt = subscription.StatusPending, nil, at(-time.Hour)
			s4.PreviousSubscriptionID = s1.ID
			// s5: terminated, due long ago (not billed).
			s5 := p.newSub(e, "s5")
			s5.Status, s5.TerminatedAt = subscription.StatusTerminated, ptr(at(-time.Hour))
			s5.NextBillingAt = ptr(at(-time.Hour))
			s5.EndingAt = ptr(at(24 * time.Hour))
			// s6: active, ending at +20d, not due.
			s6 := p.newSub(e, "s6")
			s6.EndingAt = ptr(at(20 * 24 * time.Hour))
			s6.NextBillingAt = ptr(at(48 * time.Hour))
			all := []*subscription.Subscription{s1, s2, s3, s4, s5, s6}
			for _, s := range all {
				noErr(t, "Create()", r.Subscriptions.Create(ctx, s))
			}
			o := subscription.New(other.org, core.NewID[core.SubscriptionID](e.ids), other.customer.ID, other.plan.ID, "s1", at(-time.Hour), subscription.PlanFacts{}, now)
			o.Status, o.StartedAt, o.NextBillingAt = subscription.StatusActive, ptr(at(-time.Hour)), ptr(at(-time.Hour))
			noErr(t, "Create() in another organization", r.Subscriptions.Create(ctx, o))

			list := func(f subscription.Filter) []core.SubscriptionID {
				return ids(listAll(t, 2, func(pr core.PageRequest) (core.Page[*subscription.Subscription], error) {
					return r.Subscriptions.List(ctx, org, f, pr)
				}), subID)
			}
			got := listAll(t, 1, func(pr core.PageRequest) (core.Page[*subscription.Subscription], error) {
				return r.Subscriptions.List(ctx, org, subscription.Filter{}, pr)
			})
			equal(t, "List()", got, all)
			equal(t, "List() by customer", list(subscription.Filter{CustomerID: p.other.ID}), []core.SubscriptionID{s2.ID})
			equal(t, "List() by plan", list(subscription.Filter{PlanID: p.plan.ID}), []core.SubscriptionID{s1.ID, s3.ID, s4.ID, s5.ID, s6.ID})
			equal(t, "List() by external ID", list(subscription.Filter{ExternalID: "s1"}), []core.SubscriptionID{s1.ID, s4.ID})
			equal(t, "List() by statuses", list(subscription.Filter{Statuses: []subscription.Status{subscription.StatusPending, subscription.StatusTerminated}}),
				[]core.SubscriptionID{s3.ID, s4.ID, s5.ID})
			equal(t, "List() by customer and status", list(subscription.Filter{CustomerID: p.customer.ID, Statuses: []subscription.Status{subscription.StatusActive}}),
				[]core.SubscriptionID{s1.ID, s6.ID})
			op, err := r.Subscriptions.List(ctx, other.org, subscription.Filter{}, core.PageRequest{})
			noErr(t, "List() of another organization", err)
			equal(t, "List() of another organization", ids(op.Items, subID), []core.SubscriptionID{o.ID})

			byCustomer, err := r.Subscriptions.ListByCustomer(ctx, org, p.customer.ID)
			noErr(t, "ListByCustomer()", err)
			equal(t, "ListByCustomer()", ids(byCustomer, subID), []core.SubscriptionID{s1.ID, s3.ID, s4.ID, s5.ID, s6.ID})
			byCustomer, err = r.Subscriptions.ListByCustomer(ctx, org, p.customer.ID, subscription.StatusPending, subscription.StatusTerminated)
			noErr(t, "ListByCustomer() with statuses", err)
			equal(t, "ListByCustomer() with statuses", ids(byCustomer, subID), []core.SubscriptionID{s3.ID, s4.ID, s5.ID})
			byCustomer, err = r.Subscriptions.ListByCustomer(ctx, other.org, p.customer.ID)
			noErr(t, "ListByCustomer() from another organization", err)
			equal(t, "ListByCustomer() from another organization", len(byCustomer), 0)

			// s4 shares s1's external ID but has not started.
			initial, err := r.Subscriptions.InitialStartedAt(ctx, org, p.customer.ID, "s1")
			noErr(t, "InitialStartedAt()", err)
			if initial == nil || !initial.Equal(*s1.StartedAt) {
				t.Errorf("InitialStartedAt(): got = %v, want = %v", initial, *s1.StartedAt)
			}
			initial, err = r.Subscriptions.InitialStartedAt(ctx, org, p.customer.ID, "s3")
			noErr(t, "InitialStartedAt() of a pending subscription", err)
			if initial != nil {
				t.Errorf("InitialStartedAt() of a pending subscription: got = %v, want = nil", *initial)
			}
			initial, err = r.Subscriptions.InitialStartedAt(ctx, org, p.other.ID, "s1")
			noErr(t, "InitialStartedAt() of another customer", err)
			if initial != nil {
				t.Errorf("InitialStartedAt() of another customer: got = %v, want = nil", *initial)
			}

			byPlan, err := r.Subscriptions.ListActiveOrPendingByPlan(ctx, org, p.plan.ID)
			noErr(t, "ListActiveOrPendingByPlan()", err)
			equal(t, "ListActiveOrPendingByPlan()", ids(byPlan, subID), []core.SubscriptionID{s1.ID, s3.ID, s4.ID, s6.ID})

			// Due for billing: active with next_billing_at ≤ t, each page
			// ordered by next_billing_at.
			due, err := r.Subscriptions.ListDueForBilling(ctx, org, at(2*time.Hour), core.PageRequest{})
			noErr(t, "ListDueForBilling()", err)
			equal(t, "ListDueForBilling()", ids(due.Items, subID), []core.SubscriptionID{s2.ID, s1.ID})
			dueAll := listAll(t, 1, func(pr core.PageRequest) (core.Page[*subscription.Subscription], error) {
				return r.Subscriptions.ListDueForBilling(ctx, org, at(48*time.Hour), pr)
			})
			equal(t, "ListDueForBilling() paged", ids(dueAll, subID), []core.SubscriptionID{s1.ID, s2.ID, s6.ID})
			due, err = r.Subscriptions.ListDueForBilling(ctx, org, at(time.Hour-time.Microsecond), core.PageRequest{})
			noErr(t, "ListDueForBilling() before", err)
			equal(t, "ListDueForBilling() before", len(due.Items), 0)

			toActivate, err := r.Subscriptions.ListPendingToActivate(ctx, org, at(time.Hour))
			noErr(t, "ListPendingToActivate()", err)
			equal(t, "ListPendingToActivate()", ids(toActivate, subID), []core.SubscriptionID{s3.ID})
			toActivate, err = r.Subscriptions.ListPendingToActivate(ctx, org, now)
			noErr(t, "ListPendingToActivate() before", err)
			equal(t, "ListPendingToActivate() before", len(toActivate), 0)

			ending, err := r.Subscriptions.ListEndingBetween(ctx, org, at(10*24*time.Hour), at(20*24*time.Hour))
			noErr(t, "ListEndingBetween()", err)
			equal(t, "ListEndingBetween()", ids(ending, subID), []core.SubscriptionID{s1.ID})
			ending, err = r.Subscriptions.ListEndingBetween(ctx, org, now, at(21*24*time.Hour))
			noErr(t, "ListEndingBetween() wide", err)
			equal(t, "ListEndingBetween() wide", ids(ending, subID), []core.SubscriptionID{s1.ID, s6.ID})
			return nil
		})
	})

	t.Run("chain and event matching", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		p, other := subscriptionFixtures(e)
		org := p.org
		e.tx(func(ctx context.Context, r port.Repos) error {
			first := p.newSub(e, "chain")
			first.StartedAt = ptr(at(-72 * time.Hour))
			first.Status, first.TerminatedAt = subscription.StatusTerminated, ptr(at(-48*time.Hour))
			second := p.newSub(e, "chain")
			second.StartedAt = ptr(at(-48 * time.Hour))
			second.Status, second.TerminatedAt = subscription.StatusTerminated, ptr(at(-24*time.Hour))
			second.PreviousSubscriptionID = first.ID
			third := p.newSub(e, "chain")
			third.StartedAt = ptr(at(-24 * time.Hour))
			third.PreviousSubscriptionID = second.ID
			for _, s := range []*subscription.Subscription{first, second, third} {
				noErr(t, "Create()", r.Subscriptions.Create(ctx, s))
			}
			chain, err := r.Subscriptions.Chain(ctx, org, third.ID)
			noErr(t, "Chain()", err)
			equal(t, "Chain()", chain, []*subscription.Subscription{first, second, third})
			chain, err = r.Subscriptions.Chain(ctx, org, first.ID)
			noErr(t, "Chain() of the first", err)
			equal(t, "Chain() of the first", ids(chain, subID), []core.SubscriptionID{first.ID})
			_, err = r.Subscriptions.Chain(ctx, org, core.NewID[core.SubscriptionID](e.ids))
			wantErr(t, "Chain() unknown", err, core.ErrNotFound)
			_, err = r.Subscriptions.Chain(ctx, other.org, third.ID)
			wantErr(t, "Chain() from another organization", err, core.ErrNotFound)

			for _, c := range []struct {
				name      string
				t         time.Time
				recurring bool
				want      *subscription.Subscription
			}{
				{"first period", at(-60 * time.Hour), false, first},
				{"second start", at(-48 * time.Hour), false, second},
				{"current", now, false, third},
				{"before every start", at(-100 * time.Hour), false, nil},
				{"recurring fallback", at(-100 * time.Hour), true, third},
			} {
				got, err := r.Subscriptions.MatchEvent(ctx, org, "chain", c.t, c.recurring)
				if c.want == nil {
					wantErr(t, "MatchEvent() "+c.name, err, core.ErrNotFound)
					continue
				}
				noErr(t, "MatchEvent() "+c.name, err)
				equal(t, "MatchEvent() "+c.name, got.ID, c.want.ID)
			}
			_, err = r.Subscriptions.MatchEvent(ctx, other.org, "chain", now, true)
			wantErr(t, "MatchEvent() from another organization", err, core.ErrNotFound)
			return nil
		})
	})

	t.Run("activation rules", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		p, other := subscriptionFixtures(e)
		org := p.org
		e.tx(func(ctx context.Context, r port.Repos) error {
			s := p.newSub(e, "gated")
			s.Status = subscription.StatusIncomplete
			s2 := p.newSub(e, "gated2")
			s2.Status = subscription.StatusIncomplete
			for _, x := range []*subscription.Subscription{s, s2} {
				noErr(t, "Subscriptions.Create()", r.Subscriptions.Create(ctx, x))
			}
			rule := subscription.NewActivationRule(org, core.NewID[core.ActivationRuleID](e.ids), s.ID, subscription.ActivationRuleTypePayment, 2, now)
			noErr(t, "Create()", r.ActivationRules.Create(ctx, rule))
			wantErr(t, "Create() with the same ID", r.ActivationRules.Create(ctx, rule), core.ErrConflict)
			got, err := r.ActivationRules.ListBySubscription(ctx, org, s.ID)
			noErr(t, "ListBySubscription()", err)
			equal(t, "ListBySubscription()", got, []*subscription.ActivationRule{rule})

			noErr(t, "Start()", rule.Start(at(-3*time.Hour)))
			noErr(t, "Update()", r.ActivationRules.Update(ctx, rule))
			got, err = r.ActivationRules.ListBySubscription(ctx, org, s.ID)
			noErr(t, "ListBySubscription() after Update()", err)
			equal(t, "ListBySubscription() after Update()", got, []*subscription.ActivationRule{rule})
			moved := *rule
			moved.OrganizationID = other.org
			wantErr(t, "Update() through another organization", r.ActivationRules.Update(ctx, &moved), core.ErrNotFound)
			unknown := subscription.NewActivationRule(org, core.NewID[core.ActivationRuleID](e.ids), s.ID, subscription.ActivationRuleTypePayment, 0, now)
			wantErr(t, "Update() unknown", r.ActivationRules.Update(ctx, unknown), core.ErrNotFound)
			got, err = r.ActivationRules.ListBySubscription(ctx, other.org, s.ID)
			noErr(t, "ListBySubscription() from another organization", err)
			equal(t, "ListBySubscription() from another organization", len(got), 0)

			// The rule expired an hour ago (started 3 h ago, 2 h timeout).
			expired, err := r.Subscriptions.ListIncompleteWithExpiredRules(ctx, org, now)
			noErr(t, "ListIncompleteWithExpiredRules()", err)
			equal(t, "ListIncompleteWithExpiredRules()", ids(expired, subID), []core.SubscriptionID{s.ID})
			// Expirable at its expiry (activation_rule.rb:35), not before.
			expired, err = r.Subscriptions.ListIncompleteWithExpiredRules(ctx, org, at(-time.Hour))
			noErr(t, "ListIncompleteWithExpiredRules() at expiry", err)
			equal(t, "ListIncompleteWithExpiredRules() at expiry", ids(expired, subID), []core.SubscriptionID{s.ID})
			expired, err = r.Subscriptions.ListIncompleteWithExpiredRules(ctx, org, at(-time.Hour-time.Second))
			noErr(t, "ListIncompleteWithExpiredRules() before expiry", err)
			equal(t, "ListIncompleteWithExpiredRules() before expiry", len(expired), 0)

			r1 := subscription.NewActivationRule(org, core.NewID[core.ActivationRuleID](e.ids), s.ID, subscription.ActivationRuleTypePayment, 0, at(time.Second))
			r2 := subscription.NewActivationRule(org, core.NewID[core.ActivationRuleID](e.ids), s.ID, subscription.ActivationRuleTypePayment, 5, at(time.Second))
			r2.Status = subscription.ActivationRuleStatusNotApplicable
			noErr(t, "ReplaceForSubscription()", r.ActivationRules.ReplaceForSubscription(ctx, org, s.ID, []*subscription.ActivationRule{r1, r2}))
			got, err = r.ActivationRules.ListBySubscription(ctx, org, s.ID)
			noErr(t, "ListBySubscription() after ReplaceForSubscription()", err)
			equal(t, "ListBySubscription() after ReplaceForSubscription()", got, []*subscription.ActivationRule{r1, r2})
			expired, err = r.Subscriptions.ListIncompleteWithExpiredRules(ctx, org, now)
			noErr(t, "ListIncompleteWithExpiredRules() after replacement", err)
			equal(t, "ListIncompleteWithExpiredRules() after replacement", len(expired), 0)

			foreign := subscription.NewActivationRule(org, core.NewID[core.ActivationRuleID](e.ids), s2.ID, subscription.ActivationRuleTypePayment, 0, now)
			wantErr(t, "ReplaceForSubscription() with another subscription's rule",
				r.ActivationRules.ReplaceForSubscription(ctx, org, s.ID, []*subscription.ActivationRule{foreign}), core.ErrNotFound)
			wantErr(t, "ReplaceForSubscription() with a repeated rule",
				r.ActivationRules.ReplaceForSubscription(ctx, org, s.ID, []*subscription.ActivationRule{r1, r1}), core.ErrConflict)
			got, err = r.ActivationRules.ListBySubscription(ctx, org, s.ID)
			noErr(t, "ListBySubscription() after refused replacements", err)
			equal(t, "ListBySubscription() after refused replacements", got, []*subscription.ActivationRule{r1, r2})

			noErr(t, "ReplaceForSubscription() with none", r.ActivationRules.ReplaceForSubscription(ctx, org, s.ID, nil))
			got, err = r.ActivationRules.ListBySubscription(ctx, org, s.ID)
			noErr(t, "ListBySubscription() after clearing", err)
			equal(t, "ListBySubscription() after clearing", len(got), 0)
			return nil
		})
	})

	t.Run("fixed charge events", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		p, other := subscriptionFixtures(e)
		org := p.org
		paris, err := time.LoadLocation("Europe/Paris")
		noErr(t, "LoadLocation()", err)
		e.tx(func(ctx context.Context, r port.Repos) error {
			addOn := &catalog.AddOn{
				ID: core.NewID[core.AddOnID](e.ids), OrganizationID: org, Code: "seat", Name: "Seat",
				Amount: 500, Currency: money.MustCurrency("EUR"), CreatedAt: now, UpdatedAt: now,
			}
			noErr(t, "AddOns.Create()", r.AddOns.Create(ctx, addOn))
			fixed := func(code string) *catalog.FixedCharge {
				props, err := catalog.NewProperties([]byte(`{"amount":"5"}`))
				noErr(t, "NewProperties()", err)
				f := &catalog.FixedCharge{
					ID: core.NewID[core.FixedChargeID](e.ids), OrganizationID: org, PlanID: p.plan.ID, Code: code,
					AddOnID: addOn.ID, ChargeModel: catalog.FixedChargeModelStandard, Properties: props,
					Units: money.MustParse("1"), CreatedAt: now, UpdatedAt: now,
				}
				noErr(t, "FixedCharges.Create()", r.FixedCharges.Create(ctx, f))
				return f
			}
			fc1, fc2 := fixed("seats"), fixed("rooms")
			s := p.newSub(e, "fc")
			noErr(t, "Subscriptions.Create()", r.Subscriptions.Create(ctx, s))

			ev := func(fc *catalog.FixedCharge, units string, ts time.Time, immediate bool) *subscription.FixedChargeEvent {
				return &subscription.FixedChargeEvent{
					ID: core.NewID[core.FixedChargeEventID](e.ids), OrganizationID: org, SubscriptionID: s.ID,
					FixedChargeID: fc.ID, Units: money.MustParse(units), Timestamp: ts, ApplyUnitsImmediately: immediate,
					CreatedAt: now, UpdatedAt: now,
				}
			}
			// 2025-03-14 22:30 UTC is 23:30 in Paris; 23:30 UTC is already
			// 2025-03-15 there.
			day := time.Date(2025, time.March, 14, 0, 0, 0, 0, time.UTC)
			a := ev(fc1, "2", day.Add(22*time.Hour+30*time.Minute), true)
			b := ev(fc1, "3", day.Add(23*time.Hour+30*time.Minute), false)
			c := ev(fc2, "4", day.Add(10*time.Hour), true)
			for _, x := range []*subscription.FixedChargeEvent{a, b, c} {
				noErr(t, "Record()", r.FixedChargeEvents.Record(ctx, x, paris))
			}
			wantErr(t, "Record() with the same ID", r.FixedChargeEvents.Record(ctx, b, paris), core.ErrConflict)
			got, err := r.FixedChargeEvents.ListBySubscription(ctx, org, s.ID, core.FixedChargeID{})
			noErr(t, "ListBySubscription()", err)
			equal(t, "ListBySubscription()", got, []*subscription.FixedChargeEvent{c, a, b})

			// A later event of 2025-03-15 in Paris replaces b only.
			d := ev(fc1, "5.5", day.Add(30*time.Hour), true)
			noErr(t, "Record() same day", r.FixedChargeEvents.Record(ctx, d, paris))
			got, err = r.FixedChargeEvents.ListBySubscription(ctx, org, s.ID, fc1.ID)
			noErr(t, "ListBySubscription() of one fixed charge", err)
			equal(t, "ListBySubscription() of one fixed charge", got, []*subscription.FixedChargeEvent{a, d})
			// In UTC, a and an event at 01:00 the same UTC day share the day.
			f := ev(fc1, "6", day.Add(time.Hour+24*time.Hour), true)
			noErr(t, "Record() in UTC", r.FixedChargeEvents.Record(ctx, f, time.UTC))
			got, err = r.FixedChargeEvents.ListBySubscription(ctx, org, s.ID, fc1.ID)
			noErr(t, "ListBySubscription() after a UTC record", err)
			equal(t, "ListBySubscription() after a UTC record", got, []*subscription.FixedChargeEvent{a, f})

			got, err = r.FixedChargeEvents.ListBySubscription(ctx, other.org, s.ID, core.FixedChargeID{})
			noErr(t, "ListBySubscription() from another organization", err)
			equal(t, "ListBySubscription() from another organization", len(got), 0)
			return nil
		})
	})

	t.Run("applied usage thresholds", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		p, other := subscriptionFixtures(e)
		org := p.org
		e.tx(func(ctx context.Context, r port.Repos) error {
			threshold := func(amount int64) *catalog.UsageThreshold {
				u := &catalog.UsageThreshold{
					ID: core.NewID[core.UsageThresholdID](e.ids), OrganizationID: org, PlanID: p.plan.ID,
					Amount: amount, CreatedAt: now, UpdatedAt: now,
				}
				noErr(t, "UsageThresholds.Create()", r.UsageThresholds.Create(ctx, u))
				return u
			}
			t1, t2 := threshold(1000), threshold(2000)
			s := p.newSub(e, "pb")
			s2 := p.newSub(e, "pb2")
			for _, x := range []*subscription.Subscription{s, s2} {
				noErr(t, "Subscriptions.Create()", r.Subscriptions.Create(ctx, x))
			}
			inv1 := subscriptionFixtureInvoice(ctx, e, r, p.customer)
			inv2 := subscriptionFixtureInvoice(ctx, e, r, p.customer)
			applied := func(th *catalog.UsageThreshold, sub *subscription.Subscription, inv *invoice.Invoice, amount int64) *subscription.AppliedUsageThreshold {
				return &subscription.AppliedUsageThreshold{
					ID: core.NewID[core.AppliedUsageThresholdID](e.ids), OrganizationID: org, UsageThresholdID: th.ID,
					SubscriptionID: sub.ID, InvoiceID: inv.ID, LifetimeUsageAmount: amount, CreatedAt: now, UpdatedAt: now,
				}
			}
			a1 := applied(t1, s, inv1, 1200)
			a2 := applied(t2, s, inv1, 2100)
			a3 := applied(t1, s2, inv2, 0)
			for _, a := range []*subscription.AppliedUsageThreshold{a1, a2, a3} {
				noErr(t, "Create()", r.AppliedUsageThresholds.Create(ctx, a))
			}
			wantErr(t, "Create() with the same ID", r.AppliedUsageThresholds.Create(ctx, a1), core.ErrConflict)
			wantErr(t, "Create() of a threshold twice on one invoice", r.AppliedUsageThresholds.Create(ctx, applied(t1, s, inv1, 5000)), core.ErrConflict)
			noErr(t, "Create() after a conflict", r.AppliedUsageThresholds.Create(ctx, applied(t2, s2, inv2, 2500)))

			got, err := r.AppliedUsageThresholds.ListBySubscription(ctx, org, s.ID)
			noErr(t, "ListBySubscription()", err)
			equal(t, "ListBySubscription()", got, []*subscription.AppliedUsageThreshold{a1, a2})
			got, err = r.AppliedUsageThresholds.ListByInvoice(ctx, org, inv2.ID)
			noErr(t, "ListByInvoice()", err)
			equal(t, "ListByInvoice() count", len(got), 2)
			equal(t, "ListByInvoice() first", got[0], a3)
			got, err = r.AppliedUsageThresholds.ListBySubscription(ctx, other.org, s.ID)
			noErr(t, "ListBySubscription() from another organization", err)
			equal(t, "ListBySubscription() from another organization", len(got), 0)
			got, err = r.AppliedUsageThresholds.ListByInvoice(ctx, other.org, inv1.ID)
			noErr(t, "ListByInvoice() from another organization", err)
			equal(t, "ListByInvoice() from another organization", len(got), 0)
			return nil
		})
	})

	t.Run("lifetime usages", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		p, other := subscriptionFixtures(e)
		org := p.org
		e.tx(func(ctx context.Context, r port.Repos) error {
			s1, s2, s3 := p.newSub(e, "lu1"), p.newSub(e, "lu1"), p.newSub(e, "lu2")
			s1.Status, s1.TerminatedAt = subscription.StatusTerminated, ptr(now)
			s2.PreviousSubscriptionID = s1.ID
			for _, s := range []*subscription.Subscription{s1, s2, s3} {
				noErr(t, "Subscriptions.Create()", r.Subscriptions.Create(ctx, s))
			}
			l1 := &subscription.LifetimeUsage{
				ID: core.NewID[core.LifetimeUsageID](e.ids), OrganizationID: org, SubscriptionID: s1.ID,
				ExternalSubscriptionID: "lu1", Currency: money.MustCurrency("EUR"), HistoricalUsageAmount: 100,
				InvoicedUsageAmount: 200, CurrentUsageAmount: 300, RecalculateInvoicedUsage: true,
				InvoicedUsageAmountRefreshedAt: ptr(at(-time.Hour)), CurrentUsageAmountRefreshedAt: ptr(at(-2 * time.Hour)),
				CreatedAt: now, UpdatedAt: now,
			}
			noErr(t, "Upsert() new", r.LifetimeUsages.Upsert(ctx, l1))
			got, err := r.LifetimeUsages.GetByExternalID(ctx, org, "lu1")
			noErr(t, "GetByExternalID()", err)
			equal(t, "GetByExternalID()", got, l1)

			// The chain moves to s2: a new record of the same chain keeps
			// the stored ID and creation instant.
			storedID := l1.ID
			moved := &subscription.LifetimeUsage{
				ID: core.NewID[core.LifetimeUsageID](e.ids), OrganizationID: org, SubscriptionID: s2.ID,
				ExternalSubscriptionID: "lu1", Currency: money.MustCurrency("USD"),
				CreatedAt: at(time.Hour), UpdatedAt: at(time.Hour),
			}
			noErr(t, "Upsert() existing chain", r.LifetimeUsages.Upsert(ctx, moved))
			equal(t, "Upsert() keeps the ID", moved.ID, storedID)
			equal(t, "Upsert() keeps the creation instant", moved.CreatedAt, now)
			got, err = r.LifetimeUsages.GetBySubscription(ctx, org, s2.ID)
			noErr(t, "GetBySubscription()", err)
			equal(t, "GetBySubscription()", got, moved)
			_, err = r.LifetimeUsages.GetBySubscription(ctx, org, s1.ID)
			wantErr(t, "GetBySubscription() of the previous subscription", err, core.ErrNotFound)
			_, err = r.LifetimeUsages.GetByExternalID(ctx, org, "lu404")
			wantErr(t, "GetByExternalID() unknown", err, core.ErrNotFound)
			_, err = r.LifetimeUsages.GetByExternalID(ctx, other.org, "lu1")
			wantErr(t, "GetByExternalID() from another organization", err, core.ErrNotFound)
			_, err = r.LifetimeUsages.GetBySubscription(ctx, other.org, s2.ID)
			wantErr(t, "GetBySubscription() from another organization", err, core.ErrNotFound)

			l2 := &subscription.LifetimeUsage{
				ID: core.NewID[core.LifetimeUsageID](e.ids), OrganizationID: org, SubscriptionID: s3.ID,
				ExternalSubscriptionID: "lu2", Currency: money.MustCurrency("EUR"), CreatedAt: now, UpdatedAt: now,
			}
			noErr(t, "Upsert() second chain", r.LifetimeUsages.Upsert(ctx, l2))
			list := func() []*subscription.LifetimeUsage {
				return listAll(t, 1, func(pr core.PageRequest) (core.Page[*subscription.LifetimeUsage], error) {
					return r.LifetimeUsages.ListToRecalculate(ctx, org, pr)
				})
			}
			equal(t, "ListToRecalculate() none flagged", len(list()), 0)
			moved.FlagForRecalculation(at(2 * time.Hour))
			noErr(t, "Upsert() flagged", r.LifetimeUsages.Upsert(ctx, moved))
			l2.RecalculateCurrentUsage = true
			noErr(t, "Upsert() flagged current", r.LifetimeUsages.Upsert(ctx, l2))
			equal(t, "ListToRecalculate() ignores a current usage flag", list(), []*subscription.LifetimeUsage{moved})
			l2.RecalculateInvoicedUsage = true
			noErr(t, "Upsert() flagged invoiced", r.LifetimeUsages.Upsert(ctx, l2))
			equal(t, "ListToRecalculate()", list(), []*subscription.LifetimeUsage{moved, l2})
			op, err := r.LifetimeUsages.ListToRecalculate(ctx, other.org, core.PageRequest{})
			noErr(t, "ListToRecalculate() of another organization", err)
			equal(t, "ListToRecalculate() of another organization", len(op.Items), 0)

			// A new chain with an ID already used by another chain.
			clash := &subscription.LifetimeUsage{
				ID: l2.ID, OrganizationID: org, SubscriptionID: s3.ID, ExternalSubscriptionID: "lu3",
				Currency: money.MustCurrency("EUR"), CreatedAt: now, UpdatedAt: now,
			}
			wantErr(t, "Upsert() with a taken ID", r.LifetimeUsages.Upsert(ctx, clash), core.ErrConflict)
			got, err = r.LifetimeUsages.GetByExternalID(ctx, org, "lu2")
			noErr(t, "GetByExternalID() after a conflict", err)
			equal(t, "GetByExternalID() after a conflict", got, l2)
			return nil
		})
	})
}
