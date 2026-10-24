package metrics

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	expression "github.com/tanvoxyz/tanvo/libs/expression/go"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Field error codes of the service, named after lago's.
const (
	// CodeInvalidExpression: the expression does not parse (field:
	// expression).
	CodeInvalidExpression = "invalid_expression"
	// CodeInvalidEvent: the sample event cannot be evaluated by the
	// expression (field: event).
	CodeInvalidEvent = "invalid_event"
	// CodeReferencedByProductFilter: a product filter selects a filter of
	// the metric, which cannot be deleted (field: billable_metric).
	CodeReferencedByProductFilter = "referenced_by_product_filter"
)

// Resource is the resource name of the not-found errors.
const Resource = "billable_metric"

// Compiler parses expressions; *expression.Engine implements it.
type Compiler interface {
	// Compile parses src; an invalid expression is an
	// *expression.SyntaxError.
	Compile(ctx context.Context, src string) (*expression.Program, error)
}

// defaultCompiler compiles with the process-wide engine of package
// expression.
type defaultCompiler struct{}

func (defaultCompiler) Compile(_ context.Context, src string) (*expression.Program, error) {
	return expression.Compile(src)
}

// Service manages billable metrics. It uses d.Tx, d.Clock, d.IDs and
// d.Webhooks only.
type Service struct {
	d    *kernel.Deps
	expr Compiler
}

// New returns the service over d, parsing expressions with expr; a nil
// expr uses the process-wide engine of package expression. It fails when a
// dependency the service uses is nil.
func New(d *kernel.Deps, expr Compiler) (*Service, error) {
	switch {
	case d == nil, d.Tx == nil:
		return nil, fmt.Errorf("%w: Tx", kernel.ErrMissingDependency)
	case d.Clock == nil:
		return nil, fmt.Errorf("%w: Clock", kernel.ErrMissingDependency)
	case d.IDs == nil:
		return nil, fmt.Errorf("%w: IDs", kernel.ErrMissingDependency)
	case d.Webhooks == nil:
		return nil, fmt.Errorf("%w: Webhooks", kernel.ErrMissingDependency)
	}
	if expr == nil {
		expr = defaultCompiler{}
	}
	return &Service{d: d, expr: expr}, nil
}

// CreateInput is a metric to create.
type CreateInput struct {
	// Code is the unique code events carry.
	Code string
	// Name is the display name.
	Name string
	// Description is free text.
	Description *string
	// AggregationType is how events aggregate.
	AggregationType catalog.AggregationType
	// FieldName is the aggregated event property; dropped for count.
	FieldName *string
	// WeightedInterval is the time unit of weighted_sum.
	WeightedInterval catalog.WeightedInterval
	// Recurring carries the value over periods.
	Recurring bool
	// Expression computes the aggregated field from the event.
	Expression *string
	// RoundingFunction rounds the aggregated value.
	RoundingFunction catalog.RoundingFunction
	// RoundingPrecision is the number of decimals of the rounding.
	RoundingPrecision *int
	// Filters are the metric's filters; the IDs are assigned.
	Filters []catalog.MetricFilter
}

