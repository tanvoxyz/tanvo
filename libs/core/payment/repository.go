package payment

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Filter narrows [Repository.List]; zero fields match everything.
type Filter struct {
	// CustomerID keeps the payments of one customer.
	CustomerID core.CustomerID
	// Status keeps the payments in one state.
	Status State
}

// Repository stores payments.
type Repository interface {
	// Create inserts p; it fails with [core.ErrConflict] when p is a
	// non-final provider payment and its payable already has one.
	Create(ctx context.Context, p *Payment) error
	// Update replaces p; it fails with [core.ErrNotFound] when p does not
	// exist in its organization and with [core.ErrConflict] like Create.
	Update(ctx context.Context, p *Payment) error
	// Get returns the payment id of org, or [core.ErrNotFound].
	Get(ctx context.Context, org core.OrganizationID, id core.PaymentID) (*Payment, error)
	// GetByProviderPaymentID returns the payment of a connection with the
	// provider's payment identifier, or [core.ErrNotFound].
	GetByProviderPaymentID(ctx context.Context, org core.OrganizationID, integration core.IntegrationID, providerPaymentID string) (*Payment, error)
	// ListByPayable returns the payments of a payable, oldest first.
	ListByPayable(ctx context.Context, org core.OrganizationID, payable Payable) ([]*Payment, error)
	// List returns a page of the payments of org matching f, oldest first.
	List(ctx context.Context, org core.OrganizationID, f Filter, page core.PageRequest) (core.Page[*Payment], error)
}

// ReceiptRepository stores payment receipts.
type ReceiptRepository interface {
	// Create inserts r; it fails with [core.ErrConflict] when the payment
	// already has a receipt or the number is taken.
	Create(ctx context.Context, r *Receipt) error
	// Update replaces r; it fails with [core.ErrNotFound] when r does not
	// exist in its organization, and with [core.ErrConflict] as Create
	// does.
	Update(ctx context.Context, r *Receipt) error
	// Get returns the receipt id of org, or [core.ErrNotFound].
	Get(ctx context.Context, org core.OrganizationID, id core.PaymentReceiptID) (*Receipt, error)
	// GetByPayment returns the receipt of a payment, or [core.ErrNotFound].
	GetByPayment(ctx context.Context, org core.OrganizationID, payment core.PaymentID) (*Receipt, error)
	// List returns a page of the receipts of org, oldest first.
	List(ctx context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*Receipt], error)
}

// RefundRepository stores refunds.
type RefundRepository interface {
	// Create inserts r.
	Create(ctx context.Context, r *Refund) error
	// Update replaces r; it fails with [core.ErrNotFound] when r does not
	// exist in its organization.
	Update(ctx context.Context, r *Refund) error
	// Get returns the refund id of org, or [core.ErrNotFound].
	Get(ctx context.Context, org core.OrganizationID, id core.RefundID) (*Refund, error)
	// GetByProviderRefundID returns the refund of a connection with the
	// provider's refund identifier, or [core.ErrNotFound].
	GetByProviderRefundID(ctx context.Context, org core.OrganizationID, integration core.IntegrationID, providerRefundID string) (*Refund, error)
	// ListByCreditNote returns the refunds of a credit note, oldest first.
	ListByCreditNote(ctx context.Context, org core.OrganizationID, creditNote core.CreditNoteID) ([]*Refund, error)
	// ListByPayment returns the refunds of a payment, oldest first.
	ListByPayment(ctx context.Context, org core.OrganizationID, payment core.PaymentID) ([]*Refund, error)
}

// RequestFilter narrows [RequestRepository.List]; zero fields match
// everything.
type RequestFilter struct {
	// CustomerID keeps the requests of one customer.
	CustomerID core.CustomerID
	// PaymentStatus keeps the requests in one payment status.
	PaymentStatus core.PaymentStatus
}

// RequestRepository stores payment requests.
type RequestRepository interface {
	// Create inserts r.
	Create(ctx context.Context, r *Request) error
	// Update replaces r; it fails with [core.ErrNotFound] when r does not
	// exist in its organization.
	Update(ctx context.Context, r *Request) error
	// Get returns the request id of org, or [core.ErrNotFound].
	Get(ctx context.Context, org core.OrganizationID, id core.PaymentRequestID) (*Request, error)
	// List returns a page of the requests of org matching f, oldest first.
	List(ctx context.Context, org core.OrganizationID, f RequestFilter, page core.PageRequest) (core.Page[*Request], error)
	// ListByInvoice returns the requests including an invoice, oldest first.
	ListByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) ([]*Request, error)
}

