package kernel

import (
	"github.com/tanvoxyz/tanvo/libs/core"
)

// Jobs of the dunning campaigns (domain §16.1).
const (
	// JobProcessDunningCampaigns enqueues the [JobProcessDunningCustomer]
	// of every customer of an organization not excluded from dunning that
	// has overdue invoices (lago Clock::ProcessDunningCampaignsJob and
	// DunningCampaigns::BulkProcessService); its args are [InstantArgs].
	JobProcessDunningCampaigns = "payments.process_dunning_campaigns"
	// JobProcessDunningCustomer makes the dunning attempt of one customer
	// (lago DunningCampaigns::ProcessCustomerJob); its args are
	// [CustomerArgs], unique per customer ([DunningCustomerKey]).
	JobProcessDunningCustomer = "payments.process_dunning_customer"
	// JobProcessDunningAttempt requests the payment of a customer's
	// overdue invoices in one currency and billing entity (lago
	// DunningCampaigns::ProcessAttemptJob); its args are
	// [DunningAttemptArgs].
	JobProcessDunningAttempt = "payments.process_dunning_attempt"
)

// CustomerArgs are the args of the jobs on one customer.
type CustomerArgs struct {
	// CustomerID is the customer.
	CustomerID core.CustomerID `json:"customer_id"`
}

// DunningAttemptArgs are the args of [JobProcessDunningAttempt].
type DunningAttemptArgs struct {
	// CustomerID is the dunned customer.
	CustomerID core.CustomerID `json:"customer_id"`
	// DunningCampaignID is the campaign whose threshold was reached.
	DunningCampaignID core.DunningCampaignID `json:"dunning_campaign_id"`
	// ThresholdID is the reached threshold.
	ThresholdID core.DunningCampaignThresholdID `json:"dunning_campaign_threshold_id"`
	// BillingEntityID is the billing entity of the requested invoices.
	BillingEntityID core.BillingEntityID `json:"billing_entity_id"`
}

// DunningCustomerKey is the unique key of the [JobProcessDunningCustomer]
// of customer id (lago unique :until_executed,
// app/jobs/dunning_campaigns/process_customer_job.rb:7).
func DunningCustomerKey(id core.CustomerID) string { return "dunning:" + id.String() }
