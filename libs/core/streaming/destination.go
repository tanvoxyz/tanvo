package streaming

import (
	"fmt"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// EventType is a type of event a destination streams. Every type carries
// a version suffix, so a breaking payload change ships as a new type.
type EventType string

// EventType values.
const (
	// EventCustomerUsageRefreshed is "customer_usage.refreshed.v1": the
	// current usage of each subscription of a customer, as of a wallet
	// refresh.
	EventCustomerUsageRefreshed EventType = "customer_usage.refreshed.v1"
	// EventCustomerFullUsageRefreshed is
	// "customer_full_usage.refreshed.v1": the usage of every charge of each
	// subscription's plan, including charges without usage.
	EventCustomerFullUsageRefreshed EventType = "customer_full_usage.refreshed.v1"
)

// EventTypes returns the declared event types.
//
// source: app/models/streaming_destinations/base_destination.rb:11-13
func EventTypes() []EventType {
	return []EventType{EventCustomerUsageRefreshed, EventCustomerFullUsageRefreshed}
}

// Valid reports whether t is a declared event type.
func (t EventType) Valid() bool { return slices.Contains(EventTypes(), t) }

// String returns the stored form of t.
func (t EventType) String() string { return string(t) }

// Type is the transport of a destination.
type Type string

// Type values.
const (
	// TypeKinesis is "kinesis": an Amazon Kinesis data stream reached by
	// assuming an IAM role.
	TypeKinesis Type = "kinesis"
)

// Valid reports whether t is a declared destination type.
func (t Type) Valid() bool { return t == TypeKinesis }

// String returns the stored form of t.
func (t Type) String() string { return string(t) }

// PartitionKey is the strategy that picks the partition key of a record.
type PartitionKey string

// PartitionKey values.
const (
	// PartitionKeyCustomerExternalID is "customer_external_id": records of
	// one customer land on one shard, in order.
	PartitionKeyCustomerExternalID PartitionKey = "customer_external_id"
	// DefaultPartitionKey is the strategy of a destination that sets none.
	DefaultPartitionKey = PartitionKeyCustomerExternalID
)

// PartitionKeyMaxLength is the longest partition key Kinesis accepts, in
// characters.
const PartitionKeyMaxLength = 256

// Valid reports whether k is a declared strategy.
func (k PartitionKey) Valid() bool { return k == PartitionKeyCustomerExternalID }

// String returns the stored form of k.
func (k PartitionKey) String() string { return string(k) }

// Destination is a sink of an organization that receives the events of
// the types it claims. An event type is claimed by at most one destination
// of an organization, active or not, so deactivating a destination stops
// delivery without freeing its types (parity §1.40).
//
// source: app/models/streaming_destinations/base_destination.rb,
// app/models/streaming_destinations/kinesis_destination.rb
type Destination struct {
	// ID identifies the destination.
	ID core.StreamingDestinationID
	// OrganizationID owns the destination.
	OrganizationID core.OrganizationID
	// Type is the transport.
	Type Type
	// Active destinations receive events; an inactive one keeps its
	// settings and its claim.
	Active bool
	// EventTypes are the claimed event types, at least one.
	EventTypes []EventType
	// CustomerFullUsageExcludedPlanCodes are the plans whose subscriptions
	// get no [EventCustomerFullUsageRefreshed] event.
	CustomerFullUsageExcludedPlanCodes []string
	// Kinesis holds the settings of a [TypeKinesis] destination.
	Kinesis KinesisSettings
	// CreatedAt and UpdatedAt are the row timestamps.
	CreatedAt, UpdatedAt time.Time
}

// KinesisSettings are the settings of a Kinesis destination.
type KinesisSettings struct {
	// StreamARN is the ARN of the data stream.
	StreamARN string
	// Region is the AWS region of the stream and of the STS endpoint.
	Region string
	// RoleARN is the IAM role assumed to write to the stream.
	RoleARN string
	// ExternalID is passed to AssumeRole when set, for a writer role whose
	// trust policy requires one.
	ExternalID string
	// PartitionKey is the partition key strategy; empty means
	// [DefaultPartitionKey].
	PartitionKey PartitionKey
}

// EffectivePartitionKey returns the strategy, defaulted when unset.
//
// source: app/models/streaming_destinations/kinesis_destination.rb:11
func (s KinesisSettings) EffectivePartitionKey() PartitionKey {
	if s.PartitionKey == "" {
		return DefaultPartitionKey
	}
	return s.PartitionKey
}

// Normalize defaults the partition key strategy.
func (d *Destination) Normalize() {
	if d.Type == TypeKinesis {
		d.Kinesis.PartitionKey = d.Kinesis.EffectivePartitionKey()
	}
}

// Validate checks the destination: a declared type, at least one event
// type, each declared, and for Kinesis the stream ARN, region and role
// ARN set and a declared partition key strategy. The claim of each event
// type is checked by the repository.
//
// source: app/models/streaming_destinations/base_destination.rb:17-19,47-55,
// app/models/streaming_destinations/kinesis_destination.rb:13-16
func (d *Destination) Validate() error {
	var v core.Validator
	v.Enum("type", d.Type)
	if len(d.EventTypes) == 0 {
		v.Add("event_types", core.CodeRequired)
	} else {
		v.Check(!slices.ContainsFunc(d.EventTypes, func(t EventType) bool { return !t.Valid() }), "event_types", core.CodeInvalid)
	}
	if d.Type == TypeKinesis {
		v.Required("stream_arn", d.Kinesis.StreamARN)
		v.Required("region", d.Kinesis.Region)
		v.Required("role_arn", d.Kinesis.RoleARN)
		v.Check(d.Kinesis.EffectivePartitionKey().Valid(), "partition_key", core.CodeInvalid)
	}
	return v.Err()
}

// Streams reports whether d claims t.
func (d *Destination) Streams(t EventType) bool { return slices.Contains(d.EventTypes, t) }

// EventTypesFor returns the event types d streams for a subscription to
// the plan planCode: every claimed type, without
// [EventCustomerFullUsageRefreshed] when the plan is excluded from it.
//
// source: app/models/streaming_destinations/base_destination.rb:31-35
func (d *Destination) EventTypesFor(planCode string) []EventType {
	if !slices.Contains(d.CustomerFullUsageExcludedPlanCodes, planCode) {
		return slices.Clone(d.EventTypes)
	}
	return slices.DeleteFunc(slices.Clone(d.EventTypes), func(t EventType) bool { return t == EventCustomerFullUsageRefreshed })
}

// PartitionKeyFor returns the partition key of a record about the customer
// with the given external ID: by [PartitionKeyCustomerExternalID], the
// external ID cut to [PartitionKeyMaxLength] characters. A strategy
// written past validation is an error.
//
// source: app/models/streaming_destinations/kinesis_destination.rb:22-29
func (d *Destination) PartitionKeyFor(customerExternalID string) (string, error) {
	switch k := d.Kinesis.EffectivePartitionKey(); k {
	case PartitionKeyCustomerExternalID:
		if utf8.RuneCountInString(customerExternalID) <= PartitionKeyMaxLength {
			return customerExternalID, nil
		}
		return string([]rune(customerExternalID)[:PartitionKeyMaxLength]), nil
	default:
		return "", fmt.Errorf("streaming destination: %w: unsupported partition key %q", core.ErrInvalidValue, string(k))
	}
}
