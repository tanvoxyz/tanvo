package subscription

import (
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// Field error codes specific to subscriptions.
const (
	// CodeStartedAtRequired: an incomplete, active or terminated
	// subscription must have started_at.
	CodeStartedAtRequired = "started_at_is_mandatory"
	// CodeEndingAtBeforeStart: ending_at must be after subscription_at.
	CodeEndingAtBeforeStart = "invalid_ending_at"
	// CodeTerminationCreditNoteForArrears: on_termination_credit_note is
	// only allowed on plans paid in advance.
	CodeTerminationCreditNoteForArrears = "only_for_pay_in_advance_plans"
)

// Subscription is a customer's subscription to a plan (domain §9.1).
//
// A subscription chain is formed by upgrades and downgrades: each new
// subscription points to the one it replaces with PreviousSubscriptionID
// and keeps its ExternalID.
type Subscription struct {
	// ID identifies the subscription.
	ID core.SubscriptionID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// ExternalID is the customer-facing identifier, ≤ 255 characters,
	// unique among pending, incomplete and active subscriptions (per
	// status, see [Repository]).
	ExternalID string
	// CustomerID is the subscribed customer.
	CustomerID core.CustomerID
	// PlanID is the plan; an override plan (child) when the subscription
	// was created with overrides (domain §7.2.1).
	PlanID core.PlanID
	// Name is the display name, ≤ 255 characters.
	Name *string
	// BillingTime is calendar or anniversary billing; calendar by default.
	BillingTime period.BillingTime
	// Status is the lifecycle status (domain §9.2).
	Status Status
	// SubscriptionAt is the requested start; now by default.
	SubscriptionAt time.Time
	// StartedAt is the actual activation (set when it becomes incomplete
	// or active).
	StartedAt *time.Time
	// EndingAt is the planned automatic termination; after SubscriptionAt.
	EndingAt *time.Time
	// TerminatedAt is set when it becomes terminated.
	TerminatedAt *time.Time
	// CanceledAt is set when it becomes canceled.
	CanceledAt *time.Time
	// TrialEndedAt is set when the trial ended (domain §9.4).
	TrialEndedAt *time.Time
	// CancellationReason is why it was canceled; empty when canceled
	// without a recorded reason (a pending subscription canceled by a
	// termination, a plan change or a plan deletion).
	CancellationReason CancellationReason
	// PreviousSubscriptionID is the subscription this one upgrades or
	// downgrades.
	PreviousSubscriptionID core.SubscriptionID
	// OnTerminationCreditNote is how the unused in-advance base fee is
	// returned; only for in-advance plans, credit by default there.
	OnTerminationCreditNote TerminationCreditNote
	// OnTerminationInvoice tells whether termination issues an invoice;
	// generate by default.
	OnTerminationInvoice TerminationInvoice
	// PaymentMethodType is provider by default.
	PaymentMethodType core.PaymentMethodType
	// PaymentMethodID is an explicit payment method (provider type only).
	PaymentMethodID core.PaymentMethodID
	// ProgressiveBillingDisabled disables usage thresholds (domain §7.9).
	ProgressiveBillingDisabled bool
	// ConsolidateInvoice groups the subscription with others on one
	// invoice; true by default (domain §9.6).
	ConsolidateInvoice bool
	// PurchaseOrderNumber is printed on invoices, ≤ 255 characters.
	PurchaseOrderNumber *string
	// BillingEntityID overrides the customer's billing entity.
	BillingEntityID core.BillingEntityID
	// SkipInvoiceCustomSections disables invoice custom sections.
	SkipInvoiceCustomSections bool
	// InvoiceCustomSectionIDs are the selected custom sections (domain
	// §11.9).
	InvoiceCustomSectionIDs []core.InvoiceCustomSectionID
	// NextBillingAt is the next local midnight at which the subscription
	// has something to bill ([Change] domain §22.17).
	NextBillingAt *time.Time
	// SkipDailyUsage excludes the subscription from the daily usage
	// snapshots ([DailyUsage]).
	SkipDailyUsage bool
	// LastReceivedEventOn is the day, in the customer's time zone, the
	// active subscription last received usage; nil before its first. Only
	// [Repository.RecordEventReceived] writes it: Create and Update leave
	// the stored value, so a concurrent ingestion is never overwritten.
	LastReceivedEventOn *core.Date
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// PlanFacts are the facts about the subscription's plan that its
// validation needs.
type PlanFacts struct {
	// PayInAdvance is the plan's pay_in_advance.
	PayInAdvance bool
}

// New returns a subscription with the defaults of domain §9.1: calendar
// billing, generate a termination invoice, provider payment method,
// consolidated invoices, and the credit termination credit note for
// in-advance plans. Its status is [InitialStatus] of subscriptionAt.
func New(org core.OrganizationID, id core.SubscriptionID, customer core.CustomerID, plan core.PlanID,
	externalID string, subscriptionAt time.Time, facts PlanFacts, now time.Time,
) *Subscription {
	s := &Subscription{
		ID:                   id,
		OrganizationID:       org,
		ExternalID:           strings.TrimSpace(externalID),
		CustomerID:           customer,
		PlanID:               plan,
		BillingTime:          period.Calendar,
		Status:               StatusPending,
		SubscriptionAt:       subscriptionAt.UTC(),
		OnTerminationInvoice: TerminationInvoiceGenerate,
		PaymentMethodType:    core.PaymentMethodTypeProvider,
		ConsolidateInvoice:   true,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if facts.PayInAdvance {
		s.OnTerminationCreditNote = TerminationCreditNoteCredit
	}
	return s
}

// InitialStatus is the status a new subscription is activated towards:
// active when subscriptionAt is now or in the past, pending otherwise
// (domain §9.2). An active start may still go through incomplete when an
// activation rule applies.
func InitialStatus(subscriptionAt, now time.Time) Status {
	if subscriptionAt.After(now) {
		return StatusPending
	}
	return StatusActive
}

// Validate checks the entity-local invariants of domain §9.1.
func (s *Subscription) Validate() error {
	var v core.Validator
	v.String("external_id", s.ExternalID, 255)
	v.OptionalString("name", s.Name, 255)
	v.Check(s.BillingTime.Valid(), "billing_time", core.CodeInvalid)
	v.Enum("status", s.Status)
	v.Check(!s.SubscriptionAt.IsZero(), "subscription_at", core.CodeRequired)
	if s.EndingAt != nil {
		v.Check(s.EndingAt.After(s.SubscriptionAt), "ending_at", CodeEndingAtBeforeStart)
	}
	switch s.Status {
	case StatusIncomplete, StatusActive, StatusTerminated:
		v.Check(s.StartedAt != nil, "started_at", CodeStartedAtRequired)
	}
	if s.Status == StatusTerminated {
		v.Check(s.TerminatedAt != nil, "terminated_at", core.CodeRequired)
	}
	if s.Status == StatusCanceled {
		v.Check(s.CanceledAt != nil, "canceled_at", core.CodeRequired)
		v.OptionalEnum("cancellation_reason", s.CancellationReason)
	} else {
		v.Check(s.CancellationReason == "", "cancellation_reason", core.CodeForbidden)
	}
	v.OptionalEnum("on_termination_credit_note", s.OnTerminationCreditNote)
	v.Enum("on_termination_invoice", s.OnTerminationInvoice)
	if v.Enum("payment_method_type", s.PaymentMethodType) && s.PaymentMethodType == core.PaymentMethodTypeManual {
		v.Check(s.PaymentMethodID.IsZero(), "payment_method_id", core.CodeForbidden)
	}
	v.OptionalString("purchase_order_number", s.PurchaseOrderNumber, 255)
	v.Check(len(slices.Compact(slices.SortedFunc(slices.Values(s.InvoiceCustomSectionIDs), core.InvoiceCustomSectionID.Compare))) == len(s.InvoiceCustomSectionIDs),
		"invoice_custom_section_ids", core.CodeDuplicated)
	return v.Err()
}

// ValidateForPlan checks the rules that depend on the plan:
// on_termination_credit_note is only allowed on plans paid in advance.
func (s *Subscription) ValidateForPlan(p PlanFacts) error {
	var v core.Validator
	if !p.PayInAdvance {
		v.Check(s.OnTerminationCreditNote == "", "on_termination_credit_note", CodeTerminationCreditNoteForArrears)
	}
	return v.Err()
}

// Live reports whether the subscription is pending, incomplete or active,
// the statuses among which ExternalID is unique.
func (s *Subscription) Live() bool {
	return s.Status == StatusPending || s.Status == StatusIncomplete || s.Status == StatusActive
}

// Started reports whether the subscription has started.
func (s *Subscription) Started() bool { return s.StartedAt != nil }

// StartingInTheFuture reports whether the subscription is pending on its
// own start date, as opposed to a pending downgrade (which has a previous
// subscription).
func (s *Subscription) StartingInTheFuture() bool {
	return s.Status == StatusPending && s.PreviousSubscriptionID.IsZero()
}

// TerminatedBy reports whether the subscription was terminated at or
// before t.
func (s *Subscription) TerminatedBy(t time.Time) bool {
	return s.Status == StatusTerminated && s.TerminatedAt != nil && !s.TerminatedAt.After(t)
}

// DisplayName returns the subscription's name, or planName when it has
// none.
func (s *Subscription) DisplayName(planName string) string {
	if s.Name != nil && strings.TrimSpace(*s.Name) != "" {
		return *s.Name
	}
	return planName
}

// InvoiceName returns the name printed on invoices: the subscription's
// name, else the plan's invoice display name, else the plan's name.
func (s *Subscription) InvoiceName(planName string, planInvoiceDisplayName *string) string {
	if s.Name != nil && strings.TrimSpace(*s.Name) != "" {
		return *s.Name
	}
	if planInvoiceDisplayName != nil && strings.TrimSpace(*planInvoiceDisplayName) != "" {
		return *planInvoiceDisplayName
	}
	return planName
}

// ApplicableBillingEntity returns the subscription's billing entity
// override, or customerEntity when it has none.
func (s *Subscription) ApplicableBillingEntity(customerEntity core.BillingEntityID) core.BillingEntityID {
	if !s.BillingEntityID.IsZero() {
		return s.BillingEntityID
	}
	return customerEntity
}

// PeriodSubscription returns the libs/period view of s, given its plan,
// the applicable time zone (domain §22.1), the started_at of the first
// subscription of its chain (domain §9.4, see [ChainStart]) and how it is
// replaced by its next subscription. Previous invoice boundaries are left
// for the caller to set.
func (s *Subscription) PeriodSubscription(plan period.Plan, loc *time.Location, chainStartedAt time.Time, next period.Change) period.Subscription {
	ps := period.Subscription{
		Plan:             plan,
		BillingTime:      s.BillingTime,
		Location:         loc,
		SubscriptionAt:   s.SubscriptionAt,
		InitialStartedAt: chainStartedAt,
		Next:             next,
	}
	if s.StartedAt != nil {
		ps.StartedAt = *s.StartedAt
	}
	if s.TerminatedAt != nil {
		ps.TerminatedAt = *s.TerminatedAt
	}
	if s.TrialEndedAt != nil {
		ps.TrialEndedAt = *s.TrialEndedAt
	}
	return ps
}

// NextSubscription returns, among the subscriptions that point to s with
// PreviousSubscriptionID (next), the most recently created one that is not
// canceled, or nil.
func NextSubscription(next []*Subscription) *Subscription {
	var best *Subscription
	for _, n := range next {
		if n.Status == StatusCanceled {
			continue
		}
		if best == nil || n.CreatedAt.After(best.CreatedAt) {
			best = n
		}
	}
	return best
}

// ChainStart returns the started_at of the first subscription of a chain
// given oldest first (as returned by [Repository.Chain]), or the zero time
// when none started. The trial of every subscription of the chain starts
// there (domain §9.4).
func ChainStart(chain []*Subscription) time.Time {
	for _, s := range chain {
		if s.StartedAt != nil {
			return *s.StartedAt
		}
	}
	return time.Time{}
}

// ApplicableUsageThresholds returns the thresholds that drive progressive
// billing (domain §7.9): none when progressive billing is disabled, else
// the subscription's own thresholds, else the plan's, else (for an
// override plan without thresholds) the parent plan's.
func ApplicableUsageThresholds[T any](s *Subscription, own, plan, parentPlan []T) []T {
	switch {
	case s.ProgressiveBillingDisabled:
		return nil
	case len(own) > 0:
		return own
	case len(plan) > 0:
		return plan
	default:
		return parentPlan
	}
}
