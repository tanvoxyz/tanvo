package contracts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/subscriptions"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// PaymentMethod is the payment method of a request (lago payment_method):
// the fields set are the ones changed.
type PaymentMethod struct {
	// Type is the payment method type, when set.
	Type *core.PaymentMethodType
	// ID, when set, is the customer's payment method; a zero ID clears it.
	ID *core.PaymentMethodID
}

// Settings are the invoicing and payment settings of a contract, the
// fields an active contract still accepts (lago SettingsResolvable).
type Settings struct {
	// BillingEntity, when set, is the billing entity the contract bills
	// through; zero clears the override, inheriting the customer's.
	BillingEntity core.Optional[core.BillingEntityID]
	// ConsolidateInvoice, when not nil, groups the contract's segments
	// with the customer's others on one invoice (true) or not.
	ConsolidateInvoice *bool
	// PurchaseOrderNumber, when set, is printed on the invoices; nil
	// clears it.
	PurchaseOrderNumber core.Optional[*string]
	// PaymentMethod, when not nil, is the payment method.
	PaymentMethod *PaymentMethod
	// InvoiceCustomSections, when not nil, selects the custom sections.
	InvoiceCustomSections *subscriptions.CustomSections
}

// CreateInput is the request of [Service.Create].
type CreateInput struct {
	// ExternalCustomerID is the external ID of the customer.
	ExternalCustomerID string
	// ExternalID is the contract's external ID.
	ExternalID string
	// Name is the display name.
	Name *string
	// PlanCode, when set, is the plan whose rate cards are applied.
	PlanCode string
	// BillingTime is calendar (the default, also when zero) or
	// anniversary.
	BillingTime period.BillingTime
	// BillingAnchorDate is the explicit billing anchor of the cards.
	BillingAnchorDate *core.Date
	// StartedAt is the start; now when nil.
	StartedAt *time.Time
	// EndedAt is the end of billing.
	EndedAt *time.Time
	// Settings are the invoicing and payment settings.
	Settings
}

// Create records a contract (lago Contracts::CreateService): active when
// its start has come, pending otherwise, with the rate cards of its plan
// when it has one, and schedules its customer's billing segments.
//
// source: app/services/contracts/create_service.rb:19-83
func (s *Service) Create(ctx context.Context, org core.OrganizationID, in CreateInput) (*View, error) {
	var out *View
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := findCustomer(ctx, r, org, in.ExternalCustomerID)
		if err != nil {
			return err
		}
		var plan *catalog.Plan
		if in.PlanCode != "" {
			if plan, err = findPlan(ctx, r, org, in.PlanCode); err != nil {
				return err
			}
		}
		now := s.d.Now()
		if in.EndedAt != nil && !in.EndedAt.After(now) {
			return fieldError("ended_at", subscription.CodeAlreadyEnded)
		}
		externalID := strings.TrimSpace(in.ExternalID)
		if taken, err := externalIDTaken(ctx, r, org, externalID); err != nil {
			return err
		} else if taken {
			return fieldError("external_id", CodeValueAlreadyExists)
		}
		be, err := s.checkSettings(ctx, r, c, in.Settings)
		if err != nil {
			return err
		}
		startedAt := now
		if in.StartedAt != nil {
			startedAt = in.StartedAt.UTC()
		}
		var planID core.PlanID
		if plan != nil {
			planID = plan.ID
		}
		sub := subscription.New(org, core.NewID[core.SubscriptionID](s.d.IDs), c.ID, planID, externalID, startedAt, subscription.PlanFacts{}, now)
		sub.Contract = true
		sub.Name = in.Name
		if in.BillingTime != 0 {
			sub.BillingTime = in.BillingTime
		}
		sub.BillingAnchorDate = in.BillingAnchorDate
		sub.EndingAt = utcPtr(in.EndedAt)
		applySettings(sub, in.Settings, be)
		if !startedAt.After(now) {
			if err := sub.Activate(startedAt, now); err != nil {
				return err
			}
		}
		if err := sub.Validate(); err != nil {
			return err
		}
		if err := r.Subscriptions.Create(ctx, sub); errors.Is(err, core.ErrConflict) {
			// A concurrent creation won the race past the check.
			return fieldError("external_id", CodeValueAlreadyExists)
		} else if err != nil {
			return fmt.Errorf("create contract: %w", err)
		}
		if err := s.attachSections(ctx, r, sub, in.InvoiceCustomSections, now); err != nil {
			return err
		}
		cenv, err := customerEnv(ctx, r, sub)
		if err != nil {
			return err
		}
		if err := s.materialize(ctx, r, sub, cenv.Location, now); err != nil {
			return err
		}
		if err := kernel.EnqueueScheduleSegments(ctx, r, org, c.ID); err != nil {
			return err
		}
		out, err = view(ctx, r, sub)
		return err
	})
	return out, err
}

