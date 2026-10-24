package ingest

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// Catalog is what ingestion reads from the catalog. Every method is scoped
// to an organization and must be safe for concurrent use.
type Catalog interface {
	// Metric returns the billable metric of org with that code, or nil
	// (and no error) when there is none.
	Metric(ctx context.Context, org core.OrganizationID, code string) (*catalog.BillableMetric, error)
	// Subscriptions returns the subscriptions of org with that external
	// ID, in any status; [subscription.MatchEvent] picks the event's.
	Subscriptions(ctx context.Context, org core.OrganizationID, externalID string) ([]*subscription.Subscription, error)
	// Charges returns the charges of the plan pricing the metric.
	Charges(ctx context.Context, org core.OrganizationID, plan core.PlanID, metric core.BillableMetricID) ([]*catalog.Charge, error)
}

// ContractCatalog is the optional part of a [Catalog] that finds the
// contracts billing events in advance; an enricher whose catalog lacks it
// bills no contract event in advance.
type ContractCatalog interface {
	// InAdvanceContract returns the active contract of org with that
	// external ID when one of its rate cards bills a metered product of
	// the metric in advance, or the zero ID (and no error). Billing
	// resolves which card prices the event (lago
	// Events::PayInAdvanceBillingSegmentResolver), so this only bounds the
	// events it is asked about.
	InAdvanceContract(ctx context.Context, org core.OrganizationID, externalID string, metric core.BillableMetricID) (core.SubscriptionID, error)
}

// EventWriter stores batches of enriched events and returns the positions of
// the duplicates; the backends of libs/eventstore implement it
// (eventstore.Writer has the same method).
type EventWriter interface {
	Write(ctx context.Context, events []metering.Event) (duplicates []int, err error)
}

// TxStore is the storage of [ModePostgres].
type TxStore interface {
	// InTx runs fn in one PostgreSQL transaction, with the raw event log
	// and the enriched event writer bound to it; ctx passed to fn carries
	// the transaction, so that the [InAdvanceBiller] and [UsageSignaler]
	// called inside fn join it (port.Transactor semantics). The
	// transaction commits when fn returns nil.
	InTx(ctx context.Context, fn func(ctx context.Context, log event.Log, enriched EventWriter) error) error
}

// RawProducer produces raw events to the events.raw topic ([ModeKafka]).
type RawProducer interface {
	// ProduceRaw produces the messages and returns once every one is
	// acknowledged; an error means some may not have been.
	ProduceRaw(ctx context.Context, msgs []RawMessage) error
}

// InAdvanceBiller bills the events of in-advance charges (domain §22.4).
// libs/billing implements it. Calls are at least once: the implementation
// must be idempotent on the deduplication key of the events (organization,
// external subscription ID, transaction ID), as lago-api's fee creation
// deduplicates on the event source.
type InAdvanceBiller interface {
	// BillInAdvance bills the events, which all have a subscription or a
	// contract ([Enriched.InAdvanceTarget]) and [Enriched.InAdvance] set. In [ModePostgres] ctx carries the
	// ingestion transaction.
	BillInAdvance(ctx context.Context, events []*Enriched) error
}

// UsageSignaler receives the usage refresh signals of ingested events
// (architecture §7.1). Signals are hints: the periodic sweep stays
// authoritative, so a lost or repeated signal only delays or repeats a
// refresh.
type UsageSignaler interface {
	// SignalUsage delivers the signals. In [ModePostgres] ctx carries the
	// ingestion transaction.
	SignalUsage(ctx context.Context, signals []UsageSignal) error
}
