// Package tax computes the taxes of fees, invoices and credit notes.
//
// It is a pure library: every function takes plain values and returns plain
// values, with no I/O (except [VIESClient]). Amounts are in minor units of
// the invoice currency ("cents"): rounded amounts are int64, unrounded
// ("precise") amounts are [money.Decimal]. Tax rates are percentages (20
// means 20 %), except the rates tax providers return, which are fractions
// (0.2).
//
// The package covers:
//
//   - which taxes apply to a fee ([ResolveFeeTaxes]), in the precedence order
//     explicit codes, add-on, charge, fixed charge, commitment, plan,
//     customer, billing entity;
//   - the taxes of a fee ([ApplyFeeTaxes]) and of an invoice
//     ([ApplyInvoiceTaxes], [ComputeInvoiceAmounts],
//     [AggregateInvoiceTaxes]), on the amount left after coupons;
//   - the split of an invoice's tax across its fees ([BookTaxes]) and the
//     taxes of a credit note ([ComputeCreditNoteTaxes],
//     [AdjustCreditNoteRounding]);
//   - mapping tax provider (Anrok, Avalara) results into the same applied
//     taxes ([NormalizeProviderBreakdown], [SplitProviderTaxes],
//     [ApplyProviderFeeTaxes], [ApplyProviderInvoiceTaxes]);
//   - EU VAT: the embedded rate table ([LookupEURates], [EUTaxes]), the
//     automatic tax decision ([ResolveEUTax], [ResolveEUTaxAfterVIES]) and
//     VAT number syntax ([ValidVATNumberSyntax]); the VIES lookup is the
//     package's only I/O, behind the [VIESChecker] interface ([VIESClient]
//     over the VIES web service), and its outcome is passed back as a
//     [VIESResult];
//   - the per-rate tax subtotals and categories of e-invoices
//     ([InvoiceTaxSubtotals], [CreditNoteTaxSubtotals]).
//
// Every rounding is explicit and half up (ties away from zero), as in lago:
// fee and invoice taxes are summed unrounded and rounded once, and rounded
// totals are spread across lines with [Allocate] so that the lines add up to
// the total.
package tax
