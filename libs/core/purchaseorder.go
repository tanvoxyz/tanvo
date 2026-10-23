package core

import "strings"

// MaxPurchaseOrderNumberLength is the longest purchase order number of a
// subscription, invoice, wallet, wallet transaction or recurring transaction
// rule.
const MaxPurchaseOrderNumberLength = 255

// NormalizePurchaseOrderNumber trims the surrounding white space of a
// purchase order number; a blank or nil number becomes nil (unset).
func NormalizePurchaseOrderNumber(p *string) *string {
	if p == nil {
		return nil
	}
	s := strings.TrimSpace(*p)
	if s == "" {
		return nil
	}
	return &s
}
