// Package dailyusage stores the daily usage snapshots of subscriptions
// (subscription.DailyUsage, lago app/services/daily_usages): the hourly
// selection of the subscriptions entering a new day in their customer's
// time zone ([Service.ComputeAll]), the snapshot of one day
// ([Service.Compute]), the snapshot of the last day of an invoiced period
// ([Service.FillFromInvoice]) and the backfill of past days
// ([Service.FillHistory]). Each snapshot keeps the usage of its charges
// period so far with its change from the previous snapshot of the period
// (subscription.DiffUsage), the data the revenue analytics read.
package dailyusage