// MethodRepository stores payment methods.
type MethodRepository interface {
	// Create inserts m; it fails with [core.ErrConflict] when m is the
	// default and the customer already has a live default.
	Create(ctx context.Context, m *Method) error
	// Update replaces m; it fails with [core.ErrNotFound] when m does not
	// exist in its organization and with [core.ErrConflict] like Create.
	Update(ctx context.Context, m *Method) error
	// Get returns the method id of org, deleted or not, or
	// [core.ErrNotFound].
	Get(ctx context.Context, org core.OrganizationID, id core.PaymentMethodID) (*Method, error)
	// GetDefault returns the customer's live default method, or
	// [core.ErrNotFound].
	GetDefault(ctx context.Context, org core.OrganizationID, customer core.CustomerID) (*Method, error)
	// GetByProviderMethodID returns the live method of a connection with the
	// provider's identifier, or [core.ErrNotFound].
	GetByProviderMethodID(ctx context.Context, org core.OrganizationID, integration core.IntegrationID, providerMethodID string) (*Method, error)
	// ListByCustomer returns the customer's live methods, oldest first.
	ListByCustomer(ctx context.Context, org core.OrganizationID, customer core.CustomerID) ([]*Method, error)
}

// ProviderCustomerRepository stores customers' provider connections.
type ProviderCustomerRepository interface {
	// Create inserts c; it fails with [core.ErrConflict] when the customer
	// already has a live row for the connection.
	Create(ctx context.Context, c *ProviderCustomer) error
	// Update replaces c; it fails with [core.ErrNotFound] when c does not
	// exist in its organization and with [core.ErrConflict] like Create.
	Update(ctx context.Context, c *ProviderCustomer) error
	// Get returns the provider customer id of org, or [core.ErrNotFound].
	Get(ctx context.Context, org core.OrganizationID, id core.PaymentProviderCustomerID) (*ProviderCustomer, error)
	// GetByCustomer returns the customer's live row for a connection, or
	// [core.ErrNotFound].
	GetByCustomer(ctx context.Context, org core.OrganizationID, customer core.CustomerID, integration core.IntegrationID) (*ProviderCustomer, error)
	// GetByProviderCustomerID returns the live row of a connection with the
	// provider's customer identifier, or [core.ErrNotFound].
	GetByProviderCustomerID(ctx context.Context, org core.OrganizationID, integration core.IntegrationID, providerCustomerID string) (*ProviderCustomer, error)
	// ListByCustomer returns the customer's live rows, oldest first.
	ListByCustomer(ctx context.Context, org core.OrganizationID, customer core.CustomerID) ([]*ProviderCustomer, error)
	// ListByIntegration returns the live rows of a connection, oldest first.
	ListByIntegration(ctx context.Context, org core.OrganizationID, integration core.IntegrationID) ([]*ProviderCustomer, error)
}

// IntentRepository stores hosted checkout links.
type IntentRepository interface {
	// Create inserts i; it fails with [core.ErrConflict] when i is active and
	// its invoice already has an active link.
	Create(ctx context.Context, i *Intent) error
	// Update replaces i; it fails with [core.ErrNotFound] when i does not
	// exist in its organization and with [core.ErrConflict] like Create.
	Update(ctx context.Context, i *Intent) error
	// GetActiveByInvoice returns the invoice's active link, or
	// [core.ErrNotFound].
	GetActiveByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) (*Intent, error)
	// ListAwaitingExpiration returns the active links of org past their
	// validity at now.
	ListAwaitingExpiration(ctx context.Context, org core.OrganizationID, now time.Time) ([]*Intent, error)
}

// InboundWebhookRepository stores inbound provider webhooks.
type InboundWebhookRepository interface {
	// Create inserts w.
	Create(ctx context.Context, w *InboundWebhook) error
	// Update replaces w; it fails with [core.ErrNotFound] when w does not
	// exist in its organization.
	Update(ctx context.Context, w *InboundWebhook) error
	// Get returns the webhook id of org, or [core.ErrNotFound].
	Get(ctx context.Context, org core.OrganizationID, id core.InboundWebhookID) (*InboundWebhook, error)
	// ListRetriable returns the webhooks of org due for processing at now
	// ([InboundWebhook.Retriable]), oldest first.
	ListRetriable(ctx context.Context, org core.OrganizationID, now time.Time) ([]*InboundWebhook, error)
	// DeleteExpired deletes the webhooks of org past their retention at now
	// and returns how many.
	DeleteExpired(ctx context.Context, org core.OrganizationID, now time.Time) (int, error)
}
