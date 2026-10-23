package rpc_test

import (
	"crypto/rand"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/period"
	couponv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/coupon/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/coupon/v1/couponv1connect"
)

// restCoupon is the JSON of a coupon in a REST response.
type restCoupon struct {
	ID                       string   `json:"id"`
	Code                     string   `json:"code"`
	Name                     string   `json:"name"`
	CouponType               string   `json:"coupon_type"`
	Reusable                 bool     `json:"reusable"`
	Status                   string   `json:"status"`
	ExpirationAt             string   `json:"expiration_at"`
	CreatedAt                string   `json:"created_at"`
	LimitedBillableMetricIDs []string `json:"limited_billable_metric_ids"`
	LimitedPlans             bool     `json:"limited_plans"`
	LimitedBillableMetrics   bool     `json:"limited_billable_metrics"`
	PlanCodes                []string `json:"plan_codes"`
	BillableMetricCodes      []string `json:"billable_metric_codes"`
	Amount                   struct {
		Currency string `json:"currency"`
		Amount   string `json:"amount"`
	} `json:"amount"`
}

// restAppliedCoupon is the JSON of an applied coupon in a REST response.
type restAppliedCoupon struct {
	ID                 string `json:"id"`
	CouponID           string `json:"coupon_id"`
	CouponCode         string `json:"coupon_code"`
	CouponName         string `json:"coupon_name"`
	CouponStatus       string `json:"coupon_status"`
	CouponDeletedAt    string `json:"coupon_deleted_at"`
	CustomerID         string `json:"customer_id"`
	ExternalCustomerID string `json:"external_customer_id"`
	Status             string `json:"status"`
	CreatedAt          string `json:"created_at"`
	Amount             struct {
		Currency string `json:"currency"`
		Amount   string `json:"amount"`
	} `json:"amount"`
}

func couponsClient(s *rpctest.Server, c rpctest.Credential) couponv1connect.CouponServiceClient {
	return couponv1connect.NewCouponServiceClient(s.Connect(c))
}

