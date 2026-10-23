// Package estimates estimates the fees events would create on the
// pay-in-advance charges of their subscription, without storing the events
// or the fees: the full in-advance pricing of an event with its period's
// usage, taxed (lago Fees::EstimatePayInAdvanceService), and the instant
// estimate of percentage and standard charges, priced on the event alone
// (lago Fees::EstimateInstant::PayInAdvanceService and
// BatchPayInAdvanceService).
package estimates
