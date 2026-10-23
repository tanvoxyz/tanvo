package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	expression "github.com/tanvoxyz/tanvo/libs/expression/go"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Event sources ([Enriched.Source], [RawMessage.Source]).
const (
	// SourceAPI is the event API, which evaluates expressions at intake.
	SourceAPI = "api"
)

// Enriched is an event resolved against the catalog: one row per event,
// with no charge or filter column (architecture §7.2). It is also the
// message of the events.enriched topic.
type Enriched struct {
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID `json:"organization_id"`
	// TransactionID is the event's idempotency key.
	TransactionID string `json:"transaction_id"`
	// Code is the billable metric code.
	Code string `json:"code"`
	// ExternalSubscriptionID is the subscription's external ID.
	ExternalSubscriptionID string `json:"external_subscription_id"`
	// Timestamp is the instant of the usage.
	Timestamp time.Time `json:"timestamp"`
	// Properties are the event properties, with the expression's result
	// stored under the metric's field name.
	Properties map[string]string `json:"properties"`
	// PreciseTotalAmountCents is the precise amount of a dynamic charge's
	// event.
	PreciseTotalAmountCents *money.Decimal `json:"precise_total_amount_cents,omitempty"`
	// IngestedAt is the ingestion instant; the watermark of usage refresh
	// signals.
	IngestedAt time.Time `json:"ingested_at"`
	// EnrichedAt is the enrichment instant.
	EnrichedAt time.Time `json:"enriched_at"`
	// Source is where the event came from ([SourceAPI] or a connector).
	Source string `json:"source,omitempty"`

	// SubscriptionID is the matched subscription; zero when none matched.
	SubscriptionID core.SubscriptionID `json:"subscription_id,omitzero"`
	// CustomerID is the matched subscription's customer.
	CustomerID core.CustomerID `json:"customer_id,omitzero"`
	// PlanID is the matched subscription's plan.
	PlanID core.PlanID `json:"plan_id,omitzero"`
	// BillableMetricID is the metric with the event's code; zero when no
	// metric has it.
	BillableMetricID core.BillableMetricID `json:"billable_metric_id,omitzero"`
	// AggregationType is the metric's aggregation.
	AggregationType catalog.AggregationType `json:"aggregation_type,omitempty"`
	// Value is the aggregated value as written: "1" for count metrics, the
	// field_name property otherwise ("0" when it is missing); nil without
	// a metric.
	Value *string `json:"value"`
	// DecimalValue is Value as a decimal, zero when Value is not a number.
	DecimalValue money.Decimal `json:"decimal_value"`
	// OperationType is the operation of a unique count event: add by
	// default, empty when the event names an unknown one.
	OperationType event.OperationType `json:"operation_type,omitempty"`
	// GroupedBy maps the pricing group keys of the metric's charges on the
	// plan to the event's values (nil when the property is missing).
	GroupedBy map[string]*string `json:"grouped_by,omitempty"`
	// InAdvance reports that a charge of the plan bills the metric in
	// advance.
	InAdvance bool `json:"in_advance,omitempty"`
}

// Metering returns the event store row of e: the store keeps events by
// external subscription ID and reads values from the properties.
func (e *Enriched) Metering() metering.Event {
	return metering.Event{
		OrganizationID:          e.OrganizationID.String(),
		SubscriptionID:          e.ExternalSubscriptionID,
		Code:                    e.Code,
		TransactionID:           e.TransactionID,
		Timestamp:               e.Timestamp,
		Properties:              metering.Properties(e.Properties),
		PreciseTotalAmountCents: e.PreciseTotalAmountCents,
		IngestedAt:              e.IngestedAt,
	}
}

// Key returns the Kafka partition key of e on the events.enriched topic
// ([SubscriptionPartitionKey]): (organization, external subscription ID
// of the event payload).
func (e *Enriched) Key() []byte {
	return SubscriptionPartitionKey(e.OrganizationID, e.ExternalSubscriptionID)
}

// PartitionKey returns the partition key of a raw event on the events.raw
// topic: the deduplication key (organization, external subscription ID,
// transaction ID). Every copy of a key goes through one enrichment
// consumer, in order, and so reaches its events.enriched partition in
// order; the events of a busy subscription still spread over the
// enrichment consumers, which a key of (organization, subscription) would
// not allow.
func PartitionKey(org core.OrganizationID, externalSubscriptionID, transactionID string) []byte {
	return []byte(org.String() + "/" + externalSubscriptionID + "/" + transactionID)
}

