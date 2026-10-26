// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package server

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"buf.build/go/protovalidate"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/tanvoxyz/tanvo/ee/analytics/period"
	"github.com/tanvoxyz/tanvo/ee/analytics/servicetoken"
	"github.com/tanvoxyz/tanvo/ee/analytics/store"
)

const org = "0193b1d2-7f00-7000-8000-000000000001"

// fakeReports records the calls and returns fixed rows.
type fakeReports struct {
	org    string
	filter store.Filter
	page   store.Page
	order  store.RevenueOrder
	err    error
}

func (f *fakeReports) MRR(_ context.Context, org string, flt store.Filter) ([]store.MRRPoint, error) {
	f.org, f.filter = org, flt
	return []store.MRRPoint{{
		Point: store.Point{Period: 0, Currency: "USD"}, Starting: "1000", Ending: "2550",
		New: "1550", Expansion: "0", Contraction: "0", Churn: "0",
	}}, f.err
}

func (f *fakeReports) MRRByPlan(_ context.Context, org string, flt store.Filter, p store.Page) ([]store.PlanMRR, int64, error) {
	f.org, f.filter, f.page = org, flt, p
	return []store.PlanMRR{{Plan: store.Plan{ID: "p", Code: "pro", Name: "Pro", Interval: "monthly"}, Currency: "JPY", MRR: "500", Share: "1", Customers: 2, CustomersShare: "1"}}, 3, f.err
}

func (f *fakeReports) Revenue(context.Context, string, store.Filter) ([]store.RevenuePoint, error) {
	return nil, f.err
}

func (f *fakeReports) RevenueByCustomer(_ context.Context, _ string, _ store.Filter, o store.RevenueOrder, p store.Page) ([]store.GroupRevenue, int64, error) {
	f.order, f.page = o, p
	return nil, 0, f.err
}

func (f *fakeReports) RevenueByPlan(context.Context, string, store.Filter, store.RevenueOrder, store.Page) ([]store.GroupRevenue, int64, error) {
	return nil, 0, f.err
}

func (f *fakeReports) PrepaidCredits(context.Context, string, store.Filter) ([]store.PrepaidPoint, error) {
	return nil, f.err
}

func (f *fakeReports) Usage(context.Context, string, store.Filter, store.UsageFilter, bool) ([]store.UsagePoint, error) {
	return nil, f.err
}

func (f *fakeReports) InvoicedUsage(context.Context, string, store.Filter, store.InvoicedUsageFilter) ([]store.InvoicedUsagePoint, error) {
	return nil, f.err
}

func (f *fakeReports) Recognition(context.Context, string, store.Filter) ([]store.RecognitionPoint, error) {
	return nil, f.err
}

type license struct{ err error }

func (l *license) Err() error { return l.err }

var now = time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)

// fixture serves the Data API over a fake store.
type fixture struct {
	reports *fakeReports
	license *license
	priv    ed25519.PrivateKey
	srv     *httptest.Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{reports: &fakeReports{}, license: &license{}, priv: priv}
	v, err := protovalidate.New()
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	tel, err := Telemetry(tracenoop.NewTracerProvider(), metricnoop.NewMeterProvider(), log)
	if err != nil {
		t.Fatal(err)
	}
	h, err := Handler(NewService(f.reports, func() time.Time { return now }),
		tel, Recover(log), Licensed(f.license),
		Auth(servicetoken.NewVerifier(servicetoken.Keys{"k1": pub}, func() time.Time { return now })), Validate(v))
	if err != nil {
		t.Fatal(err)
	}
	f.srv = httptest.NewServer(h)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fixture) token(t *testing.T, c servicetoken.Claims) string {
	t.Helper()
	if c.ExpiresAt.IsZero() {
		c.ExpiresAt = now.Add(15 * time.Minute)
	}
	tok, err := servicetoken.Sign(f.priv, "k1", c, now)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (f *fixture) get(t *testing.T, path, token string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, f.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("body %s: %v", body, err)
		}
	}
	return resp.StatusCode, out
}

