package plans

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// planChanger changes the plan of a subscription; lifecycle.Service, the
// Terminator of the engine, implements it.
type planChanger interface {
	// ChangePlan upgrades or downgrades the subscription of in.
	ChangePlan(ctx context.Context, in lifecycle.ChangePlanInput) (*subscription.Subscription, error)
}

// UpdateInput is a partial update of a plan: only the set fields change.
type UpdateInput struct {
	// Code is the unique code; locked once the plan has subscriptions.
	Code core.Optional[string]
	// Name is the display name.
	Name core.Optional[string]
	// InvoiceDisplayName is the invoice line name of the subscription fee.
	InvoiceDisplayName core.Optional[*string]
	// Description is free text.
	Description core.Optional[*string]
	// Interval is the billing interval; locked once the plan has
	// subscriptions.
	Interval core.Optional[period.Interval]
	// Amount is the subscription fee, in minor units of the currency.
	Amount core.Optional[int64]
	// Currency is the plan's currency; locked once the plan has
	// subscriptions.
	Currency core.Optional[money.Currency]
	// PayInAdvance bills the subscription fee at the start of the period;
	// locked once the plan has subscriptions.
	PayInAdvance core.Optional[bool]
	// TrialPeriodDays is the trial period; locked once the plan has
	// subscriptions.
	TrialPeriodDays core.Optional[int]
	// BillChargesMonthly bills usage charges monthly on yearly and
	// semiannual plans; locked once the plan has subscriptions.
	BillChargesMonthly core.Optional[*bool]
	// BillFixedChargesMonthly bills fixed charges monthly on yearly and
	// semiannual plans; locked once the plan has subscriptions.
	BillFixedChargesMonthly core.Optional[*bool]
	// Taxes replace the plan's taxes; an empty list removes them.
	Taxes core.Optional[[]refs.Ref]
	// Metadata replaces the plan's metadata, or with MergeMetadata is
	// merged into it key by key.
	Metadata core.Optional[core.Metadata]
	// MergeMetadata merges Metadata into the plan's metadata instead of
	// replacing it (lago Plans::UpdateService partial_metadata).
	MergeMetadata bool
	// Charges replace the plan's charges: an input with the ID of a charge
	// of the plan updates it, one without creates a charge, and the
	// charges left out are removed.
	Charges core.Optional[[]ChargeInput]
	// FixedCharges replace the plan's fixed charges, as Charges.
	FixedCharges core.Optional[[]FixedChargeInput]
	// UsageThresholds replace the plan's usage thresholds.
	UsageThresholds core.Optional[[]ThresholdInput]
	// MinimumCommitment creates or updates the minimum commitment; a nil
	// or blank value removes it.
	MinimumCommitment core.Optional[*CommitmentInput]
	// Cascade applies the subscription fee and the charge changes to the
	// override plans that still match the plan.
	Cascade bool
}

// locked reports whether in sets a field that is locked once the plan has
// subscriptions.
func (in UpdateInput) locked() bool {
	return in.Code.Set || in.Interval.Set || in.PayInAdvance.Set || in.Currency.Set ||
		in.TrialPeriodDays.Set || in.BillChargesMonthly.Set || in.BillFixedChargesMonthly.Set
}

