// Package customers manages customers (domain §6): their creation and
// update with the side effects on their draft invoices, taxes, invoice
// custom sections and dunning settings, their deletion with the
// termination of their subscriptions, applied coupons and wallets, the
// VIES check of their VAT numbers, and their connections to payment,
// accounting, tax and CRM integrations.
package customers
