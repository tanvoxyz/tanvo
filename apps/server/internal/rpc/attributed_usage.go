package rpc

import (
	"context"
	"encoding/base64"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/libs/billing/usageattribution"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
	attributionv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/attribution/v1"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
)

// ReasonFeatureUnavailable is the reason of an attributed usage request of
// an organization whose events are not in the ClickHouse store (lago's
// forbidden feature_unavailable).
const ReasonFeatureUnavailable = "feature_unavailable"

// WithAttributedUsage serves GetAttributedUsage over q and returns s.
func (s *UsageAttribution) WithAttributedUsage(q *usageattribution.QueryService) *UsageAttribution {
	s.query = q
	return s
}

var attributionBases = NewEnum(map[usageattribution.Basis]attributionv1.AttributionBasis{
	usageattribution.BasisUnits:  attributionv1.AttributionBasis_ATTRIBUTION_BASIS_UNITS,
	usageattribution.BasisAmount: attributionv1.AttributionBasis_ATTRIBUTION_BASIS_AMOUNT,
})

// attributedPagePrefix marks the page tokens of GetAttributedUsage.
const attributedPagePrefix = "offset:"

// attributedPageToken returns the token of the page at offset.
func attributedPageToken(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(attributedPagePrefix + strconv.Itoa(offset)))
}

// attributedOffset returns the offset of a page token; "" is 0.
func attributedOffset(token string) (int, error) {
	if token == "" {
		return 0, nil
	}
	invalid := InvalidArgument("invalid page token", []FieldViolation{{Field: "page_token", Reason: core.CodeInvalid}})
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, invalid
	}
	n, ok := strings.CutPrefix(string(b), attributedPagePrefix)
	if !ok {
		return 0, invalid
	}
	offset, err := strconv.Atoi(n)
	if err != nil {
		return 0, invalid
	}
	return offset, nil
}

// GetAttributedUsage implements UsageAttributionService.GetAttributedUsage
// (lago Api::V1::Customers::AttributedUsageController#show): the usage of
// a customer's active subscription grouped by the values of an
// attribution type, paged by rank. An organization whose events are not
// in the ClickHouse store is PermissionDenied feature_unavailable; the
// query's limits are FailedPrecondition too_many_groups, query_timeout
// and memory_limit_exceeded.
func (s *UsageAttribution) GetAttributedUsage(ctx context.Context, req *attributionv1.GetAttributedUsageRequest) (*attributionv1.GetAttributedUsageResponse, error) {
	if s.query == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "attributed usage is not served")
	}
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	in := usageattribution.QueryInput{
		OrganizationID:  p.OrganizationID,
		GroupBy:         req.GetGroupBy(),
		SplitChargeCode: req.GetSplitChargeCode(),
		Search:          req.GetSearch(),
		Basis:           attributionBases.Core(req.GetBasis()),
		Limit:           int(req.GetPageSize()),
	}
	if req.GetPageSize() < 0 {
		in.Limit = -1
	}
	c, err := subCustomerRef("customer_id", req.GetCustomerId())
	if err != nil {
		return nil, err
	}
	in.CustomerID, in.CustomerExternalID = c.ID, c.ExternalID
	sub, err := subRef("subscription_id", req.GetSubscriptionId())
	if err != nil {
		return nil, err
	}
	in.SubscriptionID, in.SubscriptionExternalID = sub.ID, sub.ExternalID
	if len(req.GetFilters()) > 0 {
		in.Filters = map[string][]string{}
		for code, v := range req.GetFilters() {
			in.Filters[code] = slices.Clone(v.GetValues())
		}
	}
	if req.HasFromTime() {
		in.From = new(req.GetFromTime().AsTime().UTC())
	}
	if req.HasToTime() {
		in.To = new(req.GetToTime().AsTime().UTC())
	}
	// A repeated field cannot tell an empty selection from none: an empty
	// one reads every charge.
	if codes := req.GetChargeCodes(); len(codes) > 0 {
		in.ChargeCodes = slices.Clone(codes)
	}
	if in.Offset, err = attributedOffset(req.GetPageToken()); err != nil {
		return nil, err
	}

	res, err := s.query.Query(ctx, in)
	if errors.Is(err, usageattribution.ErrUnavailable) {
		return nil, newError(connect.CodePermissionDenied, ReasonFeatureUnavailable, "attributed usage needs the organization's events in the ClickHouse store")
	}
	if err != nil {
		return nil, Error(ctx, err)
	}
	return attributedUsageProto(res), nil
}