// Update changes the plan id (an original or override plan that is not
// deleted). Once the plan has subscriptions, changing its code, interval,
// payment timing, currency, trial or monthly billing is code
// value_is_immutable. A change of the subscription fee cancels the pending
// downgrades it turns into upgrades, upgrades the pending subscriptions
// whose previous plan now costs less, and with Cascade enqueues the same
// change on the override plans that had the previous fee. The plan's draft
// invoices are flagged for refresh and plan.updated is emitted.
//
// source: app/services/plans/update_service.rb:22-93
func (s *Service) Update(ctx context.Context, org core.OrganizationID, id core.PlanID, in UpdateInput) (*catalog.Plan, error) {
	var out *catalog.Plan
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := kept(ctx, r, org, id)
		if err != nil {
			return err
		}
		before, err := auditlog.ObjectOf(p)
		if err != nil {
			return err
		}
		old := *p
		in.Name.Apply(&p.Name)
		in.InvoiceDisplayName.Apply(&p.InvoiceDisplayName)
		in.Description.Apply(&p.Description)
		in.Amount.Apply(&p.Amount)
		in.Code.Apply(&p.Code)
		in.Interval.Apply(&p.Interval)
		in.PayInAdvance.Apply(&p.PayInAdvance)
		in.Currency.Apply(&p.Currency)
		in.TrialPeriodDays.Apply(&p.TrialPeriodDays)
		// An interval resets both settings, as lago does; a setting alone
		// is kept on the current interval (lago resets it to nil without
		// an interval, update_service.rb:47-48,147-149).
		if in.Interval.Set || in.BillChargesMonthly.Set {
			p.BillChargesMonthly = billMonthly(p.Interval, in.BillChargesMonthly.Value)
		}
		if in.Interval.Set || in.BillFixedChargesMonthly.Set {
			p.BillFixedChargesMonthly = billMonthly(p.Interval, in.BillFixedChargesMonthly.Value)
		}
		// A contract prices against the plan by its code and in its
		// currency, so both freeze once one uses it.
		if old.Code != p.Code || old.Currency != p.Currency {
			if err := contractLock(ctx, r, p); err != nil {
				return err
			}
		}
		// The plan's rate cards were validated against its currency, so it
		// freezes before any contract too.
		//
		// source: app/services/catalog_plans/update_service.rb:30-34
		if old.Currency != p.Currency {
			if err := rateCardsLock(ctx, r, p); err != nil {
				return err
			}
		}
		if err := validateChargeables(ctx, r, org, in.Charges.Value, in.FixedCharges.Value); err != nil {
			return err
		}
		if changed := catalog.PlanLockedChanges(&old, p); in.locked() && len(changed) > 0 {
			attached, err := hasSubscriptions(ctx, r, p)
			if err != nil {
				return err
			}
			if attached {
				errs := make([]core.FieldError, len(changed))
				for i, f := range changed {
					errs[i] = core.FieldError{Field: f, Code: core.CodeImmutable}
				}
				return &core.ValidationError{Errors: errs}
			}
		}
		if in.Metadata.Set {
			if in.MergeMetadata {
				p.Metadata = p.Metadata.Merge(in.Metadata.Value)
			} else {
				p.Metadata = in.Metadata.Value.Clone()
			}
		}
		if err := validatePlan(ctx, r, p); err != nil {
			return err
		}
		if in.Taxes.Set {
			ids, err := refs.Taxes(r, org).IDs(ctx, in.Taxes.Value)
			if err != nil {
				return err
			}
			p.TaxIDs = ids
		}
		p.UpdatedAt = s.d.Now()
		if err := r.Plans.Update(ctx, p); err != nil {
			return codeConflict(err)
		}
		children, err := r.Plans.ListChildren(ctx, org, p.ID)
		if err != nil {
			return fmt.Errorf("load override plans: %w", err)
		}
		cascade := in.Cascade && len(children) > 0
		if in.Charges.Set {
			if err := s.processPlanCharges(ctx, r, p, in.Charges.Value, cascade); err != nil {
				return err
			}
		}
		if in.FixedCharges.Set {
			if err := s.processPlanFixedCharges(ctx, r, p, in.FixedCharges.Value, cascade); err != nil {
				return err
			}
		}
		if in.UsageThresholds.Set {
			if err := s.updateThresholds(ctx, r, p, in.UsageThresholds.Value); err != nil {
				return err
			}
		}
		if in.MinimumCommitment.Set {
			if err := s.processCommitment(ctx, r, p, in.MinimumCommitment.Value); err != nil {
				return err
			}
		}
		if old.Amount != p.Amount {
			if err := s.processDowngraded(ctx, r, p); err != nil {
				return err
			}
			if err := s.processPending(ctx, r, p); err != nil {
				return err
			}
			if cascade {
				if err := s.cascadeAmount(ctx, r, children, old.Amount, p.Amount); err != nil {
					return err
				}
			}
		}
		if err := s.flagDrafts(ctx, r, p); err != nil {
			return err
		}
		out = p
		if err := kernel.Emit(ctx, s.d, org, webhook.EventTypePlanUpdated, "plan", p.ID.UUID(), p); err != nil {
			return err
		}
		// An override plan's update is not logged.
		//
		// source: app/services/plans/update_service.rb:16-20
		if p.IsChild() {
			return nil
		}
		return s.logPlan(ctx, r, auditlog.ActivityPlanUpdated, before, p)
	})
	if err != nil {
		return nil, fmt.Errorf("update plan %s: %w", id, err)
	}
	return out, nil
}

