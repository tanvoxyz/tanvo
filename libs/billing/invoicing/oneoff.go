package invoicing

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Codes of the rule errors of one-off invoices.
const (
	// CodeNoFees rejects a one-off invoice without fees.
	CodeNoFees = "fees_not_found"
)

// OneOffInput is a one-off invoice of add-ons (lago
// Invoices::CreateOneOffService).
type OneOffInput struct {
	// OrganizationID is the organization.
	OrganizationID core.OrganizationID
	// CustomerID is the invoiced customer.
	CustomerID core.CustomerID
	// BillingEntityID is the issuing billing entity; zero uses the
	// customer's.
	BillingEntityID core.BillingEntityID
	// Currency is the invoice currency (any, the customer's being only a
	// default); zero uses the customer's, and one of them is required.
	Currency money.Currency
	// Fees are the add-on lines.
	Fees []fees.OneOff
	// SkipAutomaticPayment leaves the payment to the customer (lago
	// skip_psp).
	SkipAutomaticPayment bool
	// PaymentMethodType and PaymentMethodID select the payment method.
	PaymentMethodType core.PaymentMethodType
	// PaymentMethodID is the explicit payment method.
	PaymentMethodID core.PaymentMethodID
	// PurchaseOrderNumber is printed on the invoice (blank is none).
	PurchaseOrderNumber *string
	// SkipInvoiceCustomSections prints no custom section.
	SkipInvoiceCustomSections bool
	// InvoiceCustomSectionIDs are the custom sections printed; none
	// prints the customer's ([Service.applyCustomSections]).
	InvoiceCustomSectionIDs []core.InvoiceCustomSectionID
}

// CreateOneOff issues a one-off invoice: its add-on fees, taxed with their
// explicit tax codes or the add-on's and customer's taxes, without
// coupons, credit notes or prepaid credits; it is finalized (or closed
// when zero and zero-amount invoices are skipped), emits
// invoice.one_off_created and enqueues its document and payment.
func (s *Service) CreateOneOff(ctx context.Context, in OneOffInput) (*invoice.Invoice, error) {
	var out *invoice.Invoice
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = s.createOneOff(ctx, r, in)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// createOneOff is [Service.CreateOneOff] in the transaction of r.
func (s *Service) createOneOff(ctx context.Context, r port.Repos, in OneOffInput) (*invoice.Invoice, error) {
	if len(in.Fees) == 0 {
		return nil, core.NewRuleError(CodeNoFees, "a one-off invoice needs fees")
	}
	cenv, err := kernel.LoadCustomerEnv(ctx, r, in.OrganizationID, in.CustomerID, in.BillingEntityID)
	if err != nil {
		return nil, err
	}
	cur := in.Currency
	if cur.IsZero() {
		cur = cenv.Customer.Currency
	}
	if cur.IsZero() {
		var v core.Validator
		v.Add("currency", core.CodeRequired)
		return nil, v.Err()
	}
	if cenv.Customer.Currency.IsZero() {
		cenv.Customer.Currency = cur
		if err := r.Customers.Update(ctx, cenv.Customer); err != nil {
			return nil, fmt.Errorf("set currency of customer %s: %w", cenv.Customer.ID, err)
		}
	}
	inv := kernel.NewInvoice(s.d, cenv, kindOneOff, cur, s.d.Now())
	inv.SkipAutomaticPayment = in.SkipAutomaticPayment
	inv.PaymentMethodType, inv.PaymentMethodID = in.PaymentMethodType, in.PaymentMethodID
	inv.PurchaseOrderNumber = core.NormalizePurchaseOrderNumber(in.PurchaseOrderNumber)
	var v core.Validator
	if !v.OptionalString("purchase_order_number", inv.PurchaseOrderNumber, core.MaxPurchaseOrderNumberLength) {
		return nil, v.Err()
	}
	if err := r.Invoices.Create(ctx, inv); err != nil {
		return nil, fmt.Errorf("create invoice: %w", err)
	}
	ofs, err := s.fees.OneOffFees(ctx, r, inv, in.Fees)
	if err != nil {
		return nil, err
	}
	// The custom sections are applied before the taxes (lago
	// app/services/invoices/create_one_off_service.rb:58-62).
	if !in.SkipInvoiceCustomSections {
		if err := s.applyCustomSections(ctx, r, cenv, inv, in.InvoiceCustomSectionIDs); err != nil {
			return nil, err
		}
	}
	resolver := kernel.NewTaxResolver(r, cenv)
	fs := make([]*invoice.Fee, len(ofs))
	for i, of := range ofs {
		if err := r.Fees.Create(ctx, of.Fee); err != nil {
			return nil, fmt.Errorf("create fee: %w", err)
		}
		if len(of.TaxIDs) > 0 {
			resolver.SetExplicit(of.Fee.ID, of.TaxIDs)
		}
		fs[i] = of.Fee
	}
	st, err := s.settle(ctx, r, cenv, inv, fs, settleOptions{resolver: resolver, vies: true})
	if err != nil {
		return nil, err
	}
	if err := s.finish(ctx, r, cenv, inv, kindOneOff, st, modeNew); err != nil {
		return nil, err
	}
	return inv, nil
}
