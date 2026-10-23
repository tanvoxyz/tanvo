package rpc

import (
	"context"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/metrics"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	catalogv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1/catalogv1connect"
)

// BillableMetrics serves tanvo.catalog.v1.BillableMetricService over the
// billing library's metric service (lago Api::V1::BillableMetricsController).
// It is the reference service of the playbook in the package comment.
type BillableMetrics struct {
	catalogv1connect.UnimplementedBillableMetricServiceHandler

	svc *metrics.Service
}

var _ catalogv1connect.BillableMetricServiceHandler = (*BillableMetrics)(nil)

// NewBillableMetrics returns the service over svc.
func NewBillableMetrics(svc *metrics.Service) *BillableMetrics { return &BillableMetrics{svc: svc} }

var (
	aggregationTypes = NewEnum(map[catalog.AggregationType]catalogv1.AggregationType{
		catalog.AggregationTypeCount:       catalogv1.AggregationType_AGGREGATION_TYPE_COUNT,
		catalog.AggregationTypeSum:         catalogv1.AggregationType_AGGREGATION_TYPE_SUM,
		catalog.AggregationTypeMax:         catalogv1.AggregationType_AGGREGATION_TYPE_MAX,
		catalog.AggregationTypeUniqueCount: catalogv1.AggregationType_AGGREGATION_TYPE_UNIQUE_COUNT,
		catalog.AggregationTypeWeightedSum: catalogv1.AggregationType_AGGREGATION_TYPE_WEIGHTED_SUM,
		catalog.AggregationTypeLatest:      catalogv1.AggregationType_AGGREGATION_TYPE_LATEST,
	})
	weightedIntervals = NewEnum(map[catalog.WeightedInterval]catalogv1.WeightedInterval{
		catalog.WeightedIntervalSeconds: catalogv1.WeightedInterval_WEIGHTED_INTERVAL_SECONDS,
	})
	roundingFunctions = NewEnum(map[catalog.RoundingFunction]catalogv1.RoundingFunction{
		catalog.RoundingFunctionRound: catalogv1.RoundingFunction_ROUNDING_FUNCTION_ROUND,
		catalog.RoundingFunctionFloor: catalogv1.RoundingFunction_ROUNDING_FUNCTION_FLOOR,
		catalog.RoundingFunctionCeil:  catalogv1.RoundingFunction_ROUNDING_FUNCTION_CEIL,
	})
)

// metricFilterFields are the fields ListBillableMetrics filters on.
var metricFilterFields = []string{"aggregation_type", "recurring", "plan_id"}

// metricUpdateFields are the update_mask paths of UpdateBillableMetric.
var metricUpdateFields = []string{
	"code", "name", "description", "aggregation_type", "field_name", "weighted_interval",
	"recurring", "expression", "rounding_function", "rounding_precision", "filters",
}

// CreateBillableMetric implements BillableMetricService.CreateBillableMetric.
func (s *BillableMetrics) CreateBillableMetric(ctx context.Context, req *catalogv1.CreateBillableMetricRequest) (*catalogv1.CreateBillableMetricResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetBillableMetric()
	m, err := s.svc.Create(ctx, p.OrganizationID, metrics.CreateInput{
		Code:              b.GetCode(),
		Name:              b.GetName(),
		Description:       Opt(b.HasDescription(), b.GetDescription()),
		AggregationType:   aggregationTypes.Core(b.GetAggregationType()),
		FieldName:         Opt(b.HasFieldName(), b.GetFieldName()),
		WeightedInterval:  weightedIntervals.Core(b.GetWeightedInterval()),
		Recurring:         b.GetRecurring(),
		Expression:        Opt(b.HasExpression(), b.GetExpression()),
		RoundingFunction:  roundingFunctions.Core(b.GetRoundingFunction()),
		RoundingPrecision: OptInt(b.HasRoundingPrecision(), b.GetRoundingPrecision()),
		Filters:           metricFilters(b.GetFilters()),
	})
	if err != nil {
		return nil, metricError(ctx, err)
	}
	return catalogv1.CreateBillableMetricResponse_builder{BillableMetric: billableMetric(m)}.Build(), nil
}

