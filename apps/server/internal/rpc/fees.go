package rpc

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/money"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
	invoicev1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/invoice/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/invoice/v1/invoicev1connect"
)

// Fees serves tanvo.invoice.v1.FeeService over the billing library's
// invoicing service (lago Api::V1::FeesController and the adjusted fee
// mutations).
type Fees struct {
	invoicev1connect.UnimplementedFeeServiceHandler

	svc *invoicing.Service
}

var _ invoicev1connect.FeeServiceHandler = (*Fees)(nil)

// NewFees returns the service over svc.
func NewFees(svc *invoicing.Service) *Fees { return &Fees{svc: svc} }

// feeFilterFields are the equality fields of the ListFees filter.
var feeFilterFields = []string{
	"fee_type", "payment_status", "currency", "customer_id", "subscription_id",
	"billable_metric_code", "event_transaction_id",
}

// feeTimeFields are the instant fields of the ListFees filter, compared
// with >= and <=.
var feeTimeFields = []string{"created_at", "succeeded_at", "failed_at", "refunded_at"}

// GetFee implements FeeService.GetFee (lago FeesController#show).
func (s *Fees) GetFee(ctx context.Context, req *invoicev1.GetFeeRequest) (*invoicev1.GetFeeResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.FeeID]("fee_id", req.GetFeeId())
	if err != nil {
		return nil, err
	}
	fv, err := s.svc.GetFee(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return invoicev1.GetFeeResponse_builder{Fee: feeProto(fv)}.Build(), nil
}

