package subscriptions

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/plans"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// Field error codes of the service, named after lago's.
const (
	// CodeInvalidDate: ending_at is not after today and subscription_at.
	CodeInvalidDate = "invalid_date"
	// CodeInvalidPaymentMethod: the payment method type and ID do not go
	// together (field payment_method).
	CodeInvalidPaymentMethod = "invalid_payment_method"
	// CodeInvalidRuleType: an activation rule of an unknown type.
	CodeInvalidRuleType = "invalid_type"
	// CodeDuplicatedRuleType: two activation rules of one type.
	CodeDuplicatedRuleType = "duplicated_type"
	// CodeSubscriptionNotPending: activation rules change only on a
	// pending subscription.
	CodeSubscriptionNotPending = "subscription_not_pending"
	// CodeManualPaymentMethod: a payment rule with a manual payment
	// method (field customer).
	CodeManualPaymentMethod = "manual_payment_method_invalid_for_payment_activation_rules"
	// CodeNoLinkedPaymentProvider: a payment rule for a customer without a
	// payment provider (field customer).
	CodeNoLinkedPaymentProvider = "no_linked_payment_provider"
	// CodePaymentMethodNotFound: a payment rule with a payment method the
	// customer does not have (field customer).
	CodePaymentMethodNotFound = "payment_method_not_found"
	// CodeNoDefaultPaymentMethod: a payment rule for a customer without a
	// default payment method (field customer).
	CodeNoDefaultPaymentMethod = "no_default_payment_method"
)

// PaymentMethod is the payment method of a request: the fields set are
// the ones changed (lago payment_method).
type PaymentMethod struct {
	// Type is the payment method type, when set.
	Type *core.PaymentMethodType
	// ID is the explicit payment method, when set; a zero ID clears it.
	ID *core.PaymentMethodID
}

// CustomSections selects the invoice custom sections of a subscription
// (lago invoice_custom_section).
type CustomSections struct {
	// Skip, when set, turns the custom sections off (true) or on (false).
	Skip *bool
	// Sections, when not nil, are the selected sections; the unknown ones
	// are ignored.
	Sections []refs.Ref
}

// ActivationRule is an activation rule of a request.
type ActivationRule struct {
	// Type is the rule type.
	Type subscription.ActivationRuleType
	// TimeoutHours is the time the rule waits before it expires; 0 never
	// expires.
	TimeoutHours int
}

// CreateInput is the request of [Service.Create] (lago
// Api::V1::SubscriptionsController#create).
type CreateInput struct {
	// OrganizationID is the organization.
	OrganizationID core.OrganizationID
	// Customer is the subscribed customer; a customer referenced by an
	// external ID that does not exist is created.
	Customer CustomerRef
	// Plan is the original plan subscribed to.
	Plan refs.Ref
	// ExternalID is the subscription's external ID.
	ExternalID string
	// Name is the display name.
	Name *string
	// BillingTime is calendar (the default, also when zero) or anniversary.
	BillingTime period.BillingTime
	// SubscriptionAt is the requested start; now when nil.
	SubscriptionAt *time.Time
	// EndingAt is the planned termination.
	EndingAt *time.Time
	// ProgressiveBillingDisabled turns the usage thresholds off.
	ProgressiveBillingDisabled bool
	// ConsolidateInvoice, when set, is the invoice grouping setting; true
	// by default.
	ConsolidateInvoice *bool
	// PurchaseOrderNumber is printed on the invoices.
	PurchaseOrderNumber *string
	// BillingEntity is the billing entity of a created customer (the
	// default one when zero) and the subscription's own.
	BillingEntity refs.Ref
	// OnTerminationInvoice, when set, is the termination invoice setting.
	OnTerminationInvoice subscription.TerminationInvoice
	// OnTerminationCreditNote, when set, is the termination credit note
	// setting (in-advance plans only).
	OnTerminationCreditNote subscription.TerminationCreditNote
	// PaymentMethod, when not nil, is the payment method.
	PaymentMethod *PaymentMethod
	// InvoiceCustomSections, when not nil, selects the custom sections.
	InvoiceCustomSections *CustomSections
	// ActivationRules are the activation rules.
	ActivationRules []ActivationRule
	// PlanOverrides, when not nil, subscribe to an override plan of Plan
	// with these values; with fixed charge units only (and no other
	// value), the subscription stays on Plan with its own units instead.
	PlanOverrides *plans.OverrideInput
	// UsageThresholds, when not empty, are the subscription's own usage
	// thresholds; exclusive with PlanOverrides.UsageThresholds.
	UsageThresholds []plans.ThresholdInput
}