// GetBillableMetric implements BillableMetricService.GetBillableMetric.
func (s *BillableMetrics) GetBillableMetric(ctx context.Context, req *catalogv1.GetBillableMetricRequest) (*catalogv1.GetBillableMetricResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	m, err := s.lookup(p.OrganizationID).Find(ctx, "billable_metric_id", req.GetBillableMetricId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.GetBillableMetricResponse_builder{BillableMetric: billableMetric(m)}.Build(), nil
}

// ListBillableMetrics implements BillableMetricService.ListBillableMetrics:
// newest first, filtered on aggregation_type, recurring and plan_id
// (lago BillableMetricsQuery). Lago exposes those filters and the search
// in GraphQL only; the console uses the same API, so they are public here.
func (s *BillableMetrics) ListBillableMetrics(ctx context.Context, req *catalogv1.ListBillableMetricsRequest) (*catalogv1.ListBillableMetricsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	f, err := metricListFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	f.Search = req.GetSearch()
	page, err := s.svc.List(ctx, p.OrganizationID, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.ListBillableMetricsResponse_builder{
		BillableMetrics: Items(page, billableMetric),
		NextPageToken:   new(page.NextToken),
	}.Build(), nil
}

// UpdateBillableMetric implements BillableMetricService.UpdateBillableMetric.
// billable_metric.id is an ID or code:<code>. The fields updated are the
// update_mask paths or, without one, the fields set (filters when not
// empty); a metric charged on a plan keeps its locked fields (lago
// BillableMetrics::UpdateService).
func (s *BillableMetrics) UpdateBillableMetric(ctx context.Context, req *catalogv1.UpdateBillableMetricRequest) (*catalogv1.UpdateBillableMetricResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetBillableMetric()
	paths, err := UpdatePaths(req.GetUpdateMask(), metricUpdateFields, func(path string) bool {
		switch path {
		case "code":
			return b.HasCode()
		case "name":
			return b.HasName()
		case "description":
			return b.HasDescription()
		case "aggregation_type":
			return b.HasAggregationType()
		case "field_name":
			return b.HasFieldName()
		case "weighted_interval":
			return b.HasWeightedInterval()
		case "recurring":
			return b.HasRecurring()
		case "expression":
			return b.HasExpression()
		case "rounding_function":
			return b.HasRoundingFunction()
		case "rounding_precision":
			return b.HasRoundingPrecision()
		case "filters":
			return len(b.GetFilters()) > 0
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	cur, err := s.lookup(p.OrganizationID).Find(ctx, "billable_metric.id", b.GetId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	var in metrics.UpdateInput
	for _, path := range paths {
		switch path {
		case "code":
			in.Code = core.Some(b.GetCode())
		case "name":
			in.Name = core.Some(b.GetName())
		case "description":
			in.Description = core.Some(Opt(b.HasDescription(), b.GetDescription()))
		case "aggregation_type":
			in.AggregationType = core.Some(aggregationTypes.Core(b.GetAggregationType()))
		case "field_name":
			in.FieldName = core.Some(Opt(b.HasFieldName(), b.GetFieldName()))
		case "weighted_interval":
			in.WeightedInterval = core.Some(weightedIntervals.Core(b.GetWeightedInterval()))
		case "recurring":
			in.Recurring = core.Some(b.GetRecurring())
		case "expression":
			in.Expression = core.Some(Opt(b.HasExpression(), b.GetExpression()))
		case "rounding_function":
			in.RoundingFunction = core.Some(roundingFunctions.Core(b.GetRoundingFunction()))
		case "rounding_precision":
			in.RoundingPrecision = core.Some(OptInt(b.HasRoundingPrecision(), b.GetRoundingPrecision()))
		case "filters":
			in.Filters = core.Some(metricFilters(b.GetFilters()))
		}
	}
	m, err := s.svc.Update(ctx, p.OrganizationID, cur.ID, in)
	if err != nil {
		return nil, metricError(ctx, err)
	}
	return catalogv1.UpdateBillableMetricResponse_builder{BillableMetric: billableMetric(m)}.Build(), nil
}

// DeleteBillableMetric implements BillableMetricService.DeleteBillableMetric.
func (s *BillableMetrics) DeleteBillableMetric(ctx context.Context, req *catalogv1.DeleteBillableMetricRequest) (*catalogv1.DeleteBillableMetricResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := s.lookup(p.OrganizationID).Find(ctx, "billable_metric_id", req.GetBillableMetricId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	m, err := s.svc.Delete(ctx, p.OrganizationID, cur.ID)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.DeleteBillableMetricResponse_builder{BillableMetric: billableMetric(m)}.Build(), nil
}

// EvaluateExpression implements BillableMetricService.EvaluateExpression
// (lago BillableMetrics::EvaluateExpressionService): the sample event's
// timestamp defaults to now.
func (s *BillableMetrics) EvaluateExpression(ctx context.Context, req *catalogv1.EvaluateExpressionRequest) (*catalogv1.EvaluateExpressionResponse, error) {
	if _, err := principal(ctx); err != nil {
		return nil, err
	}
	v, err := s.svc.Evaluate(ctx, req.GetExpression(), metrics.SampleEvent{
		Code:       req.GetCode(),
		Timestamp:  Time(req.GetTimestamp()),
		Properties: Struct(req.GetProperties()),
	})
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.EvaluateExpressionResponse_builder{Value: &v}.Build(), nil
}

func (s *BillableMetrics) lookup(org core.OrganizationID) Lookup[core.BillableMetricID, *catalog.BillableMetric] {
	return Lookup[core.BillableMetricID, *catalog.BillableMetric]{
		ByID: func(ctx context.Context, id core.BillableMetricID) (*catalog.BillableMetric, error) {
			return s.svc.Get(ctx, org, id)
		},
		ByCode: func(ctx context.Context, code string) (*catalog.BillableMetric, error) {
			return s.svc.GetByCode(ctx, org, code)
		},
	}
}

// metricError maps the service errors, whose field paths are the
// metric's, onto the request's billable_metric message.
func metricError(ctx context.Context, err error) error {
	if v, ok := validationViolations("billable_metric.", err); ok {
		return withReason(InvalidArgument("invalid billable metric", v), ReasonValidationErrors)
	}
	return Error(ctx, err)
}

func metricListFilter(s string) (catalog.MetricListFilter, error) {
	var f catalog.MetricListFilter
	e, err := ParseFilter(s)
	if err != nil {
		return f, err
	}
	eq, err := Equalities(e, metricFilterFields...)
	if err != nil {
		return f, err
	}
	types, err := FilterEnum[catalogv1.AggregationType]("aggregation_type", eq["aggregation_type"], catalogv1.AggregationType(0).Descriptor())
	if err != nil {
		return f, err
	}
	for _, t := range types {
		f.AggregationTypes = append(f.AggregationTypes, aggregationTypes.Core(t))
	}
	if f.Recurring, err = FilterBool("recurring", eq["recurring"]); err != nil {
		return f, err
	}
	plan, err := FilterSingle("plan_id", eq["plan_id"])
	if err != nil {
		return f, err
	}
	if f.PlanID, err = ParseID[core.PlanID](FilterField, plan); err != nil {
		return f, err
	}
	return f, nil
}

func metricFilters(in []*catalogv1.MetricFilter) []catalog.MetricFilter {
	out := make([]catalog.MetricFilter, len(in))
	for i, f := range in {
		out[i] = catalog.MetricFilter{Key: f.GetKey(), Values: slices.Clone(f.GetValues())}
	}
	return out
}

func billableMetric(m *catalog.BillableMetric) *catalogv1.BillableMetric {
	b := catalogv1.BillableMetric_builder{
		Id:          new(m.ID.String()),
		Code:        new(m.Code),
		Name:        new(m.Name),
		Description: m.Description,
		FieldName:   m.FieldName,
		Recurring:   new(m.Recurring),
		Expression:  m.Expression,
		// Lago serializes zero counters (V1::BillableMetricSerializer
		// counters).
		PlansCount:               new(int32(0)),
		ActiveSubscriptionsCount: new(int32(0)),
		CreatedAt:                Timestamp(m.CreatedAt),
		UpdatedAt:                Timestamp(m.UpdatedAt),
	}
	if t := aggregationTypes.Proto(m.AggregationType); t != 0 {
		b.AggregationType = &t
	}
	if w := weightedIntervals.Proto(m.WeightedInterval); w != 0 {
		b.WeightedInterval = &w
	}
	if r := roundingFunctions.Proto(m.RoundingFunction); r != 0 {
		b.RoundingFunction = &r
	}
	if m.RoundingPrecision != nil {
		b.RoundingPrecision = new(int32(*m.RoundingPrecision))
	}
	for _, f := range m.Filters {
		b.Filters = append(b.Filters, catalogv1.MetricFilter_builder{Key: new(f.Key), Values: slices.Clone(f.Values)}.Build())
	}
	return b.Build()
}
