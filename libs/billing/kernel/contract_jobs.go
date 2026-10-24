package kernel

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Jobs of contracts and their billing segments (domain §21.4).
const (
	// JobActivateContracts enqueues the [JobActivateContract] of every
	// pending contract of an organization started by the clock instant
	// whose customer is not deleted (lago Clock::ActivateContractsJob and
	// Contracts::ActivateAllPendingService); its args are [InstantArgs].
	JobActivateContracts = "billing.activate_contracts"
	// JobActivateContract activates one pending contract (lago
	// Contracts::ActivateJob); its args are [SubscriptionArgs], unique per
	// contract ([ActivateContractKey]).
	JobActivateContract = "billing.activate_contract"
	// JobCreateBillingSegments enqueues the [JobScheduleBillingSegments]
	// of every customer having a contract rate card due at the clock
	// instant (lago Clock::CreateBillingSegmentsJob); its args are
	// [InstantArgs].
	JobCreateBillingSegments = "billing.create_billing_segments"
	// JobScheduleBillingSegments stores the due billing segments of a
	// customer's contract rate cards, then enqueues their invoicing when
	// some await it (lago BillingSegments::ScheduleJob); its args are
	// [CustomerArgs], unique per customer ([ScheduleSegmentsKey]).
	JobScheduleBillingSegments = "billing.schedule_billing_segments"
	// JobProcessBillingSegmentsAll enqueues the [JobProcessBillingSegments]
	// of every customer, not deleted, with billing segments awaiting
	// invoicing (lago Clock::ProcessBillingSegmentsJob); its args are
	// [InstantArgs].
	JobProcessBillingSegmentsAll = "billing.process_billing_segments_all"
	// JobProcessBillingSegments invoices a customer's billing segments
	// awaiting invoicing (lago BillingSegments::ProcessJob); its args are
	// [CustomerArgs], unique per customer ([ProcessSegmentsKey]).
	JobProcessBillingSegments = "billing.process_billing_segments"
)

// ActivateContractKey is the unique key of the [JobActivateContract] of
// contract id (lago unique :until_executing,
// app/jobs/contracts/activate_job.rb:13).
func ActivateContractKey(id core.SubscriptionID) string { return "activate_contract:" + id.String() }

// ScheduleSegmentsKey is the unique key of the [JobScheduleBillingSegments]
// of customer id (lago unique :until_executing,
// app/jobs/billing_segments/schedule_job.rb:13).
func ScheduleSegmentsKey(id core.CustomerID) string {
	return "schedule_billing_segments:" + id.String()
}

// ProcessSegmentsKey is the unique key of the [JobProcessBillingSegments]
// of customer id (lago unique :until_executing,
// app/jobs/billing_segments/process_job.rb:13).
func ProcessSegmentsKey(id core.CustomerID) string { return "process_billing_segments:" + id.String() }

// EnqueueScheduleSegments enqueues the [JobScheduleBillingSegments] of
// customer c; it runs once the transaction commits (lago
// perform_after_commit).
func EnqueueScheduleSegments(ctx context.Context, r port.Repos, org core.OrganizationID, c core.CustomerID) error {
	return Enqueue(ctx, r, org, JobScheduleBillingSegments, CustomerArgs{CustomerID: c}, ScheduleSegmentsKey(c), time.Time{})
}

// EnqueueProcessSegments enqueues the [JobProcessBillingSegments] of
// customer c.
func EnqueueProcessSegments(ctx context.Context, r port.Repos, org core.OrganizationID, c core.CustomerID) error {
	return Enqueue(ctx, r, org, JobProcessBillingSegments, CustomerArgs{CustomerID: c}, ProcessSegmentsKey(c), time.Time{})
}

// contractJobKinds returns the job kinds of contracts.
func contractJobKinds() []string {
	return []string{
		JobActivateContracts, JobActivateContract, JobCreateBillingSegments, JobScheduleBillingSegments,
		JobProcessBillingSegmentsAll, JobProcessBillingSegments,
	}
}
