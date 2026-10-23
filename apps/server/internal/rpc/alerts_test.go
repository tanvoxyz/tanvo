package rpc_test

import (
	"net/http"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
	alertv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/alert/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/alert/v1/alertv1connect"
)

// alertFixture is a server with an active subscription.
type alertFixture struct {
	s      *rpctest.Server
	c      alertv1connect.AlertServiceClient
	sub    *subscription.Subscription
	subRef string
}

func newAlertFixture(t *testing.T) *alertFixture {
	t.Helper()
	s := rpctest.New(t)
	cust := s.Fixtures.Customer()
	sub := s.Fixtures.Subscription(cust, s.Fixtures.Plan(period.Monthly, 1000))
	return &alertFixture{
		s: s, c: alertv1connect.NewAlertServiceClient(s.Connect(s.APIKey)),
		sub: sub, subRef: rpc.ExternalIDPrefix + sub.ExternalID,
	}
}

func alertThreshold(code, value string, recurring bool) *alertv1.AlertThreshold {
	b := alertv1.AlertThreshold_builder{Value: &value, Recurring: &recurring}
	if code != "" {
		b.Code = &code
	}
	return b.Build()
}

// alertUsage is the alert of the request spec's create.
func alertUsage(code string) *alertv1.Alert {
	return alertv1.Alert_builder{
		Code: &code, Name: new("New Alert"),
		AlertType: new(alertv1.AlertType_ALERT_TYPE_CURRENT_USAGE_AMOUNT),
		Thresholds: []*alertv1.AlertThreshold{
			alertThreshold("notice", "1000", false),
			alertThreshold("warn", "5000", false),
			alertThreshold("alert", "2000", true),
		},
	}.Build()
}

func (f *alertFixture) create(t *testing.T, a *alertv1.Alert) (*alertv1.Alert, error) {
	t.Helper()
	resp, err := f.c.CreateAlert(t.Context(), alertv1.CreateAlertRequest_builder{SubscriptionId: &f.subRef, Alert: a}.Build())
	return resp.GetAlert(), err
}

func alertWantViolation(t *testing.T, err error, field, reason string) {
	t.Helper()
	for _, v := range rpc.ErrorViolations(err) {
		if v.Field == field && v.Reason == reason {
			return
		}
	}
	t.Fatalf("got err = %v (violations %+v), want = %s %s", err, rpc.ErrorViolations(err), field, reason)
}

func alertWantNotFound(t *testing.T, err error, reason string) {
	t.Helper()
	if connect.CodeOf(err) != connect.CodeNotFound || rpc.ErrorReason(err) != reason {
		t.Fatalf("got err = %v (reason %q), want = NotFound %s", err, rpc.ErrorReason(err), reason)
	}
}

// source: spec/requests/api/v1/subscriptions/alerts_controller_spec.rb:87
func TestCreateAlert(t *testing.T) {
	t.Parallel()
	f := newAlertFixture(t)
	a, err := f.create(t, alertUsage("test"))
	if err != nil {
		t.Fatalf("CreateAlert: %v", err)
	}
	if a.GetId() == "" || a.GetCode() != "test" || a.GetName() != "New Alert" || a.GetPreviousValue() != "0" || a.HasLastProcessedAt() || !a.HasCreatedAt() {
		t.Errorf("got %v, want the created alert", a)
	}
	if a.GetSubscriptionId() != f.sub.ID.String() || a.GetExternalSubscriptionId() != f.sub.ExternalID {
		t.Errorf("got subscription = %s %s, want = %s %s", a.GetSubscriptionId(), a.GetExternalSubscriptionId(), f.sub.ID, f.sub.ExternalID)
	}
	if a.GetDirection() != alertv1.AlertDirection_ALERT_DIRECTION_INCREASING || len(a.GetThresholds()) != 3 {
		t.Errorf("got direction = %v, thresholds = %v, want = increasing, 3", a.GetDirection(), a.GetThresholds())
	}
}

