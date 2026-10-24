package plans

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// Resource is the resource name of the not-found errors.
const Resource = "plan"

// Field error codes of usage thresholds (UsageThresholds::UpdateService).
const (
	// CodeMissingAmount: a usage threshold has no amount.
	CodeMissingAmount = "missing_amount_cents"
	// CodeDuplicatedValues: two usage thresholds have the same amount and
	// recurrence.
	CodeDuplicatedValues = "duplicated_values"
)

// ThresholdInput is a usage threshold of a plan.
type ThresholdInput struct {
	// Amount is the usage amount, in minor units of the plan's currency;
	// nil is missing.
	Amount *int64
	// Recurring repeats the threshold after the largest fixed one.
	Recurring bool
	// ThresholdDisplayName is the invoice line name.
	ThresholdDisplayName *string
}

// CommitmentInput is the minimum commitment of a plan: only the set fields
// are written. A blank input (no field set) removes the commitment on
// update and creates none otherwise.
type CommitmentInput struct {
	// Amount is the committed amount per period, in minor units of the
	// plan's currency; > 0.
	Amount core.Optional[int64]
	// InvoiceDisplayName is the invoice line name of the true-up fee.
	InvoiceDisplayName core.Optional[*string]
	// Taxes replace the commitment's taxes.
	Taxes core.Optional[[]refs.Ref]
}

// blank reports whether c sets nothing (lago's params.blank?).
func (c *CommitmentInput) blank() bool {
	return c == nil || (!c.Amount.Set && !c.InvoiceDisplayName.Set && !c.Taxes.Set)
}

// CreateInput is a plan to create.
type CreateInput struct {
	// Code is the unique code among the original plans.
	Code string
	// Name is the display name.
	Name string
	// InvoiceDisplayName is the invoice line name of the subscription fee.
	InvoiceDisplayName *string
	// Description is free text.
	Description *string
	// Interval is the billing interval.
	Interval period.Interval
	// Amount is the subscription fee, in minor units of Currency.
	Amount int64
	// Currency is the plan's currency.
	Currency money.Currency
	// PayInAdvance bills the subscription fee at the start of the period.
	PayInAdvance bool
	// TrialPeriodDays is the trial period.
	TrialPeriodDays int
	// BillChargesMonthly bills usage charges monthly; kept on yearly and
	// semiannual plans only.
	BillChargesMonthly *bool
	// BillFixedChargesMonthly bills fixed charges monthly; kept on yearly
	// and semiannual plans only.
	BillFixedChargesMonthly *bool
	// Taxes reference the plan's taxes; nil sets none.
	Taxes []refs.Ref
	// Metadata is the plan's metadata.
	Metadata core.Metadata
	// Charges are the usage charges.
	Charges []ChargeInput
	// FixedCharges are the fixed charges.
	FixedCharges []FixedChargeInput
	// UsageThresholds are the progressive billing thresholds.
	UsageThresholds []ThresholdInput
	// MinimumCommitment is the minimum commitment; nil or blank sets none.
	MinimumCommitment *CommitmentInput
}