// attributedUsageProto converts a query result.
//
// source: app/serializers/v1/attributed_usage_serializer.rb:5-57
func attributedUsageProto(res *usageattribution.Result) *attributionv1.GetAttributedUsageResponse {
	rows := make([]*attributionv1.AttributedUsageRow, len(res.Rows))
	for i, r := range res.Rows {
		rows[i] = attributionv1.AttributedUsageRow_builder{
			Value: new(r.Value), Rank: new(int32(r.Rank)), Usage: attributedAggregateProto(r.Aggregate, res.Currency),
		}.Build()
	}
	b := attributionv1.GetAttributedUsageResponse_builder{
		SubscriptionId:         new(res.Subscription.ID.String()),
		ExternalSubscriptionId: new(res.Subscription.ExternalID),
		GroupBy:                new(res.GroupBy),
		Basis:                  new(attributionBases.Proto(res.Basis)),
		Period:                 dateTimeRange(res.From, res.To),
		Currency:               new(res.Currency.Code()),
		Rows:                   rows,
		Unattributed:           attributedAggregateProto(res.Unattributed, res.Currency),
		Totals:                 attributedAggregateProto(res.Totals, res.Currency),
		TotalSize:              new(res.Groups),
	}
	if len(res.Rows) == res.Limit && int64(res.Offset+res.Limit) < res.Groups {
		b.NextPageToken = new(attributedPageToken(res.Offset + res.Limit))
	}
	return b.Build()
}

// attributedAmount returns the rounded and the precise amount of minor
// units d; nil and nil when unset.
func attributedAmount(d *money.Decimal, c money.Currency) (*commonv1.Money, *string) {
	if d == nil {
		return nil, nil
	}
	minor, _ := d.Round(0, money.HalfUp).Int64()
	return Money(minor, c), new(Decimal(d.Reduce()))
}

func attributedAggregateProto(a usageattribution.Aggregate, c money.Currency) *attributionv1.AttributedAggregate {
	cells := make([]*attributionv1.AttributedChargeUsage, len(a.Cells))
	for i, cell := range a.Cells {
		b := attributionv1.AttributedChargeUsage_builder{
			ChargeId:    new(cell.Charge.ID.String()),
			ChargeCode:  new(cell.Charge.Code),
			MetricCode:  new(cell.Metric.Code),
			Units:       new(Decimal(cell.Units.Reduce())),
			EventsCount: new(cell.Events),
		}
		b.Amount, b.PreciseAmount = attributedAmount(cell.Amount, c)
		if f := cell.Filter; f != nil {
			b.ChargeFilterId = new(f.ID.String())
			b.ChargeFilterInvoiceDisplayName = f.InvoiceDisplayName
			b.ChargeFilterValues = map[string]*attributionv1.Values{}
			for _, k := range slices.Sorted(maps.Keys(f.Values)) {
				b.ChargeFilterValues[k] = attributionv1.Values_builder{Values: slices.Clone(f.Values[k])}.Build()
			}
		}
		cells[i] = b.Build()
	}
	b := attributionv1.AttributedAggregate_builder{EventsCount: new(a.Events), ChargesUsage: cells}
	b.Amount, b.PreciseAmount = attributedAmount(a.Amount, c)
	return b.Build()
}
