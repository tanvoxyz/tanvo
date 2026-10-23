// Package period is the date engine of billing. It computes the periods a
// subscription is billed for.
//
// It covers weekly, monthly, quarterly, semiannual and yearly plans, with
// calendar or anniversary billing in any IANA time zone. It resolves the
// subscription fee period, the usage charges period and the fixed charges
// period. Yearly and semiannual plans can bill charges or fixed charges
// monthly. It handles fees paid in advance or in arrears, free trials,
// subscriptions that start or end inside a period, and upgrade and downgrade
// boundaries. It also gives period lengths in days and proration ratios.
//
// # Conventions
//
// Every [Period] is half-open: [From, To). A period that ends with a day has
// To set to the next local midnight. A period cut by a termination at
// instant T has To = T. Days are counted on the wall clock of the
// subscription's location, see [DaysBetween]. A daylight saving change
// therefore never adds or removes a billing day. Instants are returned in
// UTC.
//
// Computing periods is pure: no I/O and no clock. The caller passes the
// billing instant and, when the location changed since the last invoice,
// that invoice's boundaries ([PreviousInvoice]).
//
// # Usage
//
//	d, err := sub.Dates(billingAt, period.Billing)
//	if err != nil {
//		return err
//	}
//	fee, _ := d.SubscriptionPeriod()
//	ratio := period.ProrationRatio(fee.Days(sub.Location), d.PeriodDays())
package period
