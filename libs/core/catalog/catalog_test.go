package catalog

import (
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

var (
	catalogTestNow    = time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	catalogTestOrg    = core.MustParseID[core.OrganizationID]("0190a0a0-0000-7000-8000-000000000001")
	catalogTestMetric = core.MustParseID[core.BillableMetricID]("0190a0a0-0000-7000-8000-000000000002")
	catalogTestPlan   = core.MustParseID[core.PlanID]("0190a0a0-0000-7000-8000-000000000003")
	catalogTestAddOn  = core.MustParseID[core.AddOnID]("0190a0a0-0000-7000-8000-000000000004")
	catalogTestUnit   = core.MustParseID[core.PricingUnitID]("0190a0a0-0000-7000-8000-000000000005")
)

func catalogFieldErrors(err error) []string {
	out := []string{}
	for _, fe := range core.FieldErrors(err) {
		out = append(out, fe.String())
	}
	return out
}

func catalogCheckErrors(t *testing.T, what string, err error, want []string, source string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if got := catalogFieldErrors(err); !slices.Equal(got, want) {
		t.Errorf("%s errors got = %v, want = %v (%s)", what, got, want, source)
	}
}

func catalogPtr[T any](v T) *T { return &v }

// catalogTestMetricOf returns a valid metric of aggregation type agg.
func catalogTestMetricOf(agg AggregationType, recurring bool) *BillableMetric {
	m := &BillableMetric{
		ID:              catalogTestMetric,
		OrganizationID:  catalogTestOrg,
		Code:            "api_calls",
		Name:            "API calls",
		AggregationType: agg,
		FieldName:       catalogPtr("amount"),
		Recurring:       recurring,
		CreatedAt:       catalogTestNow,
		UpdatedAt:       catalogTestNow,
	}
	if agg == AggregationTypeWeightedSum {
		m.WeightedInterval = WeightedIntervalSeconds
	}
	m.Normalize()
	return m
}

func catalogTestPlanOf() *Plan {
	return &Plan{
		ID:             catalogTestPlan,
		OrganizationID: catalogTestOrg,
		Code:           "basic",
		Name:           "Basic",
		Interval:       period.Monthly,
		Amount:         100,
		Currency:       money.MustCurrency("EUR"),
		Status:         PlanStatusActive,
		CreatedAt:      catalogTestNow,
		UpdatedAt:      catalogTestNow,
	}
}
