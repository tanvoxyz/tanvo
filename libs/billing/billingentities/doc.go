// Package billingentities manages billing entities, the legal issuers of
// documents (domain §4.2): their creation and update with the side
// effects on the draft invoices they issue (payment due dates, issuing
// dates, tax refresh), their taxes, invoice custom sections and applied
// dunning campaign, and the EU taxes generated for EU tax management
// (lago app/services/billing_entities).
package billingentities
