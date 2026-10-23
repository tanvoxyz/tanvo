// Package payments records manual payments, creates payment requests and
// payment receipts, and manages customers' payment methods (domain §15).
//
// A manual payment that settles its invoice applies the invoice's payment
// status update: the overdue flag and the customer's dunning attempts are
// reset, the fees take the payment status, active checkout links expire
// and the invoice.payment_status_updated webhook is emitted. Every
// succeeded payment gets a receipt numbered per customer.
package payments
