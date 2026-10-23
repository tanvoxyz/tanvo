package rpc_test

import (
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/period"
	usagev1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/usage/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/usage/v1/usagev1connect"
)

// source: spec/requests/api/v1/customers/usage_controller_spec.rb:41
// source: spec/serializers/v1/customers/charge_usage_serializer_spec.rb:242
func TestGetCurrentUsage(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	h := s.Fixtures
	c := h.Customer()
	p := h.Plan(period.Monthly, 0)
	m := h.Metric("calls", catalog.AggregationTypeCount, "")
	h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(ch *catalog.Charge) {
		ch.Filters = []catalog.ChargeFilter{billingtest.ChargeFilter(map[string][]string{"region": {"eu"}}, `{"amount":"2"}`)}
	})
	sub := h.Subscription(c, p, billingtest.SubscribedAt(rpctest.Now.AddDate(0, 0, -5)))
	h.Events.Add(h.Event(sub, "calls", rpctest.Now.Add(-time.Hour), "region", "eu"))
	h.Events.Add(h.Event(sub, "calls", rpctest.Now.Add(-time.Hour), "region", "us"))

	code, body := s.REST(t, s.APIKey, http.MethodGet,
		"/v1/customers/external_id%3A"+c.ExternalID+"/subscriptions/external_id%3A"+sub.ExternalID+"/usage:current?apply_taxes=false", nil)
	if code != http.StatusOK {
		t.Fatalf("GET usage:current = %d %s, want = 200", code, body)
	}
	out := decode[struct {
		Usage struct {
			Amount struct {
				Amount string `json:"amount"`
			} `json:"amount"`
			Charges []struct {
				BillableMetricCode string `json:"billable_metric_code"`
				Units              string `json:"units"`
				Filters            []struct {
					Values map[string]string `json:"values"`
					Amount struct {
						Amount string `json:"amount"`
					} `json:"amount"`
				} `json:"filters"`
			} `json:"charges"`
		} `json:"usage"`
	}](t, body)
	u := out.Usage
	if u.Amount.Amount != "3.00" || len(u.Charges) != 1 || u.Charges[0].BillableMetricCode != "calls" || u.Charges[0].Units != "2" {
		t.Fatalf("got = %+v, want = 3.00 over 2 calls", u)
	}
	amounts := map[string]string{}
	for _, f := range u.Charges[0].Filters {
		amounts[f.Values["region"]] = f.Amount.Amount
	}
	if amounts["eu"] != "2.00" || amounts[""] != "1.00" {
		t.Errorf("got filter amounts = %v, want = eu 2.00 and the rest 1.00", amounts)
	}

	cl := usagev1connect.NewUsageServiceClient(s.Connect(s.APIKey))
	// source: app/services/invoices/customer_usage_service.rb:57
	pending := h.Subscription(c, p, billingtest.SubscribedAt(rpctest.Now.AddDate(0, 0, 5)))
	_, err := cl.GetCurrentUsage(t.Context(), usagev1.GetCurrentUsageRequest_builder{
		CustomerId: new(c.ID.String()), SubscriptionId: new(pending.ID.String()),
	}.Build())
	subWantCode(t, err, connect.CodeFailedPrecondition, "no_active_subscription")

	_, err = cl.GetCurrentUsage(t.Context(), usagev1.GetCurrentUsageRequest_builder{
		CustomerId: new(c.ID.String()), SubscriptionId: new(sub.ID.String()), FilterBy: map[string]string{"region": "eu"},
	}.Build())
	subWantCode(t, err, connect.CodeUnimplemented, "")
}

// source: spec/requests/api/v1/customers/usage_controller_spec.rb:520
func TestListPastUsage(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	h := s.Fixtures
	c := h.Customer()
	sub := h.Subscription(c, h.Plan(period.Monthly, 0), billingtest.SubscribedAt(rpctest.Now.AddDate(0, -2, 0)))
	cl := usagev1connect.NewUsageServiceClient(s.Connect(s.APIKey))

	resp, err := cl.ListPastUsage(t.Context(), usagev1.ListPastUsageRequest_builder{
		CustomerId: new(rpc.ExternalIDPrefix + "unknown"), SubscriptionId: new(rpc.ExternalIDPrefix + sub.ExternalID),
	}.Build())
	if err != nil || len(resp.GetUsages()) != 0 {
		t.Fatalf("ListPastUsage(unknown customer) = %v, %v, want = empty", resp, err)
	}
	_, err = cl.ListPastUsage(t.Context(), usagev1.ListPastUsageRequest_builder{
		CustomerId: new(c.ID.String()), SubscriptionId: new(sub.ID.String()), BillableMetricCode: new("nope"),
	}.Build())
	subWantCode(t, err, connect.CodeNotFound, "billable_metric_not_found")
}
