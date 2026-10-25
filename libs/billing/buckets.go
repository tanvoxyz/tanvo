package billing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// UsageBucketLimits bound the usage bucket definitions an event store
// keeps (architecture §7.4).
type UsageBucketLimits struct {
	// MaxRowsPerBucket is the largest number of rows one bucket of one
	// subscription of a definition may hold (its combinations of
	// dimension values): past it, the definition is blocked
	// ([metering.BucketLifecycle.BlockBuckets]) and its reads stay on the
	// events. 0 blocks none.
	MaxRowsPerBucket int64
	// IdleTTL is how long a definition is kept without a read served from
	// its buckets, counted from its last read or its definition: past
	// it, the sweep drops it. 0 drops none for being idle.
	IdleTTL time.Duration
}

// DefaultUsageBucketLimits returns the default limits: 10 000 rows per
// bucket, 45 days idle (longer than a monthly billing period, so that the
// definitions read once a period are kept).
func DefaultUsageBucketLimits() UsageBucketLimits {
	return UsageBucketLimits{MaxRowsPerBucket: 10_000, IdleTTL: 45 * 24 * time.Hour}
}

// WithUsageBucketLimits sets the limits of the usage bucket definitions,
// [DefaultUsageBucketLimits] otherwise.
func WithUsageBucketLimits(l UsageBucketLimits) Option {
	return func(e *Engine) { e.bucketLimits = l }
}

// UsageBucketLimits returns the limits of the usage bucket definitions.
func (e *Engine) UsageBucketLimits() UsageBucketLimits { return e.bucketLimits }

// DefineUsageBuckets defines the usage bucket definition d of the
// organization org in the event store and backfills its buckets
// ([kernel.JobDefineUsageBuckets]). A store keeping no usage buckets has
// nothing to define, nor a blocked definition: their reads stay on the
// events. A definition whose buckets come out past
// [UsageBucketLimits.MaxRowsPerBucket] is blocked.
func (e *Engine) DefineUsageBuckets(ctx context.Context, org core.OrganizationID, d metering.BucketDefinition) error {
	if d.OrganizationID != org.String() || d.Code == "" {
		return fmt.Errorf("define usage buckets: %w: definition of %q for %q", core.ErrInvalidValue, d.OrganizationID, d.Code)
	}
	definer, ok := e.d.Events.(kernel.BucketDefiner)
	if !ok {
		return nil
	}
	err := definer.DefineBuckets(ctx, d)
	if errors.Is(err, metering.ErrBucketsUnavailable) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("define usage buckets of %s: %w", d.Code, err)
	}
	if _, err := e.BlockOversizedUsageBuckets(ctx, d); err != nil {
		return fmt.Errorf("define usage buckets of %s: %w", d.Code, err)
	}
	return nil
}

// BlockOversizedUsageBuckets blocks the definition d when one bucket of
// one subscription holds more rows than
// [UsageBucketLimits.MaxRowsPerBucket], and reports whether it did. The
// definition's dimensions are then unbounded, so that its buckets cost
// more than the events they stand for: its reads go back to the events,
// and the definition is not defined again until the block is lifted.
func (e *Engine) BlockOversizedUsageBuckets(ctx context.Context, d metering.BucketDefinition) (bool, error) {
	lc, ok := e.d.Events.(kernel.BucketLifecycle)
	limit := e.bucketLimits.MaxRowsPerBucket
	if !ok || limit <= 0 {
		return false, nil
	}
	st, err := lc.BucketStats(ctx, d)
	if err != nil {
		return false, fmt.Errorf("usage bucket stats: %w", err)
	}
	if st.MaxRowsPerBucket <= limit {
		return false, nil
	}
	if err := lc.BlockBuckets(ctx, d); err != nil {
		return false, fmt.Errorf("block usage buckets: %w", err)
	}
	e.d.Logger.WarnContext(ctx, "usage buckets: blocked a definition with unbounded dimensions",
		"organization_id", d.OrganizationID, "code", d.Code, "definition", d.ID(),
		"rows", st.Rows, "max_rows_per_bucket", st.MaxRowsPerBucket, "limit", limit)
	return true, nil
}

// Reasons the sweep drops a usage bucket definition
// ([Engine.SweepUsageBuckets]).
const (
	// BucketDropMetricDeleted drops the definitions of a code no kept
	// metric has.
	BucketDropMetricDeleted = "metric_deleted"
	// BucketDropOutdated drops the definitions the metric of their code no
	// longer reads: its aggregated property or filters changed
	// ([metering.BucketDefinitionCurrent]).
	BucketDropOutdated = "definition_outdated"
	// BucketDropNoCharges drops the definitions of a metric no kept charge
	// reads from the buckets ([metering.BucketServable]).
	BucketDropNoCharges = "no_bucket_charges"
	// BucketDropIdle drops the definitions no read used for
	// [UsageBucketLimits.IdleTTL].
	BucketDropIdle = "idle"
)

