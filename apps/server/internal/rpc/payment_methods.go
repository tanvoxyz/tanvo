package rpc

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	paymentv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/payment/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/payment/v1/paymentv1connect"
)

// PaymentMethods serves tanvo.payment.v1.PaymentMethodService over the
// billing library's payment service (lago
// Api::V1::Customers::PaymentMethodsController).
type PaymentMethods struct {
	paymentv1connect.UnimplementedPaymentMethodServiceHandler

	svc *payments.Service
}

var _ paymentv1connect.PaymentMethodServiceHandler = (*PaymentMethods)(nil)

// NewPaymentMethods returns the service over svc.
func NewPaymentMethods(svc *payments.Service) *PaymentMethods { return &PaymentMethods{svc: svc} }

// ListPaymentMethods implements PaymentMethodService.ListPaymentMethods:
// the live payment methods of a customer, newest first (lago
// PaymentMethodsQuery).
func (s *PaymentMethods) ListPaymentMethods(ctx context.Context, req *paymentv1.ListPaymentMethodsRequest) (*paymentv1.ListPaymentMethodsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	c, err := payCustomerRef("customer_id", req.GetCustomerId())
	if err != nil {
		return nil, err
	}
	page, err := s.svc.ListMethods(ctx, p.OrganizationID, c, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return paymentv1.ListPaymentMethodsResponse_builder{
		PaymentMethods: Items(page, pmProto),
		NextPageToken:  new(page.NextToken),
	}.Build(), nil
}

// SetDefaultPaymentMethod implements
// PaymentMethodService.SetDefaultPaymentMethod (lago
// PaymentMethods::SetAsDefaultService).
func (s *PaymentMethods) SetDefaultPaymentMethod(ctx context.Context, req *paymentv1.SetDefaultPaymentMethodRequest) (*paymentv1.SetDefaultPaymentMethodResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	c, id, err := pmRefs(req.GetCustomerId(), req.GetPaymentMethodId())
	if err != nil {
		return nil, err
	}
	m, err := s.svc.SetDefaultMethod(ctx, p.OrganizationID, c, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return paymentv1.SetDefaultPaymentMethodResponse_builder{PaymentMethod: pmProto(m)}.Build(), nil
}

// DeletePaymentMethod implements PaymentMethodService.DeletePaymentMethod
// (lago PaymentMethods::DestroyService): a soft delete.
func (s *PaymentMethods) DeletePaymentMethod(ctx context.Context, req *paymentv1.DeletePaymentMethodRequest) (*paymentv1.DeletePaymentMethodResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	c, id, err := pmRefs(req.GetCustomerId(), req.GetPaymentMethodId())
	if err != nil {
		return nil, err
	}
	if _, err := s.svc.DeleteMethod(ctx, p.OrganizationID, c, id); err != nil {
		return nil, Error(ctx, err)
	}
	return &paymentv1.DeletePaymentMethodResponse{}, nil
}

// pmRefs parses the customer and payment method of a request.
func pmRefs(customerID, methodID string) (payments.CustomerRef, core.PaymentMethodID, error) {
	c, err := payCustomerRef("customer_id", customerID)
	if err != nil {
		return c, core.PaymentMethodID{}, err
	}
	id, err := ParseID[core.PaymentMethodID]("payment_method_id", methodID)
	return c, id, err
}

// pmProto converts a payment method.
func pmProto(m *payment.Method) *paymentv1.PaymentMethod {
	return paymentv1.PaymentMethod_builder{
		Id:               new(m.ID.String()),
		CustomerId:       new(m.CustomerID.String()),
		IntegrationId:    payIDString(m.IntegrationID),
		ProviderMethodId: new(m.ProviderMethodID),
		Details:          payMethodDetails(m.Details),
		IsDefault:        new(m.IsDefault),
		CreatedAt:        Timestamp(m.CreatedAt),
	}.Build()
}
