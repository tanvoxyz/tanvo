package store

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/streaming"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindStreaming binds the repositories of streaming destinations.
func (t *txState) bindStreaming(r *port.Repos) {
	r.StreamingDestinations = streamingDestinationRepo{t}
	r.StreamingDeliveryLocks = streamingLockRepo{t}
}

// streamingDestinationRepo implements streaming.Repository.
type streamingDestinationRepo struct{ t *txState }

var _ streaming.Repository = streamingDestinationRepo{}

func streamingDestinationParams(d *streaming.Destination) db.CreateStreamingDestinationParams {
	types := make([]string, len(d.EventTypes))
	for i, et := range d.EventTypes {
		types[i] = string(et)
	}
	excluded := d.CustomerFullUsageExcludedPlanCodes
	if excluded == nil {
		excluded = []string{}
	}
	return db.CreateStreamingDestinationParams{
		OrganizationID:                     d.OrganizationID.UUID(),
		ID:                                 d.ID.UUID(),
		Type:                               string(d.Type),
		Active:                             d.Active,
		EventTypes:                         types,
		CustomerFullUsageExcludedPlanCodes: excluded,
		StreamArn:                          d.Kinesis.StreamARN,
		Region:                             d.Kinesis.Region,
		RoleArn:                            d.Kinesis.RoleARN,
		ExternalID:                         d.Kinesis.ExternalID,
		PartitionKey:                       string(d.Kinesis.EffectivePartitionKey()),
		CreatedAt:                          utc(d.CreatedAt),
		UpdatedAt:                          utc(d.UpdatedAt),
	}
}

func streamingDestinationOf(r db.StreamingDestination) *streaming.Destination {
	types := make([]streaming.EventType, len(r.EventTypes))
	for i, et := range r.EventTypes {
		types[i] = streaming.EventType(et)
	}
	return &streaming.Destination{
		ID:                                 idOf[core.StreamingDestinationID](r.ID),
		OrganizationID:                     idOf[core.OrganizationID](r.OrganizationID),
		Type:                               streaming.Type(r.Type),
		Active:                             r.Active,
		EventTypes:                         types,
		CustomerFullUsageExcludedPlanCodes: r.CustomerFullUsageExcludedPlanCodes,
		Kinesis: streaming.KinesisSettings{
			StreamARN:    r.StreamArn,
			Region:       r.Region,
			RoleARN:      r.RoleArn,
			ExternalID:   r.ExternalID,
			PartitionKey: streaming.PartitionKey(r.PartitionKey),
		},
		CreatedAt: utc(r.CreatedAt),
		UpdatedAt: utc(r.UpdatedAt),
	}
}

func (r streamingDestinationRepo) Create(ctx context.Context, d *streaming.Destination) error {
	if err := d.Validate(); err != nil {
		return err
	}
	q, err := r.t.queries(ctx, d.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateStreamingDestination(ctx, streamingDestinationParams(d))
	return insertErr("streaming destination", n, err)
}

func (r streamingDestinationRepo) Update(ctx context.Context, d *streaming.Destination) error {
	if err := d.Validate(); err != nil {
		return err
	}
	q, err := r.t.queries(ctx, d.OrganizationID)
	if err != nil {
		return err
	}
	// An event type is claimed once per organization: guard the update so
	// that a conflict keeps the transaction usable.
	var n int64
	err = r.t.guard(ctx, func() error {
		var err error
		n, err = q.UpdateStreamingDestination(ctx, db.UpdateStreamingDestinationParams(streamingDestinationParams(d)))
		return err
	})
	return updateErr("streaming destination", d.ID, n, err)
}

func (r streamingDestinationRepo) Get(ctx context.Context, org core.OrganizationID, id core.StreamingDestinationID) (*streaming.Destination, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetStreamingDestination(ctx, db.GetStreamingDestinationParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("streaming destination", id, err)
	}
	return streamingDestinationOf(row), nil
}

func (r streamingDestinationRepo) ForEvent(ctx context.Context, org core.OrganizationID, t streaming.EventType) ([]*streaming.Destination, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListStreamingDestinationsForEvent(ctx, db.ListStreamingDestinationsForEventParams{OrganizationID: org.UUID(), EventType: string(t)})
	if err != nil {
		return nil, mapErr(err)
	}
	return streamingDestinationsOf(rows), nil
}

func (r streamingDestinationRepo) ListActive(ctx context.Context, org core.OrganizationID) ([]*streaming.Destination, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListActiveStreamingDestinations(ctx, org.UUID())
	if err != nil {
		return nil, mapErr(err)
	}
	return streamingDestinationsOf(rows), nil
}

func streamingDestinationsOf(rows []db.StreamingDestination) []*streaming.Destination {
	out := make([]*streaming.Destination, len(rows))
	for i, row := range rows {
		out[i] = streamingDestinationOf(row)
	}
	return out
}

// streamingLockRepo implements streaming.DeliveryLockRepository.
type streamingLockRepo struct{ t *txState }

var _ streaming.DeliveryLockRepository = streamingLockRepo{}

func (r streamingLockRepo) ClaimEnqueue(ctx context.Context, org core.OrganizationID, customer core.CustomerID, t streaming.EventType, now, until time.Time) (bool, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return false, err
	}
	now, until = utc(now), utc(until)
	n, err := q.ClaimStreamingEnqueueLock(ctx, db.ClaimStreamingEnqueueLockParams{
		OrganizationID: org.UUID(), CustomerID: customer.UUID(), EventType: string(t), Until: &until, Now: &now,
	})
	if err != nil {
		return false, mapErr(err)
	}
	return n > 0, nil
}

func (r streamingLockRepo) ClaimRun(ctx context.Context, org core.OrganizationID, customer core.CustomerID, t streaming.EventType, now, until time.Time) (bool, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return false, err
	}
	now, until = utc(now), utc(until)
	taken, err := q.ClaimStreamingRunLock(ctx, db.ClaimStreamingRunLockParams{
		OrganizationID: org.UUID(), CustomerID: customer.UUID(), EventType: string(t), Until: &until, Now: &now,
	})
	if err != nil {
		return false, mapErr(err)
	}
	return taken, nil
}

func (r streamingLockRepo) ReleaseRun(ctx context.Context, org core.OrganizationID, customer core.CustomerID, t streaming.EventType) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	return mapErr(q.ReleaseStreamingRunLock(ctx, db.ReleaseStreamingRunLockParams{
		OrganizationID: org.UUID(), CustomerID: customer.UUID(), EventType: string(t),
	}))
}
