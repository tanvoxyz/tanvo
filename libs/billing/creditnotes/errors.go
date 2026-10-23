package creditnotes

import (
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Rule codes of the refused operations, carried by *core.RuleError.
const (
	// CodeInvalidTypeOrStatus: the invoice cannot receive a credit note (a
	// credit invoice without an active wallet, or not paid).
	CodeInvalidTypeOrStatus = "invalid_type_or_status"
	// CodeNotDeletable: only draft credit notes can be deleted.
	CodeNotDeletable = "not_deletable"
	// CodeNoVoidableAmount: the credit note has no balance to void.
	CodeNoVoidableAmount = "no_voidable_amount"
	// CodeCreditNoteVoided: a voided credit note cannot be recredited.
	CodeCreditNoteVoided = "credit_note_voided"
	// CodeInvoiceAppliedTaxNotFound: a fee tax matches no tax of its
	// invoice.
	CodeInvoiceAppliedTaxNotFound = "invoice_applied_tax_not_found"
	// CodeCreditableAmountIsLessThanRequested: a progressive billing
	// invoice has less left to credit than requested.
	CodeCreditableAmountIsLessThanRequested = "creditable_amount_is_less_than_requested"
	// CodeNotProgressiveBilling: the invoice is not a progressive billing
	// invoice.
	CodeNotProgressiveBilling = "not_progressive_billing_invoice"
	// CodeRefundFailed: the payment provider failed the refund.
	CodeRefundFailed = "refund_failed"
	// CodeUpgradeRefundUnsupported: an upgrade cannot refund or offset the
	// unused amount.
	CodeUpgradeRefundUnsupported = "upgrade_refund_not_supported"
)

// Field error codes the credit note checks add to those of
// libs/core/creditnote.
const (
	// CodeCannotApplyToPaidInvoice: an offset on a credit invoice that was
	// partly paid.
	CodeCannotApplyToPaidInvoice = "cannot_apply_to_paid_invoice"
	// CodeNotEqualToTotalAmount: an offset on a credit invoice must settle
	// its whole total.
	CodeNotEqualToTotalAmount = "not_equal_to_total_amount"
	// CodeTotalAmountExceedsInvoiceAmount: the credit and refund asked
	// when voiding an invoice exceed what it may credit or refund (field
	// credit_refund_amount).
	CodeTotalAmountExceedsInvoiceAmount = "total_amount_exceeds_invoice_amount"
)

// ErrRefund wraps a failure of the payment provider to refund a credit
// note.
var ErrRefund = errors.New("creditnotes: refund failed")

// ErrRefundRejected is wrapped by a [kernel.RefundGateway] error when the
// provider refuses the refund for good (lago Stripe::InvalidRequestError):
// the refund then fails. Any other gateway error is transient and leaves
// the refund pending for the job to retry.
var ErrRefundRejected = errors.New("creditnotes: refund rejected")

func notFound(resource string, id fmt.Stringer) error {
	return fmt.Errorf("%s %s: %w", resource, id, core.ErrNotFound)
}

func rule(code, message string) error { return core.NewRuleError(code, message) }
