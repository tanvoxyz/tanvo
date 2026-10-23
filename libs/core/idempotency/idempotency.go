package idempotency

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// TTL is how long a record is kept after the request that created it.
const TTL = 24 * time.Hour

// PendingTTL is how long a pending record holds its key: a request that
// has not completed by then (its server stopped, say) may be retried.
const PendingTTL = 5 * time.Minute

// MaxKeyLen bounds the length of a key, in bytes.
const MaxKeyLen = 255

// Record is the outcome of one request made with an idempotency key.
type Record struct {
	// OrganizationID is the organization of the caller.
	OrganizationID core.OrganizationID
	// Key is the Idempotency-Key header, 1 to [MaxKeyLen] bytes.
	Key string
	// Procedure is the RPC the key was first used with
	// ("/tanvo.catalog.v1.BillableMetricService/CreateBillableMetric").
	Procedure string
	// RequestHash is the SHA-256 of the request message; a retry must send
	// the same request.
	RequestHash []byte
	// Response is the serialized response message; nil while the request
	// runs.
	Response []byte
	// CreatedAt is the instant of the request.
	CreatedAt time.Time
	// ExpiresAt is CreatedAt + [PendingTTL] while pending, CreatedAt +
	// [TTL] once completed; an expired record no longer holds the key.
	ExpiresAt time.Time
}

// Pending reports whether the request that claimed the key has not
// completed.
func (r Record) Pending() bool { return r.Response == nil }

// Expired reports whether the record no longer holds its key at now.
func (r Record) Expired(now time.Time) bool { return !r.ExpiresAt.After(now) }

// Repository stores idempotency records. Every method is scoped to an
// organization unless stated otherwise.
type Repository interface {
	// Claim inserts the pending record r, replacing a record of the same
	// key that expired at r.CreatedAt. It returns false, writing nothing,
	// when a live record holds the key.
	Claim(ctx context.Context, r *Record) (bool, error)
	// Get returns the record of key, expired or not, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, key string) (*Record, error)
	// Complete stores the response of the pending record of key and its
	// new expiry; a missing record fails with core.ErrNotFound.
	Complete(ctx context.Context, org core.OrganizationID, key string, response []byte, expiresAt time.Time) error
	// Release deletes the pending record of key, so that the request can be
	// retried; a completed or missing record is left as it is.
	Release(ctx context.Context, org core.OrganizationID, key string) error
	// Purge deletes up to limit records of org expired at now and returns
	// how many.
	Purge(ctx context.Context, org core.OrganizationID, now time.Time, limit int) (int, error)
	// Organizations returns the organizations with records expired at now.
	// It is not organization-scoped: it feeds the purge clock job.
	Organizations(ctx context.Context, now time.Time) ([]core.OrganizationID, error)
}
