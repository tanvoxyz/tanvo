// Package einvoice renders finalized invoices, credit notes and payment
// receipts as structured e-invoices for billing entities that have
// e-invoicing enabled in France or Germany (see [Eligible]).
//
// Two syntaxes of EN 16931 are produced: the UN/CEFACT Cross Industry
// Invoice (CII, the XML part of Factur-X) and OASIS UBL 2.1. UBL documents
// of German sellers follow XRechnung 3.0 with the Peppol BIS Billing
// profile; payment receipts are UBL ApplicationResponse documents.
//
// Output is deterministic: an XML declaration, two-space indentation,
// elements in a fixed order and no clock reads. The VAT breakdown keeps the
// precision of the taxable bases while its tax amounts are allocated so
// they add up to the booked document tax.
package einvoice
