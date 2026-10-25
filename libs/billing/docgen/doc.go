// Package docgen renders and stores the documents of invoices, credit
// notes and payment receipts: their PDF and their e-invoice XML (UBL),
// announced with invoice.generated, credit_note.generated and
// payment_receipt.generated and their activity logs (lago
// Invoices::GenerateDocumentsJob, CreditNotes::GenerateDocumentsJob and
// PaymentReceipts::GenerateDocumentsJob with their GeneratePdfService and
// GenerateXmlService). It also loads the data of the document emails,
// which apps/server composes with libs/mail.
//
// The renderer is a port ([Renderer]) that apps/server implements with
// libs/documents; the files go to the blob store port.
package docgen
