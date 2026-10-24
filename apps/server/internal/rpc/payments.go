package rpc

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/money"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
	paymentv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/payment/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/payment/v1/paymentv1connect"
)

// Payments serves tanvo.payment.v1.PaymentService over the billing
// library's payment service (lago Api::V1::PaymentsController).
// ListRefunds stays Unimplemented: refunds have no public listing to
// match.
type Payments struct {
	paymentv1connect.UnimplementedPaymentServiceHandler

	svc *payments.Service
}

var _ paymentv1connect.PaymentServiceHandler = (*Payments)(nil)

// NewPayments returns the service over svc.
func NewPayments(svc *payments.Service) *Payments { return &Payments{svc: svc} }

var (
	payKinds = NewEnum(map[payment.Kind]paymentv1.PaymentKind{
		payment.KindProvider: paymentv1.PaymentKind_PAYMENT_KIND_PROVIDER,
		payment.KindManual:   paymentv1.PaymentKind_PAYMENT_KIND_MANUAL,
		payment.KindX402:     paymentv1.PaymentKind_PAYMENT_KIND_X402,
	})
	payPayableTypes = NewEnum(map[payment.PayableType]paymentv1.PayableType{
		payment.PayableTypeInvoice:        paymentv1.PayableType_PAYABLE_TYPE_INVOICE,
		payment.PayableTypePaymentRequest: paymentv1.PayableType_PAYABLE_TYPE_PAYMENT_REQUEST,
	})
	payStates = NewEnum(map[payment.State]commonv1.PaymentStatus{
		payment.StatePending:    commonv1.PaymentStatus_PAYMENT_STATUS_PENDING,
		payment.StateProcessing: commonv1.PaymentStatus_PAYMENT_STATUS_PROCESSING,
		payment.StateSucceeded:  commonv1.PaymentStatus_PAYMENT_STATUS_SUCCEEDED,
		payment.StateFailed:     commonv1.PaymentStatus_PAYMENT_STATUS_FAILED,
	})
	payStatuses = NewEnum(map[core.PaymentStatus]commonv1.PaymentStatus{
		core.PaymentStatusPending:   commonv1.PaymentStatus_PAYMENT_STATUS_PENDING,
		core.PaymentStatusSucceeded: commonv1.PaymentStatus_PAYMENT_STATUS_SUCCEEDED,
		core.PaymentStatusFailed:    commonv1.PaymentStatus_PAYMENT_STATUS_FAILED,
	})
	payMethodTypes = NewEnum(map[core.PaymentMethodType]commonv1.PaymentMethodType{
		core.PaymentMethodTypeProvider: commonv1.PaymentMethodType_PAYMENT_METHOD_TYPE_PROVIDER,
		core.PaymentMethodTypeManual:   commonv1.PaymentMethodType_PAYMENT_METHOD_TYPE_MANUAL,
	})
)

// CreatePayment implements PaymentService.CreatePayment: a manual payment
// of an invoice (lago Payments::ManualCreateService). The amount is in
// major units of the invoice's currency.
func (s *Payments) CreatePayment(ctx context.Context, req *paymentv1.CreatePaymentRequest) (*paymentv1.CreatePaymentResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	invID, err := ParseID[core.InvoiceID]("invoice_id", req.GetInvoiceId())
	if err != nil {
		return nil, err
	}
	amount, err := ParseDecimal("amount", req.GetAmount())
	if err != nil {
		return nil, err
	}
	pay, err := s.svc.CreateManual(ctx, payments.ManualInput{
		OrganizationID: p.OrganizationID,
		InvoiceID:      invID,
		Amount:         amount,
		Reference:      req.GetReference(),
		PaidAt:         TimePtr(req.GetPaidAt()),
	})
	if err != nil {
		return nil, Error(ctx, err)
	}
	d, err := s.svc.Get(ctx, p.OrganizationID, pay.ID)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return paymentv1.CreatePaymentResponse_builder{Payment: payProto(d)}.Build(), nil
}

// GetPayment implements PaymentService.GetPayment; a payment of an
// invoice still generating is not found.
func (s *Payments) GetPayment(ctx context.Context, req *paymentv1.GetPaymentRequest) (*paymentv1.GetPaymentResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.PaymentID]("payment_id", req.GetPaymentId())
	if err != nil {
		return nil, err
	}
	d, err := s.svc.Get(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return paymentv1.GetPaymentResponse_builder{Payment: payProto(d)}.Build(), nil
}

