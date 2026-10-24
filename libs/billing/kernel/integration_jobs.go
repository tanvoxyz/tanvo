package kernel

import (
	"context"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Jobs of the tax, accounting and CRM integrations
// (libs/billing/integrations); apps/server runs them with the provider
// adapters, on the integrations queue (lago queue_as "integrations").
const (
	// JobFetchTaxCompany resolves the Avalara company of a new connection
	// from its company code (args [IntegrationArgs]; lago
	// Integrations::Avalara::FetchCompanyIdJob).
	JobFetchTaxCompany = "integrations.fetch_tax_company"
	// JobReportInvoiceTaxes reports the taxes of a finalized invoice to
	// the customer's tax provider (args [InvoiceArgs]; lago
	// Integrations::Aggregator::Taxes::Invoices::CreateJob).
	JobReportInvoiceTaxes = "integrations.report_invoice_taxes"
	// JobSyncContact creates or updates the customer's record at the
	// provider of an integration customer (args [IntegrationCustomerArgs];
	// lago IntegrationCustomers::CreateJob and UpdateJob).
	JobSyncContact = "integrations.sync_contact"
	// JobSyncInvoice creates a finalized invoice at the customer's
	// accounting provider (args [SyncInvoiceArgs]; lago
	// Integrations::Aggregator::Invoices::CreateJob).
	JobSyncInvoice = "integrations.sync_invoice"
	// JobSyncCreditNote creates a finalized credit note at the customer's
	// accounting provider (args [CreditNoteArgs]; lago
	// Integrations::Aggregator::CreditNotes::CreateJob).
	JobSyncCreditNote = "integrations.sync_credit_note"
	// JobSyncPayment creates a succeeded payment at the customer's
	// accounting provider (args [PaymentArgs]; lago
	// Integrations::Aggregator::Payments::CreateJob).
	JobSyncPayment = "integrations.sync_payment"
	// JobCreateCRMInvoice creates the CRM record of a finalized invoice
	// (args [InvoiceArgs]; lago Integrations::Aggregator::Invoices::Hubspot::CreateJob).
	JobCreateCRMInvoice = "integrations.create_crm_invoice"
	// JobUpdateCRMInvoice updates the CRM record of an invoice (args
	// [InvoiceArgs]; lago Integrations::Aggregator::Invoices::Hubspot::UpdateJob).
	JobUpdateCRMInvoice = "integrations.update_crm_invoice"
	// JobAssociateCRMInvoice links the CRM record of an invoice to the
	// customer's record (args [InvoiceArgs]; lago
	// Integrations::Aggregator::Invoices::Hubspot::CreateCustomerAssociationJob).
	JobAssociateCRMInvoice = "integrations.associate_crm_invoice"
	// JobCreateCRMSubscription creates the CRM record of a subscription
	// (args [SubscriptionArgs]; lago
	// Integrations::Aggregator::Subscriptions::Hubspot::CreateJob).
	JobCreateCRMSubscription = "integrations.create_crm_subscription"
	// JobUpdateCRMSubscription updates the CRM record of a subscription
	// (args [SubscriptionArgs]; lago
	// Integrations::Aggregator::Subscriptions::Hubspot::UpdateJob).
	JobUpdateCRMSubscription = "integrations.update_crm_subscription"
	// JobAssociateCRMSubscription links the CRM record of a subscription to
	// the customer's record (args [SubscriptionArgs]; lago
	// Integrations::Aggregator::Subscriptions::Hubspot::CreateCustomerAssociationJob).
	JobAssociateCRMSubscription = "integrations.associate_crm_subscription"
	// JobFetchIntegrationItems refreshes the cached items of an accounting
	// connection (args [IntegrationArgs]; lago
	// Integrations::Aggregator::FetchItemsJob and PerformSyncJob).
	JobFetchIntegrationItems = "integrations.fetch_items"
	// JobDeployCRM deploys the custom objects and properties of a CRM
	// connection (args [IntegrationArgs]; lago
	// Integrations::Aggregator::SyncCustomObjectsAndPropertiesJob).
	JobDeployCRM = "integrations.deploy_crm"
	// JobSaveCRMAccount stores the account of a CRM connection, the
	// HubSpot portal (args [IntegrationArgs]; lago
	// Integrations::Hubspot::SavePortalIdJob).
	JobSaveCRMAccount = "integrations.save_crm_account"
)

// IntegrationJobKinds returns the kinds of the integrations library.
func IntegrationJobKinds() []string {
	return []string{
		JobFetchTaxCompany, JobReportInvoiceTaxes, JobSyncContact, JobSyncInvoice, JobSyncCreditNote, JobSyncPayment,
		JobCreateCRMInvoice, JobUpdateCRMInvoice, JobAssociateCRMInvoice,
		JobCreateCRMSubscription, JobUpdateCRMSubscription, JobAssociateCRMSubscription,
		JobFetchIntegrationItems, JobDeployCRM, JobSaveCRMAccount,
	}
}

// IntegrationCustomerArgs are the args of [JobSyncContact].
type IntegrationCustomerArgs struct {
	// IntegrationCustomerID is the customer's link to the integration.
	IntegrationCustomerID core.IntegrationCustomerID `json:"integration_customer_id"`
	// Update pushes the customer's changes to its existing record instead
	// of creating it.
	Update bool `json:"update,omitempty"`
}

// SyncInvoiceArgs are the args of [JobSyncInvoice].
type SyncInvoiceArgs struct {
	// InvoiceID is the invoice.
	InvoiceID core.InvoiceID `json:"invoice_id"`
	// FindFirst looks the invoice up at the provider before creating it:
	// a manual resync suspects an earlier attempt landed unrecorded (lago
	// find_first).
	FindFirst bool `json:"find_first,omitempty"`
}

// EnqueueReportInvoiceTaxes enqueues, in the transaction of r, the report
// of the taxes of the finalized invoice id of the customer cust to its tax
// provider; a customer without a tax link has none (lago
// subscriptions/activation_rules/payment/resolve_service.rb:46 "if
// invoice.customer.tax_customer", unique per invoice:
// Taxes::Invoices::CreateJob unique :until_executed).
func EnqueueReportInvoiceTaxes(ctx context.Context, r port.Repos, org core.OrganizationID, cust core.CustomerID, id core.InvoiceID) error {
	links, err := r.IntegrationCustomers.ListByCustomer(ctx, org, cust, integration.CategoryTax)
	if err != nil {
		return fmt.Errorf("list tax links of customer %s: %w", cust, err)
	}
	if len(links) == 0 {
		return nil
	}
	return Enqueue(ctx, r, org, JobReportInvoiceTaxes, InvoiceArgs{InvoiceID: id}, "invoice:"+id.String()+":report_taxes", time.Time{})
}

// EnqueueContactSync enqueues, in the transaction of r, the provider side
// of the integration customer ic of c just created or updated through
// integ (lago IntegrationCustomers::CreateOrUpdateBatchService and
// UpdateConnectionService):
//
//   - a created link without external ID that syncs with the provider has
//     the customer's record created at the provider (CreateJob); Anrok and
//     Salesforce create theirs with the first document;
//   - an updated link with an external ID has the record updated
//     (UpdateJob), except Anrok and Salesforce (update_service.rb:17-18).
//
// A partner customer is skipped.
func EnqueueContactSync(ctx context.Context, r port.Repos, c *customer.Customer, integ *integration.Integration, ic *integration.IntegrationCustomer, created bool) error {
	if c.SelfBilled() || integ.Provider == integration.ProviderAnrok || integ.Provider == integration.ProviderSalesforce {
		return nil
	}
	key := "integration_customer:" + ic.ID.String()
	switch {
	case created && ic.ExternalCustomerID == "" && ic.SyncWithProvider:
		// integration_customers/create_job.rb: unique per integration and
		// customer.
		return Enqueue(ctx, r, c.OrganizationID, JobSyncContact, IntegrationCustomerArgs{IntegrationCustomerID: ic.ID}, key+":create", time.Time{})
	case !created && ic.ExternalCustomerID != "":
		return Enqueue(ctx, r, c.OrganizationID, JobSyncContact, IntegrationCustomerArgs{IntegrationCustomerID: ic.ID, Update: true}, "", time.Time{})
	}
	return nil
}
