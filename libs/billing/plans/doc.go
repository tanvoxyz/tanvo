// Package plans deletes plans (domain §7.2): [Service.PrepareDestroy] marks
// a plan and its override plans pending deletion, emits plan.deleted and
// enqueues a [kernel.JobDestroyPlan]; the job, for each override plan and
// then the plan, terminates the active subscriptions, cancels the pending
// ones, finalizes the draft invoices billing them, soft-deletes the plan's
// entitlements and keeps the plan with the deleted status. Terminations go
// through the [Terminator] port and finalizations through
// [DraftFinalizer].
package plans
