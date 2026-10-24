package integrations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// codeFileMissing is the failure of an invoice synced to HubSpot before its
// PDF exists (lago payloads/hubspot.rb:9).
const codeFileMissing = "invoice.file_url missing"

// crmEnv is the customer's HubSpot link (lago
// customer.integration_customers.hubspot_kind.first).
type crmEnv struct {
	c    *customer.Customer
	ic   *integration.IntegrationCustomer
	i    *integration.Integration
	conn integration.Connection
	slug string
}

// hubspotLink returns the first HubSpot link of c; nil when it has none.
func hubspotLink(ctx context.Context, r port.Repos, c *customer.Customer) (*integration.IntegrationCustomer, *integration.Integration, error) {
	ics, err := r.IntegrationCustomers.ListByCustomer(ctx, c.OrganizationID, c.ID, integration.CategoryCRM)
	if err != nil {
		return nil, nil, fmt.Errorf("load CRM links of customer %s: %w", c.ID, err)
	}
	for _, ic := range ics {
		i, err := r.Integrations.Get(ctx, c.OrganizationID, ic.IntegrationID)
		if err != nil {
			return nil, nil, fmt.Errorf("load integration %s: %w", ic.IntegrationID, err)
		}
		if i.Provider == integration.ProviderHubspot {
			return ic, i, nil
		}
	}
	return nil, nil, nil
}

func (s *Service) loadCRM(ctx context.Context, r port.Repos, c *customer.Customer) (*crmEnv, error) {
	ic, i, err := hubspotLink(ctx, r, c)
	if err != nil || ic == nil {
		return nil, err
	}
	i, conn, err := s.loadConnection(ctx, r, c.OrganizationID, i.ID)
	if err != nil {
		return nil, err
	}
	o, err := r.Organizations.Get(ctx, c.OrganizationID)
	if err != nil {
		return nil, fmt.Errorf("load organization %s: %w", c.OrganizationID, err)
	}
	return &crmEnv{c: c, ic: ic, i: i, conn: conn, slug: o.Slug}, nil
}

// syncedID returns the provider's record of a synced object; empty when it
// is not synced.
func syncedID(ctx context.Context, r port.Repos, i *integration.Integration, rt integration.ResourceType, id [16]byte) (string, error) {
	res, err := r.IntegrationResources.Find(ctx, i.OrganizationID, i.ID, rt, id)
	switch {
	case errors.Is(err, core.ErrNotFound) || err == nil && res == nil:
		return "", nil
	case err != nil:
		return "", fmt.Errorf("find synced %s %x: %w", rt, id, err)
	}
	return res.ExternalID, nil
}

// crmFailure handles the failure of a CRM record sync (lago
// aggregator/{invoices,subscriptions}/hubspot/*_service.rb rescues): a
// request limit is retried; any other provider failure emits
// customer.crm_provider_error and, for an association, is retried (raise
// e), else succeeds.
func (s *Service) crmFailure(ctx context.Context, e *crmEnv, association bool, err error) error {
	switch {
	case errors.Is(err, integration.ErrRequestLimit), errors.Is(err, context.Canceled), errors.Is(err, ErrSyncPayload):
		return err
	}
	if _, ok := integration.AsProviderError(err); !ok {
		return err
	}
	if err := s.emitCustomerError(ctx, e.c, e.i, errorDetails(err)); err != nil {
		return err
	}
	if association {
		return fmt.Errorf("%w: %w", ErrSyncRetryable, err)
	}
	return nil
}

// reloadCRM deploys parts of object o of e's integration and reloads its
// connection: a record sync deploys the properties first
// (aggregator/{invoices,subscriptions}/hubspot/{create,update}_service.rb:
// DeployPropertiesService), an association the object
// (create_customer_association_service.rb:15: DeployObjectService).
func (s *Service) reloadCRM(ctx context.Context, e *crmEnv, o integration.CRMObject, parts integration.CRMSchemaPart) error {
	if err := s.deploy(ctx, e.i.OrganizationID, e.i.ID, o, parts); err != nil {
		return err
	}
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		e.i, e.conn, err = s.loadConnection(ctx, r, e.i.OrganizationID, e.i.ID)
		return err
	})
}