// Create creates a metric and its filters and emits
// billable_metric.created. A code used by another metric of the
// organization is the field error code value_already_exist, an expression
// that does not parse is expression invalid_expression. Lago also tracks
// the creation in Segment (create_service.rb:47); Tanvo's core sends no
// product analytics, so that is not ported.
//
// source: app/services/billable_metrics/create_service.rb:17-62
func (s *Service) Create(ctx context.Context, org core.OrganizationID, in CreateInput) (*catalog.BillableMetric, error) {
	now := s.d.Now()
	m := &catalog.BillableMetric{
		ID:                core.NewID[core.BillableMetricID](s.d.IDs),
		OrganizationID:    org,
		Code:              in.Code,
		Name:              in.Name,
		Description:       in.Description,
		AggregationType:   in.AggregationType,
		FieldName:         in.FieldName,
		WeightedInterval:  in.WeightedInterval,
		Recurring:         in.Recurring,
		Expression:        in.Expression,
		RoundingFunction:  in.RoundingFunction,
		RoundingPrecision: in.RoundingPrecision,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := s.validate(ctx, r, m); err != nil {
			return err
		}
		// Filters are created after the metric (create_service.rb:39-44):
		// the metric's own errors come first.
		if len(in.Filters) > 0 {
			filters, err := s.filters(nil, in.Filters)
			if err != nil {
				return err
			}
			m.Filters = filters
		}
		if err := r.Metrics.Create(ctx, m); err != nil {
			return codeConflict(err)
		}
		if err := s.logActivity(ctx, r, auditlog.ActivityBillableMetricCreated, nil, m); err != nil {
			return err
		}
		return kernel.Emit(ctx, s.d, org, webhook.EventTypeBillableMetricCreated, Resource, m.ID.UUID(), m)
	})
	if err != nil {
		return nil, fmt.Errorf("create billable metric: %w", err)
	}
	return m, nil
}

// UpdateInput is a partial update of a metric: only the set fields
// change.
type UpdateInput struct {
	// Name is the display name.
	Name core.Optional[string]
	// Description is free text.
	Description core.Optional[*string]
	// Filters replace the metric's filters; an empty list removes them.
	Filters core.Optional[[]catalog.MetricFilter]
	// Code is the unique code; locked once a plan uses the metric.
	Code core.Optional[string]
	// AggregationType is locked once a plan uses the metric.
	AggregationType core.Optional[catalog.AggregationType]
	// WeightedInterval is locked once a plan uses the metric.
	WeightedInterval core.Optional[catalog.WeightedInterval]
	// FieldName is locked once a plan uses the metric.
	FieldName core.Optional[*string]
	// Recurring is locked once a plan uses the metric.
	Recurring core.Optional[bool]
	// RoundingFunction is locked once a plan uses the metric.
	RoundingFunction core.Optional[catalog.RoundingFunction]
	// RoundingPrecision is locked once a plan uses the metric.
	RoundingPrecision core.Optional[*int]
	// Expression is locked once a plan uses the metric.
	Expression core.Optional[*string]
}

// Update changes the metric id and emits billable_metric.updated. The
// name, the description and the filters always change; the other fields
// change only while no plan that is not deleted charges the metric, and
// are ignored otherwise. Replacing the filters trims the charge filters
// that use removed values (and removes those left empty) and flags the
// draft invoices billing the metric for refresh.
//
// source: app/services/billable_metrics/update_service.rb:19-68
func (s *Service) Update(ctx context.Context, org core.OrganizationID, id core.BillableMetricID, in UpdateInput) (*catalog.BillableMetric, error) {
	var out *catalog.BillableMetric
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		m, err := r.Metrics.Get(ctx, org, id)
		if err != nil {
			return notFound(err)
		}
		before, err := auditlog.ObjectOf(m)
		if err != nil {
			return err
		}
		in.Name.Apply(&m.Name)
		in.Description.Apply(&m.Description)
		if in.Filters.Set {
			if err := s.updateFilters(ctx, r, m, in.Filters.Value); err != nil {
				return err
			}
		}
		attached, err := attachedToPlan(ctx, r, m)
		if err != nil {
			return err
		}
		// Only the name, the description and the filters may change once a
		// plan uses the metric (update_service.rb:39-55).
		if !attached {
			in.Code.Apply(&m.Code)
			in.AggregationType.Apply(&m.AggregationType)
			in.WeightedInterval.Apply(&m.WeightedInterval)
			in.FieldName.Apply(&m.FieldName)
			in.Recurring.Apply(&m.Recurring)
			in.RoundingFunction.Apply(&m.RoundingFunction)
			in.RoundingPrecision.Apply(&m.RoundingPrecision)
			in.Expression.Apply(&m.Expression)
		}
		if err := s.validate(ctx, r, m); err != nil {
			return err
		}
		m.UpdatedAt = s.d.Now()
		if err := r.Metrics.Update(ctx, m); err != nil {
			return codeConflict(err)
		}
		out = m
		if err := s.logActivity(ctx, r, auditlog.ActivityBillableMetricUpdated, before, m); err != nil {
			return err
		}
		return kernel.Emit(ctx, s.d, org, webhook.EventTypeBillableMetricUpdated, Resource, m.ID.UUID(), m)
	})
	if err != nil {
		return nil, fmt.Errorf("update billable metric %s: %w", id, err)
	}
	return out, nil
}