// Create subscribes a customer to a plan (lago
// Subscriptions::CreateService): a new subscription, or the plan change of
// the customer's active or future subscription with that external ID
// (see lifecycle.Service.Create). It returns the resulting subscription.
//
// source: app/controllers/api/v1/subscriptions_controller.rb:8, app/services/subscriptions/create_service.rb
func (s *Service) Create(ctx context.Context, in CreateInput) (*View, error) {
	org := in.OrganizationID
	var out *View
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		be, err := findBillingEntity(ctx, r, org, in.BillingEntity)
		if err != nil {
			return err
		}
		c, err := findCustomer(ctx, r, org, in.Customer)
		var nf *core.NotFoundError
		created := false
		if errors.As(err, &nf) && in.Customer.ExternalID != "" {
			// subscriptions_controller.rb:17: a new customer in API context.
			if c, err = s.newCustomer(ctx, r, org, strings.TrimSpace(in.Customer.ExternalID), be); err != nil {
				return err
			}
			created = true
		} else if err != nil {
			return err
		}
		plan, err := findParentPlan(ctx, r, org, in.Plan)
		if err != nil {
			return err
		}
		now := s.d.Now()
		at := now
		if in.SubscriptionAt != nil {
			at = in.SubscriptionAt.UTC()
		}
		var v core.Validator
		checkEndingAt(&v, in.EndingAt, at, now)
		if err := checkPaymentMethod(ctx, r, &v, org, in.PaymentMethod); err != nil {
			return err
		}
		if err := checkActivationRules(ctx, r, &v, c, nil, in.PaymentMethod, in.ActivationRules, false); err != nil {
			return err
		}
		if err := v.Err(); err != nil {
			return err
		}
		if err := checkThresholdsCompatible(in.UsageThresholds, in.PlanOverrides); err != nil {
			return err
		}
		if created {
			if err := r.Customers.Create(ctx, c); err != nil {
				return fmt.Errorf("create customer: %w", err)
			}
		}
		// The lifecycle's lock, taken before reading the customer's
		// subscriptions for the plan overrides.
		if err := kernel.LockCustomer(ctx, r, org, c.ID, customer.LockScopeBillingSchedule); err != nil {
			return err
		}
		target, units, err := s.creationPlan(ctx, r, plan, c.ID, in.ExternalID, in.PlanOverrides)
		if err != nil {
			return err
		}

		sub := subscription.New(org, core.NewID[core.SubscriptionID](s.d.IDs), c.ID, target.ID, in.ExternalID, at,
			subscription.PlanFacts{PayInAdvance: target.PayInAdvance}, now)
		if name := strings.TrimSpace(ptrString(in.Name)); name != "" {
			sub.Name = &name
		}
		if in.BillingTime != 0 {
			sub.BillingTime = in.BillingTime
		}
		sub.EndingAt = utcPtr(in.EndingAt)
		sub.PurchaseOrderNumber = core.NormalizePurchaseOrderNumber(in.PurchaseOrderNumber)
		sub.ProgressiveBillingDisabled = in.ProgressiveBillingDisabled
		if in.ConsolidateInvoice != nil {
			sub.ConsolidateInvoice = *in.ConsolidateInvoice
		}
		if be != nil {
			sub.BillingEntityID = be.ID
		}
		if in.OnTerminationInvoice != "" {
			sub.OnTerminationInvoice = in.OnTerminationInvoice
		}
		if in.OnTerminationCreditNote != "" {
			sub.OnTerminationCreditNote = in.OnTerminationCreditNote
		}
		applyPaymentMethod(sub, in.PaymentMethod)
		rules := make([]*subscription.ActivationRule, len(in.ActivationRules))
		for i, a := range in.ActivationRules {
			rules[i] = subscription.NewActivationRule(org, core.NewID[core.ActivationRuleID](s.d.IDs), sub.ID, a.Type, a.TimeoutHours, now)
		}

		var own []*subscription.FixedChargeUnitsOverride
		if units {
			if own, err = s.unitsOverrides(ctx, r, target, *in.PlanOverrides); err != nil {
				return err
			}
		}
		res, err := s.life.CreateWithUnits(ctx, sub, own, rules...)
		if err != nil {
			return err
		}
		// create_service.rb:83-85: on the resulting subscription.
		if len(in.UsageThresholds) > 0 {
			if err := s.replaceThresholds(ctx, r, res, in.UsageThresholds); err != nil {
				return err
			}
		}
		downgrade := res.ID != sub.ID && res.Status == subscription.StatusPending && !res.PreviousSubscriptionID.IsZero()
		if !downgrade && in.InvoiceCustomSections != nil {
			// create_service.rb:87: not on a downgrade.
			if err := attachSections(ctx, r, res, *in.InvoiceCustomSections); err != nil {
				return err
			}
			res.UpdatedAt = now
			if err := r.Subscriptions.Update(ctx, res); err != nil {
				return fmt.Errorf("update subscription %s: %w", res.ID, err)
			}
		}
		out, err = view(ctx, r, res, now)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("create subscription: %w", err)
	}
	return out, nil
}

