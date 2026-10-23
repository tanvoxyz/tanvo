package tax_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/tax"
)

func errorCode(code string) (error, bool) {
	switch code {
	case "invoice_applied_tax_not_found":
		return tax.ErrInvoiceAppliedTaxNotFound, true
	case "fee_missing_from_tax_response":
		return tax.ErrFeeMissingFromTaxResponse, true
	case "fee_out_of_range":
		return tax.ErrFeeOutOfRange, true
	}
	return nil, false
}

// lineFee is a fee of a provider invoice with the charge grouping it.
type lineFee struct {
	ChargeID string
	ItemKey  string
	tax.Fee
}

type providerInvoiceInput struct {
	Fees []lineFee
	// Results are the provider's results, one per line of
	// [tax.GroupFeesByCharge]; a line of several fees is split.
	Results                             []tax.ProviderTaxResult
	CouponsAmountCents                  int64
	ProgressiveBillingCreditAmountCents int64
	CreditNotesAmountCents              int64
	Credit                              bool
}

type invoiceResult struct {
	Amounts   tax.InvoiceAmounts
	Subtotals []tax.TaxSubtotal
}

func subtotals(amounts tax.InvoiceAmounts, credit bool, allowances int64) []tax.TaxSubtotal {
	inv := tax.Invoice{TaxesAmountCents: amounts.TaxesAmountCents, AppliedTaxes: amounts.AppliedTaxes, Fees: amounts.Fees}
	return tax.InvoiceTaxSubtotals(tax.InvoiceTaxSubtotalsInput{
		Credit:                credit,
		Fees:                  amounts.Fees,
		TaxesAmountCents:      amounts.TaxesAmountCents,
		ProviderTaxes:         inv.ProviderTaxes(),
		AllowancesAmountCents: allowances,
	})
}

func providerInvoice(in providerInvoiceInput) (invoiceResult, error) {
	ids := make([]string, len(in.Fees))
	for i, f := range in.Fees {
		ids[i] = f.ChargeID
	}
	lines := tax.GroupFeesByCharge(ids)
	if len(lines) != len(in.Results) {
		return invoiceResult{}, fmt.Errorf("got %d provider results, want one per line (%d)", len(in.Results), len(lines))
	}
	fees := make([]tax.InvoiceFee, len(in.Fees))
	for l, line := range lines {
		if len(line) == 1 {
			r := in.Results[l]
			fees[line[0]] = tax.InvoiceFee{Fee: in.Fees[line[0]].Fee, ProviderResult: &r}
			continue
		}
		items := make([]tax.ProviderItem, len(line))
		for k, i := range line {
			items[k] = tax.ProviderItem{ItemKey: in.Fees[i].ItemKey, Fee: in.Fees[i].Fee}
		}
		for k, r := range tax.SplitProviderTaxes(in.Results[l], items) {
			fees[line[k]] = tax.InvoiceFee{Fee: in.Fees[line[k]].Fee, ProviderResult: &r}
		}
	}
	amounts, err := tax.ComputeInvoiceAmounts(tax.InvoiceAmountsInput{
		Fees:                                fees,
		UseProviderTaxes:                    true,
		CouponsAmountCents:                  in.CouponsAmountCents,
		ProgressiveBillingCreditAmountCents: in.ProgressiveBillingCreditAmountCents,
		CreditNotesAmountCents:              in.CreditNotesAmountCents,
	})
	if err != nil {
		return invoiceResult{}, err
	}
	return invoiceResult{
		Amounts:   amounts,
		Subtotals: subtotals(amounts, in.Credit, in.CouponsAmountCents+in.ProgressiveBillingCreditAmountCents),
	}, nil
}

type invoiceAmountsInput struct {
	tax.InvoiceAmountsInput
	Credit bool
}

type creditNoteInvoiceInput struct {
	// Fees are the invoice's fees with their taxes, taxed internally.
	Fees               []tax.InvoiceFee
	CouponsAmountCents int64
	Items              []tax.CreditNoteItem
}

type creditNoteInvoiceResult struct {
	Invoice    tax.InvoiceAmounts
	CreditNote tax.CreditNoteTaxes
	Subtotals  []tax.TaxSubtotal
}

