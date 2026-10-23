// Package creditnote holds credit notes (domain §13): the [CreditNote] with
// its items and applied taxes, its three lifecycles (document status,
// credit balance status and refund status), its number format, and the
// pure amount checks of §13.2 ([Check], [CheckItem]). Amounts are in minor
// units unless a field says "precise".
package creditnote