// newCustomer builds the customer an unknown external ID names, of the
// billing entity be or else the default one (lago
// BillingEntities::ResolveService); it is stored once the request is
// valid.
func (s *Service) newCustomer(ctx context.Context, r port.Repos, org core.OrganizationID, externalID string, be *organization.BillingEntity) (*customer.Customer, error) {
	if be == nil {
		var err error
		if be, err = r.BillingEntities.GetDefault(ctx, org); errors.Is(err, core.ErrNotFound) {
			return nil, core.NotFound(ResourceBillingEntity)
		} else if err != nil {
			return nil, fmt.Errorf("load default billing entity: %w", err)
		}
	}
	c := customer.New(org, core.NewID[core.CustomerID](s.d.IDs), externalID, be.ID, s.d.Now())
	o, err := r.Organizations.Get(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("load organization: %w", err)
	}
	seq, err := r.Sequences.Next(ctx, org, port.SequenceKey{Name: port.SequenceCustomer})
	if err != nil {
		return nil, fmt.Errorf("next customer sequence: %w", err)
	}
	c.AssignSequence(seq, o.DocumentNumberPrefix)
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// checkEndingAt checks that ending_at, when set, is on a later day than
// today and than subscription_at (validate_service.rb:53-64).
func checkEndingAt(v *core.Validator, endingAt *time.Time, subscriptionAt, now time.Time) {
	if endingAt == nil {
		return
	}
	end := core.DateOf(*endingAt, time.UTC)
	v.Check(end.After(core.DateOf(now, time.UTC)) && end.After(core.DateOf(subscriptionAt, time.UTC)), "ending_at", CodeInvalidDate)
}

// checkPaymentMethod checks the type and ID of a payment method
// (lago PaymentMethods::ValidateService): a provider type with no ID or
// an existing one, or a manual type with no existing one.
func checkPaymentMethod(ctx context.Context, r port.Repos, v *core.Validator, org core.OrganizationID, pm *PaymentMethod) error {
	if pm == nil || (pm.Type == nil && (pm.ID == nil || pm.ID.IsZero())) {
		return nil
	}
	typ := ""
	if pm.Type != nil {
		typ = string(*pm.Type)
	}
	found := false
	if pm.ID != nil && !pm.ID.IsZero() {
		_, err := r.PaymentMethods.Get(ctx, org, *pm.ID)
		switch {
		case err == nil:
			found = true
		case !errors.Is(err, core.ErrNotFound):
			return fmt.Errorf("load payment method %s: %w", *pm.ID, err)
		}
	}
	switch {
	case (pm.ID == nil || pm.ID.IsZero()) && typ == string(core.PaymentMethodTypeProvider),
		found && typ == string(core.PaymentMethodTypeProvider),
		!found && typ == string(core.PaymentMethodTypeManual):
		return nil
	}
	v.Add("payment_method", CodeInvalidPaymentMethod)
	return nil
}

// checkActivationRules checks the activation rules of a creation (sub
// nil) or of an update of sub (lago
// Subscriptions::ActivationRules::ValidateService and its payment rule
// validator). present tells whether an update sets the rules.
func checkActivationRules(ctx context.Context, r port.Repos, v *core.Validator, c *customer.Customer, sub *subscription.Subscription, pm *PaymentMethod, rules []ActivationRule, update bool) error {
	if update && sub.Status != subscription.StatusPending {
		v.Add("activation_rules", CodeSubscriptionNotPending)
	}
	seen := map[subscription.ActivationRuleType]bool{}
	duplicated := false
	for _, rule := range rules {
		if !rule.Type.Valid() {
			v.Add("activation_rules", CodeInvalidRuleType)
			continue
		}
		duplicated = duplicated || seen[rule.Type]
		seen[rule.Type] = true
		if rule.Type == subscription.ActivationRuleTypePayment {
			if rule.TimeoutHours < 0 {
				v.Add("timeout_hours", "value_must_be_positive_or_zero")
			}
			code, err := paymentRuleError(ctx, r, c, sub, pm)
			if err != nil {
				return err
			}
			if code != "" {
				v.Add("customer", code)
			}
		}
	}
	if duplicated {
		v.Add("activation_rules", CodeDuplicatedRuleType)
	}
	return nil
}

// paymentRuleError returns the error code of a payment activation rule,
// or "" when the subscription will be collected by a provider payment
// method (activation_rules/payment/validate_service.rb:28-77).
func paymentRuleError(ctx context.Context, r port.Repos, c *customer.Customer, sub *subscription.Subscription, pm *PaymentMethod) (string, error) {
	var typ core.PaymentMethodType
	var id core.PaymentMethodID
	if sub != nil {
		typ, id = sub.PaymentMethodType, sub.PaymentMethodID
	}
	if pm != nil && pm.Type != nil {
		typ = *pm.Type
	}
	if pm != nil && pm.ID != nil {
		id = *pm.ID
	}
	var method *payment.Method
	var err error
	if !id.IsZero() {
		method, err = r.PaymentMethods.Get(ctx, c.OrganizationID, id)
		if err == nil && (method.CustomerID != c.ID || method.DeletedAt != nil) {
			method, err = nil, core.ErrNotFound
		}
	} else {
		method, err = r.PaymentMethods.GetDefault(ctx, c.OrganizationID, c.ID)
	}
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		return "", fmt.Errorf("load payment method: %w", err)
	}
	if err != nil {
		method = nil
	}
	providers, err := r.PaymentProviderCustomers.ListByCustomer(ctx, c.OrganizationID, c.ID)
	if err != nil {
		return "", fmt.Errorf("list provider customers of %s: %w", c.ID, err)
	}
	linked := len(providers) > 0
	switch {
	case typ != "" && typ == core.PaymentMethodTypeProvider && method != nil:
		return "", nil
	case typ == "" && linked && method != nil:
		return "", nil
	case typ != "" && typ != core.PaymentMethodTypeProvider:
		return CodeManualPaymentMethod, nil
	case typ == "" && !linked:
		return CodeNoLinkedPaymentProvider, nil
	case !id.IsZero():
		return CodePaymentMethodNotFound, nil
	}
	return CodeNoDefaultPaymentMethod, nil
}

