package metering

import (
	"context"
	"time"
)

// RawFilter narrows [RawReader.RawEvents]; zero fields do not filter.
type RawFilter struct {
	// OrganizationID scopes the read; it is required.
	OrganizationID string
	// Code keeps the events of that billable metric code.
	Code string
	// SubscriptionID keeps the events of that external subscription ID.
	SubscriptionID string
	// TimestampFrom and TimestampTo keep the events whose timestamp is in
	// [TimestampFrom, TimestampTo].
	TimestampFrom, TimestampTo *time.Time
}

// RawPosition is the position of an event in the order of
// [RawReader.RawEvents]: the last event of the previous page.
type RawPosition struct {
	IngestedAt     time.Time
	SubscriptionID string
	TransactionID  string
}

// RawReader reads the ingestion ledger: the current copy of every
// deduplication key, as ingested, with its request metadata.
type RawReader interface {
	// RawEvent returns the current copy of the event with the transaction
	// ID, of the external subscription when subscriptionID is set, else
	// the first ingested under any subscription; ok is false when there is
	// none.
	RawEvent(ctx context.Context, organizationID, subscriptionID, transactionID string) (e Event, ok bool, err error)
	// RawEvents returns up to limit events matching f, newest ingested
	// first, then by external subscription ID and transaction ID
	// ascending, starting after the position when it is set.
	RawEvents(ctx context.Context, f RawFilter, after *RawPosition, limit int) ([]Event, error)
}