// Create creates a plan with its taxes, metadata, usage thresholds,
// charges, fixed charges and minimum commitment, and emits plan.created.
// A code used by another original plan is code value_already_exist; a
// missing tax is not found, and a missing metric or add-on is
// billable_metrics_not_found or add_ons_not_found before anything is
// written.
//
// source: app/services/plans/create_service.rb:21-99
func (s *Service) Create(ctx context.Context, org core.OrganizationID, in CreateInput) (*catalog.Plan, error) {
	now := s.d.Now()
	p := &catalog.Plan{
		ID:                      core.NewID[core.PlanID](s.d.IDs),
		OrganizationID:          org,
		Code:                    in.Code,
		Name:                    in.Name,
		InvoiceDisplayName:      in.InvoiceDisplayName,
		Description:             in.Description,
		Interval:                in.Interval,
		Amount:                  in.Amount,
		Currency:                in.Currency,
		PayInAdvance:            in.PayInAdvance,
		TrialPeriodDays:         in.TrialPeriodDays,
		BillChargesMonthly:      billMonthly(in.Interval, in.BillChargesMonthly),
		BillFixedChargesMonthly: billMonthly(in.Interval, in.BillFixedChargesMonthly),
		Metadata:                in.Metadata,
		Status:                  catalog.PlanStatusActive,
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := validateChargeables(ctx, r, org, in.Charges, in.FixedCharges); err != nil {
			return err
		}
		if err := validatePlan(ctx, r, p); err != nil {
			return err
		}
		ids, err := refs.Taxes(r, org).IDs(ctx, in.Taxes)
		if err != nil {
			return err
		}
		p.TaxIDs = ids
		if err := r.Plans.Create(ctx, p); err != nil {
			return codeConflict(err)
		}
		// source: app/services/plans/create_service.rb:66-68
		if len(in.UsageThresholds) > 0 {
			if err := s.replaceThresholds(ctx, r, p, in.UsageThresholds); err != nil {
				return err
			}
		}
		for _, c := range in.Charges {
			if _, err := s.createCharge(ctx, r, p, c, false); err != nil {
				return err
			}
		}
		for _, f := range in.FixedCharges {
			if _, err := s.createFixedCharge(ctx, r, p, f, false); err != nil {
				return err
			}
		}
		// source: app/services/plans/create_service.rb:82-92
		if !in.MinimumCommitment.blank() {
			c := s.newCommitment(p)
			if err := s.writeCommitment(ctx, r, c, *in.MinimumCommitment, true); err != nil {
				return err
			}
		}
		if err := kernel.Emit(ctx, s.d, org, webhook.EventTypePlanCreated, "plan", p.ID.UUID(), p); err != nil {
			return err
		}
		// source: app/services/plans/create_service.rb:13-16
		return s.logPlan(ctx, r, auditlog.ActivityPlanCreated, nil, p)
	})
	if err != nil {
		return nil, fmt.Errorf("create plan: %w", err)
	}
	return p, nil
}

// billMonthly returns the bill_*_monthly setting of a plan on interval
// i: nil unless i is yearly or semiannual, else v or false.
//
// source: app/services/plans/create_service.rb:131-147
func billMonthly(i period.Interval, v *bool) *bool {
	if i != period.Yearly && i != period.Semiannual {
		return nil
	}
	b := v != nil && *v
	return &b
}

// validatePlan checks p as lago's model does: the domain rules and the
// code's uniqueness among the original plans, reported together.
//
// source: app/models/plan.rb:51-58,151-157
func validatePlan(ctx context.Context, r port.Repos, p *catalog.Plan) error {
	var v core.Validator
	v.Merge("", p.Validate())
	if p.IsParent() && p.Code != "" {
		other, err := r.Plans.GetByCode(ctx, p.OrganizationID, p.Code)
		switch {
		case err == nil:
			v.Check(other.ID == p.ID, "code", core.CodeAlreadyExists)
		case !errors.Is(err, core.ErrNotFound):
			return fmt.Errorf("check code: %w", err)
		}
	}
	return v.Err()
}

// codeConflict turns a uniqueness violation, which a concurrent write can
// still cause after validatePlan, into code value_already_exist.
func codeConflict(err error) error {
	if errors.Is(err, core.ErrConflict) {
		return &core.ValidationError{Errors: []core.FieldError{{Field: "code", Code: core.CodeAlreadyExists}}}
	}
	return err
}

// thresholdsError is the single validation failure of the usage
// thresholds.
func thresholdsError(code string) error {
	return &core.ValidationError{Errors: []core.FieldError{{Field: "usage_thresholds", Code: code}}}
}

// replaceThresholds replaces the usage thresholds of the plan p by in
// (UsageThresholds::UpdateService with partial: false): it discards every
// kept threshold, then writes the recurring threshold and the fixed ones.
//
// source: app/services/usage_thresholds/update_service.rb:15-32,64-108
func (s *Service) replaceThresholds(ctx context.Context, r port.Repos, p *catalog.Plan, in []ThresholdInput) error {
	type key struct {
		amount    int64
		recurring bool
	}
	seen := map[key]bool{}
	recurring := 0
	for _, t := range in {
		if t.Amount == nil {
			return thresholdsError(CodeMissingAmount)
		}
	}
	for _, t := range in {
		k := key{*t.Amount, t.Recurring}
		if seen[k] {
			return thresholdsError(CodeDuplicatedValues)
		}
		seen[k] = true
		if t.Recurring {
			recurring++
		}
	}
	if recurring > 1 {
		return thresholdsError(catalog.CodeMultipleRecurringThresholds)
	}
	kept, err := r.UsageThresholds.ListByPlan(ctx, p.OrganizationID, p.ID)
	if err != nil {
		return fmt.Errorf("load usage thresholds: %w", err)
	}
	now := s.d.Now()
	for _, t := range kept {
		if err := r.UsageThresholds.Discard(ctx, p.OrganizationID, t.ID, now); err != nil {
			return fmt.Errorf("discard usage threshold %s: %w", t.ID, err)
		}
	}
	// The recurring threshold is written first (update_service.rb:25-26).
	ordered := make([]ThresholdInput, 0, len(in))
	for _, t := range in {
		if t.Recurring {
			ordered = append(ordered, t)
		}
	}
	for _, t := range in {
		if !t.Recurring {
			ordered = append(ordered, t)
		}
	}
	return s.createThresholds(ctx, r, p, ordered)
}

