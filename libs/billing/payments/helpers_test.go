package payments_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// now is the clock of the tests.
var now = billingtest.Date(2026, time.March, 10, 12)

// newHarness returns a harness and the service over it.
func newHarness(t *testing.T) (*billingtest.Harness, *payments.Service) {
	t.Helper()
	h := billingtest.New(t, now)
	return h, payments.New(h.Deps)
}

// finalized makes a stored invoice finalized with a total and a paid
// amount, ready for payment processing.
func finalized(total, paid int64) func(*invoice.Invoice) {
	return func(inv *invoice.Invoice) {
		inv.Status = invoice.StatusFinalized
		inv.TotalAmount, inv.TotalPaidAmount = total, paid
		inv.ReadyForPaymentProcessing = true
	}
}

// offset stores a finalized credit note offsetting amount of inv, with
// its settlement, as the credit note services leave it.
func offset(h *billingtest.Harness, inv *invoice.Invoice, amount int64) {
	h.T.Helper()
	cn := h.StoredCreditNote(inv, 0, func(cn *creditnote.CreditNote) {
		cn.OffsetAmount, cn.TotalAmount = amount, amount
	})
	inv.OffsettedAmount += amount
	h.SaveInvoice(inv)
	h.Do(func(ctx context.Context, r port.Repos) error {
		return r.InvoiceSettlements.Create(ctx, &invoice.Settlement{
			ID: core.NewID[core.InvoiceSettlementID](h.IDs), OrganizationID: h.OrgID(), InvoiceID: inv.ID,
			Type: invoice.SettlementTypeCreditNote, CreditNoteID: cn.ID, Amount: amount, Currency: inv.Currency,
			CreatedAt: h.Now(),
		})
	})
}

// storedMethod creates a payment method of the customer.
func storedMethod(h *billingtest.Harness, c *customer.Customer, isDefault bool) *payment.Method {
	h.T.Helper()
	id := core.NewID[core.PaymentMethodID](h.IDs)
	m := &payment.Method{
		ID: id, OrganizationID: h.OrgID(), CustomerID: c.ID,
		ProviderMethodID: "pm_" + id.String(), IsDefault: isDefault, CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	h.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentMethods.Create(ctx, m) })
	return m
}

// method reloads a payment method.
func method(h *billingtest.Harness, id core.PaymentMethodID) *payment.Method {
	h.T.Helper()
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) (*payment.Method, error) {
		return r.PaymentMethods.Get(ctx, h.OrgID(), id)
	})
}

// receiptOf returns the receipt of a payment, or nil.
func receiptOf(h *billingtest.Harness, id core.PaymentID) *payment.Receipt {
	h.T.Helper()
	var rc *payment.Receipt
	h.Do(func(ctx context.Context, r port.Repos) error {
		got, err := r.PaymentReceipts.GetByPayment(ctx, h.OrgID(), id)
		if errors.Is(err, core.ErrNotFound) {
			return nil
		}
		rc = got
		return err
	})
	return rc
}

// fieldCodes returns the "field:code" pairs of a validation error, sorted.
func fieldCodes(err error) []string {
	var out []string
	for _, e := range core.FieldErrors(err) {
		out = append(out, e.Field+":"+e.Code)
	}
	slices.Sort(out)
	return out
}

// wantFields fails unless err carries exactly the field errors want.
func wantFields(t *testing.T, err error, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := fieldCodes(err); !slices.Equal(got, want) {
		t.Fatalf("field errors: got = %v (%v), want = %v", got, err, want)
	}
}

// wantRule fails unless err is a rule error of code.
func wantRule(t *testing.T, err error, code string) {
	t.Helper()
	var re *core.RuleError
	if !errors.As(err, &re) || re.Code != code {
		t.Fatalf("error: got = %v, want = rule %s", err, code)
	}
}

// wantNotFound fails unless err is a not found error of resource.
func wantNotFound(t *testing.T, err error, resource string) {
	t.Helper()
	var nf *core.NotFoundError
	if !errors.As(err, &nf) || nf.Resource != resource {
		t.Fatalf("error: got = %v, want = %s_not_found", err, resource)
	}
}

// hasWebhook reports whether h emitted a webhook of type e.
func hasWebhook(h *billingtest.Harness, e webhook.EventType) bool {
	return slices.Contains(h.Webhooks(), e)
}
