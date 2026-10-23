// Package alerts is the application service of usage alerts (domain §19):
// it creates, updates, deletes, reads and lists the alerts of a
// subscription or of a wallet. Each write runs in one transaction with the
// jobs that evaluate the alert: the activity of its subscription when the
// subscription is active, or the wallet alert processing of its wallet
// when the wallet is active.
//
// Errors are those of package core: a missing alert, subscription, wallet
// or billable metric is core.NotFound of that resource, an invalid input a
// *core.ValidationError with the field codes of the API.
package alerts