// Delete soft-deletes the metric id (its row and filters are kept, so
// history can still name it, and its code is free again), discards its
// alerts and its charges with their charge filters, flags the draft
// invoices billing it for refresh and emits billable_metric.deleted. The
// deleted metric is returned with DeletedAt set.
//
// The metric's filters are discarded by a [kernel.JobDestroyMetricFilters]
// enqueued with the deletion (lago BillableMetricFilters::DestroyAllJob,
// destroy_all_service.rb:18-25). The charge filters are discarded with the
// charges in the same transaction, as their filter values are stored on
// them. The catalog caches drop the metric when the discard commits (lago
// expire_cache). The deletion is refused
// while a product filter value selects one of the metric's filters
// (billable_metric referenced_by_product_filter).
//
// source: app/services/billable_metrics/destroy_service.rb:17-49
func (s *Service) Delete(ctx context.Context, org core.OrganizationID, id core.BillableMetricID) (*catalog.BillableMetric, error) {
	var out *catalog.BillableMetric
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		m, err := r.Metrics.Get(ctx, org, id)
		if err != nil {
			return notFound(err)
		}
		// A kept product filter value would be orphaned by the discard of
		// the metric's filters (destroy_service.rb:20-23).
		referenced, err := r.ProductFilters.ReferencesMetric(ctx, org, id)
		if err != nil {
			return fmt.Errorf("check product filters: %w", err)
		}
		if referenced {
			var v core.Validator
			v.Add("billable_metric", CodeReferencedByProductFilter)
			return v.Err()
		}
		// The drafts are found through the charges, so before deleting them
		// (destroy_service.rb:28-29).
		if _, err := r.Invoices.RequestRefreshByMetric(ctx, org, id); err != nil {
			return fmt.Errorf("flag draft invoices: %w", err)
		}
		now := s.d.Now()
		if err := r.Metrics.Discard(ctx, org, id, now); err != nil {
			return fmt.Errorf("discard metric: %w", err)
		}
		// The alerts are discarded, their thresholds kept (destroy_service.rb:35).
		if _, err := r.Alerts.DiscardByBillableMetric(ctx, org, id, now); err != nil {
			return fmt.Errorf("discard alerts: %w", err)
		}
		// The charges are discarded with their filters (destroy_service.rb:36,
		// destroy_all_service.rb:18-25).
		if _, err := r.Charges.DiscardByMetric(ctx, org, id, now); err != nil {
			return fmt.Errorf("discard charges: %w", err)
		}
		// The filters are discarded after the commit (destroy_service.rb:41).
		if err := kernel.Enqueue(ctx, r, org, kernel.JobDestroyMetricFilters, kernel.MetricArgs{BillableMetricID: id}, "", time.Time{}); err != nil {
			return err
		}
		m.DeletedAt, m.UpdatedAt = &now, now
		out = m
		if err := s.logActivity(ctx, r, auditlog.ActivityBillableMetricDeleted, nil, m); err != nil {
			return err
		}
		return kernel.Emit(ctx, s.d, org, webhook.EventTypeBillableMetricDeleted, Resource, m.ID.UUID(), m)
	})
	if err != nil {
		return nil, fmt.Errorf("delete billable metric %s: %w", id, err)
	}
	return out, nil
}