// ListPayments implements PaymentService.ListPayments: the payments,
// newest first then by ID, without those of invoices still generating
// (lago PaymentsQuery). invoice_id keeps the payments of an invoice,
// directly or through a payment request.
func (s *Payments) ListPayments(ctx context.Context, req *paymentv1.ListPaymentsRequest) (*paymentv1.ListPaymentsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	var in payments.ListInput
	if in.Customer, err = payCustomerRef("customer_id", req.GetCustomerId()); err != nil {
		return nil, err
	}
	if in.Filter, in.FilterCustomer, err = payListFilter(req.GetFilter()); err != nil {
		return nil, err
	}
	in.Filter.Search = req.GetSearch()
	page, err := s.svc.List(ctx, p.OrganizationID, in, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return paymentv1.ListPaymentsResponse_builder{
		Payments:      Items(page, payProto),
		NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// payListFilter parses the filter of ListPayments: customer_id,
// invoice_id and currency, as lago PaymentsQuery filters.
func payListFilter(s string) (payment.Filter, payments.CustomerRef, error) {
	var f payment.Filter
	e, err := ParseFilter(s)
	if err != nil || e == nil {
		return f, payments.CustomerRef{}, err
	}
	eq, err := Equalities(e, "customer_id", "invoice_id", "currency")
	if err != nil {
		return f, payments.CustomerRef{}, err
	}
	cust, err := payFilterCustomer(eq["customer_id"])
	if err != nil {
		return f, cust, err
	}
	if f.InvoiceID, err = payFilterID[core.InvoiceID]("invoice_id", eq["invoice_id"]); err != nil {
		return f, cust, err
	}
	f.Currency, err = payFilterCurrency(eq["currency"])
	return f, cust, err
}

// payFilterCustomer parses the single customer_id value of a filter: an
// ID or `external_id:<external_id>`; an unrestricted customer is the zero
// reference.
func payFilterCustomer(values []string) (payments.CustomerRef, error) {
	v, err := FilterSingle("customer_id", values)
	if err != nil {
		return payments.CustomerRef{}, err
	}
	return payCustomerRef(FilterField, v)
}

// payFilterCurrency parses the single currency value of a filter; an
// unrestricted currency is the zero currency.
func payFilterCurrency(values []string) (money.Currency, error) {
	v, err := FilterSingle("currency", values)
	if err != nil || v == "" {
		return money.Currency{}, err
	}
	c, err := money.LookupCurrency(v)
	if err != nil {
		return money.Currency{}, filterError(fmt.Sprintf("field \"currency\": unknown currency %q", v))
	}
	return c, nil
}

// payFilterID parses the single ID value of a filter field; an
// unrestricted field is the zero ID.
func payFilterID[I core.Identifier[I]](field string, values []string) (I, error) {
	var zero I
	v, err := FilterSingle(field, values)
	if err != nil || v == "" {
		return zero, err
	}
	id, err := core.ParseID[I](v)
	if err != nil {
		return zero, filterError(fmt.Sprintf("field %q: invalid ID %q", field, v))
	}
	return id, nil
}

// payCustomerRef parses a customer reference: an ID or
// `external_id:<external_id>`; "" is no customer. Customers have no code.
func payCustomerRef(field, s string) (payments.CustomerRef, error) {
	if s == "" {
		return payments.CustomerRef{}, nil
	}
	ref, err := ParseRef(field, s)
	if err != nil {
		return payments.CustomerRef{}, err
	}
	switch {
	case ref.Code != "":
		return payments.CustomerRef{}, InvalidArgument("invalid reference", []FieldViolation{{
			Field: field, Reason: core.CodeInvalid, Description: "this resource has no code",
		}})
	case ref.ExternalID != "":
		return payments.CustomerRef{ExternalID: ref.ExternalID}, nil
	}
	id, err := ParseID[core.CustomerID](field, s)
	return payments.CustomerRef{ID: id}, err
}

// payIDString returns the string of an optional ID, nil when zero.
func payIDString[I interface {
	IsZero() bool
	String() string
}](id I,
) *string {
	if id.IsZero() {
		return nil
	}
	return new(id.String())
}

// payMethodDetails converts payment method details; none is nil.
func payMethodDetails(d payment.MethodDetails) *paymentv1.PaymentMethodDetails {
	if d.IsZero() {
		return nil
	}
	return paymentv1.PaymentMethodDetails_builder{
		Type:     StringPtr(d.Type),
		Brand:    StringPtr(d.Brand),
		Last4:    StringPtr(d.Last4),
		ExpMonth: Opt(d.ExpMonth != 0, int32(d.ExpMonth)),
		ExpYear:  Opt(d.ExpYear != 0, int32(d.ExpYear)),
	}.Build()
}

// payProto converts a payment with its invoices and receipt.
func payProto(d *payments.Detail) *paymentv1.Payment {
	p := d.Payment
	invoiceIDs := make([]string, len(d.InvoiceIDs))
	for i, id := range d.InvoiceIDs {
		invoiceIDs[i] = id.String()
	}
	return paymentv1.Payment_builder{
		Id:                   new(p.ID.String()),
		PayableType:          new(payPayableTypes.Proto(p.Payable.Type)),
		InvoiceId:            payIDString(p.Payable.InvoiceID),
		PaymentRequestId:     payIDString(p.Payable.PaymentRequestID),
		InvoiceIds:           invoiceIDs,
		CustomerId:           new(p.CustomerID.String()),
		Kind:                 new(payKinds.Proto(p.Kind)),
		Status:               new(payStates.Proto(p.Status)),
		ProviderStatus:       p.ProviderStatus,
		Amount:               Money(p.Amount, p.Currency),
		Reference:            p.Reference,
		IntegrationId:        payIDString(p.IntegrationID),
		ProviderPaymentId:    p.ProviderPaymentID,
		PaymentMethodId:      payIDString(p.PaymentMethodID),
		PaymentMethodDetails: payMethodDetails(p.ProviderPaymentMethod),
		PaymentReceiptId:     payIDString(d.ReceiptID),
		PaidAt:               TimestampPtr(p.PaidAt),
		CreatedAt:            Timestamp(p.CreatedAt),
		UpdatedAt:            Timestamp(p.UpdatedAt),
	}.Build()
}
