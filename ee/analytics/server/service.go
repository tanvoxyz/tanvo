// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

// Package server is the Connect service of the Data API
// (tanvo.dataapi.v1.DataApiService), with its REST transcoding, the
// verification of the core's service tokens and the license check.
package server

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/genproto/googleapis/type/date"

	"github.com/tanvoxyz/tanvo/ee/analytics/currency"
	"github.com/tanvoxyz/tanvo/ee/analytics/period"
	"github.com/tanvoxyz/tanvo/ee/analytics/store"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
	customerv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/customer/v1"
	dataapiv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/dataapi/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/dataapi/v1/dataapiv1connect"
)

// Reports is what the service reads: the analytics store.
type Reports interface {
	MRR(ctx context.Context, org string, f store.Filter) ([]store.MRRPoint, error)
	MRRByPlan(ctx context.Context, org string, f store.Filter, page store.Page) ([]store.PlanMRR, int64, error)
	Revenue(ctx context.Context, org string, f store.Filter) ([]store.RevenuePoint, error)
	RevenueByCustomer(ctx context.Context, org string, f store.Filter, order store.RevenueOrder, page store.Page) ([]store.GroupRevenue, int64, error)
	RevenueByPlan(ctx context.Context, org string, f store.Filter, order store.RevenueOrder, page store.Page) ([]store.GroupRevenue, int64, error)
	PrepaidCredits(ctx context.Context, org string, f store.Filter) ([]store.PrepaidPoint, error)
	Usage(ctx context.Context, org string, f store.Filter, u store.UsageFilter, total bool) ([]store.UsagePoint, error)
	InvoicedUsage(ctx context.Context, org string, f store.Filter, u store.InvoicedUsageFilter) ([]store.InvoicedUsagePoint, error)
	Recognition(ctx context.Context, org string, f store.Filter) ([]store.RecognitionPoint, error)
}

// Service implements DataApiService.
type Service struct {
	dataapiv1connect.UnimplementedDataApiServiceHandler

	reports Reports
	now     func() time.Time
}

var _ dataapiv1connect.DataApiServiceHandler = (*Service)(nil)

// NewService returns the service over reports; now is the clock.
func NewService(reports Reports, now func() time.Time) *Service {
	return &Service{reports: reports, now: now}
}

// Page sizes.
const (
	defaultPageSize = 20
	maxPageSize     = 1000
)