// logActivity records the activity log of m, whose attributes before an
// update are before (lago activity_loggable of create_service.rb:12,
// update_service.rb:14 and destroy_service.rb:12).
func (s *Service) logActivity(ctx context.Context, r port.Repos, t auditlog.ActivityType, before map[string]any, m *catalog.BillableMetric) error {
	return s.d.LogChange(ctx, r, kernel.Change{
		Type: t, OrganizationID: m.OrganizationID, ResourceType: auditlog.ResourceBillableMetric, ResourceID: m.ID.UUID(),
		Object: m, Before: before,
	})
}

// Get returns the metric id.
func (s *Service) Get(ctx context.Context, org core.OrganizationID, id core.BillableMetricID) (*catalog.BillableMetric, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.BillableMetric, error) {
		m, err := r.Metrics.Get(ctx, org, id)
		return m, notFound(err)
	})
}

// GetByCode returns the metric with code (the API finds metrics by code,
// billable_metrics_controller.rb:61).
func (s *Service) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*catalog.BillableMetric, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.BillableMetric, error) {
		m, err := r.Metrics.GetByCode(ctx, org, code)
		return m, notFound(err)
	})
}

// List returns a page of the organization's metrics matching f, most
// recent first (lago BillableMetricsQuery).
//
// source: app/queries/billable_metrics_query.rb:10-23
func (s *Service) List(ctx context.Context, org core.OrganizationID, f catalog.MetricListFilter, page core.PageRequest) (core.Page[*catalog.BillableMetric], error) {
	f.Order = core.OrderRecent
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (core.Page[*catalog.BillableMetric], error) {
		return r.Metrics.List(ctx, org, f, page)
	})
}

// SampleEvent is the event an expression is evaluated against.
type SampleEvent struct {
	// Code is the event's code.
	Code string
	// Timestamp is the event's instant; the zero value means now.
	Timestamp time.Time
	// Properties are the event's properties. Each value is converted to
	// the text Ruby's to_s gives it, as lago does
	// (evaluate_expression_service.rb:26; see rubyText).
	Properties map[string]any
}

// Evaluate evaluates expr against ev and returns the result's text. A
// blank expression is expression value_is_mandatory, one that does not
// parse expression invalid_expression, and an event it cannot be
// evaluated for event invalid_event.
//
// source: app/services/billable_metrics/evaluate_expression_service.rb:14-33
func (s *Service) Evaluate(ctx context.Context, expr string, ev SampleEvent) (string, error) {
	if expr == "" {
		return "", fieldError("expression", core.CodeRequired)
	}
	p, err := s.compile(ctx, expr)
	if err != nil {
		return "", err
	}
	ts := ev.Timestamp
	if ts.IsZero() {
		ts = s.d.Now()
	}
	props := make(map[string]any, len(ev.Properties))
	for k, v := range ev.Properties {
		props[k] = rubyText(v)
	}
	// Lago sends the timestamp in whole seconds (.to_i).
	v, err := p.Evaluate(ctx, expression.Event{Code: ev.Code, Timestamp: ts.Truncate(time.Second), Properties: props})
	if err != nil {
		if _, ok := errors.AsType[*expression.EvaluationError](err); ok && !errors.Is(err, expression.ErrTrap) {
			return "", fieldError("event", CodeInvalidEvent)
		}
		return "", fmt.Errorf("evaluate expression: %w", err)
	}
	if d, ok := v.Decimal(); ok {
		return decimalText(d), nil
	}
	return v.String(), nil
}

