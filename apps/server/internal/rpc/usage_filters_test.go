package rpc_test

import (
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// usageFiltersResponse is the part of a current usage the filter cases
// read.
type usageFiltersResponse struct {
	Usage struct {
		Charges []struct {
			ChargeID           string `json:"charge_id"`
			BillableMetricCode string `json:"billable_metric_code"`
			Units              string `json:"units"`
			Amount             struct {
				Amount string `json:"amount"`
			} `json:"amount"`
			GroupedUsage []struct {
				GroupedBy map[string]string `json:"grouped_by"`
				Units     string            `json:"units"`
			} `json:"grouped_usage"`
		} `json:"charges"`
	} `json:"usage"`
}

// TestGetCurrentUsageFilters narrows a current usage over REST by charge,
// by group and by skipping the grouping, and asks for the usage since the
// subscription started.
func TestGetCurrentUsageFilters(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	h := s.Fixtures
	c := h.Customer()
	p := h.Plan(period.Monthly, 0)
	cloud := h.Metric("cloud_units", catalog.AggregationTypeSum, "units")
	other := h.Metric("other_units", catalog.AggregationTypeSum, "units")
	team := h.Metric("team_units", catalog.AggregationTypeSum, "units")
	chA := h.Charge(p, cloud, catalog.ChargeModelStandard, `{"amount":"10"}`)
	h.Charge(p, other, catalog.ChargeModelStandard, `{"amount":"20"}`)
	h.Charge(p, team, catalog.ChargeModelStandard, `{"amount":"10","pricing_group_keys":["workspace","user"]}`)
	start := rpctest.Now.AddDate(0, 0, -40)
	sub := h.Subscription(c, p, billingtest.SubscribedAt(start))

	// usage_controller_spec.rb:150-169: the previous period, then the
	// current one.
	h.Event(sub, "cloud_units", start.AddDate(0, 0, 1), "cloud", "aws", "units", "5")
	h.Event(sub, "cloud_units", start.AddDate(0, 0, 1), "cloud", "gcp", "units", "7")
	h.Event(sub, "other_units", start.AddDate(0, 0, 1), "units", "3")
	now := rpctest.Now.Add(-time.Hour)
	h.Event(sub, "cloud_units", now, "cloud", "aws", "units", "4")
	h.Event(sub, "cloud_units", now, "cloud", "aws", "units", "4")
	h.Event(sub, "cloud_units", now, "cloud", "gcp", "units", "6")
	h.Event(sub, "other_units", now, "units", "2")
	// customer_usage_spec.rb:240-252: workspace_a has the users 0 to 4,
	// workspace_b the users 5 to 9.
	for i := range 10 {
		ws := "workspace_a"
		if i >= 5 {
			ws = "workspace_b"
		}
		h.Event(sub, "team_units", now, "workspace", ws, "user", string(rune('0'+i)), "units", "3")
	}

	get := func(t *testing.T, query url.Values) (int, usageFiltersResponse) {
		t.Helper()
		query.Set("apply_taxes", "false")
		code, body := s.REST(t, s.APIKey, http.MethodGet,
			"/v1/customers/external_id%3A"+c.ExternalID+"/subscriptions/external_id%3A"+sub.ExternalID+"/usage:current?"+query.Encode(), nil)
		var out usageFiltersResponse
		if code == http.StatusOK {
			out = decode[usageFiltersResponse](t, body)
		} else {
			t.Logf("body: %s", body)
		}
		return code, out
	}
	charge := func(t *testing.T, out usageFiltersResponse, metric string) (units, amount string, groups int) {
		t.Helper()
		for _, ch := range out.Usage.Charges {
			if ch.BillableMetricCode == metric {
				return ch.Units, ch.Amount.Amount, len(ch.GroupedUsage)
			}
		}
		t.Fatalf("charges: got = %+v, want = one of %s", out.Usage.Charges, metric)
		return "", "", 0
	}

	for _, tc := range []struct {
		name, source string
		query        url.Values
		// charges is the number of charges, metric the one checked.
		charges        int
		metric         string
		units, amount  string
		groups         int
		status         int
		usersOfAGroups []string
	}{
		{
			name: "charge code", source: "usage_controller_spec.rb:172",
			query: url.Values{"charge_code": {chA.Code}}, charges: 1, metric: "cloud_units", units: "14", amount: "140.00",
		},
		{
			name: "unknown charge code", source: "usage_controller_spec.rb:189",
			query: url.Values{"charge_code": {"nonexistent"}}, status: http.StatusNotFound,
		},
		{
			name: "charge id", source: "usage_controller_spec.rb:210",
			query: url.Values{"charge_id": {chA.ID.String()}}, charges: 1, metric: "cloud_units", units: "14", amount: "140.00",
		},
		{
			name: "unknown charge id", source: "usage_controller_spec.rb:226",
			query: url.Values{"charge_id": {"0198a0c4-5f6e-7a8b-9c0d-1e2f3a4b5c6d"}}, status: http.StatusNotFound,
		},
		{
			name: "full usage without a filter", source: "usage_controller_spec.rb:300",
			query: url.Values{"full_usage": {"true"}}, status: http.StatusBadRequest,
		},
		{
			// lago gates it behind granular_lifetime_usage
			// (usage_controller_spec.rb:328-341).
			name: "full usage of a charge", source: "usage_controller_spec.rb:328",
			query: url.Values{"charge_code": {chA.Code}, "full_usage": {"true"}}, charges: 1, metric: "cloud_units", units: "26", amount: "260.00",
		},
		{
			name: "group filter", source: "usage_controller_spec.rb:381",
			query: url.Values{"filter_by_group": {"cloud:aws"}}, charges: 3, metric: "cloud_units", units: "8", amount: "80.00",
		},
		{
			name:  "group filter with two values",
			query: url.Values{"filter_by_group": {"cloud:aws", "cloud:gcp"}}, charges: 3, metric: "cloud_units", units: "14", amount: "140.00",
		},
		{
			name: "group filter keeps the other grouping key", source: "customer_usage_spec.rb:256",
			query: url.Values{"filter_by_group": {"workspace:workspace_a"}}, charges: 3, metric: "team_units", units: "15", amount: "150.00",
			groups: 5, usersOfAGroups: []string{"0", "1", "2", "3", "4"},
		},
		{
			name: "grouped by both keys", source: "customer_usage_spec.rb:313",
			query: url.Values{}, charges: 3, metric: "team_units", units: "30", amount: "300.00", groups: 10,
		},
		{
			name: "skip grouping", source: "customer_usage_spec.rb:338",
			query: url.Values{"skip_grouping": {"true"}}, charges: 3, metric: "team_units", units: "30", amount: "300.00", groups: 0,
		},
		{
			name: "full usage with a group filter", source: "usage_controller_spec.rb:402",
			query: url.Values{"filter_by_group": {"cloud:aws"}, "full_usage": {"true"}}, charges: 3, metric: "cloud_units", units: "13", amount: "130.00",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, out := get(t, tc.query)
			want := cmpOr(tc.status, http.StatusOK)
			if status != want {
				t.Fatalf("status: got = %d, want = %d", status, want)
			}
			if status != http.StatusOK {
				return
			}
			if len(out.Usage.Charges) != tc.charges {
				t.Fatalf("charges: got = %d, want = %d", len(out.Usage.Charges), tc.charges)
			}
			units, amount, groups := charge(t, out, tc.metric)
			if units != tc.units || amount != tc.amount || groups != tc.groups {
				t.Errorf("%s: got = %s units, %s, %d groups, want = %s units, %s, %d groups", tc.metric, units, amount, groups, tc.units, tc.amount, tc.groups)
			}
			if tc.usersOfAGroups != nil {
				var users []string
				for _, ch := range out.Usage.Charges {
					if ch.BillableMetricCode != tc.metric {
						continue
					}
					for _, g := range ch.GroupedUsage {
						if _, ok := g.GroupedBy["workspace"]; ok {
							t.Errorf("grouped_by: got = %v, want = the user only", g.GroupedBy)
						}
						users = append(users, g.GroupedBy["user"])
					}
				}
				slices.Sort(users)
				if !slices.Equal(users, tc.usersOfAGroups) {
					t.Errorf("users: got = %v, want = %v", users, tc.usersOfAGroups)
				}
			}
		})
	}
}

// cmpOr returns a, or b when a is zero.
func cmpOr(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}