func run(t *testing.T, c testCase) (any, error) {
	t.Helper()
	switch c.Op {
	case "fee_taxes":
		var in struct {
			Sources tax.TaxSources
			Fee     tax.Fee
		}
		decodeInput(t, &c.Input, &in)
		taxes, source := tax.ResolveFeeTaxes(in.Sources)
		return struct {
			Source tax.TaxSource
			tax.FeeTaxes
		}{source, tax.ApplyFeeTaxes(in.Fee, taxes)}, nil
	case "invoice_taxes":
		var in struct {
			SubTotal int64
			Fees     []tax.Fee
		}
		decodeInput(t, &c.Input, &in)
		return tax.ApplyInvoiceTaxes(in.SubTotal, in.Fees), nil
	case "invoice_amounts":
		var in invoiceAmountsInput
		decodeInput(t, &c.Input, &in)
		amounts, err := tax.ComputeInvoiceAmounts(in.InvoiceAmountsInput)
		if err != nil {
			return nil, err
		}
		return invoiceResult{amounts, subtotals(amounts, in.Credit, in.CouponsAmountCents+in.ProgressiveBillingCreditAmountCents)}, nil
	case "provider_invoice":
		var in providerInvoiceInput
		decodeInput(t, &c.Input, &in)
		return providerInvoice(in)
	case "aggregate_invoice_taxes":
		var in struct{ Fees []tax.Fee }
		decodeInput(t, &c.Input, &in)
		return tax.AggregateInvoiceTaxes(in.Fees), nil
	case "provider_invoice_taxes":
		var in struct {
			SubTotal int64
			Fees     []tax.Fee
		}
		decodeInput(t, &c.Input, &in)
		return tax.ApplyProviderInvoiceTaxes(in.SubTotal, in.Fees), nil
	case "provider_fee_taxes":
		var in struct {
			Fee    tax.Fee
			Result *tax.ProviderTaxResult
		}
		decodeInput(t, &c.Input, &in)
		return tax.ApplyProviderFeeTaxes(in.Fee, in.Result)
	case "provider_breakdown":
		var in struct {
			Lines      []tax.ProviderBreakdownLine
			TaxesToPay int64
		}
		decodeInput(t, &c.Input, &in)
		return tax.NormalizeProviderBreakdown(in.Lines, in.TaxesToPay), nil
	case "split_provider_taxes":
		var in struct {
			Group tax.ProviderTaxResult
			Items []tax.ProviderItem
		}
		decodeInput(t, &c.Input, &in)
		return tax.SplitProviderTaxes(in.Group, in.Items), nil
	case "group_fees_by_charge":
		var in struct{ ChargeIDs []string }
		decodeInput(t, &c.Input, &in)
		return tax.GroupFeesByCharge(in.ChargeIDs), nil
	case "allocate":
		var in struct {
			Total   money.Decimal
			Weights []money.Decimal
		}
		decodeInput(t, &c.Input, &in)
		return tax.Allocate(in.Total, in.Weights), nil
	case "allocate_precise":
		var in struct {
			Total   money.Decimal
			Weights []money.Decimal
		}
		decodeInput(t, &c.Input, &in)
		return tax.AllocatePrecise(in.Total, in.Weights), nil
	case "allocate_by_group":
		var in struct {
			Total  money.Decimal
			Groups []tax.AllocationGroup
		}
		decodeInput(t, &c.Input, &in)
		return tax.AllocateByGroup(in.Total, in.Groups), nil
	case "book_taxes":
		var in tax.Invoice
		decodeInput(t, &c.Input, &in)
		return tax.BookTaxes(in), nil
	case "credit_note_taxes":
		var in tax.CreditNoteTaxesInput
		decodeInput(t, &c.Input, &in)
		return tax.ComputeCreditNoteTaxes(in)
	case "credit_note_rounding":
		var in struct {
			Amounts tax.CreditNoteAmounts
			// When Items is given, the subtotal is computed from them.
			Items                               []tax.CreditNoteItem
			PreciseCouponsAdjustmentAmountCents money.Decimal
		}
		decodeInput(t, &c.Input, &in)
		if in.Items != nil {
			in.Amounts.SubTotalExcludingTaxesAmountCents = tax.CreditNoteSubTotal(in.Items, in.PreciseCouponsAdjustmentAmountCents)
		}
		return tax.AdjustCreditNoteRounding(in.Amounts), nil
	case "credit_note_invoice":
		var in creditNoteInvoiceInput
		decodeInput(t, &c.Input, &in)
		amounts, err := tax.ComputeInvoiceAmounts(tax.InvoiceAmountsInput{Fees: in.Fees, CouponsAmountCents: in.CouponsAmountCents})
		if err != nil {
			return nil, err
		}
		inv := tax.Invoice{TaxesAmountCents: amounts.TaxesAmountCents, AppliedTaxes: amounts.AppliedTaxes, Fees: amounts.Fees}
		cn, err := tax.ComputeCreditNoteTaxes(tax.CreditNoteTaxesInput{Invoice: inv, Items: in.Items})
		if err != nil {
			return nil, err
		}
		st, err := tax.CreditNoteTaxSubtotals(inv, in.Items, cn.TaxesAmountCents)
		if err != nil {
			return nil, err
		}
		return creditNoteInvoiceResult{amounts, cn, st}, nil
	case "invoice_tax_subtotals":
		var in tax.InvoiceTaxSubtotalsInput
		decodeInput(t, &c.Input, &in)
		return tax.InvoiceTaxSubtotals(in), nil
	case "eu_country_codes":
		return tax.EUCountryCodes(), nil
	case "eu_rates":
		var in struct {
			Country string
			At      time.Time
		}
		decodeInput(t, &c.Input, &in)
		r, ok := tax.LookupEURates(in.Country, in.At)
		if !ok {
			return nil, fmt.Errorf("no rates for %s", in.Country)
		}
		names := make([]string, len(r.Exceptions))
		for i, e := range r.Exceptions {
			names[i] = e.Name
		}
		return struct {
			Standard   money.Decimal
			Exceptions []string
		}{r.Standard(), names}, nil
	case "eu_taxes":
		var in struct{ At time.Time }
		decodeInput(t, &c.Input, &in)
		taxes := tax.EUTaxes(in.At)
		return struct {
			Count int
			Taxes []tax.Tax
		}{len(taxes), taxes}, nil
	case "eu_tax":
		var in struct {
			tax.EUTaxInput
			Applicable *struct {
				NewRecord, HasEUTax, TaxAttributesChanged bool
			}
		}
		decodeInput(t, &c.Input, &in)
		if a := in.Applicable; a != nil && !tax.EUTaxesApplicable(a.NewRecord, a.HasEUTax, a.TaxAttributesChanged) {
			return struct{ Applicable bool }{false}, nil
		}
		d := tax.ResolveEUTax(in.EUTaxInput)
		return struct {
			Applicable bool
			tax.EUTaxDecision
		}{true, d}, nil
	case "eu_tax_after_vies":
		var in struct {
			tax.EUTaxInput
			VIES tax.VIESResult
		}
		decodeInput(t, &c.Input, &in)
		return struct{ TaxCode string }{tax.ResolveEUTaxAfterVIES(in.EUTaxInput, in.VIES)}, nil
	case "vat_number":
		var in struct{ Number string }
		decodeInput(t, &c.Input, &in)
		cc, _ := tax.VATNumberCountry(in.Number)
		return struct {
			Normalized string
			Country    string
			Valid      bool
		}{tax.NormalizeVATNumber(in.Number), cc, tax.ValidVATNumberSyntax(in.Number)}, nil
	case "vies_fault":
		var in struct{ Fault string }
		decodeInput(t, &c.Input, &in)
		typ, failed := tax.VIESFault(in.Fault)
		return struct {
			Type   tax.VIESErrorType
			Failed bool
		}{typ, failed}, nil
	case "vies_retry_delay":
		var in struct{ Attempts int }
		decodeInput(t, &c.Input, &in)
		return struct{ Minutes int }{int(tax.VIESRetryDelay(in.Attempts) / time.Minute)}, nil
	}
	t.Fatalf("%s: unknown op %q", c.Source, c.Op)
	return nil, nil
}

func TestFeeTaxes(t *testing.T)          { runFile(t, "fee_taxes.yaml", run) }
func TestInvoiceTaxes(t *testing.T)      { runFile(t, "invoice_taxes.yaml", run) }
func TestBookedTaxes(t *testing.T)       { runFile(t, "booked_taxes.yaml", run) }
func TestCreditNoteTaxes(t *testing.T)   { runFile(t, "credit_note_taxes.yaml", run) }
func TestAllocation(t *testing.T)        { runFile(t, "allocation.yaml", run) }
func TestProviderTaxes(t *testing.T)     { runFile(t, "provider_taxes.yaml", run) }
func TestEUVAT(t *testing.T)             { runFile(t, "eu_vat.yaml", run) }
func TestVATNumbers(t *testing.T)        { runFile(t, "vat_numbers.yaml", run) }
func TestEInvoiceSubtotals(t *testing.T) { runFile(t, "einvoice_taxes.yaml", run) }
