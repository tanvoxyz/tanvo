package kernel

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Jobs of the fixed-charge cascades from a parent plan to its override
// plans, and of the billing of pay-in-advance fixed charges whose units
// change.
const (
	// JobCreateFixedChargeChildren copies a new fixed charge of a parent
	// plan onto its override plans that have active or pending
	// subscriptions (lago FixedCharges::CreateChildrenJob and
	// CreateChildrenBatchJob); its args are [CreateFixedChargeChildrenArgs].
	JobCreateFixedChargeChildren = "billing.create_fixed_charge_children"
	// JobUpdateFixedChargeChildren cascades the new pricing of a parent
	// fixed charge to its overrides on plans with active or pending
	// subscriptions (lago FixedCharges::UpdateChildrenJob and
	// UpdateChildrenBatchJob); its args are [UpdateFixedChargeChildrenArgs].
	JobUpdateFixedChargeChildren = "billing.update_fixed_charge_children"
	// JobDestroyFixedChargeChildren discards the overrides of a discarded
	// parent fixed charge on plans with active or pending subscriptions
	// (lago FixedCharges::DestroyChildrenJob); its args are
	// [FixedChargeArgs].
	JobDestroyFixedChargeChildren = "billing.destroy_fixed_charge_children"
	// JobCascadeFixedCharges cascades the fixed charges created and updated
	// by a plan update to the plan's override plans that have active or
	// pending subscriptions (lago FixedCharges::CascadePlanUpdateJob and
	// CascadeChildPlanUpdateJob); its args are [CascadeFixedChargesArgs].
	JobCascadeFixedCharges = "billing.cascade_fixed_charges"
	// JobCreateAllInAdvanceFixedCharges enqueues a
	// [JobCreateInAdvanceFixedCharges] for each active subscription of a
	// plan (lago Invoices::CreateAllPayInAdvanceFixedChargesJob); its args
	// are [AllInAdvanceFixedChargesArgs].
	JobCreateAllInAdvanceFixedCharges = "billing.create_all_in_advance_fixed_charges"
	// JobCreateInAdvanceFixedCharges bills, on an invoice of their own, the
	// pay-in-advance fixed charges of a subscription whose units changed
	// at an instant (lago Invoices::CreatePayInAdvanceFixedChargesJob); its
	// args are [InAdvanceFixedChargesArgs].
	JobCreateInAdvanceFixedCharges = "billing.create_in_advance_fixed_charges"
)

// FixedChargeChildrenBatch is the number of override plans or fixed
// charges one cascade job changes in one transaction (lago each_slice(20)).
const FixedChargeChildrenBatch = 20

// FixedChargeArgs are the args of jobs about one fixed charge.
type FixedChargeArgs struct {
	// FixedChargeID is the fixed charge.
	FixedChargeID core.FixedChargeID `json:"fixed_charge_id"`
}

// CreateFixedChargeChildrenArgs are the args of a
// [JobCreateFixedChargeChildren].
type CreateFixedChargeChildrenArgs struct {
	// FixedChargeID is the new parent fixed charge.
	FixedChargeID core.FixedChargeID `json:"fixed_charge_id"`
	// Payload is the fixed charge input the parent was created from,
	// encoded by the plans package.
	Payload json.RawMessage `json:"payload"`
	// PlanIDs are the override plans of this batch; nil lists them and
	// enqueues one job per batch.
	PlanIDs []core.PlanID `json:"plan_ids,omitempty"`
}

// UpdateFixedChargeChildrenArgs are the args of a
// [JobUpdateFixedChargeChildren].
type UpdateFixedChargeChildrenArgs struct {
	// FixedChargeID is the parent fixed charge.
	FixedChargeID core.FixedChargeID `json:"fixed_charge_id"`
	// Code is the parent's code, given to the overrides.
	Code string `json:"code"`
	// ChargeModel is the parent's model; overrides of another model are
	// left alone.
	ChargeModel catalog.FixedChargeModel `json:"charge_model"`
	// Properties are the parent's new properties.
	Properties catalog.Properties `json:"properties"`
	// Units are the parent's new units.
	Units money.Decimal `json:"units"`
	// OldChargeModel, OldProperties and OldUnits are the parent's pricing
	// before the update: overrides that still price like it follow the new
	// pricing.
	OldChargeModel catalog.FixedChargeModel `json:"old_charge_model"`
	// OldProperties are the parent's properties before the update.
	OldProperties catalog.Properties `json:"old_properties"`
	// OldUnits are the parent's units before the update.
	OldUnits money.Decimal `json:"old_units"`
	// ChildIDs are the override fixed charges of this batch; nil lists
	// them and enqueues one job per batch.
	ChildIDs []core.FixedChargeID `json:"child_ids,omitempty"`
}

// CascadeFixedChargesArgs are the args of a [JobCascadeFixedCharges].
type CascadeFixedChargesArgs struct {
	// PlanID is the parent plan, or the override plan to change when
	// Child is set.
	PlanID core.PlanID `json:"plan_id"`
	// Child is set on the job of one override plan.
	Child bool `json:"child,omitempty"`
	// Payload are the cascaded creations and updates, encoded by the plans
	// package.
	Payload json.RawMessage `json:"payload"`
	// Timestamp is the instant of the plan update.
	Timestamp time.Time `json:"timestamp"`
}

// AllInAdvanceFixedChargesArgs are the args of a
// [JobCreateAllInAdvanceFixedCharges].
type AllInAdvanceFixedChargesArgs struct {
	// PlanID is the plan whose active subscriptions are billed.
	PlanID core.PlanID `json:"plan_id"`
	// Timestamp is the instant of the units change.
	Timestamp time.Time `json:"timestamp"`
	// FixedChargeID, when set, skips the subscriptions with their own
	// units for this fixed charge.
	FixedChargeID core.FixedChargeID `json:"fixed_charge_id,omitzero"`
}

// InAdvanceFixedChargesArgs are the args of a
// [JobCreateInAdvanceFixedCharges].
type InAdvanceFixedChargesArgs struct {
	// SubscriptionID is the billed subscription.
	SubscriptionID core.SubscriptionID `json:"subscription_id"`
	// Timestamp is the instant of the units change: the events recorded
	// at it are billed.
	Timestamp time.Time `json:"timestamp"`
}

// InAdvanceFixedChargesKey is the unique key of the
// [JobCreateInAdvanceFixedCharges] of a subscription at an instant (lago
// unique :until_executed).
func InAdvanceFixedChargesKey(sub core.SubscriptionID, at time.Time) string {
	return fmt.Sprintf("fixed_advance:%s:%d", sub, at.UnixNano())
}

// fixedChargeJobKinds returns the job kinds of the fixed-charge cascades.
func fixedChargeJobKinds() []string {
	return []string{
		JobCreateFixedChargeChildren, JobUpdateFixedChargeChildren, JobDestroyFixedChargeChildren,
		JobCascadeFixedCharges, JobCreateAllInAdvanceFixedCharges, JobCreateInAdvanceFixedCharges,
	}
}