// externalIDTaken reports whether a live contract, or a live
// subscription, has the external ID: events route by it, so contracts and
// subscriptions share one namespace.
func externalIDTaken(ctx context.Context, r port.Repos, org core.OrganizationID, externalID string) (bool, error) {
	if _, err := r.Contracts.GetLive(ctx, org, externalID); err == nil {
		return true, nil
	} else if !errors.Is(err, core.ErrNotFound) {
		return false, fmt.Errorf("load contract %q: %w", externalID, err)
	}
	if _, err := r.Subscriptions.GetByExternalID(ctx, org, externalID); err == nil {
		return true, nil
	} else if !errors.Is(err, core.ErrNotFound) {
		return false, fmt.Errorf("load subscription %q: %w", externalID, err)
	}
	return false, nil
}

// checkSettings resolves and checks the references of the settings
// (lago SettingsResolvable#settings_references_failure): the billing
// entity, the customer's payment method and their combination with the
// payment method type. It returns the billing entity set, or nil.
//
// source: app/services/contracts/settings_resolvable.rb:30-42
func (s *Service) checkSettings(ctx context.Context, r port.Repos, c *customer.Customer, st Settings) (*organization.BillingEntity, error) {
	org := c.OrganizationID
	var be *organization.BillingEntity
	if st.BillingEntity.Set && !st.BillingEntity.Value.IsZero() {
		var err error
		be, err = r.BillingEntities.Get(ctx, org, st.BillingEntity.Value)
		if errors.Is(err, core.ErrNotFound) {
			return nil, core.NotFound(ResourceBillingEntity)
		}
		if err != nil {
			return nil, fmt.Errorf("load billing entity: %w", err)
		}
	}
	pm := st.PaymentMethod
	if pm == nil {
		return be, nil
	}
	var method *payment.Method
	if pm.ID != nil && !pm.ID.IsZero() {
		var err error
		method, err = r.PaymentMethods.Get(ctx, org, *pm.ID)
		if err == nil && (method.CustomerID != c.ID || method.DeletedAt != nil) {
			err = core.ErrNotFound
		}
		if errors.Is(err, core.ErrNotFound) {
			return nil, core.NotFound(ResourcePaymentMethod)
		}
		if err != nil {
			return nil, fmt.Errorf("load payment method: %w", err)
		}
	}
	// payment_methods/validate_service.rb:19-27
	typ := core.PaymentMethodType("")
	if pm.Type != nil {
		typ = *pm.Type
	}
	noID := pm.ID == nil || pm.ID.IsZero()
	switch {
	case typ == "" && noID,
		noID && typ == core.PaymentMethodTypeProvider,
		method != nil && typ == core.PaymentMethodTypeProvider,
		method == nil && typ == core.PaymentMethodTypeManual:
		return be, nil
	}
	return nil, fieldError("payment_method", CodeInvalidPaymentMethod)
}

// applySettings sets the settings on contract c; be is the billing
// entity set (lago SettingsResolvable#apply_settings).
//
// source: app/services/contracts/settings_resolvable.rb:48-54
func applySettings(c *subscription.Subscription, st Settings, be *organization.BillingEntity) {
	if st.BillingEntity.Set {
		c.BillingEntityID = core.BillingEntityID{}
		if be != nil {
			c.BillingEntityID = be.ID
		}
	}
	if st.ConsolidateInvoice != nil {
		c.ConsolidateInvoice = *st.ConsolidateInvoice
	}
	if st.PurchaseOrderNumber.Set {
		c.PurchaseOrderNumber = core.NormalizePurchaseOrderNumber(st.PurchaseOrderNumber.Value)
	}
	if pm := st.PaymentMethod; pm != nil {
		if pm.ID != nil {
			c.PaymentMethodID = *pm.ID
		}
		if pm.Type != nil {
			c.PaymentMethodType = *pm.Type
		}
	}
}

// attachSections applies the custom section selection cs to contract c
// and stores it.
func (s *Service) attachSections(ctx context.Context, r port.Repos, c *subscription.Subscription, cs *subscriptions.CustomSections, now time.Time) error {
	if cs == nil {
		return nil
	}
	if err := subscriptions.AttachSections(ctx, r, c, *cs); err != nil {
		return err
	}
	c.UpdatedAt = now
	if err := r.Subscriptions.Update(ctx, c); err != nil {
		return fmt.Errorf("update contract %s: %w", c.ID, err)
	}
	return nil
}

