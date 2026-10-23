// Package coupon holds discounts (domain §8): [Coupon], its application to
// a customer, [AppliedCoupon], and the pure rules of their application to
// invoices (domain §22.7) and of voiding (domain §11.4).
//
// A coupon targets plans or billable metrics, never both. Once applied,
// only its name, description and expiration change ([KeepLocked]). An
// applied coupon captures the coupon's terms with any [Overrides];
// [AppliedCoupon.Discount] computes a deduction, [AppliedCoupon.Consume]
// records it and [AppliedCoupon.Restore] gives it back.
package coupon
