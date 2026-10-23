// Package invoice holds invoices and their parts (domain §11, §12): the
// [Invoice] with its lifecycle, the invoice subscriptions that make billing
// idempotent ([Subscription]), the [Fee] lines and their applied taxes,
// invoice applied taxes ([AppliedTax]), settlements, adjusted fees, invoice
// custom sections and error details.
//
// Pure rules live here too: status transitions ([CanTransition]), the final
// status of a built invoice ([FinalStatus]), voidability, totals and capped
// deductions ([ComputeTotals]), the issuing and due dates of §22.12 and the
// number formats of §22.11. Amounts are in minor units unless a field says
// "precise".
package invoice
