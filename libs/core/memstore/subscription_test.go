package memstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func TestSubscriptionRepos(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	ids := core.UUIDv7Generator{}
	d := newSubscriptionData()
	subs := subscriptionRepo{d}
	org, other := core.NewID[core.OrganizationID](ids), core.NewID[core.OrganizationID](ids)
	customer, plan := core.NewID[core.CustomerID](ids), core.NewID[core.PlanID](ids)
	newSub := func(ext string, st subscription.Status) *subscription.Subscription {
		s := subscription.New(org, core.NewID[core.SubscriptionID](ids), customer, plan, ext, now, subscription.PlanFacts{}, now)
		s.Status = st
		if st != subscription.StatusPending {
			started := now.Add(-time.Hour)
			s.StartedAt = &started
		}
		return s
	}

	// spec/models/subscription_spec.rb:243: external_id is unique per live
	// status.
	active := newSub("sub_1", subscription.StatusActive)
	if err := subs.Create(ctx, active); err != nil {
		t.Fatal(err)
	}
	incomplete := newSub("sub_1", subscription.StatusIncomplete)
	if err := subs.Create(ctx, incomplete); err != nil {
		t.Errorf("Create(incomplete beside active): got = %v, want = nil", err)
	}
	if err := subs.Create(ctx, newSub("sub_1", subscription.StatusActive)); !errors.Is(err, core.ErrConflict) {
		t.Errorf("Create(second active): got = %v, want = ErrConflict", err)
	}
	if err := subs.Create(ctx, newSub("sub_1", subscription.StatusIncomplete)); !errors.Is(err, core.ErrConflict) {
		t.Errorf("Create(second incomplete): got = %v, want = ErrConflict", err)
	}
	next := newSub("sub_1", subscription.StatusPending)
	next.PreviousSubscriptionID = active.ID
	if err := subs.Create(ctx, next); err != nil {
		t.Errorf("Create(pending downgrade): got = %v, want = nil", err)
	}
	// A pending subscription activated beside an active one is refused.
	next.Status = subscription.StatusActive
	next.StartedAt = &now
	if err := subs.Update(ctx, next); !errors.Is(err, core.ErrConflict) {
		t.Errorf("Update(activation beside active): got = %v, want = ErrConflict", err)
	}
	next.Status, next.StartedAt = subscription.StatusPending, nil

	if got, err := subs.GetByExternalID(ctx, org, "sub_1"); err != nil || got.ID != active.ID {
		t.Errorf("GetByExternalID: got = %v %v, want = %v", got, err, active.ID)
	}
	if _, err := subs.Get(ctx, other, active.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get(other organization): got = %v, want = ErrNotFound", err)
	}
	if got, err := subs.GetNext(ctx, org, active.ID); err != nil || got.ID != next.ID {
		t.Errorf("GetNext: got = %v %v, want = %v", got, err, next.ID)
	}
	if chain, err := subs.Chain(ctx, org, next.ID); err != nil || len(chain) != 2 || chain[0].ID != active.ID {
		t.Errorf("Chain: got = %v %v, want = [active next]", chain, err)
	}
	if got, err := subs.MatchEvent(ctx, org, "sub_1", now, false); err != nil || got.ID != active.ID {
		t.Errorf("MatchEvent: got = %v %v, want = %v", got, err, active.ID)
	}
	if _, err := subs.MatchEvent(ctx, org, "sub_1", now.Add(-2*time.Hour), false); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("MatchEvent(before start): got = %v, want = ErrNotFound", err)
	}
	if got, _ := subs.ListActiveOrPendingByPlan(ctx, org, plan); len(got) != 2 {
		t.Errorf("ListActiveOrPendingByPlan: got = %d, want = 2", len(got))
	}
	if got, _ := subs.ListByCustomer(ctx, org, customer, subscription.StatusIncomplete); len(got) != 1 || got[0].ID != incomplete.ID {
		t.Errorf("ListByCustomer(incomplete): got = %v, want = [%v]", got, incomplete.ID)
	}
	page, err := subs.List(ctx, org, subscription.Filter{Statuses: []subscription.Status{subscription.StatusActive, subscription.StatusPending}}, core.PageRequest{Size: 1})
	if err != nil || len(page.Items) != 1 || page.NextToken == "" {
		t.Errorf("List page 1: got = %v %v, want = one item and a token", page, err)
	}

	// Billing, activation and ending queries.
	due := now.Add(-time.Minute)
	active.NextBillingAt = &due
	ending := now.Add(15 * 24 * time.Hour)
	active.EndingAt = &ending
	if err := subs.Update(ctx, active); err != nil {
		t.Fatal(err)
	}
	if p, _ := subs.ListDueForBilling(ctx, org, now, core.PageRequest{}); len(p.Items) != 1 {
		t.Errorf("ListDueForBilling: got = %d, want = 1", len(p.Items))
	}
	if p, _ := subs.ListDueForBilling(ctx, org, due.Add(-time.Second), core.PageRequest{}); len(p.Items) != 0 {
		t.Errorf("ListDueForBilling(before): got = %d, want = 0", len(p.Items))
	}
	if got, _ := subs.ListEndingBetween(ctx, org, ending, ending.Add(time.Hour)); len(got) != 1 {
		t.Errorf("ListEndingBetween: got = %d, want = 1", len(got))
	}
	future := newSub("sub_2", subscription.StatusPending)
	future.SubscriptionAt = now.Add(time.Hour)
	if err := subs.Create(ctx, future); err != nil {
		t.Fatal(err)
	}
	if got, _ := subs.ListPendingToActivate(ctx, org, now); len(got) != 0 {
		t.Errorf("ListPendingToActivate(now): got = %d, want = 0", len(got))
	}
	if got, _ := subs.ListPendingToActivate(ctx, org, now.Add(time.Hour)); len(got) != 1 || got[0].ID != future.ID {
		t.Errorf("ListPendingToActivate(later): got = %v, want = [%v]", got, future.ID)
	}

	// Activation rules.
	rules := activationRuleRepo{d}
	rule := subscription.NewActivationRule(org, core.NewID[core.ActivationRuleID](ids), incomplete.ID, subscription.ActivationRuleTypePayment, 1, now)
	if err := rule.Start(now.Add(-2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := rules.ReplaceForSubscription(ctx, org, incomplete.ID, []*subscription.ActivationRule{rule}); err != nil {
		t.Fatal(err)
	}
	if got, _ := subs.ListIncompleteWithExpiredRules(ctx, org, now); len(got) != 1 || got[0].ID != incomplete.ID {
		t.Errorf("ListIncompleteWithExpiredRules: got = %v, want = [%v]", got, incomplete.ID)
	}
	if err := rule.Expire(now); err != nil {
		t.Fatal(err)
	}
	if err := rules.Update(ctx, rule); err != nil {
		t.Fatal(err)
	}
	if got, _ := rules.ListBySubscription(ctx, org, incomplete.ID); len(got) != 1 || got[0].Status != subscription.ActivationRuleStatusExpired {
		t.Errorf("ListBySubscription: got = %v, want = one expired rule", got)
	}
	if err := rules.ReplaceForSubscription(ctx, org, incomplete.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := rules.ListBySubscription(ctx, org, incomplete.ID); len(got) != 0 {
		t.Errorf("ReplaceForSubscription(nil): got = %d rules, want = 0", len(got))
	}

	// Fixed charge events: one per local day.
	events := fixedChargeEventRepo{d}
	fc := core.NewID[core.FixedChargeID](ids)
	fce := func(ts time.Time, units int64) *subscription.FixedChargeEvent {
		return &subscription.FixedChargeEvent{
			ID: core.NewID[core.FixedChargeEventID](ids), OrganizationID: org, SubscriptionID: active.ID,
			FixedChargeID: fc, Units: money.NewFromInt(units), Timestamp: ts,
		}
	}
	for _, e := range []*subscription.FixedChargeEvent{fce(now.Add(time.Hour), 1), fce(now.Add(5*time.Hour), 2), fce(now.Add(30*time.Hour), 3)} {
		if err := events.Record(ctx, e, time.UTC); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := events.ListBySubscription(ctx, org, active.ID, fc); len(got) != 2 || !got[0].Units.Equal(money.NewFromInt(2)) {
		t.Errorf("fixed charge events: got = %v, want = units 2 then 3", got)
	}

	// Applied usage thresholds.
	applied := appliedUsageThresholdRepo{d}
	threshold, invoice := core.NewID[core.UsageThresholdID](ids), core.NewID[core.InvoiceID](ids)
	a := &subscription.AppliedUsageThreshold{
		ID: core.NewID[core.AppliedUsageThresholdID](ids), OrganizationID: org,
		UsageThresholdID: threshold, SubscriptionID: active.ID, InvoiceID: invoice, LifetimeUsageAmount: 1000,
	}
	if err := applied.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	dupe := *a
	dupe.ID = core.NewID[core.AppliedUsageThresholdID](ids)
	if err := applied.Create(ctx, &dupe); !errors.Is(err, core.ErrConflict) {
		t.Errorf("Create(same threshold and invoice): got = %v, want = ErrConflict", err)
	}
	if got, _ := applied.ListByInvoice(ctx, org, invoice); len(got) != 1 {
		t.Errorf("ListByInvoice: got = %d, want = 1", len(got))
	}
	if got, _ := applied.ListBySubscription(ctx, org, active.ID); len(got) != 1 {
		t.Errorf("ListBySubscription: got = %d, want = 1", len(got))
	}

	// Lifetime usage follows the chain.
	usages := lifetimeUsageRepo{d}
	l := &subscription.LifetimeUsage{
		ID: core.NewID[core.LifetimeUsageID](ids), OrganizationID: org, SubscriptionID: active.ID,
		ExternalSubscriptionID: "sub_1", Currency: money.MustCurrency("EUR"), CurrentUsageAmount: 10,
	}
	if err := usages.Upsert(ctx, l); err != nil {
		t.Fatal(err)
	}
	moved := *l
	moved.ID, moved.SubscriptionID, moved.RecalculateCurrentUsage = core.NewID[core.LifetimeUsageID](ids), next.ID, true
	if err := usages.Upsert(ctx, &moved); err != nil {
		t.Fatal(err)
	}
	if moved.ID != l.ID {
		t.Errorf("Upsert ID: got = %v, want = %v", moved.ID, l.ID)
	}
	if got, err := usages.GetBySubscription(ctx, org, next.ID); err != nil || got.ID != l.ID {
		t.Errorf("GetBySubscription(next): got = %v %v, want = %v", got, err, l.ID)
	}
	if _, err := usages.GetBySubscription(ctx, org, active.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("GetBySubscription(previous): got = %v, want = ErrNotFound", err)
	}
	if p, _ := usages.ListToRecalculate(ctx, org, core.PageRequest{}); len(p.Items) != 1 {
		t.Errorf("ListToRecalculate: got = %d, want = 1", len(p.Items))
	}
	if got, err := usages.GetByExternalID(ctx, org, "sub_1"); err != nil || got.Total() != 10 {
		t.Errorf("GetByExternalID: got = %v %v, want = total 10", got, err)
	}

	// Snapshots are independent.
	snap := d.snapshot()
	active.Status = subscription.StatusTerminated
	active.TerminatedAt = &now
	if err := subs.Update(ctx, active); err != nil {
		t.Fatal(err)
	}
	if got, _ := (subscriptionRepo{snap}).Get(ctx, org, active.ID); got.Status != subscription.StatusActive {
		t.Errorf("snapshot status: got = %s, want = active", got.Status)
	}
}