// crmInvoiceJob creates, updates or associates the HubSpot record of the
// invoice id (lago Integrations::Aggregator::Invoices::Hubspot::CreateJob,
// UpdateJob and CreateCustomerAssociationJob). A created record enqueues
// its association with the customer's record.
func (s *Service) crmInvoiceJob(ctx context.Context, org core.OrganizationID, kind string, id core.InvoiceID) error {
	var (
		e      *crmEnv
		inv    *invoice.Invoice
		record string
	)
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if inv, err = r.Invoices.Get(ctx, org, id); err != nil {
			return fmt.Errorf("load invoice %s: %w", id, err)
		}
		c, err := r.Customers.Get(ctx, org, inv.CustomerID)
		if err != nil {
			return fmt.Errorf("load customer %s: %w", inv.CustomerID, err)
		}
		if e, err = s.loadCRM(ctx, r, c); err != nil || e == nil {
			return err
		}
		if !e.i.SyncInvoices {
			e = nil
			return nil
		}
		record, err = syncedID(ctx, r, e.i, integration.ResourceTypeInvoice, inv.ID.UUID())
		return err
	})
	if err != nil || e == nil {
		return err
	}
	switch kind {
	case kernel.JobCreateCRMInvoice:
		if inv.Status != invoice.StatusFinalized {
			return nil
		}
		if record != "" {
			return s.enqueueAssociation(ctx, org, kernel.JobAssociateCRMInvoice, kernel.InvoiceArgs{InvoiceID: id})
		}
	case kernel.JobAssociateCRMInvoice:
		if record == "" {
			return nil
		}
		return s.associate(ctx, e, integration.CRMObjectInvoices, record)
	default:
		if record == "" {
			return nil
		}
	}
	if err := s.reloadCRM(ctx, e, integration.CRMObjectInvoices, integration.CRMSchemaProperties); err != nil {
		return err
	}
	doc, err := s.crmInvoice(ctx, e, inv, record)
	if err != nil {
		return err
	}
	g, err := s.crm(e.i)
	if err != nil {
		return err
	}
	ext, err := g.SyncInvoice(ctx, e.conn, doc)
	if err != nil {
		return s.crmFailure(ctx, e, false, err)
	}
	if record != "" || ext == "" {
		return nil
	}
	if err := s.recorded(ctx, e.i, integration.ResourceTypeInvoice, inv.ID.UUID(), ext); err != nil {
		return err
	}
	return s.enqueueAssociation(ctx, org, kernel.JobAssociateCRMInvoice, kernel.InvoiceArgs{InvoiceID: id})
}

func (s *Service) enqueueAssociation(ctx context.Context, org core.OrganizationID, kind string, args any) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		return kernel.Enqueue(ctx, r, org, kind, args, "", time.Time{})
	})
}

// associate links the record of object o to the customer's record (lago
// CreateCustomerAssociationService).
func (s *Service) associate(ctx context.Context, e *crmEnv, o integration.CRMObject, record string) error {
	if err := s.reloadCRM(ctx, e, o, integration.CRMSchemaObject); err != nil {
		return err
	}
	g, err := s.crm(e.i)
	if err != nil {
		return err
	}
	to, _ := e.ic.Setting(icTargetedObject)
	err = g.Associate(ctx, e.conn, integration.CRMAssociation{
		Object: o, RecordID: record, TargetedObject: to, CustomerRecordID: e.ic.ExternalCustomerID,
	})
	if err != nil {
		return s.crmFailure(ctx, e, true, err)
	}
	return nil
}