func TestCreateAlertErrors(t *testing.T) {
	t.Parallel()
	f := newAlertFixture(t)
	if _, err := f.create(t, alertUsage("taken")); err != nil {
		t.Fatalf("CreateAlert: %v", err)
	}
	metric := f.s.Fixtures.Metric("bm_code", catalog.AggregationTypeSum, "amount")

	t.Run("code taken", func(t *testing.T) {
		// source: spec/requests/api/v1/subscriptions/alerts_controller_spec.rb:101
		a := alertv1.Alert_builder{
			Code: new("taken"), AlertType: new(alertv1.AlertType_ALERT_TYPE_BILLABLE_METRIC_CURRENT_USAGE_AMOUNT),
			BillableMetricId: new(metric.ID.String()), Thresholds: []*alertv1.AlertThreshold{alertThreshold("", "10", false)},
		}.Build()
		_, err := f.create(t, a)
		alertWantViolation(t, err, "alert.code", "value_already_exist")
	})
	t.Run("missing required param", func(t *testing.T) {
		// source: spec/requests/api/v1/subscriptions/alerts_controller_spec.rb:116
		a := alertUsage("x")
		a.ClearCode()
		_, err := f.create(t, a)
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("got err = %v, want = InvalidArgument", err)
		}
	})
	t.Run("billable metric not found", func(t *testing.T) {
		// source: spec/requests/api/v1/subscriptions/alerts_controller_spec.rb:170
		a := alertv1.Alert_builder{
			Code: new("bm"), AlertType: new(alertv1.AlertType_ALERT_TYPE_BILLABLE_METRIC_CURRENT_USAGE_AMOUNT),
			BillableMetricId: new("code:missing"), Thresholds: []*alertv1.AlertThreshold{alertThreshold("alert", "1000", true)},
		}.Build()
		_, err := f.create(t, a)
		alertWantNotFound(t, err, "billable_metric_not_found")
	})
	t.Run("billable metric by code", func(t *testing.T) {
		// source: spec/requests/api/v1/subscriptions/alerts_controller_spec.rb:159
		a := alertv1.Alert_builder{
			Code: new("bm"), AlertType: new(alertv1.AlertType_ALERT_TYPE_BILLABLE_METRIC_CURRENT_USAGE_AMOUNT),
			BillableMetricId: new("code:bm_code"), Thresholds: []*alertv1.AlertThreshold{alertThreshold("alert", "1000", true)},
		}.Build()
		got, err := f.create(t, a)
		if err != nil {
			t.Fatalf("CreateAlert: %v", err)
		}
		if got.GetBillableMetricId() != metric.ID.String() || got.GetAlertType() != alertv1.AlertType_ALERT_TYPE_BILLABLE_METRIC_CURRENT_USAGE_AMOUNT {
			t.Errorf("got %v, want the metric alert", got)
		}
	})
	t.Run("wallet type on a subscription", func(t *testing.T) {
		// source: spec/requests/api/v1/customers/wallets/alerts_controller_spec.rb:141
		a := alertUsage("wallet")
		a.SetAlertType(alertv1.AlertType_ALERT_TYPE_WALLET_BALANCE_AMOUNT)
		_, err := f.create(t, a)
		alertWantViolation(t, err, "alert.alert_type", "invalid_type")
	})
	t.Run("subscription not found", func(t *testing.T) {
		// source: spec/support/shared_examples/alerts.rb (returns error if subscription not found)
		_, err := f.c.CreateAlert(t.Context(), alertv1.CreateAlertRequest_builder{
			SubscriptionId: new("external_id:missing"), Alert: alertUsage("y"),
		}.Build())
		alertWantNotFound(t, err, "subscription_not_found")
	})
}

