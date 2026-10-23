package rpc

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/core"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
	paymentv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/payment/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/payment/v1/paymentv1connect"
)

// PaymentReceipts serves tanvo.payment.v1.PaymentReceiptService over the
// billing library's payment service (lago
// Api::V1::PaymentReceiptsController). ResendPaymentReceipt stays
// Unimplemented: the server sends no document emails yet.
type PaymentReceipts struct {
	paymentv1connect.UnimplementedPaymentReceiptServiceHandler

	svc *payments.Service
}

var _ paymentv1connect.PaymentReceiptServiceHandler = (*PaymentReceipts)(nil)

// NewPaymentReceipts returns the service over svc.
func NewPaymentReceipts(svc *payments.Service) *PaymentReceipts { return &PaymentReceipts{svc: svc} }

// GetPaymentReceipt implements PaymentReceiptService.GetPaymentReceipt.
func (s *PaymentReceipts) GetPaymentReceipt(ctx context.Context, req *paymentv1.GetPaymentReceiptRequest) (*paymentv1.GetPaymentReceiptResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.PaymentReceiptID]("payment_receipt_id", req.GetPaymentReceiptId())
	if err != nil {
		return nil, err
	}
	d, err := s.svc.GetReceipt(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return paymentv1.GetPaymentReceiptResponse_builder{PaymentReceipt: prcptProto(d)}.Build(), nil
}

// ListPaymentReceipts implements PaymentReceiptService.ListPaymentReceipts:
// the receipts, newest first, filtered on invoice_id (paid directly or
// through a payment request) as lago PaymentReceiptsQuery does. The
// payment_id and customer_id terms the API documents have no released
// counterpart and are rejected.
func (s *PaymentReceipts) ListPaymentReceipts(ctx context.Context, req *paymentv1.ListPaymentReceiptsRequest) (*paymentv1.ListPaymentReceiptsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	var in payments.ReceiptListInput
	e, err := ParseFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	eq, err := Equalities(e, "invoice_id")
	if err != nil {
		return nil, err
	}
	if in.Filter.InvoiceID, err = payFilterID[core.InvoiceID]("invoice_id", eq["invoice_id"]); err != nil {
		return nil, err
	}
	page, err := s.svc.ListReceipts(ctx, p.OrganizationID, in, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return paymentv1.ListPaymentReceiptsResponse_builder{
		PaymentReceipts: Items(page, prcptProto),
		NextPageToken:   new(page.NextToken),
	}.Build(), nil
}

// DownloadPaymentReceipt implements
// PaymentReceiptService.DownloadPaymentReceipt: a rendered document is
// returned with its media type, its URL left empty until the server signs
// blob store URLs; a receipt without one gets an empty response, as
// nothing renders payment receipt documents yet.
func (s *PaymentReceipts) DownloadPaymentReceipt(ctx context.Context, req *paymentv1.DownloadPaymentReceiptRequest) (*paymentv1.DownloadPaymentReceiptResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.PaymentReceiptID]("payment_receipt_id", req.GetPaymentReceiptId())
	if err != nil {
		return nil, err
	}
	rc, ready, err := s.svc.DownloadReceipt(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := paymentv1.DownloadPaymentReceiptResponse_builder{}
	if ready {
		out.Pdf = commonv1.Document_builder{ContentType: new("application/pdf")}.Build()
		if !rc.XMLFile.IsZero() {
			out.Xml = commonv1.Document_builder{ContentType: new("application/xml")}.Build()
		}
	}
	return out.Build(), nil
}

// prcptProto converts a receipt with its payment.
func prcptProto(d *payments.ReceiptDetail) *paymentv1.PaymentReceipt {
	rc := d.Receipt
	return paymentv1.PaymentReceipt_builder{
		Id:               new(rc.ID.String()),
		Number:           new(rc.Number),
		Payment:          payProto(d.Payment),
		BillingEntityId:  new(rc.BillingEntityID.String()),
		FileAvailable:    new(!rc.File.IsZero()),
		XmlFileAvailable: new(!rc.XMLFile.IsZero()),
		CreatedAt:        Timestamp(rc.CreatedAt),
	}.Build()
}