// createThresholds creates the usage thresholds in on the plan p, which
// has none. A repeated amount among the fixed thresholds, or a second
// recurring threshold, is code value_already_exist, as the model's
// uniqueness validations.
//
// source: app/services/usage_thresholds/update_service.rb:101-108, app/models/usage_threshold.rb:19-21
func (s *Service) createThresholds(ctx context.Context, r port.Repos, p *catalog.Plan, in []ThresholdInput) error {
	now := s.d.Now()
	fixed := map[int64]bool{}
	recurring := false
	for _, t := range in {
		switch {
		case t.Recurring && recurring:
			return &core.ValidationError{Errors: []core.FieldError{{Field: "usage_thresholds.recurring", Code: core.CodeAlreadyExists}}}
		case t.Recurring:
			recurring = true
		case t.Amount != nil && fixed[*t.Amount]:
			return &core.ValidationError{Errors: []core.FieldError{{Field: "usage_thresholds.amount", Code: core.CodeAlreadyExists}}}
		case t.Amount != nil:
			fixed[*t.Amount] = true
		}
	}
	for _, t := range in {
		var amount int64
		if t.Amount != nil {
			amount = *t.Amount
		}
		u := &catalog.UsageThreshold{
			ID:                   core.NewID[core.UsageThresholdID](s.d.IDs),
			OrganizationID:       p.OrganizationID,
			PlanID:               p.ID,
			Amount:               amount,
			Recurring:            t.Recurring,
			ThresholdDisplayName: t.ThresholdDisplayName,
			CreatedAt:            now,
			UpdatedAt:            now,
		}
		if err := u.Validate(); err != nil {
			return prefixed("usage_thresholds", err)
		}
		if err := r.UsageThresholds.Create(ctx, u); err != nil {
			return fmt.Errorf("create usage threshold: %w", err)
		}
	}
	return nil
}

// newCommitment returns an unsaved minimum commitment of the plan p.
func (s *Service) newCommitment(p *catalog.Plan) *catalog.Commitment {
	now := s.d.Now()
	return &catalog.Commitment{
		ID:             core.NewID[core.CommitmentID](s.d.IDs),
		OrganizationID: p.OrganizationID,
		PlanID:         p.ID,
		CommitmentType: catalog.CommitmentTypeMinimumCommitment,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

// writeCommitment applies the set fields of in to c, then creates c
// (create) or updates it; set taxes replace the commitment's
// (Commitments::ApplyTaxesService).
//
// source: app/services/plans/create_service.rb:82-92,103-111, app/services/plans/update_service.rb:223-243
func (s *Service) writeCommitment(ctx context.Context, r port.Repos, c *catalog.Commitment, in CommitmentInput, create bool) error {
	in.Amount.Apply(&c.Amount)
	in.InvoiceDisplayName.Apply(&c.InvoiceDisplayName)
	if err := c.Validate(); err != nil {
		return prefixed("minimum_commitment", err)
	}
	if in.Taxes.Set {
		ids, err := refs.Taxes(r, c.OrganizationID).IDs(ctx, in.Taxes.Value)
		if err != nil {
			return err
		}
		c.TaxIDs = ids
	}
	c.UpdatedAt = s.d.Now()
	if create {
		if err := r.Commitments.Create(ctx, c); err != nil {
			return fmt.Errorf("create commitment: %w", err)
		}
		return nil
	}
	if err := r.Commitments.Update(ctx, c); err != nil {
		return fmt.Errorf("update commitment %s: %w", c.ID, err)
	}
	return nil
}

// prefixed moves the field errors of a validation error under field.
func prefixed(field string, err error) error {
	var v core.Validator
	v.Merge(field, err)
	if e := v.Err(); e != nil {
		return e
	}
	return err
}