// source: spec/requests/api/v1/subscriptions/alerts_controller_spec.rb:36
// source: spec/requests/api/v1/subscriptions/alerts_controller_spec.rb:184
// source: spec/requests/api/v1/subscriptions/alerts_controller_spec.rb:198
func TestGetAndListAlerts(t *testing.T) {
	t.Parallel()
	f := newAlertFixture(t)
	ctx := t.Context()
	resp, err := f.c.ListAlerts(ctx, alertv1.ListAlertsRequest_builder{SubscriptionId: &f.subRef}.Build())
	if err != nil || len(resp.GetAlerts()) != 0 {
		t.Fatalf("got %v, %v, want = no alerts", resp, err)
	}
	a, err := f.create(t, alertUsage("my-alert"))
	if err != nil {
		t.Fatalf("CreateAlert: %v", err)
	}
	b := alertUsage("other")
	b.SetAlertType(alertv1.AlertType_ALERT_TYPE_LIFETIME_USAGE_AMOUNT)
	if _, err := f.create(t, b); err != nil {
		t.Fatalf("CreateAlert: %v", err)
	}
	resp, err = f.c.ListAlerts(ctx, alertv1.ListAlertsRequest_builder{SubscriptionId: &f.subRef, PageSize: new(int32(1))}.Build())
	if err != nil || len(resp.GetAlerts()) != 1 || resp.GetNextPageToken() == "" {
		t.Fatalf("got %v, %v, want = one alert and a next page", resp, err)
	}
	for _, ref := range []string{a.GetId(), "code:my-alert"} {
		got, err := f.c.GetAlert(ctx, alertv1.GetAlertRequest_builder{SubscriptionId: new(f.sub.ID.String()), AlertId: &ref}.Build())
		if err != nil || got.GetAlert().GetId() != a.GetId() {
			t.Errorf("GetAlert(%s) = %v, %v, want = %s", ref, got, err, a.GetId())
		}
	}
	_, err = f.c.GetAlert(ctx, alertv1.GetAlertRequest_builder{SubscriptionId: &f.subRef, AlertId: new("code:missing")}.Build())
	alertWantNotFound(t, err, "alert_not_found")
}

func TestUpdateAlert(t *testing.T) {
	t.Parallel()
	f := newAlertFixture(t)
	ctx := t.Context()
	a, err := f.create(t, alertUsage("my-alert"))
	if err != nil {
		t.Fatalf("CreateAlert: %v", err)
	}
	if _, err := f.create(t, func() *alertv1.Alert {
		b := alertUsage("taken")
		b.SetAlertType(alertv1.AlertType_ALERT_TYPE_LIFETIME_USAGE_AMOUNT)
		return b
	}()); err != nil {
		t.Fatalf("CreateAlert: %v", err)
	}
	update := func(a *alertv1.Alert, mask ...string) (*alertv1.Alert, error) {
		req := alertv1.UpdateAlertRequest_builder{SubscriptionId: &f.subRef, Alert: a}.Build()
		if mask != nil {
			req.SetUpdateMask(&fieldmaskpb.FieldMask{Paths: mask})
		}
		resp, err := f.c.UpdateAlert(ctx, req)
		return resp.GetAlert(), err
	}

	// source: spec/requests/api/v1/subscriptions/alerts_controller_spec.rb:233
	_, err = update(alertv1.Alert_builder{Id: new("code:my-alert"), Code: new("taken")}.Build())
	alertWantViolation(t, err, "alert.code", "value_already_exist")

	// source: spec/requests/api/v1/subscriptions/alerts_controller_spec.rb:258
	f.s.Fixtures.Metric("bm_code", catalog.AggregationTypeSum, "amount")
	_, err = update(alertv1.Alert_builder{Id: new(a.GetId()), BillableMetricId: new("code:bm_code")}.Build())
	alertWantViolation(t, err, "alert.billable_metric_id", "value_must_be_blank")

	// source: spec/requests/api/v1/subscriptions/alerts_controller_spec.rb:218
	got, err := update(alertv1.Alert_builder{
		Id: new("code:my-alert"), Code: new("test"),
		Thresholds: []*alertv1.AlertThreshold{alertThreshold("notice", "8800", false)},
	}.Build())
	if err != nil {
		t.Fatalf("UpdateAlert: %v", err)
	}
	if got.GetId() != a.GetId() || got.GetCode() != "test" || got.GetName() != "New Alert" || len(got.GetThresholds()) != 1 || got.GetThresholds()[0].GetValue() != "8800" {
		t.Errorf("got %v, want code test, the name kept and one threshold", got)
	}

	// An update_mask path clears the name.
	got, err = update(alertv1.Alert_builder{Id: new(a.GetId())}.Build(), "name")
	if err != nil || got.HasName() {
		t.Errorf("got %v, %v, want = no name", got, err)
	}
	_, err = update(alertv1.Alert_builder{Id: new("code:missing"), Name: new("x")}.Build())
	alertWantNotFound(t, err, "alert_not_found")
}