// SweptUsageBuckets is a definition the sweep dropped.
type SweptUsageBuckets struct {
	// Definition is the definition dropped.
	Definition metering.BucketDefinition
	// Blocked reports a block lifted with it.
	Blocked bool
	// Reason is why: one of the BucketDrop reasons.
	Reason string
}

// metricBuckets is what the sweep decides the definitions of one code on.
type metricBuckets struct {
	// metric is the kept metric of the code; nil when none.
	metric  metering.Metric
	filters []metering.MetricFilter
	deleted bool
	// served reports a kept charge of the metric the buckets serve.
	served bool
}

// SweepUsageBuckets drops the usage bucket definitions of the organization
// org nothing reads any more at the instant at
// ([kernel.JobSweepUsageBuckets]), and returns them. A definition is
// dropped when no kept metric has its code, when it is not a definition
// of the metric any more ([metering.BucketDefinitionCurrent]), when no
// kept charge of the metric reads the buckets ([metering.BucketServable]),
// or, unless blocked, when no read used it for
// [UsageBucketLimits.IdleTTL]. A block is lifted on the first three only,
// so that an oversized definition is not defined again a period later.
//
// Every drop is safe: a read that still needs the definition falls back
// to the events and defines it again, which costs one backfill.
func (e *Engine) SweepUsageBuckets(ctx context.Context, org core.OrganizationID, at time.Time) ([]SweptUsageBuckets, error) {
	lc, ok := e.d.Events.(kernel.BucketLifecycle)
	if !ok {
		return nil, nil
	}
	states, err := lc.BucketDefinitionStates(ctx, org.String())
	if err != nil {
		return nil, fmt.Errorf("sweep usage buckets: %w", err)
	}
	if len(states) == 0 {
		return nil, nil
	}
	metrics := map[string]*metricBuckets{}
	err = e.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		for _, st := range states {
			code := st.Definition.Code
			if _, ok := metrics[code]; ok {
				continue
			}
			mb, err := loadMetricBuckets(ctx, r, org, code)
			if err != nil {
				return err
			}
			metrics[code] = mb
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("sweep usage buckets: %w", err)
	}
	var (
		out  []SweptUsageBuckets
		errs []error
	)
	for _, st := range states {
		if st.Definition.OrganizationID != org.String() {
			continue
		}
		reason := sweepReason(st, metrics[st.Definition.Code], e.bucketLimits.IdleTTL, at)
		if reason == "" {
			continue
		}
		if err := lc.DropBuckets(ctx, st.Definition); err != nil {
			errs = append(errs, fmt.Errorf("drop usage buckets of %s: %w", st.Definition.Code, err))
			continue
		}
		e.d.Logger.InfoContext(ctx, "usage buckets: dropped a definition",
			"organization_id", org.String(), "code", st.Definition.Code, "definition", st.Definition.ID(),
			"reason", reason, "blocked", st.Blocked)
		out = append(out, SweptUsageBuckets{Definition: st.Definition, Blocked: st.Blocked, Reason: reason})
	}
	if err := errors.Join(errs...); err != nil {
		return out, fmt.Errorf("sweep usage buckets: %w", err)
	}
	return out, nil
}

// loadMetricBuckets loads the kept metric of code and whether a kept
// charge of it reads the buckets.
func loadMetricBuckets(ctx context.Context, r port.Repos, org core.OrganizationID, code string) (*metricBuckets, error) {
	m, err := r.Metrics.GetByCode(ctx, org, code)
	if errors.Is(err, core.ErrNotFound) {
		return &metricBuckets{deleted: true}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load metric %s: %w", code, err)
	}
	mb := &metricBuckets{metric: kernel.MeteringMetric(m), filters: kernel.MeteringMetricFilters(m)}
	charges, err := r.Charges.ListByMetric(ctx, org, m.ID)
	if err != nil {
		return nil, fmt.Errorf("list charges of metric %s: %w", code, err)
	}
	for _, c := range charges {
		if bucketCharge(mb.metric, c) {
			mb.served = true
			break
		}
	}
	return mb, nil
}

// bucketCharge reports whether the usage of the charge c of the metric m
// is served from the buckets.
func bucketCharge(m metering.Metric, c *catalog.Charge) bool {
	return metering.BucketServable(m, metering.BucketCharge{
		ChargeModel: string(c.ChargeModel), PayInAdvance: c.PayInAdvance, Prorated: c.Prorated,
		AcceptsTargetWallet: c.AcceptsTargetWallet,
	})
}

// sweepReason returns why the sweep drops the definition of st at the
// instant at, or "" when it keeps it.
func sweepReason(st metering.BucketDefinitionState, mb *metricBuckets, idle time.Duration, at time.Time) string {
	switch {
	case mb == nil || mb.deleted:
		return BucketDropMetricDeleted
	case !metering.BucketDefinitionCurrent(st.Definition, mb.metric, mb.filters):
		return BucketDropOutdated
	case !mb.served:
		return BucketDropNoCharges
	case !st.Blocked && idle > 0 && !st.LastUsed().IsZero() && st.LastUsed().Add(idle).Before(at):
		return BucketDropIdle
	}
	return ""
}
