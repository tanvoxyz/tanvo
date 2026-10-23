package catalog

import (
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// FixedCharge is a recurring, quantity-based charge on a plan, priced from
// an add-on (domain §7.5). A fixed charge with a ParentID belongs to an
// override plan. Per-subscription unit changes are fixed charge events of
// package subscription.
type FixedCharge struct {
	// ID identifies the fixed charge.
	ID core.FixedChargeID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// PlanID is the plan the fixed charge belongs to.
	PlanID core.PlanID
	// ParentID is the fixed charge of the original plan this override copy
	// comes from.
	ParentID core.FixedChargeID
	// Code is unique per plan; see [NextFreeCode].
	Code string
	// AddOnID is the priced add-on.
	AddOnID core.AddOnID
	// ChargeModel is standard, graduated or volume.
	ChargeModel FixedChargeModel
	// Properties are the model's properties (validated by libs/pricing).
	Properties Properties
	// Units is the default quantity of subscriptions, ≥ 0.
	Units money.Decimal
	// PayInAdvance bills the fixed charge at the start of the period; not
	// with volume.
	PayInAdvance bool
	// Prorated prorates units by time; not with graduated in advance.
	Prorated bool
	// InvoiceDisplayName replaces the add-on name on invoices.
	InvoiceDisplayName *string
	// TaxIDs are the fixed charge's taxes (domain §22.10, step 5).
	TaxIDs []core.TaxID
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
	// DeletedAt is the instant the fixed charge was discarded (nil while kept);
	// a discarded one is read by ID only, for the fees
	// that reference it (lago fixed_charge.rb default_scope kept).
	DeletedAt *time.Time
}

// IsChild reports whether the fixed charge belongs to an override plan.
func (f *FixedCharge) IsChild() bool { return !f.ParentID.IsZero() }

// InvoiceName returns the invoice display name, or addOnName when unset.
func (f *FixedCharge) InvoiceName(addOnName string) string {
	if f.InvoiceDisplayName != nil && *f.InvoiceDisplayName != "" {
		return *f.InvoiceDisplayName
	}
	return addOnName
}

// EqualProperties reports whether f and other price the same way: same
// model, equal properties and equal units.
func (f *FixedCharge) EqualProperties(other *FixedCharge) bool {
	return f.ChargeModel == other.ChargeModel && f.Properties.Equal(other.Properties) && f.Units.Equal(other.Units)
}

// Validate checks the invariants of domain §7.5.
func (f *FixedCharge) Validate() error {
	var v core.Validator
	v.Code("code", f.Code, 255)
	v.Check(!f.PlanID.IsZero(), "plan_id", core.CodeRequired)
	v.Check(!f.AddOnID.IsZero(), "add_on_id", core.CodeRequired)
	v.Enum("charge_model", f.ChargeModel)
	v.Check(f.Properties.IsObject(), "properties", core.CodeInvalid)
	v.DecimalNonNegative("units", f.Units)
	v.OptionalString("invoice_display_name", f.InvoiceDisplayName, 255)
	if f.PayInAdvance && f.ChargeModel == FixedChargeModelVolume {
		v.Add("pay_in_advance", CodeInvalidChargeModel)
	}
	if f.Prorated && f.PayInAdvance && f.ChargeModel == FixedChargeModelGraduated {
		v.Add("prorated", CodeInvalidChargeModel)
	}
	return v.Err()
}

// NewOverride returns the copy of f for the override plan plan.
func (f *FixedCharge) NewOverride(id core.FixedChargeID, plan core.PlanID, now time.Time) *FixedCharge {
	o := f.Clone()
	o.ID = id
	o.PlanID = plan
	o.ParentID = f.ID
	o.CreatedAt = now
	o.UpdatedAt = now
	o.DeletedAt = nil
	return o
}

// Clone returns a deep copy of f.
func (f *FixedCharge) Clone() *FixedCharge {
	o := *f
	o.Properties = f.Properties.Clone()
	o.TaxIDs = slices.Clone(f.TaxIDs)
	o.DeletedAt = clonePtr(f.DeletedAt)
	return &o
}