// kept returns the plan id unless it is deleted.
func kept(ctx context.Context, r port.Repos, org core.OrganizationID, id core.PlanID) (*catalog.Plan, error) {
	p, err := r.Plans.Get(ctx, org, id)
	if errors.Is(err, core.ErrNotFound) {
		return nil, core.NotFound(Resource)
	}
	if err != nil {
		return nil, fmt.Errorf("load plan: %w", err)
	}
	if p.Deleted() {
		return nil, core.NotFound(Resource)
	}
	return p, nil
}

// hasSubscriptions reports whether the plan p has subscriptions in any
// status (Plan#attached_to_subscriptions?).
//
// source: app/models/plan.rb:86-90
func hasSubscriptions(ctx context.Context, r port.Repos, p *catalog.Plan) (bool, error) {
	page, err := r.Subscriptions.List(ctx, p.OrganizationID, subscription.Filter{PlanID: p.ID}, core.PageRequest{Size: 1})
	if err != nil {
		return false, fmt.Errorf("load subscriptions: %w", err)
	}
	return len(page.Items) > 0, nil
}

// updateThresholds replaces the usage thresholds of the plan p and, when
// the plan keeps some, flags the lifetime usages of its subscriptions for
// refresh (Plans::UpdateUsageThresholdsService).
//
// source: app/services/plans/update_usage_thresholds_service.rb:13-25
func (s *Service) updateThresholds(ctx context.Context, r port.Repos, p *catalog.Plan, in []ThresholdInput) error {
	if err := s.replaceThresholds(ctx, r, p, in); err != nil {
		return err
	}
	if len(in) == 0 {
		return nil
	}
	if _, err := usage.New(s.d).FlagRefreshFromPlanUpdate(ctx, r, p); err != nil {
		return fmt.Errorf("flag lifetime usages: %w", err)
	}
	return nil
}

// processCommitment creates or updates the minimum commitment of the plan
// p from in, or removes it when in is blank.
//
// source: app/services/plans/update_service.rb:223-243
func (s *Service) processCommitment(ctx context.Context, r port.Repos, p *catalog.Plan, in *CommitmentInput) error {
	cur, err := r.Commitments.GetByPlan(ctx, p.OrganizationID, p.ID, catalog.CommitmentTypeMinimumCommitment)
	switch {
	case errors.Is(err, core.ErrNotFound):
		cur = nil
	case err != nil:
		return fmt.Errorf("load minimum commitment: %w", err)
	}
	if in.blank() {
		if cur == nil {
			return nil
		}
		if err := r.Commitments.Delete(ctx, p.OrganizationID, cur.ID); err != nil {
			return fmt.Errorf("delete minimum commitment %s: %w", cur.ID, err)
		}
		return nil
	}
	if cur == nil {
		return s.writeCommitment(ctx, r, s.newCommitment(p), *in, true)
	}
	return s.writeCommitment(ctx, r, cur, *in, false)
}

// processDowngraded cancels the pending downgrades of the active
// subscriptions of the plan p whose new plan now costs more than p: the
// change became an upgrade, so the downgrade no longer applies.
//
// source: app/services/plans/update_service.rb:357-366
func (s *Service) processDowngraded(ctx context.Context, r port.Repos, p *catalog.Plan) error {
	subs, err := r.Subscriptions.ListActiveOrPendingByPlan(ctx, p.OrganizationID, p.ID)
	if err != nil {
		return fmt.Errorf("load subscriptions: %w", err)
	}
	for _, sub := range subs {
		if sub.Status != subscription.StatusActive {
			continue
		}
		next, err := r.Subscriptions.GetNext(ctx, p.OrganizationID, sub.ID)
		if errors.Is(err, core.ErrNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("load next subscription of %s: %w", sub.ID, err)
		}
		nextPlan, err := r.Plans.Get(ctx, p.OrganizationID, next.PlanID)
		if err != nil {
			return fmt.Errorf("load plan %s: %w", next.PlanID, err)
		}
		if p.Amount >= nextPlan.Amount {
			continue
		}
		if err := kernel.LockSubscription(ctx, r, p.OrganizationID, next.ID); err != nil {
			return err
		}
		// mark_as_canceled! records no reason and emits no webhook.
		if err := next.Cancel(s.d.Now(), ""); err != nil {
			return fmt.Errorf("cancel subscription %s: %w", next.ID, err)
		}
		if err := r.Subscriptions.Update(ctx, next); err != nil {
			return fmt.Errorf("update subscription %s: %w", next.ID, err)
		}
	}
	return nil
}