// source: spec/requests/api/v1/subscriptions/alerts_controller_spec.rb:305
// source: spec/requests/api/v1/subscriptions/alerts_controller_spec.rb:313
func TestDeleteAlert(t *testing.T) {
	t.Parallel()
	f := newAlertFixture(t)
	ctx := t.Context()
	if _, err := f.create(t, alertUsage("my-alert")); err != nil {
		t.Fatalf("CreateAlert: %v", err)
	}
	del := alertv1.DeleteAlertRequest_builder{SubscriptionId: &f.subRef, AlertId: new("code:my-alert")}.Build()
	if _, err := f.c.DeleteAlert(ctx, del); err != nil {
		t.Fatalf("DeleteAlert: %v", err)
	}
	_, err := f.c.DeleteAlert(ctx, del)
	alertWantNotFound(t, err, "alert_not_found")
	// The code is free again.
	if _, err := f.create(t, alertUsage("my-alert")); err != nil {
		t.Errorf("CreateAlert after delete: %v", err)
	}
}

// source: spec/requests/api/v1/subscriptions/alerts_controller_spec.rb:443
// source: spec/requests/api/v1/subscriptions/alerts_controller_spec.rb:445
// source: spec/requests/api/v1/subscriptions/alerts_controller_spec.rb:454
func TestDeleteAlerts(t *testing.T) {
	t.Parallel()
	f := newAlertFixture(t)
	ctx := t.Context()
	lifetime := alertUsage("a2")
	lifetime.SetAlertType(alertv1.AlertType_ALERT_TYPE_LIFETIME_USAGE_AMOUNT)
	for _, a := range []*alertv1.Alert{alertUsage("a1"), lifetime} {
		if _, err := f.create(t, a); err != nil {
			t.Fatalf("CreateAlert(%s): %v", a.GetCode(), err)
		}
	}
	del := alertv1.DeleteAlertsRequest_builder{SubscriptionId: &f.subRef}.Build()
	if _, err := f.c.DeleteAlerts(ctx, del); err != nil {
		t.Fatalf("DeleteAlerts: %v", err)
	}
	list, err := f.c.ListAlerts(ctx, alertv1.ListAlertsRequest_builder{SubscriptionId: &f.subRef}.Build())
	if err != nil || len(list.GetAlerts()) != 0 {
		t.Errorf("got %d alerts, %v, want = none", len(list.GetAlerts()), err)
	}
	if _, err := f.c.DeleteAlerts(ctx, del); err != nil {
		t.Errorf("DeleteAlerts without alerts: %v", err)
	}
	_, err = f.c.DeleteAlerts(ctx, alertv1.DeleteAlertsRequest_builder{SubscriptionId: new(rpc.ExternalIDPrefix + "unknown")}.Build())
	alertWantNotFound(t, err, "subscription_not_found")
}

// source: spec/requests/api/v1/customers/wallets/alerts_controller_spec.rb:398
// source: spec/requests/api/v1/customers/wallets/alerts_controller_spec.rb:400
// source: spec/requests/api/v1/customers/wallets/alerts_controller_spec.rb:409
func TestDeleteWalletAlertsREST(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	w := s.Fixtures.Wallet(s.Fixtures.Customer(), "100")
	path := "/v1/wallets/" + w.ID.String() + "/alerts"
	status, body := s.REST(t, s.APIKey, http.MethodPost, path, `{"code":"low","alert_type":"ALERT_TYPE_WALLET_BALANCE_AMOUNT","thresholds":[{"value":"10"}]}`)
	if status != http.StatusOK {
		t.Fatalf("POST %s = %d %s, want = 200", path, status, body)
	}
	for _, step := range []string{"with alerts", "without alerts"} {
		if status, body := s.REST(t, s.APIKey, http.MethodDelete, path, nil); status != http.StatusOK {
			t.Errorf("DELETE %s %s = %d %s, want = 200", path, step, status, body)
		}
	}
	status, body = s.REST(t, s.APIKey, http.MethodGet, path, nil)
	if n := len(decode[struct {
		Alerts []struct{} `json:"alerts"`
	}](t, body).Alerts); status != http.StatusOK || n != 0 {
		t.Errorf("GET %s = %d %s, want = 200 with no alert", path, status, body)
	}
	unknown := "/v1/wallets/" + uuid.NewString() + "/alerts"
	if status, body := s.REST(t, s.APIKey, http.MethodDelete, unknown, nil); status != http.StatusNotFound {
		t.Errorf("DELETE %s = %d %s, want = 404", unknown, status, body)
	}
}