// applyPaymentMethod sets the payment method fields pm sets.
func applyPaymentMethod(sub *subscription.Subscription, pm *PaymentMethod) {
	if pm == nil {
		return
	}
	if pm.Type != nil {
		sub.PaymentMethodType = *pm.Type
	}
	if pm.ID != nil {
		sub.PaymentMethodID = *pm.ID
	}
}

// AttachSections applies the custom section selection cs to sub, as a
// subscription's creation or update does (lago
// InvoiceCustomSections::AttachToResourceService); contracts share it.
func AttachSections(ctx context.Context, r port.Repos, sub *subscription.Subscription, cs CustomSections) error {
	return attachSections(ctx, r, sub, cs)
}

// attachSections applies a custom section selection to sub (lago
// InvoiceCustomSections::AttachToResourceService): an explicit skip turns
// the sections off and clears them; otherwise, unless sub skips them,
// the selection replaces them.
func attachSections(ctx context.Context, r port.Repos, sub *subscription.Subscription, cs CustomSections) error {
	if cs.Skip != nil {
		sub.SkipInvoiceCustomSections = *cs.Skip
		if *cs.Skip {
			sub.InvoiceCustomSectionIDs = nil
			return nil
		}
	} else if sub.SkipInvoiceCustomSections {
		return nil
	}
	if cs.Sections == nil {
		return nil
	}
	ids := []core.InvoiceCustomSectionID{}
	for _, ref := range cs.Sections {
		var (
			sec *invoice.CustomSection
			err error
		)
		if ref.Code != "" {
			sec, err = r.InvoiceCustomSections.GetByCode(ctx, sub.OrganizationID, ref.Code)
		} else {
			var id core.InvoiceCustomSectionID
			if id, err = core.ParseID[core.InvoiceCustomSectionID](ref.ID.String()); err == nil {
				sec, err = r.InvoiceCustomSections.Get(ctx, sub.OrganizationID, id)
			}
		}
		if errors.Is(err, core.ErrNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("load invoice custom section %s: %w", ref, err)
		}
		if !slices.Contains(ids, sec.ID) {
			ids = append(ids, sec.ID)
		}
	}
	sub.InvoiceCustomSectionIDs = ids
	return nil
}

func ptrString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