// filter converts a request filter, with g the default granularity.
func (s *Service) filter(f *dataapiv1.Filter, g period.Granularity) (store.Filter, error) {
	switch f.GetTimeGranularity() {
	case dataapiv1.TimeGranularity_TIME_GRANULARITY_DAILY:
		g = period.Daily
	case dataapiv1.TimeGranularity_TIME_GRANULARITY_WEEKLY:
		g = period.Weekly
	case dataapiv1.TimeGranularity_TIME_GRANULARITY_MONTHLY:
		g = period.Monthly
	}
	to := period.Day(s.now(), time.UTC)
	if f.HasToDate() {
		d, err := day(f.GetToDate(), "filter.to_date")
		if err != nil {
			return store.Filter{}, err
		}
		to = d
	}
	from := period.DefaultFrom(to, g)
	if f.HasFromDate() {
		d, err := day(f.GetFromDate(), "filter.from_date")
		if err != nil {
			return store.Filter{}, err
		}
		from = d
	}
	ps, err := period.Split(from, to, g)
	if err != nil {
		return store.Filter{}, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	out := store.Filter{
		Periods:                ps,
		Currency:               f.GetCurrency(),
		CustomerCountry:        f.GetCustomerCountry(),
		ExternalCustomerID:     f.GetExternalCustomerId(),
		ExternalSubscriptionID: f.GetExternalSubscriptionId(),
		BillingEntityCode:      f.GetBillingEntityCode(),
		PlanCode:               f.GetPlanCode(),
	}
	switch f.GetCustomerType() {
	case customerv1.CustomerType_CUSTOMER_TYPE_COMPANY:
		out.CustomerType = "company"
	case customerv1.CustomerType_CUSTOMER_TYPE_INDIVIDUAL:
		out.CustomerType = "individual"
	}
	if f.HasIsCustomerTinEmpty() {
		v := f.GetIsCustomerTinEmpty()
		out.CustomerTINEmpty = &v
	}
	return out, nil
}

// day converts a date of field.
func day(d *date.Date, field string) (time.Time, error) {
	t := period.Date(int(d.GetYear()), time.Month(d.GetMonth()), int(d.GetDay()))
	if d.GetYear() < 1 || d.GetMonth() < 1 || d.GetMonth() > 12 || d.GetDay() < 1 || t.Day() != int(d.GetDay()) {
		return time.Time{}, connect.NewError(connect.CodeInvalidArgument, field+": not a date")
	}
	return t, nil
}

func protoDate(t time.Time) *date.Date {
	return &date.Date{Year: int32(t.Year()), Month: int32(t.Month()), Day: int32(t.Day())} //nolint:gosec // calendar fields
}

func protoPeriod(f store.Filter, i int) *dataapiv1.Period {
	p := f.Periods[i]
	return dataapiv1.Period_builder{StartDate: protoDate(p.Start), EndDate: protoDate(p.End)}.Build()
}

// moneys converts amounts in minor units of c.
type moneys struct {
	currency string
	err      error
}

func (m *moneys) of(minor string) *commonv1.Money {
	amount, err := currency.MajorString(minor, m.currency)
	if err != nil && m.err == nil {
		m.err = err
	}
	return commonv1.Money_builder{Currency: &m.currency, Amount: &amount}.Build()
}

// page decodes a page request.
func page(size int32, token string) (store.Page, error) {
	p := store.Page{Size: int(size)}
	if p.Size == 0 {
		p.Size = defaultPageSize
	}
	p.Size = min(p.Size, maxPageSize)
	if token == "" {
		return p, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err == nil {
		p.Offset, err = strconv.Atoi(string(raw))
	}
	if err != nil || p.Offset < 0 {
		return store.Page{}, connect.NewError(connect.CodeInvalidArgument, "page_token: invalid")
	}
	return p, nil
}

// nextToken returns the token of the page after p, empty after the last.
func nextToken(p store.Page, n int, total int64) string {
	next := p.Offset + n
	if n == 0 || int64(next) >= total {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(next)))
}

// internal wraps a store error.
func internal(err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return connect.NewError(connect.CodeCanceled, err.Error()).WithCause(err)
	}
	return connect.NewError(connect.CodeInternal, "internal error").WithCause(err)
}

func interval(s string) commonv1.Interval {
	switch s {
	case "weekly":
		return commonv1.Interval_INTERVAL_WEEKLY
	case "monthly":
		return commonv1.Interval_INTERVAL_MONTHLY
	case "quarterly":
		return commonv1.Interval_INTERVAL_QUARTERLY
	case "semiannual":
		return commonv1.Interval_INTERVAL_SEMIANNUAL
	case "yearly":
		return commonv1.Interval_INTERVAL_YEARLY
	}
	return commonv1.Interval_INTERVAL_UNSPECIFIED
}

func planRef(p store.Plan) *dataapiv1.PlanRef {
	iv := interval(p.Interval)
	return dataapiv1.PlanRef_builder{
		PlanId: &p.ID, PlanCode: &p.Code, PlanName: &p.Name, PlanInterval: &iv, PlanDeleted: &p.Deleted,
	}.Build()
}

func revenueOrder(o dataapiv1.RevenueOrder) store.RevenueOrder {
	if o == dataapiv1.RevenueOrder_REVENUE_ORDER_NET_REVENUE {
		return store.ByNet
	}
	return store.ByGross
}
