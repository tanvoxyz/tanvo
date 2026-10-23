// Package alert holds usage monitoring (domain §19): an [Alert] watches a
// subscription's usage or a wallet's balance against [Threshold]s, and
// every evaluation that crosses thresholds records a [TriggeredAlert] and
// emits alert.triggered.
//
// [Crossed] is the pure crossing algorithm of lago's
// UsageMonitoring::Alert. Alerts have no resolved or seeded states
// (domain §24 item 13).
package alert