// crmInvoice returns the neutral CRM document of inv; an invoice without a
// PDF yet fails with [ErrSyncPayload], retried.
func (s *Service) crmInvoice(ctx context.Context, e *crmEnv, inv *invoice.Invoice, record string) (*integration.CRMInvoice, error) {
	if inv.File.IsZero() || s.blobs == nil {
		return nil, fmt.Errorf("invoice %s: %s: %w", inv.ID, codeFileMissing, ErrSyncPayload)
	}
	file, err := s.blobs.URL(ctx, inv.OrganizationID, inv.File)
	if err != nil {
		return nil, fmt.Errorf("invoice %s: file URL: %w", inv.ID, err)
	}
	return &integration.CRMInvoice{
		RecordID: record, ID: inv.ID, Number: inv.Number, PurchaseOrderNumber: deref(inv.PurchaseOrderNumber),
		IssuingDate: inv.IssuingDate, PaymentDueDate: inv.PaymentDueDate, PaymentOverdue: inv.PaymentOverdue,
		Type: string(inv.Type), Status: string(inv.Status), PaymentStatus: string(inv.PaymentStatus), Currency: inv.Currency,
		TotalAmountCents: inv.TotalAmount, TotalDueAmountCents: inv.TotalDueAmount(),
		SubTotalExcludingTaxesAmountCents: inv.SubTotalExcludingTaxesAmount,
		FileURL:                           file, URL: s.invoiceURL(e.slug, inv),
	}, nil
}

// crmSubscriptionJob creates, updates or associates the HubSpot record of
// the subscription id (lago Integrations::Aggregator::Subscriptions::
// Hubspot::CreateJob, UpdateJob and CreateCustomerAssociationJob).
func (s *Service) crmSubscriptionJob(ctx context.Context, org core.OrganizationID, kind string, id core.SubscriptionID) error {
	var (
		e      *crmEnv
		sub    *subscription.Subscription
		plan   string
		record string
	)
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if sub, err = r.Subscriptions.Get(ctx, org, id); err != nil {
			return fmt.Errorf("load subscription %s: %w", id, err)
		}
		c, err := r.Customers.Get(ctx, org, sub.CustomerID)
		if err != nil {
			return fmt.Errorf("load customer %s: %w", sub.CustomerID, err)
		}
		if e, err = s.loadCRM(ctx, r, c); err != nil || e == nil {
			return err
		}
		if !e.i.SyncSubscriptions {
			e = nil
			return nil
		}
		p, err := r.Plans.Get(ctx, org, sub.PlanID)
		if err != nil {
			return fmt.Errorf("load plan %s: %w", sub.PlanID, err)
		}
		plan = p.Code
		record, err = syncedID(ctx, r, e.i, integration.ResourceTypeSubscription, sub.ID.UUID())
		return err
	})
	if err != nil || e == nil {
		return err
	}
	switch kind {
	case kernel.JobCreateCRMSubscription:
		if record != "" {
			return s.enqueueAssociation(ctx, org, kernel.JobAssociateCRMSubscription, kernel.SubscriptionArgs{SubscriptionID: id})
		}
	case kernel.JobAssociateCRMSubscription:
		if record == "" {
			return nil
		}
		return s.associate(ctx, e, integration.CRMObjectSubscriptions, record)
	default:
		if record == "" {
			return nil
		}
	}
	if err := s.reloadCRM(ctx, e, integration.CRMObjectSubscriptions, integration.CRMSchemaProperties); err != nil {
		return err
	}
	g, err := s.crm(e.i)
	if err != nil {
		return err
	}
	ext, err := g.SyncSubscription(ctx, e.conn, &integration.CRMSubscription{
		RecordID: record, ID: sub.ID, ExternalID: sub.ExternalID, BillingTime: sub.BillingTime.String(),
		Name: deref(sub.Name), PlanCode: plan, Status: string(sub.Status), CreatedAt: sub.CreatedAt,
		StartedAt: at(sub.StartedAt), EndingAt: at(sub.EndingAt), SubscriptionAt: sub.SubscriptionAt,
		TerminatedAt: at(sub.TerminatedAt), TrialEndedAt: at(sub.TrialEndedAt),
		URL: s.link(e.slug, "customer", sub.CustomerID.String(), "subscription", sub.ID.String(), "overview"),
	})
	if err != nil {
		return s.crmFailure(ctx, e, false, err)
	}
	if record != "" || ext == "" {
		return nil
	}
	if err := s.recorded(ctx, e.i, integration.ResourceTypeSubscription, sub.ID.UUID(), ext); err != nil {
		return err
	}
	return s.enqueueAssociation(ctx, org, kernel.JobAssociateCRMSubscription, kernel.SubscriptionArgs{SubscriptionID: id})
}

