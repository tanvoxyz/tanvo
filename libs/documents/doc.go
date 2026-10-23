// Package documents renders Tanvo's business documents: invoices (every
// lago-api template variant: subscription, self-billed, one-off, in-advance
// charge and fixed charge, credit and progressive billing), credit notes,
// payment receipts and payment requests.
//
// A [Renderer] lays each document out in Go, with every text translated
// (nine locales) and formatted, and hands the layout as JSON to an
// embedded Typst template compiled by the typst CLI in a child process.
// Documents are PDF/A-3b; when the billing entity issues e-invoices, the
// invoice's and credit note's CII XML is embedded as a Factur-X/ZUGFeRD
// attachment. The same e-invoices are available standalone as CII and UBL
// 2.1 (see package einvoice).
//
// The view models ([Invoice], [CreditNote], [PaymentReceipt],
// [PaymentRequest]) wrap core types with the objects their templates print;
// [FromCore] maps loaded core objects to them. Package blob stores the
// rendered files.
package documents