// SubscriptionPartitionKey returns the partition key of an enriched event
// on the events.enriched topic: (organization, external subscription ID),
// so that one writer consumer stores every event of a subscription, and
// maintains its usage buckets alone. The ID is the one the event carries,
// not the subscription enrichment resolved, so that the key depends on
// the payload only. An event without one (validation rejects it, but a
// connector may produce one) keys on the organization alone, the key of
// the store's "" subscription.
//
// The key holds the subscription of the deduplication key: every copy of
// a key lands on one partition, in order, and one writer consumer at a
// time sees the first before the later ones. It also caps the throughput
// of one subscription at one writer consumer, and the events without a
// subscription of an organization share one key.
func SubscriptionPartitionKey(org core.OrganizationID, externalSubscriptionID string) []byte {
	return []byte(org.String() + "/" + externalSubscriptionID)
}

// ExpressionError reports a billable metric expression that cannot be
// evaluated for an event; such an event is invalid, it will never succeed.
type ExpressionError struct {
	// Code is the metric code.
	Code string
	// Expression is the metric's expression.
	Expression string
	// Err is the evaluation error.
	Err error
}

// Error implements error.
func (e *ExpressionError) Error() string {
	return fmt.Sprintf("ingest: expression %q of metric %q: %v", e.Expression, e.Code, e.Err)
}

// Unwrap returns the evaluation error.
func (e *ExpressionError) Unwrap() error { return e.Err }

// Programs compiles and keeps metric expressions. It is safe for concurrent
// use.
type Programs struct {
	engine *expression.Engine
	mu     sync.Mutex
	byText map[string]*expression.Program
}

// NewPrograms returns programs compiled by engine, or by the default engine
// of libs/expression when engine is nil.
func NewPrograms(engine *expression.Engine) *Programs {
	return &Programs{engine: engine, byText: map[string]*expression.Program{}}
}

// maxPrograms bounds the compiled programs kept.
const maxPrograms = 10_000

func (p *Programs) compile(ctx context.Context, src string) (*expression.Program, error) {
	p.mu.Lock()
	prog, ok := p.byText[src]
	p.mu.Unlock()
	if ok {
		return prog, nil
	}
	var err error
	if p.engine != nil {
		prog, err = p.engine.Compile(ctx, src)
	} else {
		prog, err = expression.Compile(src)
	}
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if len(p.byText) >= maxPrograms {
		clear(p.byText)
	}
	p.byText[src] = prog
	p.mu.Unlock()
	return prog, nil
}

// Apply evaluates the metric's expression for e and stores its result in
// e.Properties under the metric's field name. A metric without an
// expression leaves e unchanged. An expression that cannot be evaluated is
// an [*ExpressionError]; other errors are failures of the engine.
func (p *Programs) Apply(ctx context.Context, m *catalog.BillableMetric, e *event.Event) error {
	if m == nil || m.Expression == nil || *m.Expression == "" || m.FieldName == nil {
		return nil
	}
	prog, err := p.compile(ctx, *m.Expression)
	if err != nil {
		var se *expression.SyntaxError
		if errors.As(err, &se) {
			return &ExpressionError{Code: m.Code, Expression: *m.Expression, Err: err}
		}
		return fmt.Errorf("ingest: compile expression of metric %q: %w", m.Code, err)
	}
	props := make(map[string]any, len(e.Properties))
	for k, v := range e.Properties {
		props[k] = v
	}
	v, err := prog.Evaluate(ctx, expression.Event{Code: e.Code, Timestamp: e.Timestamp, Properties: props})
	if err != nil {
		var ee *expression.EvaluationError
		if errors.As(err, &ee) {
			return &ExpressionError{Code: m.Code, Expression: *m.Expression, Err: err}
		}
		return fmt.Errorf("ingest: evaluate expression of metric %q: %w", m.Code, err)
	}
	if e.Properties == nil {
		e.Properties = map[string]string{}
	}
	e.Properties[*m.FieldName] = v.String()
	return nil
}

// Enricher resolves events against a [Catalog]. It is safe for concurrent
// use.
type Enricher struct {
	catalog  Catalog
	programs *Programs
	clock    core.Clock
}

// NewEnricher returns an enricher reading c and evaluating expressions with
// programs (a default [Programs] when nil), stamping enrichment times with
// clock (the system clock when nil).
func NewEnricher(c Catalog, programs *Programs, clock core.Clock) *Enricher {
	if programs == nil {
		programs = NewPrograms(nil)
	}
	if clock == nil {
		clock = core.SystemClock{}
	}
	return &Enricher{catalog: c, programs: programs, clock: clock}
}