// UpdateInput is the request of [Service.Update]: only the set fields
// change.
type UpdateInput struct {
	// Name is the display name.
	Name core.Optional[*string]
	// PlanCode, when set, is the plan whose rate cards replace the
	// contract's; empty detaches the plan.
	PlanCode core.Optional[string]
	// BillingTime, when not zero, is the billing time.
	BillingTime period.BillingTime
	// BillingAnchorDate is the explicit billing anchor; nil clears it.
	BillingAnchorDate core.Optional[*core.Date]
	// StartedAt, when not nil, is the start.
	StartedAt *time.Time
	// EndedAt is the end of billing; nil clears it.
	EndedAt core.Optional[*time.Time]
	// Settings are the invoicing and payment settings.
	Settings
}

// Update changes the live contract with the external ID (lago
// Contracts::UpdateService): a pending contract takes every field, an
// active one only its name, end and settings. Moving the start or the
// anchor reseeds its rate cards, changing the plan replaces them, and a
// start moved to the past activates it.
//
// source: app/services/contracts/update_service.rb:24-110
func (s *Service) Update(ctx context.Context, org core.OrganizationID, externalID string, in UpdateInput) (*View, error) {
	var out *View
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := live(ctx, r, org, externalID)
		if err != nil {
			return err
		}
		if c, err = lockContract(ctx, r, c); err != nil {
			return err
		}
		if !c.Live() {
			return contractLocked()
		}
		// update_service.rb:121-131: a resent current code is the
		// contract's own plan.
		var plan *catalog.Plan
		if in.PlanCode.Set && in.PlanCode.Value != "" {
			if !c.PlanID.IsZero() {
				cur, err := r.Plans.Get(ctx, org, c.PlanID)
				if err != nil {
					return fmt.Errorf("load plan %s: %w", c.PlanID, err)
				}
				if cur.Code == in.PlanCode.Value {
					plan = cur
				}
			}
			if plan == nil {
				if plan, err = findPlan(ctx, r, org, in.PlanCode.Value); err != nil {
					return err
				}
			}
		}
		cust, err := r.Customers.Get(ctx, org, c.CustomerID)
		if err != nil {
			return fmt.Errorf("load customer %s: %w", c.CustomerID, err)
		}
		be, err := s.checkSettings(ctx, r, cust, in.Settings)
		if err != nil {
			return err
		}
		cenv, err := customerEnv(ctx, r, c)
		if err != nil {
			return err
		}
		loc := cenv.Location
		var planID core.PlanID
		if plan != nil {
			planID = plan.ID
		}
		if c.Status == subscription.StatusActive && lockedFieldChanged(c, in, planID, loc) {
			return contractLocked()
		}
		now := s.d.Now()
		if e := in.EndedAt; e.Set && e.Value != nil && !e.Value.After(now) && (c.EndingAt == nil || e.Value.Unix() != c.EndingAt.Unix()) {
			return fieldError("ended_at", subscription.CodeAlreadyEnded)
		}
		planChanged := in.PlanCode.Set && planID != c.PlanID
		previousAnchor := c.BillingAnchorDate
		previousStart := c.SubscriptionAt

		if in.Name.Set {
			c.Name = in.Name.Value
		}
		if in.EndedAt.Set {
			c.EndingAt = utcPtr(in.EndedAt.Value)
		}
		applySettings(c, in.Settings, be)
		if c.Status == subscription.StatusPending {
			if in.BillingTime != 0 {
				c.BillingTime = in.BillingTime
			}
			if in.BillingAnchorDate.Set {
				c.BillingAnchorDate = in.BillingAnchorDate.Value
			}
			if in.StartedAt != nil {
				c.SubscriptionAt = in.StartedAt.UTC()
			}
			if in.PlanCode.Set {
				c.PlanID = planID
			}
		}
		startMoved := !c.SubscriptionAt.Equal(previousStart)
		lifecycleMoved := startMoved || !equalDates(c.BillingAnchorDate, previousAnchor)
		if err := c.Validate(); err != nil {
			return err
		}
		c.UpdatedAt = now
		if err := r.Subscriptions.Update(ctx, c); err != nil {
			return fmt.Errorf("update contract %s: %w", c.ID, err)
		}
		if err := s.attachSections(ctx, r, c, in.InvoiceCustomSections, now); err != nil {
			return err
		}
		switch {
		case planChanged:
			cards, err := r.ContractRateCards.ListBySubscription(ctx, org, c.ID)
			if err != nil {
				return fmt.Errorf("list rate cards of %s: %w", c.ID, err)
			}
			for _, card := range cards {
				if err := s.discardCard(ctx, r, c, card, now); err != nil {
					return err
				}
			}
			if err := s.materialize(ctx, r, c, loc, now); err != nil {
				return err
			}
		case lifecycleMoved:
			if err := s.reseed(ctx, r, c, loc, previousAnchor, nil, now); err != nil {
				return err
			}
		}
		if startMoved && c.Status == subscription.StatusPending {
			if _, err := s.activate(ctx, r, c, now); err != nil {
				return err
			}
		}
		out, err = view(ctx, r, c)
		return err
	})
	return out, err
}

