package catalog

import (
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// Plan is a priced offer (domain §7.2). A plan with a ParentID is an
// override plan materialized for one subscription (§7.2.1).
type Plan struct {
	// ID identifies the plan.
	ID core.PlanID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// Code is unique among live plans that are not children; a child has
	// its parent's code. Locked once subscribed.
	Code string
	// Name is required, ≤ 255 characters.
	Name string
	// InvoiceDisplayName replaces Name on invoices.
	InvoiceDisplayName *string
	// Description is free text.
	Description *string
	// Interval is the billing interval; locked once subscribed.
	Interval period.Interval
	// Amount is the base (subscription) fee per period, ≥ 0, in minor units
	// of Currency.
	Amount int64
	// Currency is the plan currency; locked once subscribed.
	Currency money.Currency
	// PayInAdvance bills the base fee at the start of the period; locked
	// once subscribed.
	PayInAdvance bool
	// TrialPeriodDays is the trial length in whole days, ≥ 0 ([Change]
	// domain §7.2); locked once subscribed.
	TrialPeriodDays int
	// BillChargesMonthly bills usage charges monthly; only for yearly and
	// semiannual plans; locked once subscribed.
	BillChargesMonthly *bool
	// BillFixedChargesMonthly bills fixed charges monthly; only for yearly
	// and semiannual plans; locked once subscribed.
	BillFixedChargesMonthly *bool
	// ParentID is the original plan of an override plan.
	ParentID core.PlanID
	// TaxIDs are the plan's taxes (domain §22.10, step 6).
	TaxIDs []core.TaxID
	// Metadata is the plan's metadata.
	Metadata core.Metadata
	// Status is the lifecycle status.
	Status PlanStatus
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// IsChild reports whether p is an override plan.
func (p *Plan) IsChild() bool { return !p.ParentID.IsZero() }

// IsParent reports whether p is an original plan (not an override).
func (p *Plan) IsParent() bool { return p.ParentID.IsZero() }

// HasTrial reports whether the plan has a trial.
func (p *Plan) HasTrial() bool { return p.TrialPeriodDays > 0 }

// PayInArrears reports whether the base fee is billed at the period end.
func (p *Plan) PayInArrears() bool { return !p.PayInAdvance }

// InvoiceName returns the name printed on invoices.
func (p *Plan) InvoiceName() string {
	if p.InvoiceDisplayName != nil && *p.InvoiceDisplayName != "" {
		return *p.InvoiceDisplayName
	}
	return p.Name
}

func catalogSplitsMonthly(i period.Interval) bool {
	return i == period.Yearly || i == period.Semiannual
}

// ChargesBilledMonthly reports whether usage charges are billed on monthly
// periods (domain §22.2).
func (p *Plan) ChargesBilledMonthly() bool {
	return catalogSplitsMonthly(p.Interval) && p.BillChargesMonthly != nil && *p.BillChargesMonthly
}

// FixedChargesBilledMonthly reports whether fixed charges are billed on
// monthly periods (domain §22.2).
func (p *Plan) FixedChargesBilledMonthly() bool {
	return catalogSplitsMonthly(p.Interval) && p.BillFixedChargesMonthly != nil && *p.BillFixedChargesMonthly
}

// ChargesOrFixedChargesBilledMonthly reports whether either is billed
// monthly.
func (p *Plan) ChargesOrFixedChargesBilledMonthly() bool {
	return p.ChargesBilledMonthly() || p.FixedChargesBilledMonthly()
}

// YearlyAmount returns the base fee brought to a year, in minor units; it
// decides upgrade versus downgrade (domain §22.13).
func (p *Plan) YearlyAmount() money.Decimal {
	return period.YearlyAmount(p.Interval, money.NewFromInt(p.Amount))
}

// PeriodPlan returns the plan as libs/period sees it.
func (p *Plan) PeriodPlan() period.Plan {
	return period.Plan{
		Interval:                p.Interval,
		PayInAdvance:            p.PayInAdvance,
		TrialDays:               p.TrialPeriodDays,
		BillChargesMonthly:      p.BillChargesMonthly != nil && *p.BillChargesMonthly,
		BillFixedChargesMonthly: p.BillFixedChargesMonthly != nil && *p.BillFixedChargesMonthly,
	}
}

// Validate checks the invariants of domain §7.2.
func (p *Plan) Validate() error {
	var v core.Validator
	v.Code("code", p.Code, 255)
	v.String("name", p.Name, 255)
	if p.Interval == 0 {
		v.Add("interval", core.CodeRequired)
	} else {
		v.Check(p.Interval.Valid(), "interval", core.CodeInvalid)
	}
	v.NonNegative("amount", p.Amount)
	v.Currency("currency", p.Currency)
	v.Check(p.TrialPeriodDays >= 0, "trial_period_days", core.CodeOutOfRange)
	if p.BillChargesMonthly != nil && *p.BillChargesMonthly {
		v.Check(catalogSplitsMonthly(p.Interval), "bill_charges_monthly", core.CodeForbidden)
	}
	if p.BillFixedChargesMonthly != nil && *p.BillFixedChargesMonthly {
		v.Check(catalogSplitsMonthly(p.Interval), "bill_fixed_charges_monthly", core.CodeForbidden)
	}
	v.Check(p.ParentID != p.ID || p.ID.IsZero(), "parent_id", core.CodeInvalid)
	v.Merge("metadata", p.Metadata.Validate(core.MetadataLimits{}))
	v.Enum("status", p.Status)
	return v.Err()
}

// MarkPendingDeletion moves an active plan to pending_deletion (domain
// §7.2: its subscriptions are then terminated and the plan deleted by a
// job).
func (p *Plan) MarkPendingDeletion(now time.Time) error {
	if p.Status != PlanStatusActive {
		return &core.TransitionError{Entity: "plan", From: string(p.Status), To: string(PlanStatusPendingDeletion)}
	}
	p.Status = PlanStatusPendingDeletion
	p.UpdatedAt = now
	return nil
}

// NewOverride returns the override (child) plan of parent for one
// subscription: a copy with a new ID, ParentID = parent and the parent's
// code (domain §7.2.1). The caller applies the overridden fields.
func (p *Plan) NewOverride(id core.PlanID, now time.Time) *Plan {
	c := *p
	c.ID = id
	c.ParentID = p.ID
	c.TaxIDs = slices.Clone(p.TaxIDs)
	c.Metadata = p.Metadata.Clone()
	c.Status = PlanStatusActive
	c.CreatedAt = now
	c.UpdatedAt = now
	return &c
}

// PlanLockedFields are the fields locked once a plan has subscriptions
// (domain §7.2).
func PlanLockedFields() []string {
	return []string{"code", "interval", "pay_in_advance", "currency", "trial_period_days", "bill_charges_monthly", "bill_fixed_charges_monthly"}
}

// PlanLockedChanges returns the locked fields whose value differs between
// old and updated, in [PlanLockedFields] order.
func PlanLockedChanges(old, updated *Plan) []string {
	var out []string
	add := func(field string, changed bool) {
		if changed {
			out = append(out, field)
		}
	}
	add("code", old.Code != updated.Code)
	add("interval", old.Interval != updated.Interval)
	add("pay_in_advance", old.PayInAdvance != updated.PayInAdvance)
	add("currency", old.Currency.Code() != updated.Currency.Code())
	add("trial_period_days", old.TrialPeriodDays != updated.TrialPeriodDays)
	add("bill_charges_monthly", !catalogPtrEqual(old.BillChargesMonthly, updated.BillChargesMonthly))
	add("bill_fixed_charges_monthly", !catalogPtrEqual(old.BillFixedChargesMonthly, updated.BillFixedChargesMonthly))
	return out
}

// ValidatePlanUpdate refuses changes of locked fields (core.CodeImmutable)
// when the plan has subscriptions, then validates updated.
func ValidatePlanUpdate(old, updated *Plan, hasSubscriptions bool) error {
	var v core.Validator
	if hasSubscriptions {
		for _, f := range PlanLockedChanges(old, updated) {
			v.Add(f, core.CodeImmutable)
		}
	}
	v.Merge("", updated.Validate())
	return v.Err()
}