// Enrich resolves e. When evaluate is set, the metric's expression is
// evaluated first (events of connectors); events of the API had it
// evaluated at intake. e is not modified.
//
// An event without a metric or a subscription is enriched without them:
// it is stored, and post-validation reports it. Catalog failures are
// returned as errors, an expression that cannot be evaluated as an
// [*ExpressionError].
func (en *Enricher) Enrich(ctx context.Context, e *event.Event, source string, evaluate bool) (*Enriched, error) {
	m, err := en.catalog.Metric(ctx, e.OrganizationID, e.Code)
	if err != nil {
		return nil, err
	}
	if evaluate {
		e = e.Clone()
		if err := en.programs.Apply(ctx, m, e); err != nil {
			return nil, err
		}
	}
	out := &Enriched{
		OrganizationID:          e.OrganizationID,
		TransactionID:           e.TransactionID,
		Code:                    e.Code,
		ExternalSubscriptionID:  e.ExternalSubscriptionID,
		Timestamp:               e.Timestamp,
		Properties:              maps.Clone(e.Properties),
		PreciseTotalAmountCents: e.PreciseTotalAmount,
		IngestedAt:              e.IngestedAt,
		EnrichedAt:              en.clock.Now().UTC().Truncate(time.Microsecond),
		Source:                  source,
		DecimalValue:            money.Zero,
	}
	if out.Properties == nil {
		out.Properties = map[string]string{}
	}
	if m != nil {
		withMetric(out, m)
	}
	subs, err := en.catalog.Subscriptions(ctx, e.OrganizationID, e.ExternalSubscriptionID)
	if err != nil {
		return nil, err
	}
	sub := subscription.MatchEvent(subs, e.Timestamp, m != nil && m.Recurring)
	if sub == nil {
		return out, nil
	}
	out.SubscriptionID, out.CustomerID, out.PlanID = sub.ID, sub.CustomerID, sub.PlanID
	if m == nil {
		return out, nil
	}
	charges, err := en.catalog.Charges(ctx, e.OrganizationID, sub.PlanID, m.ID)
	if err != nil {
		return nil, err
	}
	withCharges(out, charges)
	return out, nil
}

// withMetric sets the metric columns (lago-api's Events::EnrichService).
func withMetric(out *Enriched, m *catalog.BillableMetric) {
	out.BillableMetricID, out.AggregationType = m.ID, m.AggregationType
	var value string
	switch {
	case m.AggregationType == catalog.AggregationTypeCount:
		value = "1"
	case m.FieldName != nil && out.Properties[*m.FieldName] != "":
		value = out.Properties[*m.FieldName]
	default:
		value = "0"
	}
	out.Value = &value
	if d, err := money.Parse(value); err == nil && numericText.MatchString(value) {
		out.DecimalValue = d
	}
	if m.AggregationType == catalog.AggregationTypeUniqueCount {
		op := event.OperationTypeAdd
		if s, ok := out.Properties[event.OperationTypeProperty]; ok {
			op = event.OperationType(s)
		}
		if op.Valid() {
			out.OperationType = op
		}
	}
}

// pricingKeys is the part of charge properties naming pricing group keys.
type pricingKeys struct {
	PricingGroupKeys []string `json:"pricing_group_keys"`
}

func groupKeys(p catalog.Properties, into map[string]bool) {
	if p.IsZero() {
		return
	}
	var k pricingKeys
	if json.Unmarshal(p.Raw(), &k) != nil {
		return
	}
	for _, key := range k.PricingGroupKeys {
		into[key] = true
	}
}

// withCharges sets the in-advance flag and the grouping keys: the pricing
// group keys of the charges and of their filters.
func withCharges(out *Enriched, charges []*catalog.Charge) {
	keys := map[string]bool{}
	for _, ch := range charges {
		out.InAdvance = out.InAdvance || ch.PayInAdvance
		groupKeys(ch.Properties, keys)
		for _, f := range ch.Filters {
			groupKeys(f.Properties, keys)
		}
	}
	if len(keys) == 0 {
		return
	}
	out.GroupedBy = make(map[string]*string, len(keys))
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		if v, ok := out.Properties[k]; ok {
			out.GroupedBy[k] = &v
		} else {
			out.GroupedBy[k] = nil
		}
	}
}