// lockedFieldChanged reports whether in changes a field an active
// contract keeps (lago UpdateService#locked_field_changed?): the plan, the
// billing time, the start or the anchor (an anchor equal to the stored or
// the effective one is no change).
//
// source: app/services/contracts/update_service.rb:5-8, :103-119
func lockedFieldChanged(c *subscription.Subscription, in UpdateInput, plan core.PlanID, loc *time.Location) bool {
	if in.PlanCode.Set && plan != c.PlanID {
		return true
	}
	if in.BillingTime != 0 && in.BillingTime != c.BillingTime {
		return true
	}
	if in.StartedAt != nil && in.StartedAt.Unix() != c.SubscriptionAt.Unix() {
		return true
	}
	if a := in.BillingAnchorDate; a.Set {
		effective := c.EffectiveBillingAnchorDate(loc)
		switch {
		case a.Value == nil:
			return c.BillingAnchorDate != nil
		case c.BillingAnchorDate != nil && *a.Value == *c.BillingAnchorDate, *a.Value == effective:
			return false
		default:
			return true
		}
	}
	return false
}

// reseed reseeds the contract's kept cards (all of them when only is
// nil, else those only selects): a card on the anchor it inherited from
// previousAnchor follows the contract, another keeps its own (lago
// UpdateService#reseed_rate_cards).
//
// source: app/services/contracts/update_service.rb:96-101
func (s *Service) reseed(ctx context.Context, r port.Repos, c *subscription.Subscription, loc *time.Location, previousAnchor *core.Date, only func(*subscription.RateCard) bool, now time.Time) error {
	cards, err := r.ContractRateCards.ListBySubscription(ctx, c.OrganizationID, c.ID)
	if err != nil {
		return fmt.Errorf("list rate cards of %s: %w", c.ID, err)
	}
	for _, card := range cards {
		if only != nil && !only(card) {
			continue
		}
		var own *core.Date
		if !card.InheritedBillingAnchor(previousAnchor) {
			a := card.BillingAnchorDate
			own = &a
		}
		if err := s.seedLifecycle(ctx, r, c, loc, card, own, now); err != nil {
			return err
		}
	}
	return nil
}

// Terminate ends the terminatable contract with the external ID (lago
// Contracts::TerminateService): an active contract is terminated now, a
// pending one canceled; any other is contract cannot_terminate. Billing
// itself stops at the contract's end, not on its status.
//
// source: app/services/contracts/terminate_service.rb:20-40
func (s *Service) Terminate(ctx context.Context, org core.OrganizationID, externalID string) (*View, error) {
	var out *View
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := r.Contracts.GetTerminatable(ctx, org, externalID)
		if errors.Is(err, core.ErrNotFound) {
			return core.NotFound(Resource)
		}
		if err != nil {
			return fmt.Errorf("load contract %q: %w", externalID, err)
		}
		if c, err = lockContract(ctx, r, c); err != nil {
			return err
		}
		now := s.d.Now()
		switch c.Status {
		case subscription.StatusActive:
			err = c.Terminate(now)
		case subscription.StatusPending:
			err = c.Cancel(now, "")
		default:
			return fieldError("contract", subscription.CodeCannotTerminate)
		}
		if err != nil {
			return err
		}
		if err := r.Subscriptions.Update(ctx, c); err != nil {
			return fmt.Errorf("update contract %s: %w", c.ID, err)
		}
		out, err = view(ctx, r, c)
		return err
	})
	return out, err
}

