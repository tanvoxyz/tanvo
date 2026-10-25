package porttest

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/streaming"
)

// streamingFixture returns an active Kinesis destination of org claiming
// types.
//
// source: spec/factories/streaming_destinations.rb
func streamingFixture(e *env, org core.OrganizationID, types ...streaming.EventType) *streaming.Destination {
	return &streaming.Destination{
		ID: core.NewID[core.StreamingDestinationID](e.ids), OrganizationID: org, Type: streaming.TypeKinesis,
		Active: true, EventTypes: types,
		Kinesis: streaming.KinesisSettings{
			StreamARN:    "arn:aws:kinesis:eu-west-1:123456789012:stream/tanvo-streaming-sandbox",
			Region:       "eu-west-1",
			RoleARN:      "arn:aws:iam::123456789012:role/tanvo-streaming-sandbox-writer",
			PartitionKey: streaming.PartitionKeyCustomerExternalID,
		},
		CreatedAt: now, UpdatedAt: now,
	}
}

func testStreaming(t *testing.T, h Harness) {
	cu, fu := streaming.EventCustomerUsageRefreshed, streaming.EventCustomerFullUsageRefreshed

	t.Run("destinations", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org := a.OrganizationID
		d := streamingFixture(e, org, cu, fu)
		d.CustomerFullUsageExcludedPlanCodes = []string{"self_serve_monthly"}
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create()", r.StreamingDestinations.Create(ctx, d))
			// source: spec/models/streaming_destinations/kinesis_destination_spec.rb:55
			got, err := r.StreamingDestinations.Get(ctx, org, d.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, d)
			_, err = r.StreamingDestinations.Get(ctx, b.OrganizationID, d.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)

			// source: spec/models/streaming_destinations/kinesis_destination_spec.rb:62
			d.Kinesis.ExternalID = "00000000-0000-4000-8000-000000000001"
			// source: spec/models/streaming_destinations/base_destination_spec.rb:68
			d.Kinesis.Region = "us-east-1"
			d.UpdatedAt = at(time.Hour)
			noErr(t, "Update() does not conflict with itself", r.StreamingDestinations.Update(ctx, d))
			got, err = r.StreamingDestinations.Get(ctx, org, d.ID)
			noErr(t, "Get() updated", err)
			equal(t, "Get() updated", got, d)

			// source: spec/models/streaming_destinations/kinesis_destination_spec.rb:70
			unset := streamingFixture(e, b.OrganizationID, cu)
			unset.Kinesis.PartitionKey = ""
			noErr(t, "Create() without a partition key", r.StreamingDestinations.Create(ctx, unset))
			got, err = r.StreamingDestinations.Get(ctx, b.OrganizationID, unset.ID)
			noErr(t, "Get() without a partition key", err)
			equal(t, "partition key", got.Kinesis.PartitionKey, streaming.DefaultPartitionKey)

			invalid := streamingFixture(e, org)
			wantErr(t, "Create() without event types", r.StreamingDestinations.Create(ctx, invalid), core.ErrInvalidValue)
			return nil
		})
	})

	t.Run("claims", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org := a.OrganizationID
		current := streamingFixture(e, org, cu)
		current.Active = false
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create()", r.StreamingDestinations.Create(ctx, current))
			// source: spec/models/streaming_destinations/base_destination_spec.rb:42
			// source: spec/models/streaming_destinations/base_destination_spec.rb:52
			wantErr(t, "Create() claiming a taken type", r.StreamingDestinations.Create(ctx, streamingFixture(e, org, cu)), core.ErrConflict)
			wantErr(t, "Create() overlapping a taken type", r.StreamingDestinations.Create(ctx, streamingFixture(e, org, fu, cu)), core.ErrConflict)
			// source: spec/models/streaming_destinations/base_destination_spec.rb:62
			noErr(t, "Create() in another organization", r.StreamingDestinations.Create(ctx, streamingFixture(e, b.OrganizationID, cu)))
			full := streamingFixture(e, org, fu)
			noErr(t, "Create() claiming the other type", r.StreamingDestinations.Create(ctx, full))
			full.EventTypes = []streaming.EventType{fu, cu}
			wantErr(t, "Update() claiming a taken type", r.StreamingDestinations.Update(ctx, full), core.ErrConflict)
			full.EventTypes = []streaming.EventType{fu}
			full.Active = false
			noErr(t, "Update() after a conflict", r.StreamingDestinations.Update(ctx, full))
			return nil
		})
	})

	t.Run("for event", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org := a.OrganizationID
		d := streamingFixture(e, org, cu)
		e.tx(func(ctx context.Context, r port.Repos) error {
			// source: spec/models/streaming_destinations/base_destination_spec.rb:115
			got, err := r.StreamingDestinations.ForEvent(ctx, org, cu)
			noErr(t, "ForEvent() without destinations", err)
			equal(t, "ForEvent() without destinations", len(got), 0)

			noErr(t, "Create()", r.StreamingDestinations.Create(ctx, d))
			// source: spec/models/streaming_destinations/base_destination_spec.rb:109
			// source: spec/models/streaming_destinations/base_destination_spec.rb:124
			got, err = r.StreamingDestinations.ForEvent(ctx, org, cu)
			noErr(t, "ForEvent()", err)
			equal(t, "ForEvent()", got, []*streaming.Destination{d})
			got, err = r.StreamingDestinations.ListActive(ctx, org)
			noErr(t, "ListActive()", err)
			equal(t, "ListActive()", got, []*streaming.Destination{d})

			// source: spec/models/streaming_destinations/base_destination_spec.rb:134
			got, err = r.StreamingDestinations.ForEvent(ctx, org, fu)
			noErr(t, "ForEvent() of an unclaimed type", err)
			equal(t, "ForEvent() of an unclaimed type", len(got), 0)
			// source: spec/models/streaming_destinations/base_destination_spec.rb:140
			got, err = r.StreamingDestinations.ForEvent(ctx, b.OrganizationID, cu)
			noErr(t, "ForEvent() of another organization", err)
			equal(t, "ForEvent() of another organization", len(got), 0)

			// source: spec/models/streaming_destinations/base_destination_spec.rb:128
			d.Active = false
			noErr(t, "Update() inactive", r.StreamingDestinations.Update(ctx, d))
			got, err = r.StreamingDestinations.ForEvent(ctx, org, cu)
			noErr(t, "ForEvent() inactive", err)
			equal(t, "ForEvent() inactive", len(got), 0)
			got, err = r.StreamingDestinations.ListActive(ctx, org)
			noErr(t, "ListActive() inactive", err)
			equal(t, "ListActive() inactive", len(got), 0)
			return nil
		})
	})

	t.Run("delivery locks", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, _ := e.twoOrgs()
		org := a.OrganizationID
		c1 := couponFixtureCustomer(e, a, "cust_1")
		c2 := couponFixtureCustomer(e, a, "cust_2")
		ttl := streaming.LockTTL
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create() customer", r.Customers.Create(ctx, c1))
			noErr(t, "Create() customer", r.Customers.Create(ctx, c2))
			l := r.StreamingDeliveryLocks
			claim := func(what string, ok bool, err error, want bool) {
				t.Helper()
				noErr(t, what, err)
				equal(t, what, ok, want)
			}
			// source: spec/jobs/deliver_event_job_spec.rb:25
			ok, err := l.ClaimEnqueue(ctx, org, c1.ID, cu, now, now.Add(ttl))
			claim("ClaimEnqueue()", ok, err, true)
			ok, err = l.ClaimEnqueue(ctx, org, c1.ID, cu, at(time.Second), at(time.Second+ttl))
			claim("ClaimEnqueue() while waiting", ok, err, false)
			ok, err = l.ClaimEnqueue(ctx, org, c2.ID, cu, now, now.Add(ttl))
			claim("ClaimEnqueue() of another customer", ok, err, true)
			ok, err = l.ClaimEnqueue(ctx, org, c1.ID, fu, now, now.Add(ttl))
			claim("ClaimEnqueue() of another event type", ok, err, true)

			// source: spec/jobs/deliver_event_job_spec.rb:32
			// source: spec/jobs/deliver_event_job_spec.rb:46
			ok, err = l.ClaimRun(ctx, org, c1.ID, cu, at(time.Minute), at(time.Minute+ttl))
			claim("ClaimRun()", ok, err, true)
			ok, err = l.ClaimEnqueue(ctx, org, c1.ID, cu, at(2*time.Minute), at(2*time.Minute+ttl))
			claim("ClaimEnqueue() while running", ok, err, true)
			// source: spec/jobs/deliver_event_job_spec.rb:36
			ok, err = l.ClaimRun(ctx, org, c1.ID, cu, at(3*time.Minute), at(3*time.Minute+ttl))
			claim("ClaimRun() while running", ok, err, false)
			ok, err = l.ClaimEnqueue(ctx, org, c1.ID, cu, at(3*time.Minute), at(3*time.Minute+ttl))
			claim("ClaimEnqueue() after a superseded run", ok, err, true)

			noErr(t, "ReleaseRun()", l.ReleaseRun(ctx, org, c1.ID, cu))
			ok, err = l.ClaimRun(ctx, org, c1.ID, cu, at(4*time.Minute), at(4*time.Minute+ttl))
			claim("ClaimRun() after release", ok, err, true)
			ok, err = l.ClaimRun(ctx, org, c1.ID, cu, at(5*time.Minute+ttl), at(5*time.Minute+2*ttl))
			claim("ClaimRun() after expiry", ok, err, true)
			ok, err = l.ClaimEnqueue(ctx, org, c2.ID, cu, at(ttl), at(2*ttl))
			claim("ClaimEnqueue() after expiry", ok, err, true)
			noErr(t, "ReleaseRun() without a lock", l.ReleaseRun(ctx, org, c2.ID, fu))
			return nil
		})
	})
}