func TestAuth(t *testing.T) {
	f := newFixture(t)
	read := servicetoken.Claims{OrganizationID: org, Subject: "user:u", Permissions: []string{"data_api:read"}}
	for _, tc := range []struct {
		name   string
		token  string
		status int
	}{
		{"no token", "", http.StatusUnauthorized},
		{"garbage", "abc.def", http.StatusUnauthorized},
		{"expired", f.token(t, servicetoken.Claims{OrganizationID: org, Subject: "user:u", Permissions: read.Permissions, ExpiresAt: now.Add(-time.Hour)}), http.StatusUnauthorized},
		{"wildcard organization", f.token(t, servicetoken.Claims{OrganizationID: "*", Subject: "user:u", Permissions: read.Permissions}), http.StatusUnauthorized},
		{"no permission", f.token(t, servicetoken.Claims{OrganizationID: org, Subject: "user:u"}), http.StatusForbidden},
		{"granted", f.token(t, read), http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := f.get(t, "/v1/data_api/mrr", tc.token)
			if status != tc.status {
				t.Errorf("status got = %d, want = %d (%v)", status, tc.status, body)
			}
		})
	}
	if f.reports.org != org {
		t.Errorf("organization got = %q, want = %q", f.reports.org, org)
	}
}

func TestLicensed(t *testing.T) {
	f := newFixture(t)
	f.license.err = errors.New("expired")
	tok := f.token(t, servicetoken.Claims{OrganizationID: org, Subject: "api_key:k", Permissions: []string{"data_api:read"}})
	status, body := f.get(t, "/v1/data_api/mrr", tok)
	if status != http.StatusServiceUnavailable {
		t.Errorf("status got = %d, want = %d (%v)", status, http.StatusServiceUnavailable, body)
	}
}

func TestMRRREST(t *testing.T) {
	f := newFixture(t)
	tok := f.token(t, servicetoken.Claims{OrganizationID: org, Subject: "api_key:k", Permissions: []string{"data_api:read"}})
	status, body := f.get(t, "/v1/data_api/mrr?filter.from_date=2026-01-10&filter.to_date=2026-02-20&filter.currency=USD&filter.is_customer_tin_empty=true", tok)
	if status != http.StatusOK {
		t.Fatalf("status got = %d, want = 200 (%v)", status, body)
	}
	flt := f.reports.filter
	want := []period.Period{
		{Start: period.Date(2026, 1, 10), End: period.Date(2026, 1, 31)},
		{Start: period.Date(2026, 2, 1), End: period.Date(2026, 2, 20)},
	}
	if len(flt.Periods) != len(want) || flt.Periods[0] != want[0] || flt.Periods[1] != want[1] {
		t.Errorf("periods got = %v, want = %v", flt.Periods, want)
	}
	if flt.Currency != "USD" || flt.CustomerTINEmpty == nil || !*flt.CustomerTINEmpty {
		t.Errorf("filter got = %+v, want USD and TIN empty", flt)
	}
	got, _ := json.Marshal(body)
	for _, s := range []string{
		`"start_date":{"day":10,"month":1,"year":2026}`,
		`"ending_mrr":{"amount":"25.50","currency":"USD"}`,
		`"mrr_change":{"amount":"15.50","currency":"USD"}`,
	} {
		if !strings.Contains(string(got), s) {
			t.Errorf("body got = %s, want it to contain %s", got, s)
		}
	}
}

func TestDefaultRange(t *testing.T) {
	f := newFixture(t)
	tok := f.token(t, servicetoken.Claims{OrganizationID: org, Subject: "api_key:k", Permissions: []string{"data_api:read"}})
	if status, body := f.get(t, "/v1/data_api/mrr", tok); status != http.StatusOK {
		t.Fatalf("status got = %d, want = 200 (%v)", status, body)
	}
	ps := f.reports.filter.Periods
	// Twelve months ending with the one of today, 2026-03-15.
	if len(ps) != 12 || ps[0].Start != period.Date(2025, 4, 1) || ps[11].End != period.Date(2026, 3, 15) {
		t.Errorf("periods got = %v, want 2025-04-01 … 2026-03-15 in 12 months", ps)
	}
}

