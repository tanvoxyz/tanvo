package catalog

import (
	"errors"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

func TestPlanValidate(t *testing.T) {
	type tc struct {
		Name                    string   `yaml:"name"`
		Source                  string   `yaml:"source"`
		Code                    *string  `yaml:"code"`
		PlanName                *string  `yaml:"plan_name"`
		Interval                string   `yaml:"interval"`
		Amount                  int64    `yaml:"amount"`
		PayInAdvance            bool     `yaml:"pay_in_advance"`
		TrialPeriodDays         int      `yaml:"trial_period_days"`
		BillChargesMonthly      *bool    `yaml:"bill_charges_monthly"`
		BillFixedChargesMonthly *bool    `yaml:"bill_fixed_charges_monthly"`
		InvoiceDisplayName      *string  `yaml:"invoice_display_name"`
		WantHasTrial            *bool    `yaml:"want_has_trial"`
		WantPayInArrears        *bool    `yaml:"want_pay_in_arrears"`
		WantChargesMonthly      *bool    `yaml:"want_charges_monthly"`
		WantFixedMonthly        *bool    `yaml:"want_fixed_monthly"`
		WantEitherMonthly       *bool    `yaml:"want_either_monthly"`
		WantYearlyAmount        *string  `yaml:"want_yearly_amount"`
		WantInvoiceName         *string  `yaml:"want_invoice_name"`
		Errors                  []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "plan.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			p := catalogTestPlanOf()
			if c.Code != nil {
				p.Code = *c.Code
			}
			if c.PlanName != nil {
				p.Name = *c.PlanName
			}
			p.Interval = 0
			if c.Interval != "" {
				i, err := period.ParseInterval(c.Interval)
				if err != nil {
					t.Fatal(err)
				}
				p.Interval = i
			}
			p.Amount = c.Amount
			p.PayInAdvance = c.PayInAdvance
			p.TrialPeriodDays = c.TrialPeriodDays
			p.BillChargesMonthly = c.BillChargesMonthly
			p.BillFixedChargesMonthly = c.BillFixedChargesMonthly
			p.InvoiceDisplayName = c.InvoiceDisplayName
			catalogCheckErrors(t, "Validate()", p.Validate(), c.Errors, c.Source)
			checkBool := func(what string, got bool, want *bool) {
				if want != nil && got != *want {
					t.Errorf("%s got = %v, want = %v (%s)", what, got, *want, c.Source)
				}
			}
			checkBool("HasTrial()", p.HasTrial(), c.WantHasTrial)
			checkBool("PayInArrears()", p.PayInArrears(), c.WantPayInArrears)
			checkBool("ChargesBilledMonthly()", p.ChargesBilledMonthly(), c.WantChargesMonthly)
			checkBool("FixedChargesBilledMonthly()", p.FixedChargesBilledMonthly(), c.WantFixedMonthly)
			checkBool("ChargesOrFixedChargesBilledMonthly()", p.ChargesOrFixedChargesBilledMonthly(), c.WantEitherMonthly)
			if c.WantYearlyAmount != nil {
				if got := p.YearlyAmount(); !got.Equal(money.MustParse(*c.WantYearlyAmount)) {
					t.Errorf("YearlyAmount() got = %s, want = %s (%s)", got, *c.WantYearlyAmount, c.Source)
				}
			}
			if c.WantInvoiceName != nil {
				if got := p.InvoiceName(); got != *c.WantInvoiceName {
					t.Errorf("InvoiceName() got = %q, want = %q (%s)", got, *c.WantInvoiceName, c.Source)
				}
			}
		})
	}
}

func TestPlanLifecycleAndOverride(t *testing.T) {
	p := catalogTestPlanOf()
	p.TaxIDs = []core.TaxID{core.MustParseID[core.TaxID]("0190a0a0-0000-7000-8000-0000000000aa")}
	childID := core.MustParseID[core.PlanID]("0190a0a0-0000-7000-8000-0000000000bb")
	child := p.NewOverride(childID, catalogTestNow)
	if !child.IsChild() || child.ParentID != p.ID || child.Code != p.Code || child.ID != childID {
		t.Errorf("NewOverride() got = %+v, want = a child of %s with code %q", child, p.ID, p.Code)
	}
	child.TaxIDs[0] = core.TaxID{}
	if p.TaxIDs[0].IsZero() {
		t.Error("NewOverride() shares TaxIDs with the parent, want = a copy")
	}
	if err := p.MarkPendingDeletion(catalogTestNow); err != nil || p.Status != PlanStatusPendingDeletion {
		t.Fatalf("MarkPendingDeletion() got = %v, status %s, want = nil, pending_deletion", err, p.Status)
	}
	if err := p.MarkPendingDeletion(catalogTestNow); !errors.Is(err, core.ErrInvalidTransition) {
		t.Errorf("MarkPendingDeletion() twice got = %v, want = %v", err, core.ErrInvalidTransition)
	}
}

func TestValidatePlanUpdate(t *testing.T) {
	old := catalogTestPlanOf()
	updated := *old
	updated.Interval = period.Yearly
	updated.Currency = money.MustCurrency("USD")
	updated.Name = "Basic 2"
	want := []string{"interval: value_is_immutable", "currency: value_is_immutable"}
	catalogCheckErrors(t, "ValidatePlanUpdate()", ValidatePlanUpdate(old, &updated, true), want, "tanvo")
	catalogCheckErrors(t, "ValidatePlanUpdate() without subscriptions", ValidatePlanUpdate(old, &updated, false), nil, "tanvo")
	if got := PlanLockedChanges(old, old); len(got) != 0 {
		t.Errorf("PlanLockedChanges(same) got = %v, want = []", got)
	}
	if !slices.Contains(PlanLockedFields(), "trial_period_days") {
		t.Errorf("PlanLockedFields() got = %v, want = trial_period_days included", PlanLockedFields())
	}
}
