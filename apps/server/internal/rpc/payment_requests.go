package rpc

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
	paymentv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/payment/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/payment/v1/paymentv1connect"
)

// PaymentRequests serves tanvo.payment.v1.PaymentRequestService over the
// billing library's payment service (lago
// Api::V1::PaymentRequestsController).
type PaymentRequests struct {
	paymentv1connect.UnimplementedPaymentRequestServiceHandler

	svc *payments.Service
}

var _ paymentv1connect.PaymentRequestServiceHandler = (*PaymentRequests)(nil)

// NewPaymentRequests returns the service over svc.
func NewPaymentRequests(svc *payments.Service) *PaymentRequests { return &PaymentRequests{svc: svc} }

// CreatePaymentRequest implements
// PaymentRequestService.CreatePaymentRequest (lago
// PaymentRequests::CreateService): a request to pay overdue invoices of
// a customer at once.
func (s *PaymentRequests) CreatePaymentRequest(ctx context.Context, req *paymentv1.CreatePaymentRequestRequest) (*paymentv1.CreatePaymentRequestResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	in := payments.RequestInput{
		OrganizationID:    p.OrganizationID,
		Email:             Opt(req.HasEmail(), req.GetEmail()),
		PaymentMethodType: payMethodTypes.Core(req.GetPaymentMethodType()),
	}
	if in.Customer, err = payCustomerRef("customer_id", req.GetCustomerId()); err != nil {
		return nil, err
	}
	for _, v := range req.GetInvoiceIds() {
		id, err := ParseID[core.InvoiceID]("invoice_ids", v)
		if err != nil {
			return nil, err
		}
		in.InvoiceIDs = append(in.InvoiceIDs, id)
	}
	if in.PaymentMethodID, err = ParseID[core.PaymentMethodID]("payment_method_id", req.GetPaymentMethodId()); err != nil {
		return nil, err
	}
	q, err := s.svc.CreateRequest(ctx, in)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return paymentv1.CreatePaymentRequestResponse_builder{PaymentRequest: preqProto(q)}.Build(), nil
}

// GetPaymentRequest implements PaymentRequestService.GetPaymentRequest.
func (s *PaymentRequests) GetPaymentRequest(ctx context.Context, req *paymentv1.GetPaymentRequestRequest) (*paymentv1.GetPaymentRequestResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.PaymentRequestID]("payment_request_id", req.GetPaymentRequestId())
	if err != nil {
		return nil, err
	}
	q, err := s.svc.GetRequest(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return paymentv1.GetPaymentRequestResponse_builder{PaymentRequest: preqProto(q)}.Build(), nil
}

// ListPaymentRequests implements PaymentRequestService.ListPaymentRequests:
// the payment requests, newest first then by ID, filtered on
// payment_status, currency and billing_entity_id as lago
// PaymentRequestsQuery and PaymentRequestIndex do.
func (s *PaymentRequests) ListPaymentRequests(ctx context.Context, req *paymentv1.ListPaymentRequestsRequest) (*paymentv1.ListPaymentRequestsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	var in payments.RequestListInput
	if in.Customer, err = payCustomerRef("customer_id", req.GetCustomerId()); err != nil {
		return nil, err
	}
	if in.Filter, in.FilterCustomer, in.BillingEntities, err = preqListFilter(req.GetFilter()); err != nil {
		return nil, err
	}
	page, err := s.svc.ListRequests(ctx, p.OrganizationID, in, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return paymentv1.ListPaymentRequestsResponse_builder{
		PaymentRequests: Items(page, preqProto),
		NextPageToken:   new(page.NextToken),
	}.Build(), nil
}

// preqListFilter parses the filter of ListPaymentRequests, the customer
// of its customer_id term and the billing entities (IDs or codes) of its
// billing_entity_id term.
func preqListFilter(s string) (payment.RequestFilter, payments.CustomerRef, []refs.Ref, error) {
	var cust payments.CustomerRef
	var f payment.RequestFilter
	e, err := ParseFilter(s)
	if err != nil || e == nil {
		return f, cust, nil, err
	}
	eq, err := Equalities(e, "customer_id", "payment_status", "currency", "billing_entity_id")
	if err != nil {
		return f, cust, nil, err
	}
	if cust, err = payFilterCustomer(eq["customer_id"]); err != nil {
		return f, cust, nil, err
	}
	if _, err := FilterSingle("payment_status", eq["payment_status"]); err != nil {
		return f, cust, nil, err
	}
	st, err := FilterEnum[commonv1.PaymentStatus]("payment_status", eq["payment_status"], commonv1.PaymentStatus(0).Descriptor())
	if err != nil {
		return f, cust, nil, err
	}
	if len(st) == 1 {
		if f.PaymentStatus = payStatuses.Core(st[0]); f.PaymentStatus == "" {
			return f, cust, nil, filterError(fmt.Sprintf("field \"payment_status\": unsupported value %q", eq["payment_status"][0]))
		}
	}
	if f.Currency, err = payFilterCurrency(eq["currency"]); err != nil {
		return f, cust, nil, err
	}
	bes, err := CatalogRefs(FilterField, eq["billing_entity_id"])
	return f, cust, bes, err
}

// preqProto converts a payment request.
func preqProto(q *payment.Request) *paymentv1.PaymentRequest {
	invoiceIDs := make([]string, len(q.InvoiceIDs))
	for i, id := range q.InvoiceIDs {
		invoiceIDs[i] = id.String()
	}
	return paymentv1.PaymentRequest_builder{
		Id:                new(q.ID.String()),
		CustomerId:        new(q.CustomerID.String()),
		InvoiceIds:        invoiceIDs,
		Amount:            Money(q.Amount, q.Currency),
		Email:             q.Email,
		PaymentStatus:     new(payStatuses.Proto(q.PaymentStatus)),
		PaymentAttempts:   new(int32(q.PaymentAttempts)),
		DunningCampaignId: payIDString(q.DunningCampaignID),
		PaymentMethodType: Opt(q.PaymentMethodType != "", payMethodTypes.Proto(q.PaymentMethodType)),
		PaymentMethodId:   payIDString(q.PaymentMethodID),
		CreatedAt:         Timestamp(q.CreatedAt),
		UpdatedAt:         Timestamp(q.UpdatedAt),
	}.Build()
}
