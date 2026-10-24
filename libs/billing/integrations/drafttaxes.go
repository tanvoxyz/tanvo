package integrations

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/tax"
)

// DraftInvoiceTaxesInput is a one-off invoice not created yet whose taxes
// are asked of the customer's tax provider.
type DraftInvoiceTaxesInput struct {
	// CustomerID is the customer, or zero for CustomerExternalID.
	CustomerID core.CustomerID
	// CustomerExternalID is the customer's external ID.
	CustomerExternalID string
	// Currency is the invoice currency; zero uses the customer's.
	Currency money.Currency
	// Fees are the add-on lines.
	Fees []DraftInvoiceFee
}

// DraftInvoiceFee is one line of a [DraftInvoiceTaxesInput].
type DraftInvoiceFee struct {
	// AddOn is the add-on.
	AddOn refs.Ref
	// Units is the quantity; nil means 1.
	Units *money.Decimal
	// UnitAmount is the unit price in currency units; nil means the
	// add-on's.
	UnitAmount *money.Decimal
}

// DraftInvoiceTaxes asks the customer's tax provider for the taxes of a
// one-off invoice not created yet, issued today in the customer's time
// zone (lago Mutations::Integrations::FetchDraftInvoiceTaxes), with the
// invoice currency. Each line
// stands for its add-on: its item ID is the add-on's and its amount the
// rounded product of the unit price and the units. Lines without an
// amount are left out unless none has one; the first then stands for the
// invoice. Nothing is stored. The result is empty for a customer taxed by
// no provider; a refusal emits customer.tax_provider_error and is a
// validation error of tax_error with the provider's code.
func (t *Taxes) DraftInvoiceTaxes(ctx context.Context, org core.OrganizationID, in DraftInvoiceTaxesInput) ([]tax.ProviderTaxResult, money.Currency, error) {
	var (
		e   *taxEnv
		tx  *integration.TaxTransaction
		cur money.Currency
	)
	err := t.s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := draftCustomer(ctx, r, org, in)
		if err != nil {
			return err
		}
		cur = in.Currency
		if cur.IsZero() {
			cur = c.Currency
		}
		fees, err := draftFees(ctx, r, org, cur, in.Fees)
		if err != nil {
			return err
		}
		if e, err = t.load(ctx, r, c); err != nil || e == nil {
			return err
		}
		tx = e.transaction()
		tx.IssuingDate, tx.Currency = e.env.Today(t.s.d.Now()), cur
		for _, f := range taxableDraftFees(fees) {
			item, err := e.mapper.fee(ctx, f)
			if err != nil {
				return err
			}
			// The line has no item key and the add-on as item ID
			// (fetch_draft_invoice_taxes.rb:38-58, payloads/anrok.rb:43).
			tx.Lines = append(tx.Lines, integration.TaxLine{
				ItemID: f.AddOnID.String(), ItemCode: externalID(item), AmountCents: f.Amount, Units: f.Units,
			})
		}
		return nil
	})
	if err != nil {
		return nil, cur, fmt.Errorf("draft invoice taxes: %w", err)
	}
	if e == nil {
		// create_draft_service.rb:13.
		return []tax.ProviderTaxResult{}, cur, nil
	}
	res, err := t.quote(ctx, e, tx)
	if err != nil {
		err = t.failure(ctx, e, err)
		if pe, ok := errors.AsType[*kernel.TaxProviderError](err); ok {
			// fetch_draft_invoice_taxes.rb:25.
			return nil, cur, fieldError("tax_error", pe.Code)
		}
		return nil, cur, err
	}
	out := make([]tax.ProviderTaxResult, len(res.Lines))
	for i, l := range res.Lines {
		out[i] = providerResult(l)
	}
	return out, cur, nil
}

// draftCustomer loads the customer of in, kept, or fails with not found.
func draftCustomer(ctx context.Context, r port.Repos, org core.OrganizationID, in DraftInvoiceTaxesInput) (*customer.Customer, error) {
	var (
		c   *customer.Customer
		err error
	)
	if in.CustomerID.IsZero() {
		c, err = r.Customers.GetByExternalID(ctx, org, in.CustomerExternalID)
	} else {
		c, err = r.Customers.Get(ctx, org, in.CustomerID)
		if err == nil && c.DeletedAt != nil {
			err = core.ErrNotFound
		}
	}
	if errors.Is(err, core.ErrNotFound) {
		return nil, core.NotFound("customer")
	}
	if err != nil {
		return nil, fmt.Errorf("load customer: %w", err)
	}
	return c, nil
}

// draftFees builds the add-on fees of the lines, in memory and without
// IDs. A unit price finer than the currency's minor unit is invalid.
func draftFees(ctx context.Context, r port.Repos, org core.OrganizationID, cur money.Currency, lines []DraftInvoiceFee) ([]*invoice.Fee, error) {
	addOns := refs.AddOns(r, org)
	out := make([]*invoice.Fee, 0, len(lines))
	for i, l := range lines {
		a, err := addOns.One(ctx, l.AddOn)
		if err != nil {
			return nil, err
		}
		unit := a.Amount
		if l.UnitAmount != nil {
			field := fmt.Sprintf("fees[%d].unit_amount", i)
			if cur.IsZero() {
				return nil, fieldError("currency", core.CodeRequired)
			}
			if !l.UnitAmount.Mul(cur.SubunitToUnit()).IsInteger() {
				return nil, fieldError(field, core.CodeInvalid)
			}
			if unit, err = cur.ToMinor(*l.UnitAmount, money.HalfUp); err != nil {
				if errors.Is(err, money.ErrOverflow) {
					return nil, fieldError(field, core.CodeInvalid)
				}
				return nil, err
			}
		}
		units := money.One
		if l.Units != nil {
			units = *l.Units
		}
		precise := kernel.Cents(unit).Mul(units)
		out = append(out, &invoice.Fee{
			OrganizationID: org, Type: core.FeeTypeAddOn, AddOnID: a.ID,
			Amount: kernel.RoundCents(precise), PreciseAmount: precise, UnitAmount: unit,
			Units: units, Currency: cur,
		})
	}
	return out, nil
}

// taxableDraftFees returns the fees with an amount, or the first when
// none has one (lago invoices/base_service.rb:27-29; the draft fees have
// no creation time, so they keep their order).
func taxableDraftFees(fs []*invoice.Fee) []*invoice.Fee {
	var out []*invoice.Fee
	for _, f := range fs {
		if f.Amount != 0 {
			out = append(out, f)
		}
	}
	if len(out) == 0 && len(fs) > 0 {
		return fs[:1]
	}
	return out
}

// fieldError is a validation error of one field.
func fieldError(field, code string) error {
	return &core.ValidationError{Errors: []core.FieldError{{Field: field, Code: code}}}
}

// DraftInvoiceTaxes is [Taxes.DraftInvoiceTaxes] of the tax provider over
// s; it fails with [integration.ErrUnsupported] when none is set.
func (s *Service) DraftInvoiceTaxes(ctx context.Context, org core.OrganizationID, in DraftInvoiceTaxesInput) ([]tax.ProviderTaxResult, money.Currency, error) {
	if s.taxes == nil {
		return nil, money.Currency{}, fmt.Errorf("draft invoice taxes: %w", integration.ErrUnsupported)
	}
	return s.taxes.DraftInvoiceTaxes(ctx, org, in)
}