func at(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// SyncInvoice enqueues the sync of the invoice id to its customer's
// accounting provider, looking it up first (lago SyncIntegrationInvoice
// mutation: Invoices::CreateService.call_async(find_first: true)).
func (s *Service) SyncInvoice(ctx context.Context, org core.OrganizationID, id core.InvoiceID) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if _, err := r.Invoices.Get(ctx, org, id); err != nil {
			return fmt.Errorf("load invoice %s: %w", id, err)
		}
		return kernel.Enqueue(ctx, r, org, kernel.JobSyncInvoice, kernel.SyncInvoiceArgs{InvoiceID: id, FindFirst: true}, "invoice:"+id.String()+":accounting", time.Time{})
	})
}

// SyncCreditNote enqueues the sync of the credit note id to its customer's
// accounting provider (lago SyncIntegrationCreditNote mutation).
func (s *Service) SyncCreditNote(ctx context.Context, org core.OrganizationID, id core.CreditNoteID) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if _, err := r.CreditNotes.Get(ctx, org, id); err != nil {
			return fmt.Errorf("load credit note %s: %w", id, err)
		}
		return kernel.Enqueue(ctx, r, org, kernel.JobSyncCreditNote, kernel.CreditNoteArgs{CreditNoteID: id}, "credit_note:"+id.String()+":accounting", time.Time{})
	})
}

// SyncCRMInvoice enqueues the creation of the HubSpot record of the
// invoice id (lago Hubspot SyncInvoice mutation); an invoice the
// mutation does not see is not found.
func (s *Service) SyncCRMInvoice(ctx context.Context, org core.OrganizationID, id core.InvoiceID) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if _, err := visibleInvoice(ctx, r, org, id); err != nil {
			return err
		}
		return kernel.Enqueue(ctx, r, org, kernel.JobCreateCRMInvoice, kernel.InvoiceArgs{InvoiceID: id}, "invoice:"+id.String()+":crm", time.Time{})
	})
}

// ResyncSalesforceInvoice emits invoice.resynced for the invoice id, which
// Salesforce listens to (lago Integrations::Salesforce::Invoices::
// SyncService); an invoice the Salesforce SyncInvoice mutation does not
// see is not found.
func (s *Service) ResyncSalesforceInvoice(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (*invoice.Invoice, error) {
	var inv *invoice.Invoice
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if inv, err = visibleInvoice(ctx, r, org, id); err != nil {
			return err
		}
		return kernel.Emit(ctx, s.d, org, webhook.EventTypeInvoiceResynced, "invoice", inv.ID.UUID(), inv)
	})
	return inv, err
}

// visibleInvoice loads the invoice id, not found unless its status is one
// the CRM sync mutations see: lago's Invoice.visible scope leaves out
// generating, open, closed and deleted invoices (app/models/invoice.rb:
// 104-105, 144; hubspot/sync_invoice.rb:19, salesforce/sync_invoice.rb:20).
func visibleInvoice(ctx context.Context, r port.Repos, org core.OrganizationID, id core.InvoiceID) (*invoice.Invoice, error) {
	inv, err := r.Invoices.Get(ctx, org, id)
	if err == nil {
		switch inv.Status {
		case invoice.StatusGenerating, invoice.StatusOpen, invoice.StatusClosed, invoice.StatusDeleted:
			err = core.ErrNotFound
		}
	}
	if err != nil {
		return nil, fmt.Errorf("load invoice %s: %w", id, err)
	}
	return inv, nil
}

// SetInvoiceSalesforceID records externalID as the Salesforce record of
// the invoice id for the Salesforce integration code (lago
// Invoices::SyncSalesforceIdService). An invoice already recorded keeps
// its record.
func (s *Service) SetInvoiceSalesforceID(ctx context.Context, org core.OrganizationID, id core.InvoiceID, code, externalID string) (*invoice.Invoice, error) {
	var inv *invoice.Invoice
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if inv, err = r.Invoices.Get(ctx, org, id); err != nil {
			return fmt.Errorf("load invoice %s: %w", id, err)
		}
		i, err := r.Integrations.GetByCode(ctx, org, code)
		if err == nil && i.Provider != integration.ProviderSalesforce {
			err = core.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("integration %q: %w", code, err)
		}
		return s.recordResource(ctx, r, i, integration.ResourceTypeInvoice, inv.ID.UUID(), externalID)
	})
	return inv, err
}
