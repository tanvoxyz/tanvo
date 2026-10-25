package rpc

import (
	"context"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/core"
	paymentv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/payment/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/payment/v1/paymentv1connect"
)

// PaymentReceipts serves tanvo.payment.v1.PaymentReceiptService over the
// billing library's payment service (lago
// Api::V1::PaymentReceiptsController). Without documents
// ([PaymentReceipts.WithFiles]) the links are left empty and
// ResendPaymentReceipt is Unimplemented.
type PaymentReceipts struct {
	paymentv1connect.UnimplementedPaymentReceiptServiceHandler

	svc  *payments.Service
	docs DocumentFiles
}

var _ paymentv1connect.PaymentReceiptServiceHandler = (*PaymentReceipts)(nil)

// NewPaymentReceipts returns the service over svc.
func NewPaymentReceipts(svc *payments.Service) *PaymentReceipts { return &PaymentReceipts{svc: svc} }

// WithFiles returns s rendering, linking and resending documents with
// docs.
func (s *PaymentReceipts) WithFiles(docs DocumentFiles) *PaymentReceipts {
	c := *s
	c.docs = docs
	return &c
}

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
// PaymentReceiptService.DownloadPaymentReceipt (lago
// Mutations::PaymentReceipts::Download): the PDF is rendered when missing,
// then the documents are returned with their media type and permanent
// link. Without documents, only the rendered ones are returned, unlinked.
func (s *PaymentReceipts) DownloadPaymentReceipt(ctx context.Context, req *paymentv1.DownloadPaymentReceiptRequest) (*paymentv1.DownloadPaymentReceiptResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.PaymentReceiptID]("payment_receipt_id", req.GetPaymentReceiptId())
	if err != nil {
		return nil, err
	}
	rc, _, err := s.svc.DownloadReceipt(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	if s.docs != nil {
		if rc, err = s.docs.PaymentReceipt(ctx, p.OrganizationID, id); err != nil {
			return nil, Error(ctx, err)
		}
	}
	out := paymentv1.DownloadPaymentReceiptResponse_builder{}
	if out.Pdf, out.Xml, err = documentPair(ctx, s.docs, p.OrganizationID, rc.File, rc.XMLFile); err != nil {
		return nil, Error(ctx, err)
	}
	return out.Build(), nil
}

// ResendPaymentReceipt implements PaymentReceiptService.ResendPaymentReceipt
// (lago Api::V1::PaymentReceiptsController#resend_email and
// Emails::ResendService): the receipt is emailed again to the custom
// recipients or the payment's customer.
func (s *PaymentReceipts) ResendPaymentReceipt(ctx context.Context, req *paymentv1.ResendPaymentReceiptRequest) (*paymentv1.ResendPaymentReceiptResponse, error) {
	if s.docs == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, "document emails are not available")
	}
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.PaymentReceiptID]("payment_receipt_id", req.GetPaymentReceiptId())
	if err != nil {
		return nil, err
	}
	if err := s.docs.ResendPaymentReceipt(ctx, p.OrganizationID, id, Recipients{To: req.GetTo(), Cc: req.GetCc(), Bcc: req.GetBcc()}); err != nil {
		return nil, Error(ctx, err)
	}
	return paymentv1.ResendPaymentReceiptResponse_builder{}.Build(), nil
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
