package creditnote

import (
	"context"
	"encoding/json"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// ErrorCode is the kind of an error recorded on a credit note.
type ErrorCode string

// ErrorCode values.
const (
	// ErrorCodeTaxError is "tax_error": the tax provider refused the
	// credit note's report.
	ErrorCodeTaxError ErrorCode = "tax_error"
)

// ErrorDetail is an error recorded on a credit note (lago ErrorDetail
// owned by a CreditNote): a tax provider's refusal of its report, whose
// details hold the provider's code under "tax_error".
type ErrorDetail struct {
	// ID identifies the error detail.
	ID core.ErrorDetailID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// CreditNoteID is the credit note the error is about.
	CreditNoteID core.CreditNoteID
	// Code is the kind of error.
	Code ErrorCode
	// Details is the provider payload.
	Details json.RawMessage
	// CreatedAt is the creation instant.
	CreatedAt time.Time
}

// ErrorDetailRepository stores the error details of credit notes; they
// are deleted with their credit note.
type ErrorDetailRepository interface {
	// Create inserts e.
	Create(ctx context.Context, e *ErrorDetail) error
	// ListByCreditNotes returns the error details of the credit notes ids
	// in creation order.
	ListByCreditNotes(ctx context.Context, org core.OrganizationID, ids []core.CreditNoteID) ([]*ErrorDetail, error)
	// DeleteByCreditNote deletes the error details of one credit note
	// with code.
	DeleteByCreditNote(ctx context.Context, org core.OrganizationID, id core.CreditNoteID, code ErrorCode) error
}
