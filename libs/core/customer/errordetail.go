package customer

import (
	"context"
	"encoding/json"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// ErrorCode is the kind of an error recorded on a customer.
type ErrorCode string

// ErrorCode values.
const (
	// ErrorCodeTaxError is "tax_error": the tax provider could not
	// resolve the customer's address while the ongoing balances of the
	// customer's wallets were refreshed. The refresh of the customer stops
	// until its address changes.
	ErrorCodeTaxError ErrorCode = "tax_error"
)

// ErrorDetail is an error recorded on a customer (lago ErrorDetail owned
// by a Customer, app/jobs/customers/refresh_wallet_job.rb:53-64): its
// details hold the provider's error under "tax_error" and the failure
// under "error".
type ErrorDetail struct {
	// ID identifies the error detail.
	ID core.ErrorDetailID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// CustomerID is the customer the error is about.
	CustomerID core.CustomerID
	// Code is the kind of error.
	Code ErrorCode
	// Details is the provider payload.
	Details json.RawMessage
	// CreatedAt is the creation instant.
	CreatedAt time.Time
}

// ErrorDetailRepository stores the error details of customers; they are
// deleted with their customer.
type ErrorDetailRepository interface {
	// Create inserts e; it fails with [core.ErrNotFound] when e's customer
	// does not exist in its organization.
	Create(ctx context.Context, e *ErrorDetail) error
	// ListByCustomers returns the error details of the customers ids, in
	// ID order.
	ListByCustomers(ctx context.Context, org core.OrganizationID, ids []core.CustomerID) ([]*ErrorDetail, error)
	// DeleteByCustomer deletes the error details of customer id with code.
	DeleteByCustomer(ctx context.Context, org core.OrganizationID, id core.CustomerID, code ErrorCode) error
}