// source: spec/requests/api/v1/subscriptions/alerts_controller_spec.rb:347
// source: spec/requests/api/v1/customers/wallets/alerts_controller_spec.rb:329
// source: spec/requests/api/v1/customers/wallets/alerts_controller_spec.rb:359
func TestBatchCreateAlerts(t *testing.T) {
	t.Parallel()
	f := newAlertFixture(t)
	ctx := t.Context()
	lifetime := alertUsage("lifetime")
	lifetime.SetAlertType(alertv1.AlertType_ALERT_TYPE_LIFETIME_USAGE_AMOUNT)
	resp, err := f.c.BatchCreateAlerts(ctx, alertv1.BatchCreateAlertsRequest_builder{
		SubscriptionId: &f.subRef, Alerts: []*alertv1.Alert{alertUsage("usage"), lifetime},
	}.Build())
	if err != nil || len(resp.GetAlerts()) != 2 {
		t.Fatalf("got %v, %v, want = two alerts", resp, err)
	}

	bad := alertUsage("bad")
	bad.SetAlertType(alertv1.AlertType_ALERT_TYPE_WALLET_CREDITS_BALANCE)
	_, err = f.c.BatchCreateAlerts(ctx, alertv1.BatchCreateAlertsRequest_builder{
		SubscriptionId: &f.subRef, Alerts: []*alertv1.Alert{alertUsage("usage"), bad},
	}.Build())
	alertWantViolation(t, err, "alerts[0].base", "alert_already_exists")
	alertWantViolation(t, err, "alerts[1].alert_type", "invalid_type")
	list, err := f.c.ListAlerts(ctx, alertv1.ListAlertsRequest_builder{SubscriptionId: &f.subRef}.Build())
	if err != nil || len(list.GetAlerts()) != 2 {
		t.Errorf("got %d alerts, %v, want = 2 (none of the failed batch)", len(list.GetAlerts()), err)
	}
}

// source: spec/requests/api/v1/customers/wallets/alerts_controller_spec.rb:98
// source: spec/requests/api/v1/customers/wallets/alerts_controller_spec.rb:46
// source: spec/requests/api/v1/customers/wallets/alerts_controller_spec.rb:273
func TestWalletAlertsREST(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	w := s.Fixtures.Wallet(s.Fixtures.Customer(), "100")
	path := "/v1/wallets/" + w.ID.String() + "/alerts"
	status, body := s.REST(t, s.APIKey, http.MethodPost, path, `{"code":"low","alert_type":"ALERT_TYPE_WALLET_BALANCE_AMOUNT","thresholds":[{"code":"warn","value":"10"}]}`)
	if status != http.StatusOK {
		t.Fatalf("POST %s = %d %s, want = 200", path, status, body)
	}
	a := decode[struct {
		Alert struct {
			ID            string `json:"id"`
			WalletID      string `json:"wallet_id"`
			Direction     string `json:"direction"`
			PreviousValue string `json:"previous_value"`
		} `json:"alert"`
	}](t, body).Alert
	if a.WalletID != w.ID.String() || a.Direction != "ALERT_DIRECTION_DECREASING" || a.PreviousValue != "10000" {
		// Lago compares wallet amounts in cents (wallet_balance_amount_alert.rb:5-7).
		t.Errorf("got %+v, want a decreasing alert of the wallet at its balance in cents", a)
	}
	status, body = s.REST(t, s.APIKey, http.MethodPost, path, `{"code":"usage","alert_type":"ALERT_TYPE_CURRENT_USAGE_AMOUNT","thresholds":[{"value":"10"}]}`)
	if status != http.StatusBadRequest {
		t.Errorf("POST %s (usage type) = %d %s, want = 400", path, status, body)
	}
	status, body = s.REST(t, s.APIKey, http.MethodGet, path, nil)
	if n := len(decode[struct {
		Alerts []struct{} `json:"alerts"`
	}](t, body).Alerts); status != http.StatusOK || n != 1 {
		t.Errorf("GET %s = %d %s, want = 200 with one alert", path, status, body)
	}
	status, body = s.REST(t, s.APIKey, http.MethodDelete, path+"/code%3Alow", nil)
	if status != http.StatusOK {
		t.Errorf("DELETE = %d %s, want = 200", status, body)
	}
	status, _ = s.REST(t, s.APIKey, http.MethodGet, path+"/code%3Alow", nil)
	if status != http.StatusNotFound {
		t.Errorf("GET deleted = %d, want = 404", status)
	}
}
