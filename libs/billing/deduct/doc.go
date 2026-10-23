// Package deduct applies the invoice deductions in their order (domain
// §22.7): progressive billing credits, coupons, then (after taxes) credit
// notes and prepaid wallet credits, and restores them when an invoice is
// voided or regenerated.
package deduct