func TestInvalidFilter(t *testing.T) {
	f := newFixture(t)
	tok := f.token(t, servicetoken.Claims{OrganizationID: org, Subject: "api_key:k", Permissions: []string{"data_api:read"}})
	for _, q := range []string{
		"filter.from_date=2026-03-01&filter.to_date=2026-01-01",
		"filter.to_date=2026-02-30",
		"filter.time_granularity=TIME_GRANULARITY_DAILY&filter.from_date=2020-01-01&filter.to_date=2026-01-01",
		"filter.currency=usd",
	} {
		if status, body := f.get(t, "/v1/data_api/mrr?"+q, tok); status != http.StatusBadRequest {
			t.Errorf("%s: status got = %d, want = 400 (%v)", q, status, body)
		}
	}
}

func TestPagination(t *testing.T) {
	f := newFixture(t)
	tok := f.token(t, servicetoken.Claims{OrganizationID: org, Subject: "api_key:k", Permissions: []string{"data_api:read"}})
	status, body := f.get(t, "/v1/data_api/mrr/plans?page_size=1", tok)
	if status != http.StatusOK {
		t.Fatalf("status got = %d, want = 200 (%v)", status, body)
	}
	if f.reports.page != (store.Page{Size: 1}) {
		t.Errorf("page got = %+v, want = {0 1}", f.reports.page)
	}
	next, _ := body["next_page_token"].(string)
	if next == "" || body["total_size"] != "3" {
		t.Fatalf("body got = %v, want a next page token and total 3", body)
	}
	plans, _ := body["plans"].([]any)
	if len(plans) != 1 {
		t.Fatalf("plans got = %v, want 1", plans)
	}
	// JPY has no minor unit.
	if mrr := plans[0].(map[string]any)["mrr"].(map[string]any)["amount"]; mrr != "500" {
		t.Errorf("JPY mrr got = %v, want = 500", mrr)
	}
	if status, _ := f.get(t, "/v1/data_api/mrr/plans?page_token="+next, tok); status != http.StatusOK {
		t.Fatalf("second page status got = %d, want = 200", status)
	}
	if f.reports.page != (store.Page{Offset: 1, Size: defaultPageSize}) {
		t.Errorf("page got = %+v, want = {1 %d}", f.reports.page, defaultPageSize)
	}
	if status, _ := f.get(t, "/v1/data_api/mrr/plans?page_token=!!", tok); status != http.StatusBadRequest {
		t.Errorf("bad token status got = %d, want = 400", status)
	}
}

func TestOrder(t *testing.T) {
	f := newFixture(t)
	tok := f.token(t, servicetoken.Claims{OrganizationID: org, Subject: "api_key:k", Permissions: []string{"data_api:read"}})
	if status, body := f.get(t, "/v1/data_api/revenue_streams/customers?order_by=REVENUE_ORDER_NET_REVENUE", tok); status != http.StatusOK {
		t.Fatalf("status got = %d, want = 200 (%v)", status, body)
	}
	if f.reports.order != store.ByNet {
		t.Errorf("order got = %v, want = ByNet", f.reports.order)
	}
}

func TestStoreError(t *testing.T) {
	f := newFixture(t)
	f.reports.err = errors.New("boom")
	tok := f.token(t, servicetoken.Claims{OrganizationID: org, Subject: "api_key:k", Permissions: []string{"data_api:read"}})
	status, body := f.get(t, "/v1/data_api/mrr", tok)
	if status != http.StatusInternalServerError || body["message"] != "internal error" {
		t.Errorf("got = %d %v, want = 500 internal error", status, body)
	}
}