// ListFees implements FeeService.ListFees: the fees oldest first, then by ID
// (lago FeesQuery). customer_id and subscription_id take an ID or
// "external_id:<id>"; the instants take RFC 3339 times.
func (s *Fees) ListFees(ctx context.Context, req *invoicev1.ListFeesRequest) (*invoicev1.ListFeesResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	f, err := feeListFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	page, err := s.svc.ListFees(ctx, p.OrganizationID, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return invoicev1.ListFeesResponse_builder{
		Fees:          Items(page, feeProto),
		NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// UpdateFee implements FeeService.UpdateFee: the payment status of a fee
// attached to no invoice (lago Fees::UpdateService).
func (s *Fees) UpdateFee(ctx context.Context, req *invoicev1.UpdateFeeRequest) (*invoicev1.UpdateFeeResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetFee()
	paths, err := UpdatePaths(req.GetUpdateMask(), []string{"payment_status"}, func(path string) bool {
		return path == "payment_status" && b.HasPaymentStatus()
	})
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.FeeID]("fee.id", b.GetId())
	if err != nil {
		return nil, err
	}
	var fv invoicing.FeeView
	if len(paths) == 0 {
		fv, err = s.svc.GetFee(ctx, p.OrganizationID, id)
	} else {
		st := feePaymentStatuses.Core(b.GetPaymentStatus())
		if st == "" {
			return nil, InvalidArgument("invalid fee", []FieldViolation{{Field: "fee.payment_status", Reason: core.CodeInvalid}})
		}
		fv, err = s.svc.UpdateFeePaymentStatus(ctx, p.OrganizationID, id, st)
	}
	if err != nil {
		if v, ok := validationViolations("fee.", err); ok {
			return nil, withReason(InvalidArgument("invalid fee", v), ReasonValidationErrors)
		}
		return nil, Error(ctx, err)
	}
	return invoicev1.UpdateFeeResponse_builder{Fee: feeProto(fv)}.Build(), nil
}

// DeleteFee implements FeeService.DeleteFee: it discards a fee attached to
// no invoice and returns it (lago Fees::DestroyService).
func (s *Fees) DeleteFee(ctx context.Context, req *invoicev1.DeleteFeeRequest) (*invoicev1.DeleteFeeResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.FeeID]("fee_id", req.GetFeeId())
	if err != nil {
		return nil, err
	}
	fv, err := s.svc.DeleteFee(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return invoicev1.DeleteFeeResponse_builder{Fee: feeProto(fv)}.Build(), nil
}

// AdjustFee implements FeeService.AdjustFee (lago
// AdjustedFees::CreateService): unit_amount is the unit price in
// currency units.
func (s *Fees) AdjustFee(ctx context.Context, req *invoicev1.AdjustFeeRequest) (*invoicev1.AdjustFeeResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	in := invoicing.FeeAdjustment{Mode: feeAdjustmentModes.Core(req.GetMode())}
	if in.InvoiceID, err = ParseID[core.InvoiceID]("invoice_id", req.GetInvoiceId()); err != nil {
		return nil, err
	}
	for _, id := range []struct {
		field string
		v     string
		parse func(string) error
	}{
		{"fee_id", req.GetFeeId(), func(s string) (err error) { in.FeeID, err = ParseID[core.FeeID]("fee_id", s); return err }},
		{"subscription_id", req.GetSubscriptionId(), func(s string) (err error) {
			in.SubscriptionID, err = ParseID[core.SubscriptionID]("subscription_id", s)
			return err
		}},
		{"charge_id", req.GetChargeId(), func(s string) (err error) { in.ChargeID, err = ParseID[core.ChargeID]("charge_id", s); return err }},
		{"charge_filter_id", req.GetChargeFilterId(), func(s string) (err error) {
			in.ChargeFilterID, err = ParseID[core.ChargeFilterID]("charge_filter_id", s)
			return err
		}},
		{"fixed_charge_id", req.GetFixedChargeId(), func(s string) (err error) {
			in.FixedChargeID, err = ParseID[core.FixedChargeID]("fixed_charge_id", s)
			return err
		}},
	} {
		if id.v == "" {
			continue
		}
		if err := id.parse(id.v); err != nil {
			return nil, err
		}
	}
	if req.HasInvoiceDisplayName() {
		in.InvoiceDisplayName = new(req.GetInvoiceDisplayName())
	}
	if v := req.GetUnits(); v != "" {
		if in.Units, err = ParseDecimal("units", v); err != nil {
			return nil, err
		}
	}
	if v := req.GetUnitAmount(); v != "" {
		if in.UnitPreciseAmount, err = ParseDecimal("unit_amount", v); err != nil {
			return nil, err
		}
	}
	fv, err := s.svc.AdjustFee(ctx, p.OrganizationID, in)
	if err != nil {
		return nil, adjustError(ctx, err)
	}
	return invoicev1.AdjustFeeResponse_builder{Fee: feeProto(fv)}.Build(), nil
}

// DeleteFeeAdjustment implements FeeService.DeleteFeeAdjustment (lago
// AdjustedFees::DestroyService).
func (s *Fees) DeleteFeeAdjustment(ctx context.Context, req *invoicev1.DeleteFeeAdjustmentRequest) (*invoicev1.DeleteFeeAdjustmentResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.FeeID]("fee_id", req.GetFeeId())
	if err != nil {
		return nil, err
	}
	fv, err := s.svc.DeleteFeeAdjustment(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return invoicev1.DeleteFeeAdjustmentResponse_builder{Fee: feeProto(fv)}.Build(), nil
}

// adjustError maps the service errors of AdjustFee, renaming the
// adjustment's unit_precise_amount to the request's unit_amount.
func adjustError(ctx context.Context, err error) error {
	v, ok := validationViolations("", err)
	if !ok {
		return Error(ctx, err)
	}
	for i := range v {
		if v[i].Field == "unit_precise_amount" {
			v[i].Field = "unit_amount"
		}
	}
	return withReason(InvalidArgument("invalid fee adjustment", v), ReasonValidationErrors)
}

// feeListFilter parses the AIP-160 filter of ListFees.
func feeListFilter(s string) (invoice.FeeFilter, error) {
	var f invoice.FeeFilter
	e, err := ParseFilter(s)
	if err != nil || e == nil {
		return f, err
	}
	terms := []FilterExpr{e}
	if and, ok := e.(FilterAnd); ok {
		terms = and
	}
	var rest FilterAnd
	for _, t := range terms {
		r, ok := t.(Restriction)
		if !ok || !slices.Contains(feeTimeFields, r.Field) {
			rest = append(rest, t)
			continue
		}
		if err := feeTimeRange(&f, r); err != nil {
			return f, err
		}
	}
	eq, err := Equalities(rest, feeFilterFields...)
	if err != nil {
		return f, err
	}
	types, err := FilterEnum[commonv1.FeeType]("fee_type", eq["fee_type"], commonv1.FeeType(0).Descriptor())
	if err != nil {
		return f, err
	}
	for _, v := range types {
		f.Types = append(f.Types, invFeeTypes.Core(v))
	}
	statuses, err := FilterEnum[invoicev1.FeePaymentStatus]("payment_status", eq["payment_status"], invoicev1.FeePaymentStatus(0).Descriptor())
	if err != nil {
		return f, err
	}
	for _, v := range statuses {
		f.PaymentStatuses = append(f.PaymentStatuses, feePaymentStatuses.Core(v))
	}
	if v, err := FilterSingle("currency", eq["currency"]); err != nil {
		return f, err
	} else if v != "" {
		if f.Currency, err = money.LookupCurrency(v); err != nil {
			return f, filterError(fmt.Sprintf("field \"currency\": unknown currency %q", v))
		}
	}
	if v, err := FilterSingle("customer_id", eq["customer_id"]); err != nil {
		return f, err
	} else if v != "" {
		ref, err := ParseRef("customer_id", v)
		switch {
		case err != nil || ref.Code != "":
			return f, filterError(fmt.Sprintf("field \"customer_id\": invalid reference %q", v))
		case ref.ExternalID != "":
			f.CustomerExternalID = ref.ExternalID
		default:
			if f.CustomerID, err = core.ParseID[core.CustomerID](v); err != nil {
				return f, filterError(fmt.Sprintf("field \"customer_id\": invalid ID %q", v))
			}
		}
	}
	if v, err := FilterSingle("subscription_id", eq["subscription_id"]); err != nil {
		return f, err
	} else if v != "" {
		ref, err := ParseRef("subscription_id", v)
		switch {
		case err != nil || ref.Code != "":
			return f, filterError(fmt.Sprintf("field \"subscription_id\": invalid reference %q", v))
		case ref.ExternalID != "":
			f.SubscriptionExternalID = ref.ExternalID
		default:
			if f.SubscriptionID, err = core.ParseID[core.SubscriptionID](v); err != nil {
				return f, filterError(fmt.Sprintf("field \"subscription_id\": invalid ID %q", v))
			}
		}
	}
	if f.BillableMetricCode, err = FilterSingle("billable_metric_code", eq["billable_metric_code"]); err != nil {
		return f, err
	}
	if f.EventTransactionID, err = FilterSingle("event_transaction_id", eq["event_transaction_id"]); err != nil {
		return f, err
	}
	return f, nil
}

// feeTimeRange applies an instant restriction with =, >= or <= (lago
// <field>_from and <field>_to, both inclusive).
func feeTimeRange(f *invoice.FeeFilter, r Restriction) error {
	from, to := r.Op == CmpEq || r.Op == CmpGe, r.Op == CmpEq || r.Op == CmpLe
	if !from && !to {
		return filterError(fmt.Sprintf("field %q is compared with =, >= or <=", r.Field))
	}
	t, err := time.Parse(time.RFC3339Nano, r.Value)
	if err != nil {
		return filterError(fmt.Sprintf("field %q: invalid RFC 3339 time %q", r.Field, r.Value))
	}
	t = t.UTC()
	var lo, hi *time.Time
	switch r.Field {
	case "created_at":
		lo, hi = &f.CreatedFrom, &f.CreatedTo
	case "succeeded_at":
		lo, hi = &f.SucceededFrom, &f.SucceededTo
	case "failed_at":
		lo, hi = &f.FailedFrom, &f.FailedTo
	default:
		lo, hi = &f.RefundedFrom, &f.RefundedTo
	}
	if from {
		*lo = t
	}
	if to {
		*hi = t
	}
	return nil
}
