package billingtest

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// FeeTax returns a fee option applying the tax to the fee: the fee's taxes are
// its amount after coupons times the rate, rounded half up.
func (h *Harness) FeeTax(t *catalog.Tax) func(*invoice.Fee) {
	return func(f *invoice.Fee) {
		precise := f.SubTotalExcludingTaxesPreciseAmount().Mul(t.Rate).Div(money.NewFromInt(100))
		f.AppliedTaxes = append(f.AppliedTaxes, invoice.FeeAppliedTax{
			ID: core.NewID[core.FeeAppliedTaxID](h.IDs), TaxID: t.ID, TaxCode: t.Code, TaxName: t.Name,
			TaxRate: t.Rate, Amount: kernel.RoundCents(precise), PreciseAmount: precise,
		})
		f.TaxesRate = f.TaxesRate.Add(t.Rate)
		f.TaxesPreciseAmount = f.TaxesPreciseAmount.Add(precise)
		f.TaxesAmount = kernel.RoundCents(f.TaxesPreciseAmount)
	}
}

// Coupons is a fee option setting the fee's share of the invoice coupons.
func Coupons(precise string) func(*invoice.Fee) {
	return func(f *invoice.Fee) { f.PreciseCouponsAmount = money.MustParse(precise) }
}

// CreditFee is a fee option making it the credit fee paying the wallet
// transaction.
func CreditFee(tx core.WalletTransactionID) func(*invoice.Fee) {
	return func(f *invoice.Fee) { f.Type, f.WalletTransactionID = core.FeeTypeCredit, tx }
}

// InvoiceTaxes stores the invoice applied taxes of the taxes from its fees'
// applied taxes: each one's fees amount is Σ the taxed fees after coupons,
// its amount Σ their taxes.
func (h *Harness) InvoiceTaxes(inv *invoice.Invoice, fees []*invoice.Fee, taxes ...*catalog.Tax) []*invoice.AppliedTax {
	h.T.Helper()
	out := make([]*invoice.AppliedTax, 0, len(taxes))
	for _, t := range taxes {
		a := &invoice.AppliedTax{
			ID: core.NewID[core.InvoiceAppliedTaxID](h.IDs), OrganizationID: h.OrgID(), InvoiceID: inv.ID,
			TaxID: t.ID, TaxCode: t.Code, TaxName: t.Name, TaxRate: t.Rate, Currency: inv.Currency,
			PreciseAmount: money.Zero, CreatedAt: h.Now(),
		}
		base := money.Zero
		for _, f := range fees {
			for _, ft := range f.AppliedTaxes {
				if ft.TaxID == t.ID {
					base = base.Add(f.SubTotalExcludingTaxesPreciseAmount())
					a.PreciseAmount = a.PreciseAmount.Add(ft.PreciseAmount)
				}
			}
		}
		a.FeesAmount = kernel.RoundCents(base)
		a.Amount = kernel.RoundCents(a.PreciseAmount)
		out = append(out, a)
	}
	h.Do(func(ctx context.Context, r port.Repos) error {
		return r.InvoiceAppliedTaxes.ReplaceForInvoice(ctx, h.OrgID(), inv.ID, out)
	})
	return out
}

// CreditableInvoice creates a finalized USD subscription invoice of the
// customer whose fees have the amounts (minor units), each taxed by taxes,
// with its totals computed from them and paid with status succeeded.
// Options run on the invoice before its fees are created; fee options run
// on every fee.
func (h *Harness) CreditableInvoice(c *customer.Customer, amounts []int64, taxes []*catalog.Tax, opts []func(*invoice.Invoice), feeOpts ...func(*invoice.Fee)) (*invoice.Invoice, []*invoice.Fee) {
	h.T.Helper()
	inv := h.StoredInvoice(c, func(inv *invoice.Invoice) {
		inv.Status, inv.PaymentStatus = invoice.StatusFinalized, core.PaymentStatusSucceeded
	})
	apply(inv, opts)
	fees := make([]*invoice.Fee, 0, len(amounts))
	for _, a := range amounts {
		fo := append([]func(*invoice.Fee){}, feeOpts...)
		for _, t := range taxes {
			fo = append(fo, h.FeeTax(t))
		}
		fees = append(fees, h.StoredFee(inv, a, fo...))
	}
	h.InvoiceTaxes(inv, fees, taxes...)
	h.Totals(inv, fees)
	return inv, fees
}