// decimalText renders a number result as lago's JSON does (a BigDecimal
// in Rails' "F" format): in plain notation, without trailing zeros, and
// with at least one fractional digit ("2.0", "1.5").
func decimalText(d money.Decimal) string {
	s := d.Reduce().String()
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// validate normalizes m and checks it as lago's model does: the domain
// rules, the code's uniqueness among the organization's metrics and the
// expression's syntax, reported together.
func (s *Service) validate(ctx context.Context, r port.Repos, m *catalog.BillableMetric) error {
	m.Normalize()
	var v core.Validator
	v.Merge("", m.Validate())
	if m.Code != "" {
		other, err := r.Metrics.GetByCode(ctx, m.OrganizationID, m.Code)
		switch {
		case err == nil:
			v.Check(other.ID == m.ID, "code", core.CodeAlreadyExists)
		case !errors.Is(err, core.ErrNotFound):
			return fmt.Errorf("check code: %w", err)
		}
	}
	if m.Expression != nil {
		if _, err := s.compile(ctx, *m.Expression); err != nil {
			if _, ok := errors.AsType[*core.ValidationError](err); !ok {
				return err
			}
			v.Add("expression", CodeInvalidExpression)
		}
	}
	return v.Err()
}

// compile parses src: a syntax error is expression invalid_expression.
func (s *Service) compile(ctx context.Context, src string) (*expression.Program, error) {
	p, err := s.expr.Compile(ctx, src)
	if err != nil {
		if _, ok := errors.AsType[*expression.SyntaxError](err); ok {
			return nil, fieldError("expression", CodeInvalidExpression)
		}
		return nil, fmt.Errorf("compile expression: %w", err)
	}
	return p, nil
}

// filters checks the requested filters as lago's batch service does and
// returns them sorted by key, deduplicated, keeping the IDs of the old
// filters with the same key.
//
// source: app/services/billable_metric_filters/create_or_update_batch_service.rb:27-31
func (s *Service) filters(old, in []catalog.MetricFilter) ([]catalog.MetricFilter, error) {
	if slices.ContainsFunc(in, func(f catalog.MetricFilter) bool { return len(f.Values) == 0 }) {
		return nil, fieldError("values", core.CodeRequired)
	}
	keys := map[string]bool{}
	for _, f := range in {
		if keys[f.Key] {
			return nil, fieldError("key", core.CodeAlreadyExists)
		}
		keys[f.Key] = true
	}
	out := make([]catalog.MetricFilter, len(in))
	var v core.Validator
	for i, f := range in {
		f.Values = uniq(f.Values)
		if o := slices.IndexFunc(old, func(x catalog.MetricFilter) bool { return x.Key == f.Key }); o >= 0 {
			f.ID = old[o].ID
		} else {
			f.ID = core.NewID[core.BillableMetricFilterID](s.d.IDs)
		}
		v.Merge(fmt.Sprintf("filters[%d]", i), f.Validate())
		out[i] = f
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	// Lago lists filters by key (app/models/billable_metric.rb:21).
	slices.SortFunc(out, func(a, b catalog.MetricFilter) int { return cmp.Compare(a.Key, b.Key) })
	return out, nil
}

// updateFilters replaces the filters of m with in and applies the change
// to the charge filters that use them: removed keys and values are trimmed
// from their values and the filters left empty are removed. Unless in is
// empty, the draft invoices billing the metric are flagged for refresh. The
// replacement is refused while it would remove a filter or a value that a
// product filter value selects (filters referenced_by_product_filter).
//
// source: app/services/billable_metric_filters/create_or_update_batch_service.rb:16-70
func (s *Service) updateFilters(ctx context.Context, r port.Repos, m *catalog.BillableMetric, in []catalog.MetricFilter) error {
	refs, err := r.ProductFilters.MetricReferences(ctx, m.OrganizationID, m.ID)
	if err != nil {
		return fmt.Errorf("load product filter values: %w", err)
	}
	var updated []catalog.MetricFilter
	if len(in) > 0 {
		if updated, err = s.filters(m.Filters, in); err != nil {
			return err
		}
	}
	if orphaning(m.Filters, updated, refs) {
		return fieldError("filters", CodeReferencedByProductFilter)
	}
	change := catalog.DiffMetricFilters(m.Filters, updated)
	m.Filters = updated
	if !change.IsZero() {
		charges, err := r.Charges.ListByMetric(ctx, m.OrganizationID, m.ID)
		if err != nil {
			return fmt.Errorf("load charges: %w", err)
		}
		for _, c := range charges {
			if !trimCharge(c, change) {
				continue
			}
			c.UpdatedAt = s.d.Now()
			if err := r.Charges.Update(ctx, c); err != nil {
				return fmt.Errorf("update charge %s: %w", c.ID, err)
			}
		}
	}
	if len(in) == 0 {
		return nil
	}
	// RefreshDraftInvoicesJob runs after the commit in lago; the flag is
	// set in the same transaction here (create_or_update_batch_service.rb:67),
	// so it commits with the change it reflects and a crash cannot lose it.
	if _, err := r.Invoices.RequestRefreshByMetric(ctx, m.OrganizationID, m.ID); err != nil {
		return fmt.Errorf("flag draft invoices: %w", err)
	}
	return nil
}

// orphaning reports whether replacing the filters old with updated would
// orphan a product filter value of refs: one that selects a removed filter,
// or a removed value of a kept filter. A value selecting the whole filter
// (nil) blocks only the filter's removal.
//
// source: app/services/billable_metric_filters/create_or_update_batch_service.rb:20, :32, :85-96
func orphaning(old, updated []catalog.MetricFilter, refs []catalog.ProductFilterValue) bool {
	for _, f := range old {
		i := slices.IndexFunc(updated, func(u catalog.MetricFilter) bool { return u.Key == f.Key })
		for _, v := range refs {
			if v.BillableMetricFilterID != f.ID {
				continue
			}
			if i < 0 || v.Value != nil && slices.Contains(f.Values, *v.Value) && !slices.Contains(updated[i].Values, *v.Value) {
				return true
			}
		}
	}
	return false
}

// trimCharge applies change to the filters of c, removing those left
// empty, and reports whether c changed.
func trimCharge(c *catalog.Charge, change catalog.MetricFilterChange) bool {
	changed := false
	kept := c.Filters[:0]
	for _, f := range c.Filters {
		before := f.Clone()
		if !catalog.TrimChargeFilter(&f, change) {
			changed = true
			continue
		}
		if !catalog.ChargeFilterValuesEqual(before.Values, f.Values) {
			changed = true
		}
		kept = append(kept, f)
	}
	c.Filters = kept
	return changed
}

// attachedToPlan reports whether a plan that is not deleted charges m
// (BillableMetric#attached_to_plan?).
//
// source: app/models/billable_metric.rb:96-100
func attachedToPlan(ctx context.Context, r port.Repos, m *catalog.BillableMetric) (bool, error) {
	charges, err := r.Charges.ListByMetric(ctx, m.OrganizationID, m.ID)
	if err != nil {
		return false, fmt.Errorf("load charges: %w", err)
	}
	if len(charges) == 0 {
		return false, nil
	}
	ids := map[core.PlanID]bool{}
	for _, c := range charges {
		ids[c.PlanID] = true
	}
	plans, err := r.Plans.ListByIDs(ctx, m.OrganizationID, slices.Collect(maps.Keys(ids)))
	if err != nil {
		return false, fmt.Errorf("load plans: %w", err)
	}
	return slices.ContainsFunc(plans, func(p *catalog.Plan) bool { return !p.Deleted() }), nil
}

func read[T any](ctx context.Context, d *kernel.Deps, fn func(context.Context, port.Repos) (T, error)) (T, error) {
	var out T
	err := d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		v, err := fn(ctx, r)
		out = v
		return err
	})
	return out, err
}

// notFound turns a missing metric into core.NotFound("billable_metric").
func notFound(err error) error {
	if errors.Is(err, core.ErrNotFound) {
		return core.NotFound(Resource)
	}
	return err
}

// codeConflict turns a uniqueness violation, which a concurrent write can
// still cause after validate, into code value_already_exist.
func codeConflict(err error) error {
	if errors.Is(err, core.ErrConflict) {
		return fieldError("code", core.CodeAlreadyExists)
	}
	return err
}

func fieldError(field, code string) error {
	return &core.ValidationError{Errors: []core.FieldError{{Field: field, Code: code}}}
}

func uniq(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