// processPending upgrades now the subscriptions waiting to move onto the
// plan p whose previous plan's yearly amount is not above p's: the
// previous subscription changes plan to p, with the pending subscription's
// name and activation rules (Subscriptions::PlanUpgradeService).
//
// source: app/services/plans/update_service.rb:368-392, app/services/plans/update_amount_service.rb:39-59
func (s *Service) processPending(ctx context.Context, r port.Repos, p *catalog.Plan) error {
	subs, err := r.Subscriptions.ListActiveOrPendingByPlan(ctx, p.OrganizationID, p.ID)
	if err != nil {
		return fmt.Errorf("load subscriptions: %w", err)
	}
	for _, sub := range subs {
		if sub.Status != subscription.StatusPending || sub.PreviousSubscriptionID.IsZero() {
			continue
		}
		prev, err := r.Subscriptions.Get(ctx, p.OrganizationID, sub.PreviousSubscriptionID)
		if err != nil {
			return fmt.Errorf("load subscription %s: %w", sub.PreviousSubscriptionID, err)
		}
		prevPlan, err := r.Plans.Get(ctx, p.OrganizationID, prev.PlanID)
		if err != nil {
			return fmt.Errorf("load plan %s: %w", prev.PlanID, err)
		}
		if p.YearlyAmount().Cmp(prevPlan.YearlyAmount()) < 0 {
			continue
		}
		rules, err := r.ActivationRules.ListBySubscription(ctx, p.OrganizationID, sub.ID)
		if err != nil {
			return fmt.Errorf("load activation rules of %s: %w", sub.ID, err)
		}
		in := lifecycle.ChangePlanInput{OrganizationID: p.OrganizationID, SubscriptionID: prev.ID, PlanID: p.ID, Name: sub.Name}
		now := s.d.Now()
		for _, rule := range rules {
			in.ActivationRules = append(in.ActivationRules, subscription.NewActivationRule(
				p.OrganizationID, core.NewID[core.ActivationRuleID](s.d.IDs), core.SubscriptionID{}, rule.RuleType, rule.TimeoutHours, now))
		}
		changer, ok := s.subs.(planChanger)
		if !ok {
			return errors.New("upgrade pending subscription: the subscription service does not change plans")
		}
		if _, err := changer.ChangePlan(ctx, in); err != nil {
			return fmt.Errorf("upgrade subscription %s: %w", prev.ID, err)
		}
	}
	return nil
}

// cascadeAmount enqueues a [kernel.JobUpdatePlanAmount] for each override
// plan in children whose fee is still old.
//
// source: app/services/plans/update_service.rb:155-162
func (s *Service) cascadeAmount(ctx context.Context, r port.Repos, children []*catalog.Plan, old, amount int64) error {
	now := s.d.Now()
	for _, c := range children {
		if c.Amount != old {
			continue
		}
		args := kernel.PlanAmountArgs{PlanID: c.ID, Amount: amount, ExpectedAmount: old}
		if err := kernel.Enqueue(ctx, r, c.OrganizationID, kernel.JobUpdatePlanAmount, args, "", now); err != nil {
			return err
		}
	}
	return nil
}

// flagDrafts flags for refresh the draft invoices that bill a
// subscription of the plan p.
//
// source: app/services/plans/update_service.rb:101-125
func (s *Service) flagDrafts(ctx context.Context, r port.Repos, p *catalog.Plan) error {
	subs, err := kernel.All(func(page core.PageRequest) (core.Page[*subscription.Subscription], error) {
		return r.Subscriptions.List(ctx, p.OrganizationID, subscription.Filter{PlanID: p.ID}, page)
	})
	if err != nil {
		return fmt.Errorf("load subscriptions: %w", err)
	}
	if len(subs) == 0 {
		return nil
	}
	ids := make([]core.SubscriptionID, len(subs))
	for i, sub := range subs {
		ids[i] = sub.ID
	}
	drafts, err := kernel.All(func(page core.PageRequest) (core.Page[*invoice.Invoice], error) {
		return r.Invoices.List(ctx, p.OrganizationID, invoice.Filter{Statuses: []invoice.Status{invoice.StatusDraft}, SubscriptionIDs: ids}, page)
	})
	if err != nil {
		return fmt.Errorf("load draft invoices: %w", err)
	}
	now := s.d.Now()
	done := map[core.InvoiceID]bool{}
	for _, inv := range drafts {
		if done[inv.ID] || inv.RefreshRequested {
			continue
		}
		done[inv.ID] = true
		inv.RefreshRequested, inv.UpdatedAt = true, now
		if err := r.Invoices.Update(ctx, inv); err != nil {
			return fmt.Errorf("flag draft invoice %s: %w", inv.ID, err)
		}
	}
	return nil
}