func TestCreateCouponRPC(t *testing.T) {
	t.Parallel()
	t.Log("source: spec/requests/api/v1/coupons_controller_spec.rb:33")
	s := rpctest.New(t)
	m := s.Fixtures.Metric("bm", catalog.AggregationTypeCount, "")
	exp := s.Clock.Now().Add(15 * 24 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
	status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/coupons", `{"name":"coupon1","code":"coupon1_code",
		"coupon_type":"COUPON_TYPE_FIXED_AMOUNT","frequency":"COUPON_FREQUENCY_ONCE","amount":{"currency":"EUR","amount":"1.23"},
		"expiration":"COUPON_EXPIRATION_TIME_LIMIT","expiration_at":"`+exp+`","reusable":false,
		"limited_billable_metric_ids":["code:bm"]}`)
	if status != http.StatusOK {
		t.Fatalf("got status = %d (%s), want = %d", status, body, http.StatusOK)
	}
	c := decode[struct {
		Coupon restCoupon `json:"coupon"`
	}](t, body).Coupon
	if c.ID == "" || c.Code != "coupon1_code" || c.Name != "coupon1" || c.CreatedAt == "" || c.Reusable {
		t.Errorf("got %+v, want coupon1_code, not reusable", c)
	}
	if c.ExpirationAt != exp {
		t.Errorf("got expiration_at = %s, want = %s", c.ExpirationAt, exp)
	}
	if c.Amount.Currency != "EUR" || c.Amount.Amount != "1.23" {
		t.Errorf("got amount = %+v, want = 1.23 EUR", c.Amount)
	}
	if len(c.LimitedBillableMetricIDs) != 1 || c.LimitedBillableMetricIDs[0] != m.ID.String() {
		t.Errorf("got limited_billable_metric_ids = %v, want = [%s]", c.LimitedBillableMetricIDs, m.ID)
	}
}

func TestUpdateCouponRPC(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		// ref is the path reference; "" is the coupon's code:<code>.
		ref    string
		code   string
		status int
		reason string
	}{
		{"updates", "spec/requests/api/v1/coupons_controller_spec.rb:72", "", "coupon_code", http.StatusOK, ""},
		{"not found", "spec/requests/api/v1/coupons_controller_spec.rb:84", "code%3Aunknown", "coupon_code", http.StatusNotFound, "coupon_not_found"},
		{"duplicate code", "spec/requests/api/v1/coupons_controller_spec.rb:94", "", "other", http.StatusBadRequest, rpc.ReasonValidationErrors},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			s := rpctest.New(t)
			c := s.Fixtures.FixedAmountCoupon(10)
			s.Fixtures.FixedAmountCoupon(10, func(c *coupon.Coupon) { c.Code = "other" })
			ref := tt.ref
			if ref == "" {
				ref = "code%3A" + c.Code
			}
			exp := s.Clock.Now().Add(15 * 24 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
			status, body := s.REST(t, s.APIKey, http.MethodPatch, "/v1/coupons/"+ref, `{"name":"coupon1","code":"`+tt.code+`",
				"coupon_type":"COUPON_TYPE_FIXED_AMOUNT","frequency":"COUPON_FREQUENCY_ONCE","amount":{"currency":"EUR","amount":"1.23"},
				"expiration":"COUPON_EXPIRATION_TIME_LIMIT","expiration_at":"`+exp+`"}`)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if tt.status != http.StatusOK {
				if got := couponReason(t, body); got != tt.reason {
					t.Errorf("got reason = %q, want = %q", got, tt.reason)
				}
				return
			}
			got := decode[struct {
				Coupon restCoupon `json:"coupon"`
			}](t, body).Coupon
			if got.ID != c.ID.String() || got.Code != "coupon_code" || got.ExpirationAt != exp {
				t.Errorf("got %+v, want id %s, code coupon_code, expiration_at %s", got, c.ID, exp)
			}
		})
	}
}

func TestGetCouponRPC(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	c := s.Fixtures.FixedAmountCoupon(10)
	tests := []struct {
		name   string
		source string
		ref    string
		status int
	}{
		{"by code", "spec/requests/api/v1/coupons_controller_spec.rb:109", "code%3A" + c.Code, http.StatusOK},
		{"by id", "spec/requests/api/v1/coupons_controller_spec.rb:109", c.ID.String(), http.StatusOK},
		{"not found", "spec/requests/api/v1/coupons_controller_spec.rb:120", "code%3Aunknown", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/coupons/"+tt.ref, nil)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if tt.status != http.StatusOK {
				return
			}
			got := decode[struct {
				Coupon restCoupon `json:"coupon"`
			}](t, body).Coupon
			if got.ID != c.ID.String() || got.Code != c.Code {
				t.Errorf("got %+v, want id %s code %s", got, c.ID, c.Code)
			}
		})
	}
	t.Run("limited to plans", func(t *testing.T) {
		t.Log("source: spec/serializers/v1/coupon_serializer_spec.rb:13")
		t.Log("source: spec/serializers/v1/coupon_serializer_spec.rb:38")
		p := s.Fixtures.Plan(period.Monthly, 1000)
		child := s.Fixtures.Plan(period.Monthly, 1000, func(c *catalog.Plan) { c.Code = p.Code; c.ParentID = p.ID })
		lc := s.Fixtures.FixedAmountCoupon(10, func(c *coupon.Coupon) { c.LimitedPlanIDs = []core.PlanID{p.ID, child.ID} })
		status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/coupons/"+lc.ID.String(), nil)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		got := decode[struct {
			Coupon restCoupon `json:"coupon"`
		}](t, body).Coupon
		if !got.LimitedPlans || got.LimitedBillableMetrics || !slices.Equal(got.PlanCodes, []string{p.Code}) || len(got.BillableMetricCodes) != 0 {
			t.Errorf("got limited = %t/%t, codes = %v/%v, want = true/false, [%s]/[]",
				got.LimitedPlans, got.LimitedBillableMetrics, got.PlanCodes, got.BillableMetricCodes, p.Code)
		}
	})
}

func TestDeleteCouponRPC(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	c := s.Fixtures.FixedAmountCoupon(10)
	a := s.Fixtures.ApplyCoupon(c, s.Fixtures.Customer())

	t.Log("source: spec/requests/api/v1/coupons_controller_spec.rb:139")
	status, body := s.REST(t, s.APIKey, http.MethodDelete, "/v1/coupons/code%3A"+c.Code, nil)
	if status != http.StatusOK {
		t.Fatalf("got status = %d (%s), want = %d", status, body, http.StatusOK)
	}
	got := decode[struct {
		Coupon restCoupon `json:"coupon"`
	}](t, body).Coupon
	if got.ID != c.ID.String() || got.Code != c.Code {
		t.Errorf("got %+v, want id %s code %s", got, c.ID, c.Code)
	}
	if got := s.Fixtures.ReloadAppliedCoupon(a.ID).Status; got != coupon.StatusTerminated {
		t.Errorf("got applied coupon status = %s, want = %s", got, coupon.StatusTerminated)
	}

	t.Log("source: spec/requests/api/v1/coupons_controller_spec.rb:135")
	if status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/coupons/"+c.ID.String(), nil); status != http.StatusNotFound {
		t.Errorf("get deleted: got status = %d (%s), want = %d", status, body, http.StatusNotFound)
	}

	t.Log("source: spec/requests/api/v1/coupons_controller_spec.rb:150")
	if status, body := s.REST(t, s.APIKey, http.MethodDelete, "/v1/coupons/code%3A"+c.Code, nil); status != http.StatusNotFound {
		t.Errorf("delete again: got status = %d (%s), want = %d", status, body, http.StatusNotFound)
	}
}

func TestListCouponsRPC(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	first := s.Fixtures.FixedAmountCoupon(10)

	t.Log("source: spec/requests/api/v1/coupons_controller_spec.rb:165")
	status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/coupons", nil)
	if status != http.StatusOK {
		t.Fatalf("got status = %d (%s), want = %d", status, body, http.StatusOK)
	}
	page := decode[struct {
		Coupons []restCoupon `json:"coupons"`
	}](t, body)
	if len(page.Coupons) != 1 || page.Coupons[0].ID != first.ID.String() || page.Coupons[0].Code != first.Code {
		t.Fatalf("got %+v, want only %s", page.Coupons, first.Code)
	}

	t.Log("source: spec/requests/api/v1/coupons_controller_spec.rb:179")
	s.Fixtures.FixedAmountCoupon(10)
	status, body = s.REST(t, s.APIKey, http.MethodGet, "/v1/coupons?page_size=1", nil)
	if status != http.StatusOK {
		t.Fatalf("got status = %d (%s), want = %d", status, body, http.StatusOK)
	}
	p1 := decode[struct {
		Coupons       []restCoupon `json:"coupons"`
		NextPageToken string       `json:"next_page_token"`
	}](t, body)
	if len(p1.Coupons) != 1 || p1.NextPageToken == "" {
		t.Fatalf("got %+v, want one coupon and a next page", p1)
	}
	status, body = s.REST(t, s.APIKey, http.MethodGet, "/v1/coupons?page_size=1&page_token="+url.QueryEscape(p1.NextPageToken), nil)
	if status != http.StatusOK {
		t.Fatalf("got status = %d (%s), want = %d", status, body, http.StatusOK)
	}
	p2 := decode[struct {
		Coupons       []restCoupon `json:"coupons"`
		NextPageToken string       `json:"next_page_token"`
	}](t, body)
	if len(p2.Coupons) != 1 || p2.NextPageToken != "" || p2.Coupons[0].ID == p1.Coupons[0].ID {
		t.Fatalf("got %+v, want the other coupon on the last page", p2)
	}

	t.Log("source: spec/queries/coupons_query_spec.rb:80")
	status, body = s.REST(t, s.APIKey, http.MethodGet, "/v1/coupons?filter="+url.QueryEscape(`status = "terminated"`), nil)
	if status != http.StatusOK {
		t.Fatalf("got status = %d (%s), want = %d", status, body, http.StatusOK)
	}
	if got := decode[struct {
		Coupons []restCoupon `json:"coupons"`
	}](t, body).Coupons; len(got) != 0 {
		t.Errorf("got %+v, want no terminated coupon", got)
	}
}

func TestApplyCouponRPC(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	cust := s.Fixtures.Customer()
	c := s.Fixtures.FixedAmountCoupon(123)
	tests := []struct {
		name   string
		source string
		path   string
		body   string
		status int
		reason string
	}{
		{
			"applies", "spec/requests/api/v1/applied_coupons_controller_spec.rb:28", "/v1/applied_coupons",
			`{"customer_id":"external_id:` + cust.ExternalID + `","coupon_id":"code:` + c.Code + `"}`, http.StatusOK, "",
		},
		{
			"customer binding", "spec/requests/api/v1/applied_coupons_controller_spec.rb:28",
			"/v1/customers/external_id%3A" + cust.ExternalID + "/applied_coupons",
			`{"coupon_id":"` + c.ID.String() + `"}`, http.StatusOK, "",
		},
		// applied_coupons/create_service.rb:67-68.
		{
			"unknown customer", "spec/requests/api/v1/applied_coupons_controller_spec.rb:48", "/v1/applied_coupons",
			`{"customer_id":"external_id:unknown","coupon_id":"code:unknown"}`, http.StatusNotFound, "customer_not_found",
		},
		{
			"unknown coupon", "spec/requests/api/v1/applied_coupons_controller_spec.rb:48", "/v1/applied_coupons",
			`{"customer_id":"` + cust.ID.String() + `","coupon_id":"code:unknown"}`, http.StatusNotFound, "coupon_not_found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			status, body := s.REST(t, s.APIKey, http.MethodPost, tt.path, tt.body)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if tt.status != http.StatusOK {
				if got := couponReason(t, body); got != tt.reason {
					t.Errorf("got reason = %q, want = %q", got, tt.reason)
				}
				return
			}
			got := decode[struct {
				AppliedCoupon restAppliedCoupon `json:"applied_coupon"`
			}](t, body).AppliedCoupon
			if got.ID == "" || got.CouponID != c.ID.String() || got.CustomerID != cust.ID.String() || got.CreatedAt == "" {
				t.Errorf("got %+v, want coupon %s on customer %s", got, c.ID, cust.ID)
			}
			if got.Amount.Amount != "1.23" || got.Amount.Currency != "USD" {
				t.Errorf("got amount = %+v, want = 1.23 USD", got.Amount)
			}
		})
	}
}

// TestListAppliedCouponsRPC ports the shared examples "a applied coupon
// index endpoint" on both routes.
func TestListAppliedCouponsRPC(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	f := s.Fixtures
	cust := f.Customer()
	c1, c2 := f.FixedAmountCoupon(10), f.FixedAmountCoupon(10)
	gone := f.FixedAmountCoupon(10)
	ac1 := f.ApplyCoupon(c1, cust)
	s.Clock.Advance(time.Second)
	ac2 := f.ApplyCoupon(c2, cust)
	other := f.Customer()
	s.Clock.Advance(time.Second)
	acOther := f.ApplyCoupon(c1, other)
	s.Clock.Advance(time.Second)
	acGone := f.ApplyCoupon(gone, cust)
	if status, body := s.REST(t, s.APIKey, http.MethodDelete, "/v1/coupons/"+gone.ID.String(), nil); status != http.StatusOK {
		t.Fatalf("delete coupon: got status = %d (%s)", status, body)
	}

	ids := func(t *testing.T, path string) ([]string, string) {
		t.Helper()
		status, body := s.REST(t, s.APIKey, http.MethodGet, path, nil)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = %d", status, body, http.StatusOK)
		}
		page := decode[struct {
			AppliedCoupons []restAppliedCoupon `json:"applied_coupons"`
			NextPageToken  string              `json:"next_page_token"`
		}](t, body)
		out := make([]string, len(page.AppliedCoupons))
		for i, a := range page.AppliedCoupons {
			out[i] = a.ID
		}
		return out, page.NextPageToken
	}
	filter := func(f string) string { return "filter=" + url.QueryEscape(f) }
	routes := map[string]string{
		"all":      "/v1/applied_coupons?customer_id=external_id%3A" + cust.ExternalID,
		"customer": "/v1/customers/external_id%3A" + cust.ExternalID + "/applied_coupons?page_size=100",
	}
	for route, base := range routes {
		tests := []struct {
			name   string
			source string
			query  string
			want   []string
		}{
			{"newest first", "spec/support/shared_examples/applied_coupon_index.rb:35", "", []string{acGone.ID.String(), ac2.ID.String(), ac1.ID.String()}},
			{"status active", "spec/support/shared_examples/applied_coupon_index.rb:77", filter(`status = "active"`), []string{ac2.ID.String(), ac1.ID.String()}},
			{"status terminated", "spec/support/shared_examples/applied_coupon_index.rb:88", filter(`status = "terminated"`), []string{acGone.ID.String()}},
			{"coupon code", "spec/support/shared_examples/applied_coupon_index.rb:101", filter(`coupon_id = "code:` + c1.Code + `"`), []string{ac1.ID.String()}},
			{"code of a deleted coupon", "spec/support/shared_examples/applied_coupon_index.rb:111", filter(`coupon_id = "code:` + gone.Code + `"`), []string{acGone.ID.String()}},
			{"unknown coupon code", "spec/support/shared_examples/applied_coupon_index.rb:128", filter(`coupon_id = "code:unknown"`), []string{}},
			{"coupon id", "", filter(`coupon_id = "` + c2.ID.String() + `"`), []string{ac2.ID.String()}},
		}
		for _, tt := range tests {
			t.Run(route+"/"+tt.name, func(t *testing.T) {
				if tt.source != "" {
					t.Logf("source: %s", tt.source)
				}
				got, _ := ids(t, base+"&"+tt.query)
				if !slices.Equal(got, tt.want) {
					t.Errorf("got = %v, want = %v", got, tt.want)
				}
			})
		}
		t.Run(route+"/pagination", func(t *testing.T) {
			t.Log("source: spec/support/shared_examples/applied_coupon_index.rb:54")
			got, next := ids(t, base+"&page_size=2")
			if !slices.Equal(got, []string{acGone.ID.String(), ac2.ID.String()}) || next == "" {
				t.Fatalf("got = %v (next %q), want the two newest and a next page", got, next)
			}
			got, next = ids(t, base+"&page_size=2&page_token="+url.QueryEscape(next))
			if !slices.Equal(got, []string{ac1.ID.String()}) || next != "" {
				t.Errorf("got = %v (next %q), want the oldest on the last page", got, next)
			}
		})
	}

	t.Run("coupon attributes", func(t *testing.T) {
		t.Log("source: spec/serializers/v1/applied_coupon_serializer_spec.rb:13")
		status, body := s.REST(t, s.APIKey, http.MethodGet, routes["all"], nil)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		for _, a := range decode[struct {
			AppliedCoupons []restAppliedCoupon `json:"applied_coupons"`
		}](t, body).AppliedCoupons {
			want := c1
			switch a.ID {
			case ac2.ID.String():
				want = c2
			case acGone.ID.String():
				want = gone
			}
			if a.CouponCode != want.Code || a.CouponName != want.Name || a.ExternalCustomerID != cust.ExternalID {
				t.Errorf("got %+v, want coupon %s %q and customer %s", a, want.Code, want.Name, cust.ExternalID)
			}
			if deleted := a.CouponDeletedAt != ""; deleted != (want == gone) {
				t.Errorf("applied coupon %s: got coupon_deleted_at = %q, want set = %t", a.ID, a.CouponDeletedAt, want == gone)
			}
		}
	})
	t.Run("without customer", func(t *testing.T) {
		got, _ := ids(t, "/v1/applied_coupons")
		want := []string{acGone.ID.String(), acOther.ID.String(), ac2.ID.String(), ac1.ID.String()}
		if !slices.Equal(got, want) {
			t.Errorf("got = %v, want = %v", got, want)
		}
	})
	t.Run("deleted customer", func(t *testing.T) {
		t.Log("source: spec/queries/applied_coupons_query_spec.rb:46")
		f.UpdateCustomer(other, func(c *customer.Customer) {
			if err := c.Archive(s.Clock.Now()); err != nil {
				t.Fatal(err)
			}
		})
		got, _ := ids(t, "/v1/applied_coupons")
		want := []string{acGone.ID.String(), ac2.ID.String(), ac1.ID.String()}
		if !slices.Equal(got, want) {
			t.Errorf("got = %v, want = %v", got, want)
		}
	})
	for _, path := range []string{
		"/v1/customers/external_id%3Aunknown/applied_coupons",
		"/v1/applied_coupons?customer_id=external_id%3Aunknown",
	} {
		t.Run("unknown customer "+path, func(t *testing.T) {
			t.Log("source: spec/requests/api/v1/customers/applied_coupons_controller_spec.rb:17")
			if status, body := s.REST(t, s.APIKey, http.MethodGet, path, nil); status != http.StatusNotFound {
				t.Errorf("got status = %d (%s), want = %d", status, body, http.StatusNotFound)
			}
		})
	}
}

func TestTerminateAppliedCouponRPC(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	a := s.Fixtures.ApplyCoupon(s.Fixtures.FixedAmountCoupon(10), s.Fixtures.Customer())

	t.Log("source: spec/requests/api/v1/customers/applied_coupons_controller_spec.rb:53")
	t.Log("source: spec/requests/api/v1/customers/applied_coupons_controller_spec.rb:59")
	status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/applied_coupons/"+a.ID.String()+":terminate", `{}`)
	if status != http.StatusOK {
		t.Fatalf("got status = %d (%s), want = %d", status, body, http.StatusOK)
	}
	got := decode[struct {
		AppliedCoupon restAppliedCoupon `json:"applied_coupon"`
	}](t, body).AppliedCoupon
	if got.ID != a.ID.String() || got.Status != "COUPON_STATUS_TERMINATED" {
		t.Errorf("got %+v, want %s terminated", got, a.ID)
	}

	t.Log("source: spec/requests/api/v1/customers/applied_coupons_controller_spec.rb:79")
	unknown := core.NewID[core.AppliedCouponID](core.UUIDv7Generator{}).String()
	status, body = s.REST(t, s.APIKey, http.MethodPost, "/v1/applied_coupons/"+unknown+":terminate", `{}`)
	if status != http.StatusNotFound {
		t.Fatalf("got status = %d (%s), want = %d", status, body, http.StatusNotFound)
	}
	if got := couponReason(t, body); got != "applied_coupon_not_found" {
		t.Errorf("got reason = %q, want = applied_coupon_not_found", got)
	}
}

// TestCouponPermissions ports the "requires API permission" examples of
// the coupon and applied coupon endpoints.
func TestCouponPermissions(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	c := s.Fixtures.FixedAmountCoupon(10)
	cust := s.Fixtures.Customer()
	d := auth.Deps{Tx: s.Store, Clock: s.Clock, IDs: core.UUIDv7Generator{}, Rand: rand.Reader}
	keyWith := func(resource permission.APIResource, level permission.Level) rpctest.Credential {
		k, err := auth.NewAPIKeys(d, nil).Create(t.Context(), s.Org.ID, nil, map[permission.APIResource]permission.Level{resource: level})
		if err != nil {
			t.Fatal(err)
		}
		return rpctest.Credential{Token: k.Secret}
	}
	couponRead, couponWrite := keyWith(permission.APIResourceCoupon, permission.LevelRead), keyWith(permission.APIResourceCoupon, permission.LevelWrite)
	appliedRead, appliedWrite := keyWith(permission.APIResourceAppliedCoupon, permission.LevelRead), keyWith(permission.APIResourceAppliedCoupon, permission.LevelWrite)

	create := func(cr rpctest.Credential) error {
		_, err := couponsClient(s, cr).CreateCoupon(t.Context(), couponv1.CreateCouponRequest_builder{
			Coupon: couponv1.Coupon_builder{
				Code: new("new_" + cr.Token[len(cr.Token)-6:]), Name: new("n"),
				CouponType:     couponv1.CouponType_COUPON_TYPE_PERCENTAGE.Enum(),
				PercentageRate: new("10"),
				Frequency:      couponv1.CouponFrequency_COUPON_FREQUENCY_ONCE.Enum(),
				Expiration:     couponv1.CouponExpiration_COUPON_EXPIRATION_NO_EXPIRATION.Enum(),
			}.Build(),
		}.Build())
		return err
	}
	get := func(cr rpctest.Credential) error {
		_, err := couponsClient(s, cr).GetCoupon(t.Context(), couponv1.GetCouponRequest_builder{CouponId: new(c.ID.String())}.Build())
		return err
	}
	apply := func(cr rpctest.Credential) error {
		_, err := couponsClient(s, cr).ApplyCoupon(t.Context(), couponv1.ApplyCouponRequest_builder{
			AppliedCoupon: couponv1.AppliedCoupon_builder{CouponId: new(c.ID.String()), CustomerId: new(cust.ID.String())}.Build(),
		}.Build())
		return err
	}
	list := func(cr rpctest.Credential) error {
		_, err := couponsClient(s, cr).ListAppliedCoupons(t.Context(), couponv1.ListAppliedCouponsRequest_builder{}.Build())
		return err
	}
	tests := []struct {
		name   string
		source string
		call   func(rpctest.Credential) error
		cred   rpctest.Credential
		code   connect.Code
		reason string
	}{
		{"coupon write key creates", "spec/requests/api/v1/coupons_controller_spec.rb:31", create, couponWrite, 0, ""},
		{"coupon read key cannot create", "spec/requests/api/v1/coupons_controller_spec.rb:31", create, couponRead, connect.CodePermissionDenied, "write_action_not_allowed_for_coupon"},
		{"coupon read key reads", "spec/requests/api/v1/coupons_controller_spec.rb:107", get, couponRead, 0, ""},
		{"applied coupon key cannot read coupons", "spec/requests/api/v1/coupons_controller_spec.rb:107", get, appliedWrite, connect.CodePermissionDenied, "read_action_not_allowed_for_coupon"},
		{"applied coupon read key cannot apply", "spec/requests/api/v1/applied_coupons_controller_spec.rb:26", apply, appliedRead, connect.CodePermissionDenied, "write_action_not_allowed_for_applied_coupon"},
		{"coupon key cannot apply", "spec/requests/api/v1/applied_coupons_controller_spec.rb:26", apply, couponWrite, connect.CodePermissionDenied, "write_action_not_allowed_for_applied_coupon"},
		{"applied coupon write key applies", "spec/requests/api/v1/applied_coupons_controller_spec.rb:26", apply, appliedWrite, 0, ""},
		{"applied coupon read key lists", "spec/support/shared_examples/applied_coupon_index.rb:33", list, appliedRead, 0, ""},
		{"coupon key cannot list applications", "spec/support/shared_examples/applied_coupon_index.rb:33", list, couponRead, connect.CodePermissionDenied, "read_action_not_allowed_for_applied_coupon"},
		{"anonymous", "", get, rpctest.Anonymous, connect.CodeUnauthenticated, rpc.ReasonUnauthenticated},
		{"admin session", "", get, s.Session, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.source != "" {
				t.Logf("source: %s", tt.source)
			}
			err := tt.call(tt.cred)
			if tt.code == 0 {
				if err != nil {
					t.Fatalf("got %v, want = nil", err)
				}
				return
			}
			if got := connect.CodeOf(err); got != tt.code {
				t.Fatalf("got = %v (%v), want = %v", got, err, tt.code)
			}
			if got := rpc.ErrorReason(err); got != tt.reason {
				t.Errorf("got reason = %q, want = %q", got, tt.reason)
			}
		})
	}
}

// couponReason returns the ErrorInfo reason of a REST error body.
func couponReason(t *testing.T, body []byte) string {
	t.Helper()
	e := decode[struct {
		Details []struct {
			Type   string `json:"@type"`
			Reason string `json:"reason"`
		} `json:"details"`
	}](t, body)
	for _, d := range e.Details {
		if d.Type == "type.googleapis.com/google.rpc.ErrorInfo" {
			return d.Reason
		}
	}
	return ""
}
