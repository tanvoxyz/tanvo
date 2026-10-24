package integration

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// CRMObject is a kind of record a CRM adapter deploys its schema for.
type CRMObject string

// CRMObject values (lago Integrations::Hubspot::*::DeployObjectService and
// DeployPropertiesService).
const (
	// CRMObjectInvoices is the custom invoice object and its properties.
	CRMObjectInvoices CRMObject = "invoices"
	// CRMObjectSubscriptions is the custom subscription object and its
	// properties.
	CRMObjectSubscriptions CRMObject = "subscriptions"
	// CRMObjectCompanies is the properties of companies.
	CRMObjectCompanies CRMObject = "companies"
	// CRMObjectContacts is the properties of contacts.
	CRMObjectContacts CRMObject = "contacts"
)

// CRMSchemaPart is a part of a CRM object's schema; [CRMGateway.Deploy]
// deploys the parts it is given. Lago deploys them separately: the
// connection job deploys the custom objects and the company and contact
// properties, a record sync deploys the properties of its object first,
// and an association the object first
// (aggregator/sync_custom_objects_and_properties_job.rb:11-14,
// aggregator/invoices/hubspot/create_service.rb:16,
// create_customer_association_service.rb:15).
type CRMSchemaPart uint8

// CRMSchemaPart values.
const (
	// CRMSchemaObject is the custom object (invoices and subscriptions;
	// nothing for companies and contacts).
	CRMSchemaObject CRMSchemaPart = 1 << iota
	// CRMSchemaProperties is the object's extra properties.
	CRMSchemaProperties
)

// CRMGateway is the port of CRM providers that receive records (HubSpot).
// Salesforce pulls from Tanvo and implements only [Gateway].
type CRMGateway interface {
	Gateway
	ContactGateway
	// Deploy creates or upgrades the parts of object's schema at the
	// provider whose recorded version on the connection is older than the
	// adapter's, and returns what to store (object type IDs, versions); an
	// empty update when nothing changed.
	Deploy(ctx context.Context, conn Connection, object CRMObject, parts CRMSchemaPart) (ConnectionUpdate, error)
	// AccountID returns the provider account of the connection (the
	// HubSpot portal ID).
	AccountID(ctx context.Context, conn Connection) (string, error)
	// SyncInvoice creates the invoice record, or updates inv.RecordID, and
	// returns the record's ID.
	SyncInvoice(ctx context.Context, conn Connection, inv *CRMInvoice) (string, error)
	// SyncSubscription creates the subscription record, or updates
	// sub.RecordID, and returns the record's ID.
	SyncSubscription(ctx context.Context, conn Connection, sub *CRMSubscription) (string, error)
	// Associate links a record to the customer's company or contact.
	Associate(ctx context.Context, conn Connection, a CRMAssociation) error
}

// CRMAssociation links a synced record to the customer's record.
type CRMAssociation struct {
	// Object is the kind of the synced record.
	Object CRMObject
	// RecordID is the synced record.
	RecordID string
	// TargetedObject is the customer's record type (companies or
	// contacts).
	TargetedObject string
	// CustomerRecordID is the customer's record.
	CustomerRecordID string
}

// CRMInvoice is the neutral document of an invoice synced to a CRM.
type CRMInvoice struct {
	// RecordID is the CRM record to update; empty to create one.
	RecordID string
	// ID is the invoice.
	ID core.InvoiceID
	// Number is the invoice number.
	Number string
	// PurchaseOrderNumber is the customer's purchase order number.
	PurchaseOrderNumber string
	// IssuingDate is the issuing date.
	IssuingDate core.Date
	// PaymentDueDate is the payment due date.
	PaymentDueDate core.Date
	// PaymentOverdue reports an overdue invoice.
	PaymentOverdue bool
	// Type is the invoice type.
	Type string
	// Status is the invoice status.
	Status string
	// PaymentStatus is the payment status.
	PaymentStatus string
	// Currency is the invoice currency.
	Currency money.Currency
	// TotalAmountCents is the total.
	TotalAmountCents int64
	// TotalDueAmountCents is the amount still due.
	TotalDueAmountCents int64
	// SubTotalExcludingTaxesAmountCents is the subtotal before taxes.
	SubTotalExcludingTaxesAmountCents int64
	// FileURL is the PDF's URL; a sync without one fails (lago
	// invoice.file_url missing).
	FileURL string
	// URL is the invoice's page in the Tanvo front end.
	URL string
}

// CRMSubscription is the neutral document of a subscription synced to a
// CRM.
type CRMSubscription struct {
	// RecordID is the CRM record to update; empty to create one.
	RecordID string
	// ID is the subscription.
	ID core.SubscriptionID
	// ExternalID is the subscription's external ID.
	ExternalID string
	// BillingTime is calendar or anniversary.
	BillingTime string
	// Name is the subscription name.
	Name string
	// PlanCode is the plan's code.
	PlanCode string
	// Status is the subscription status.
	Status string
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// StartedAt is the start; zero when not started.
	StartedAt time.Time
	// EndingAt is the planned end; zero without one.
	EndingAt time.Time
	// SubscriptionAt is the subscription date.
	SubscriptionAt time.Time
	// TerminatedAt is the termination; zero when not terminated.
	TerminatedAt time.Time
	// TrialEndedAt is the end of the trial; zero without one.
	TrialEndedAt time.Time
	// URL is the subscription's page in the Tanvo front end.
	URL string
}