// Totals computes the totals of the invoice from its fees, keeping a set
// TotalPaidAmount unless the invoice was paid in full, and stores it.
func (h *Harness) Totals(inv *invoice.Invoice, fees []*invoice.Fee) {
	h.T.Helper()
	coupons, taxes := money.Zero, money.Zero
	var amount int64
	for _, f := range fees {
		amount += f.Amount
		coupons = coupons.Add(f.PreciseCouponsAmount)
		taxes = taxes.Add(f.TaxesPreciseAmount)
	}
	inv.FeesAmount = amount
	inv.CouponsAmount = kernel.RoundCents(coupons)
	inv.SubTotalExcludingTaxesAmount = amount - inv.CouponsAmount
	inv.TaxesAmount = kernel.RoundCents(taxes)
	inv.SubTotalIncludingTaxesAmount = inv.SubTotalExcludingTaxesAmount + inv.TaxesAmount
	inv.TotalAmount = inv.SubTotalIncludingTaxesAmount
	if inv.PaymentStatus == core.PaymentStatusSucceeded && inv.TotalPaidAmount == 0 {
		inv.TotalPaidAmount = inv.TotalAmount
	}
	h.SaveInvoice(inv)
}

// StoredPayment creates a payment of the payable with the state, through
// the integration.
func (h *Harness) StoredPayment(p payment.Payable, cust core.CustomerID, amount int64, state payment.State, integration core.IntegrationID) *payment.Payment {
	h.T.Helper()
	pay := &payment.Payment{
		ID: core.NewID[core.PaymentID](h.IDs), OrganizationID: h.OrgID(), Payable: p, CustomerID: cust,
		Kind: payment.KindProvider, Status: state, Amount: amount, Currency: USD, IntegrationID: integration,
		CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	h.Do(func(ctx context.Context, r port.Repos) error { return r.Payments.Create(ctx, pay) })
	return pay
}

// StoredPaymentRequest creates a payment request of the invoices with the
// payment status.
func (h *Harness) StoredPaymentRequest(cust core.CustomerID, status core.PaymentStatus, invs ...*invoice.Invoice) *payment.Request {
	h.T.Helper()
	req := &payment.Request{
		ID: core.NewID[core.PaymentRequestID](h.IDs), OrganizationID: h.OrgID(), CustomerID: cust,
		Currency: USD, PaymentStatus: status, CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	for _, inv := range invs {
		req.InvoiceIDs = append(req.InvoiceIDs, inv.ID)
		req.Amount += inv.TotalAmount
	}
	h.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentRequests.Create(ctx, req) })
	return req
}

// CreditNoteRefunds returns the refunds of a credit note.
func (h *Harness) CreditNoteRefunds(id core.CreditNoteID) []*payment.Refund {
	h.T.Helper()
	return Read(h, func(ctx context.Context, r port.Repos) ([]*payment.Refund, error) {
		return r.Refunds.ListByCreditNote(ctx, h.OrgID(), id)
	})
}

// Settlements returns the settlements of an invoice.
func (h *Harness) Settlements(id core.InvoiceID) []*invoice.Settlement {
	h.T.Helper()
	return Read(h, func(ctx context.Context, r port.Repos) ([]*invoice.Settlement, error) {
		return r.InvoiceSettlements.ListByInvoice(ctx, h.OrgID(), id)
	})
}