// Activate activates the pending contract id once its start has come
// (lago Contracts::ActivateService); it reports whether it did. An active
// contract with the same external ID fails with external_id
// active_contract_exists.
//
// source: app/services/contracts/activate_service.rb:17-46
func (s *Service) Activate(ctx context.Context, org core.OrganizationID, id core.SubscriptionID) (bool, error) {
	var done bool
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := r.Subscriptions.Get(ctx, org, id)
		if errors.Is(err, core.ErrNotFound) || (err == nil && !c.Contract) {
			return core.NotFound(Resource)
		}
		if err != nil {
			return fmt.Errorf("load contract %s: %w", id, err)
		}
		done, err = s.activate(ctx, r, c, s.d.Now())
		return err
	})
	return done, err
}

// activate activates contract c, inside the caller's transaction, when it
// is pending, started by now and its customer kept: it reseeds the cards
// seeded for another start day, resets every card's clock at the start
// and schedules the customer's segments.
func (s *Service) activate(ctx context.Context, r port.Repos, c *subscription.Subscription, now time.Time) (bool, error) {
	c, err := lockContract(ctx, r, c)
	if err != nil {
		return false, err
	}
	org := c.OrganizationID
	cenv, err := customerEnv(ctx, r, c)
	if err != nil {
		return false, err
	}
	// activate_service.rb:51-53
	if c.Status != subscription.StatusPending || c.SubscriptionAt.After(now) || cenv.Customer.Deleted() {
		return false, nil
	}
	if _, err := r.Contracts.GetByStatus(ctx, org, c.ExternalID, subscription.StatusActive); err == nil {
		return false, fieldError("external_id", subscription.CodeActiveContractExists)
	} else if !errors.Is(err, core.ErrNotFound) {
		return false, fmt.Errorf("load active contract %q: %w", c.ExternalID, err)
	}
	cards, err := r.ContractRateCards.ListBySubscription(ctx, org, c.ID)
	if err != nil {
		return false, fmt.Errorf("list rate cards of %s: %w", c.ID, err)
	}
	// Card and phase edits lock the card, then read the contract.
	for _, card := range cards {
		if err := r.Locks.Lock(ctx, org, cardLock(card.ID)); err != nil {
			return false, fmt.Errorf("lock contract rate card %s: %w", card.ID, err)
		}
	}
	loc := cenv.Location
	start := core.DateOf(c.SubscriptionAt, loc)
	if err := s.reseed(ctx, r, c, loc, c.BillingAnchorDate, func(card *subscription.RateCard) bool { return card.EffectiveDate != start }, now); err != nil {
		return false, err
	}
	if cards, err = r.ContractRateCards.ListBySubscription(ctx, org, c.ID); err != nil {
		return false, fmt.Errorf("list rate cards of %s: %w", c.ID, err)
	}
	for _, card := range cards {
		if err := s.resetClock(ctx, r, c, loc, card, c.SubscriptionAt); err != nil {
			return false, err
		}
	}
	if err := c.Activate(c.SubscriptionAt, now); err != nil {
		return false, err
	}
	if err := r.Subscriptions.Update(ctx, c); errors.Is(err, core.ErrConflict) {
		return false, fieldError("external_id", subscription.CodeActiveContractExists)
	} else if err != nil {
		return false, fmt.Errorf("update contract %s: %w", c.ID, err)
	}
	return true, kernel.EnqueueScheduleSegments(ctx, r, org, c.CustomerID)
}

// ActivateAllPending enqueues the activation of every pending contract
// started by at whose customer is kept, one job each (lago
// Contracts::ActivateAllPendingService).
//
// source: app/services/contracts/activate_all_pending_service.rb:13-20
func (s *Service) ActivateAllPending(ctx context.Context, org core.OrganizationID, at time.Time) (int, error) {
	n := 0
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		due, err := r.Contracts.ListDueForActivation(ctx, org, at)
		if err != nil {
			return fmt.Errorf("list contracts due for activation: %w", err)
		}
		for _, c := range due {
			if err := kernel.Enqueue(ctx, r, org, kernel.JobActivateContract, kernel.SubscriptionArgs{SubscriptionID: c.ID}, kernel.ActivateContractKey(c.ID), time.Time{}); err != nil {
				return err
			}
		}
		n = len(due)
		return nil
	})
	return n, err
}

// logActivationFailure logs an activation the job leaves failed: lago
// does not retry Contracts::ActivateJob.
func (s *Service) logActivationFailure(ctx context.Context, org core.OrganizationID, id core.SubscriptionID, err error) {
	s.d.Logger.WarnContext(ctx, "contract activation failed", slog.String("organization_id", org.String()),
		slog.String("contract_id", id.String()), slog.Any("error", err))
}

func equalDates(a, b *core.Date) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
