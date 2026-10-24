package payments

import (
	"context"
	"errors"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// ReceiptDetail is a receipt with its payment.
type ReceiptDetail struct {
	*payment.Receipt
	// Payment is the receipted payment.
	Payment *Detail
}

// createReceipt issues the receipt of a succeeded payment (lago
// PaymentReceipts::CreateService): none for a payment not succeeded or of
// a partner customer, the existing one when the payment has one, else a
// new receipt numbered "<customer slug>-RCPT-<n %06d>" by the customer's
// receipt sequence, announced by the payment_receipt.created webhook.
// Issuing receipts is a core feature, so every organization issues them.
func (s *Service) createReceipt(ctx context.Context, r port.Repos, p *payment.Payment) (*payment.Receipt, error) {
	org := p.OrganizationID
	if p.Status != payment.StateSucceeded {
		return nil, nil
	}
	c, err := r.Customers.Get(ctx, org, p.CustomerID)
	if err != nil {
		return nil, wrap(err, "load customer %s", p.CustomerID)
	}
	if c.SelfBilled() {
		return nil, nil
	}
	existing, err := r.PaymentReceipts.GetByPayment(ctx, org, p.ID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, core.ErrNotFound) {
		return nil, wrap(err, "load receipt of payment %s", p.ID)
	}
	be, err := payableBillingEntity(ctx, r, p)
	if err != nil {
		return nil, err
	}
	n, err := r.Sequences.Next(ctx, org, port.SequenceKey{Name: port.SequencePaymentReceipt, Scope: c.ID.UUID()})
	if err != nil {
		return nil, wrap(err, "number receipt of payment %s", p.ID)
	}
	now := s.d.Now()
	rc := &payment.Receipt{
		ID:              core.NewID[core.PaymentReceiptID](s.d.IDs),
		OrganizationID:  org,
		PaymentID:       p.ID,
		BillingEntityID: be,
		Number:          payment.FormatReceiptNumber(c.Slug, int(n)),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := rc.Validate(); err != nil {
		return nil, err
	}
	if err := r.PaymentReceipts.Create(ctx, rc); err != nil {
		return nil, wrap(err, "create receipt of payment %s", p.ID)
	}
	if err := kernel.Emit(ctx, s.d, org, webhook.EventTypePaymentReceiptCreated, "payment_receipt", rc.ID.UUID(), rc); err != nil {
		return nil, err
	}
	// payment_receipts/create_service.rb:28; the customer is the
	// payment's.
	if err := s.d.LogChange(ctx, r, kernel.Change{
		Type: auditlog.ActivityPaymentReceiptCreated, OrganizationID: org, ResourceType: auditlog.ResourcePaymentReceipt,
		ResourceID: rc.ID.UUID(), Object: rc, CustomerID: p.CustomerID,
	}); err != nil {
		return nil, err
	}
	return rc, nil
}

// payableBillingEntity returns the billing entity of what p pays: the
// invoice's, or that of the payment request's invoices.
func payableBillingEntity(ctx context.Context, r port.Repos, p *payment.Payment) (core.BillingEntityID, error) {
	id := p.Payable.InvoiceID
	if p.Payable.Type == payment.PayableTypePaymentRequest {
		q, err := r.PaymentRequests.Get(ctx, p.OrganizationID, p.Payable.PaymentRequestID)
		if err != nil {
			return core.BillingEntityID{}, wrap(err, "load payment request %s", p.Payable.PaymentRequestID)
		}
		if len(q.InvoiceIDs) == 0 {
			return core.BillingEntityID{}, notFound("invoice")
		}
		id = q.InvoiceIDs[0]
	}
	inv, err := r.Invoices.Get(ctx, p.OrganizationID, id)
	if err != nil {
		return core.BillingEntityID{}, wrap(err, "load invoice %s", id)
	}
	return inv.BillingEntityID, nil
}

// GetReceipt returns a receipt with its payment.
func (s *Service) GetReceipt(ctx context.Context, org core.OrganizationID, id core.PaymentReceiptID) (*ReceiptDetail, error) {
	return inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*ReceiptDetail, error) {
		rc, err := r.PaymentReceipts.Get(ctx, org, id)
		if errors.Is(err, core.ErrNotFound) {
			return nil, notFound("payment_receipt")
		}
		if err != nil {
			return nil, wrap(err, "load receipt %s", id)
		}
		return receiptDetail(ctx, r, rc)
	})
}

// ReceiptListInput selects receipts.
type ReceiptListInput struct {
	// Filter narrows the list; its order is set by ListReceipts.
	Filter payment.ReceiptFilter
}

// ListReceipts returns a page of the receipts of org, newest first then
// by ID (lago
// PaymentReceiptsQuery).
func (s *Service) ListReceipts(ctx context.Context, org core.OrganizationID, in ReceiptListInput, page core.PageRequest) (core.Page[*ReceiptDetail], error) {
	return inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (core.Page[*ReceiptDetail], error) {
		f := in.Filter
		f.Order = core.OrderRecent
		rs, err := r.PaymentReceipts.List(ctx, org, f, page)
		if err != nil {
			return core.Page[*ReceiptDetail]{}, wrap(err, "list receipts")
		}
		out := core.Page[*ReceiptDetail]{Items: make([]*ReceiptDetail, 0, len(rs.Items)), NextToken: rs.NextToken}
		for _, rc := range rs.Items {
			d, err := receiptDetail(ctx, r, rc)
			if err != nil {
				return core.Page[*ReceiptDetail]{}, err
			}
			out.Items = append(out.Items, d)
		}
		return out, nil
	})
}

// DownloadReceipt returns a receipt and whether its documents are
// rendered. Nothing renders payment receipt documents yet, so a receipt
// without a file stays without one.
func (s *Service) DownloadReceipt(ctx context.Context, org core.OrganizationID, id core.PaymentReceiptID) (*payment.Receipt, bool, error) {
	d, err := s.GetReceipt(ctx, org, id)
	if err != nil {
		return nil, false, err
	}
	return d.Receipt, !d.File.IsZero(), nil
}

// receiptDetail loads the payment of rc.
func receiptDetail(ctx context.Context, r port.Repos, rc *payment.Receipt) (*ReceiptDetail, error) {
	p, err := r.Payments.Get(ctx, rc.OrganizationID, rc.PaymentID)
	if err != nil {
		return nil, wrap(err, "load payment %s", rc.PaymentID)
	}
	d, err := detail(ctx, r, p)
	if err != nil {
		return nil, err
	}
	return &ReceiptDetail{Receipt: rc, Payment: d}, nil
}
