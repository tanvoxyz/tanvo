package port

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/core/streaming"
)

// StreamProducer writes records to streaming destinations (parity §1.40).
// The Kinesis implementation lives in libs/webhooks/kinesis.
type StreamProducer interface {
	// Produce writes one record, the JSON document data, to the stream of
	// d under partitionKey. A record the destination refuses, or cannot
	// be reached for (an unknown stream, a denied identity, a validation
	// fault, a throttle, missing credentials, a network failure), is
	// logged with [streaming.Logger] and returns nil; any other failure
	// is returned. A delivered record is logged too.
	Produce(ctx context.Context, d *streaming.Destination, data []byte, partitionKey string) error
	// CredentialsAvailable reports whether this process has not already
	// failed to obtain credentials for d. Callers with somewhere else to
	// send the work use it to stop paying the timeout.
	CredentialsAvailable(d *streaming.Destination) bool
}
