package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindPayment binds the repositories of the payment aggregates.
func (t *txState) bindPayment(r *port.Repos) {
	r.Payments = paymentRepo{t}
	r.PaymentReceipts = paymentReceiptRepo{t}
	r.Refunds = refundRepo{t}
	r.PaymentRequests = paymentRequestRepo{t}
	r.PaymentMethods = paymentMethodRepo{t}
	r.PaymentProviderCustomers = paymentProviderCustomerRepo{t}
	r.PaymentIntents = paymentIntentRepo{t}
	r.InboundWebhooks = inboundWebhookRepo{t}
}

// paymentMethodDetailsJSON is the stored form of payment.MethodDetails.
type paymentMethodDetailsJSON struct {
	Type     string `json:"type,omitempty"`
	Brand    string `json:"brand,omitempty"`
	Last4    string `json:"last4,omitempty"`
	ExpMonth int    `json:"exp_month,omitempty"`
	ExpYear  int    `json:"exp_year,omitempty"`
}

// paymentEncodeDetails encodes d for a jsonb column (an empty object when
// nothing is known).
func paymentEncodeDetails(d payment.MethodDetails) []byte {
	b, _ := json.Marshal(paymentMethodDetailsJSON(d)) // strings and integers always encode
	return b
}

// paymentOptDetails encodes d, or returns nil (NULL) when nothing is known.
func paymentOptDetails(d payment.MethodDetails) []byte {
	if d.IsZero() {
		return nil
	}
	return paymentEncodeDetails(d)
}

// paymentDecodeDetails decodes a details column (NULL to the zero value).
func paymentDecodeDetails(b []byte) (payment.MethodDetails, error) {
	var d paymentMethodDetailsJSON
	if err := decodeJSON(b, &d); err != nil {
		return payment.MethodDetails{}, err
	}
	return payment.MethodDetails(d), nil
}

// paymentOptEnum returns nil for the empty enumeration value.
func paymentOptEnum[E ~string](e E) *string { return optString(string(e)) }

// paymentInterval converts d to an interval parameter.
func paymentInterval(d time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: d.Microseconds(), Valid: true}
}

// paymentRepo implements payment.Repository.
type paymentRepo struct{ t *txState }

var _ payment.Repository = paymentRepo{}

func paymentParams(p *payment.Payment) db.CreatePaymentParams {
	return db.CreatePaymentParams{
		OrganizationID:          p.OrganizationID.UUID(),
		ID:                      p.ID.UUID(),
		PayableType:             string(p.Payable.Type),
		InvoiceID:               optUUID(p.Payable.InvoiceID),
		PaymentRequestID:        optUUID(p.Payable.PaymentRequestID),
		CustomerID:              p.CustomerID.UUID(),
		PaymentKind:             string(p.Kind),
		Status:                  string(p.Status),
		ProviderStatus:          p.ProviderStatus,
		Amount:                  p.Amount,
		Currency:                p.Currency.Code(),
		Reference:               p.Reference,
		PaidAt:                  optUTC(p.PaidAt),
		IntegrationID:           optUUID(p.IntegrationID),
		ProviderPaymentID:       p.ProviderPaymentID,
		ProviderPaymentMethodID: p.ProviderPaymentMethodID,
		ProviderPaymentData:     rawJSON(p.ProviderPaymentData),
		ProviderMethodDetails:   paymentOptDetails(p.ProviderPaymentMethod),
		PaymentMethodID:         optUUID(p.PaymentMethodID),
		CreatedAt:               utc(p.CreatedAt),
		UpdatedAt:               utc(p.UpdatedAt),
	}
}

func paymentOf(r db.Payment) (*payment.Payment, error) {
	cur, err := mustCurrency(r.Currency)
	if err != nil {
		return nil, fmt.Errorf("payment %s: %w", r.ID, err)
	}
	details, err := paymentDecodeDetails(r.ProviderMethodDetails)
	if err != nil {
		return nil, fmt.Errorf("payment %s: %w", r.ID, err)
	}
	return &payment.Payment{
		ID:             idOf[core.PaymentID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		Payable: payment.Payable{
			Type:             payment.PayableType(r.PayableType),
			InvoiceID:        optIDOf[core.InvoiceID](r.InvoiceID),
			PaymentRequestID: optIDOf[core.PaymentRequestID](r.PaymentRequestID),
		},
		CustomerID:              idOf[core.CustomerID](r.CustomerID),
		Kind:                    payment.Kind(r.PaymentKind),
		Status:                  payment.State(r.Status),
		ProviderStatus:          r.ProviderStatus,
		Amount:                  r.Amount,
		Currency:                cur,
		Reference:               r.Reference,
		PaidAt:                  optUTC(r.PaidAt),
		IntegrationID:           optIDOf[core.IntegrationID](r.IntegrationID),
		ProviderPaymentID:       r.ProviderPaymentID,
		ProviderPaymentMethodID: r.ProviderPaymentMethodID,
		ProviderPaymentData:     rawJSON(r.ProviderPaymentData),
		ProviderPaymentMethod:   details,
		PaymentMethodID:         optIDOf[core.PaymentMethodID](r.PaymentMethodID),
		CreatedAt:               utc(r.CreatedAt),
		UpdatedAt:               utc(r.UpdatedAt),
	}, nil
}

func (r paymentRepo) Create(ctx context.Context, p *payment.Payment) error {
	q, err := r.t.queries(ctx, p.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreatePayment(ctx, paymentParams(p))
	return insertErr("payment", n, err)
}

func (r paymentRepo) Update(ctx context.Context, p *payment.Payment) error {
	q, err := r.t.queries(ctx, p.OrganizationID)
	if err != nil {
		return err
	}
	// At most one non-final provider payment per payable.
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdatePayment(ctx, db.UpdatePaymentParams(paymentParams(p)))
		return err
	})
	return updateErr("payment", p.ID, n, err)
}

func (r paymentRepo) Get(ctx context.Context, org core.OrganizationID, id core.PaymentID) (*payment.Payment, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetPayment(ctx, db.GetPaymentParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("payment", id, err)
	}
	return paymentOf(row)
}

func (r paymentRepo) GetByProviderPaymentID(ctx context.Context, org core.OrganizationID, integration core.IntegrationID, providerPaymentID string) (*payment.Payment, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetPaymentByProviderPaymentID(ctx, db.GetPaymentByProviderPaymentIDParams{
		OrganizationID: org.UUID(), IntegrationID: optUUID(integration), ProviderPaymentID: &providerPaymentID,
	})
	if err != nil {
		return nil, getErr("payment", providerPaymentID, err)
	}
	return paymentOf(row)
}

func (r paymentRepo) ListByPayable(ctx context.Context, org core.OrganizationID, payable payment.Payable) ([]*payment.Payment, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	var rows []db.Payment
	switch payable.Type {
	case payment.PayableTypeInvoice:
		rows, err = q.ListPaymentsByInvoice(ctx, db.ListPaymentsByInvoiceParams{
			OrganizationID: org.UUID(), InvoiceID: optUUID(payable.InvoiceID),
		})
	case payment.PayableTypePaymentRequest:
		rows, err = q.ListPaymentsByPaymentRequest(ctx, db.ListPaymentsByPaymentRequestParams{
			OrganizationID: org.UUID(), PaymentRequestID: optUUID(payable.PaymentRequestID),
		})
	default:
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list payments of %s: %w", payable, mapErr(err))
	}
	return convAll(rows, paymentOf)
}

func (r paymentRepo) List(ctx context.Context, org core.OrganizationID, f payment.Filter, page core.PageRequest) (core.Page[*payment.Payment], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*payment.Payment]{}, err
	}
	if !f.Order.Valid() {
		return core.Page[*payment.Payment]{}, fmt.Errorf("list payments: %w: order", core.ErrInvalidValue)
	}
	after, afterAt, lim, recent, err := paymentListArgs(f.Order, page)
	if err != nil {
		return core.Page[*payment.Payment]{}, err
	}
	var searchID *uuid.UUID
	if id, ok := payment.SearchID(core.SearchTerm(f.Search)); ok {
		searchID = new(id.UUID())
	}
	rows, err := q.ListPayments(ctx, db.ListPaymentsParams{
		OrganizationID: org.UUID(), CustomerID: optUUID(f.CustomerID), Status: paymentOptEnum(f.Status),
		Currency: currencyCode(f.Currency), InvoiceID: optUUID(f.InvoiceID), Visible: f.Visible,
		Search: searchArg(f.Search), SearchID: searchID,
		After: after, Recent: recent, AfterAt: afterAt, Lim: lim,
	})
	if err != nil {
		return core.Page[*payment.Payment]{}, fmt.Errorf("list payments: %w", mapErr(err))
	}
	return pageOf(rows, page, paymentOf, func(p *payment.Payment) string { return paymentListToken(recent, p.CreatedAt, p.ID.UUID()) })
}

// paymentListArgs returns the cursor and row limit of a payment list: in
// ID order, or with core.OrderRecent by creation time, newest first, then
// by ID (see [timeCursorArgs]).
func paymentListArgs(order core.ListOrder, p core.PageRequest) (after *uuid.UUID, afterAt *time.Time, lim int32, recent bool, err error) {
	if order == core.OrderRecent {
		after, afterAt, lim, err = timeCursorArgs(p)
		return after, afterAt, lim, true, err
	}
	after, lim, err = pageArgs(p)
	return after, nil, lim, false, err
}

// paymentListToken returns the page token after a row of a payment list.
func paymentListToken(recent bool, createdAt time.Time, id uuid.UUID) string {
	if recent {
		return core.Cursor{Key: createdAt.UTC().Format(time.RFC3339Nano), ID: id}.Encode()
	}
	return id.String()
}

// paymentReceiptRepo implements payment.ReceiptRepository.
type paymentReceiptRepo struct{ t *txState }

var _ payment.ReceiptRepository = paymentReceiptRepo{}

func paymentReceiptParams(x *payment.Receipt) db.CreatePaymentReceiptParams {
	return db.CreatePaymentReceiptParams{
		OrganizationID:  x.OrganizationID.UUID(),
		ID:              x.ID.UUID(),
		PaymentID:       x.PaymentID.UUID(),
		BillingEntityID: optUUID(x.BillingEntityID),
		Number:          x.Number,
		File:            optString(string(x.File)),
		XmlFile:         optString(string(x.XMLFile)),
		CreatedAt:       utc(x.CreatedAt),
		UpdatedAt:       utc(x.UpdatedAt),
	}
}

func paymentReceiptOf(r db.PaymentReceipt) (*payment.Receipt, error) {
	return &payment.Receipt{
		ID:              idOf[core.PaymentReceiptID](r.ID),
		OrganizationID:  idOf[core.OrganizationID](r.OrganizationID),
		PaymentID:       idOf[core.PaymentID](r.PaymentID),
		BillingEntityID: optIDOf[core.BillingEntityID](r.BillingEntityID),
		Number:          r.Number,
		File:            core.BlobRef(deref(r.File)),
		XMLFile:         core.BlobRef(deref(r.XmlFile)),
		CreatedAt:       utc(r.CreatedAt),
		UpdatedAt:       utc(r.UpdatedAt),
	}, nil
}

func (r paymentReceiptRepo) Create(ctx context.Context, x *payment.Receipt) error {
	q, err := r.t.queries(ctx, x.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreatePaymentReceipt(ctx, paymentReceiptParams(x))
	return insertErr("payment_receipt", n, err)
}

func (r paymentReceiptRepo) Update(ctx context.Context, x *payment.Receipt) error {
	q, err := r.t.queries(ctx, x.OrganizationID)
	if err != nil {
		return err
	}
	// The payment and the number are unique.
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdatePaymentReceipt(ctx, db.UpdatePaymentReceiptParams(paymentReceiptParams(x)))
		return err
	})
	return updateErr("payment_receipt", x.ID, n, err)
}

func (r paymentReceiptRepo) Get(ctx context.Context, org core.OrganizationID, id core.PaymentReceiptID) (*payment.Receipt, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetPaymentReceipt(ctx, db.GetPaymentReceiptParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("payment_receipt", id, err)
	}
	return paymentReceiptOf(row)
}

func (r paymentReceiptRepo) GetByPayment(ctx context.Context, org core.OrganizationID, p core.PaymentID) (*payment.Receipt, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetPaymentReceiptByPayment(ctx, db.GetPaymentReceiptByPaymentParams{OrganizationID: org.UUID(), PaymentID: p.UUID()})
	if err != nil {
		return nil, getErr("payment_receipt", p, err)
	}
	return paymentReceiptOf(row)
}

func (r paymentReceiptRepo) List(ctx context.Context, org core.OrganizationID, f payment.ReceiptFilter, page core.PageRequest) (core.Page[*payment.Receipt], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*payment.Receipt]{}, err
	}
	if !f.Order.Valid() {
		return core.Page[*payment.Receipt]{}, fmt.Errorf("list payment receipts: %w: order", core.ErrInvalidValue)
	}
	after, afterAt, lim, recent, err := paymentListArgs(f.Order, page)
	if err != nil {
		return core.Page[*payment.Receipt]{}, err
	}
	rows, err := q.ListPaymentReceipts(ctx, db.ListPaymentReceiptsParams{
		OrganizationID: org.UUID(), InvoiceID: optUUID(f.InvoiceID),
		After: after, Recent: recent, AfterAt: afterAt, Lim: lim,
	})
	if err != nil {
		return core.Page[*payment.Receipt]{}, fmt.Errorf("list payment receipts: %w", mapErr(err))
	}
	return pageOf(rows, page, paymentReceiptOf, func(x *payment.Receipt) string { return paymentListToken(recent, x.CreatedAt, x.ID.UUID()) })
}

// refundRepo implements payment.RefundRepository.
type refundRepo struct{ t *txState }

var _ payment.RefundRepository = refundRepo{}

func refundParams(x *payment.Refund) db.CreateRefundParams {
	return db.CreateRefundParams{
		OrganizationID:            x.OrganizationID.UUID(),
		ID:                        x.ID.UUID(),
		PaymentID:                 x.PaymentID.UUID(),
		Reason:                    paymentOptEnum(x.Reason),
		CreditNoteID:              optUUID(x.CreditNoteID),
		SubscriptionID:            optUUID(x.SubscriptionID),
		IntegrationID:             optUUID(x.IntegrationID),
		PaymentProviderCustomerID: optUUID(x.PaymentProviderCustomerID),
		Amount:                    x.Amount,
		Currency:                  x.Currency.Code(),
		Status:                    string(x.Status),
		ProviderRefundID:          x.ProviderRefundID,
		CreatedAt:                 utc(x.CreatedAt),
		UpdatedAt:                 utc(x.UpdatedAt),
	}
}

func refundOf(r db.Refund) (*payment.Refund, error) {
	cur, err := mustCurrency(r.Currency)
	if err != nil {
		return nil, fmt.Errorf("refund %s: %w", r.ID, err)
	}
	return &payment.Refund{
		ID:                        idOf[core.RefundID](r.ID),
		OrganizationID:            idOf[core.OrganizationID](r.OrganizationID),
		PaymentID:                 idOf[core.PaymentID](r.PaymentID),
		Reason:                    payment.RefundReason(deref(r.Reason)),
		CreditNoteID:              optIDOf[core.CreditNoteID](r.CreditNoteID),
		SubscriptionID:            optIDOf[core.SubscriptionID](r.SubscriptionID),
		IntegrationID:             optIDOf[core.IntegrationID](r.IntegrationID),
		PaymentProviderCustomerID: optIDOf[core.PaymentProviderCustomerID](r.PaymentProviderCustomerID),
		Amount:                    r.Amount,
		Currency:                  cur,
		Status:                    core.RefundStatus(r.Status),
		ProviderRefundID:          r.ProviderRefundID,
		CreatedAt:                 utc(r.CreatedAt),
		UpdatedAt:                 utc(r.UpdatedAt),
	}, nil
}

func (r refundRepo) Create(ctx context.Context, x *payment.Refund) error {
	q, err := r.t.queries(ctx, x.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateRefund(ctx, refundParams(x))
	return insertErr("refund", n, err)
}

func (r refundRepo) Update(ctx context.Context, x *payment.Refund) error {
	q, err := r.t.queries(ctx, x.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.UpdateRefund(ctx, db.UpdateRefundParams(refundParams(x)))
	return updateErr("refund", x.ID, n, err)
}

func (r refundRepo) Get(ctx context.Context, org core.OrganizationID, id core.RefundID) (*payment.Refund, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetRefund(ctx, db.GetRefundParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("refund", id, err)
	}
	return refundOf(row)
}

func (r refundRepo) GetByProviderRefundID(ctx context.Context, org core.OrganizationID, integration core.IntegrationID, providerRefundID string) (*payment.Refund, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetRefundByProviderRefundID(ctx, db.GetRefundByProviderRefundIDParams{
		OrganizationID: org.UUID(), IntegrationID: optUUID(integration), ProviderRefundID: &providerRefundID,
	})
	if err != nil {
		return nil, getErr("refund", providerRefundID, err)
	}
	return refundOf(row)
}

func (r refundRepo) ListByCreditNote(ctx context.Context, org core.OrganizationID, creditNote core.CreditNoteID) ([]*payment.Refund, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListRefundsByCreditNote(ctx, db.ListRefundsByCreditNoteParams{OrganizationID: org.UUID(), CreditNoteID: optUUID(creditNote)})
	if err != nil {
		return nil, fmt.Errorf("list refunds of credit note %s: %w", creditNote, mapErr(err))
	}
	return convAll(rows, refundOf)
}

func (r refundRepo) ListByPayment(ctx context.Context, org core.OrganizationID, p core.PaymentID) ([]*payment.Refund, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListRefundsByPayment(ctx, db.ListRefundsByPaymentParams{OrganizationID: org.UUID(), PaymentID: p.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list refunds of payment %s: %w", p, mapErr(err))
	}
	return convAll(rows, refundOf)
}

// paymentRequestRepo implements payment.RequestRepository.
type paymentRequestRepo struct{ t *txState }

var _ payment.RequestRepository = paymentRequestRepo{}

func paymentRequestParams(x *payment.Request) db.CreatePaymentRequestParams {
	return db.CreatePaymentRequestParams{
		OrganizationID:            x.OrganizationID.UUID(),
		ID:                        x.ID.UUID(),
		CustomerID:                x.CustomerID.UUID(),
		Amount:                    x.Amount,
		Currency:                  x.Currency.Code(),
		Email:                     x.Email,
		PaymentStatus:             string(x.PaymentStatus),
		PaymentAttempts:           int32(x.PaymentAttempts), // attempts are few
		DunningCampaignID:         optUUID(x.DunningCampaignID),
		PaymentMethodType:         paymentOptEnum(x.PaymentMethodType),
		PaymentMethodID:           optUUID(x.PaymentMethodID),
		ReadyForPaymentProcessing: x.ReadyForPaymentProcessing,
		CreatedAt:                 utc(x.CreatedAt),
		UpdatedAt:                 utc(x.UpdatedAt),
	}
}

// paymentRequestRow is the common shape of the payment request reads.
type paymentRequestRow struct {
	PaymentRequest db.PaymentRequest
	InvoiceIds     []uuid.UUID
}

func paymentRequestOf(row paymentRequestRow) (*payment.Request, error) {
	r := row.PaymentRequest
	cur, err := mustCurrency(r.Currency)
	if err != nil {
		return nil, fmt.Errorf("payment request %s: %w", r.ID, err)
	}
	return &payment.Request{
		ID:                        idOf[core.PaymentRequestID](r.ID),
		OrganizationID:            idOf[core.OrganizationID](r.OrganizationID),
		CustomerID:                idOf[core.CustomerID](r.CustomerID),
		InvoiceIDs:                idsOf[core.InvoiceID](row.InvoiceIds),
		Amount:                    r.Amount,
		Currency:                  cur,
		Email:                     r.Email,
		PaymentStatus:             core.PaymentStatus(r.PaymentStatus),
		PaymentAttempts:           int(r.PaymentAttempts),
		DunningCampaignID:         optIDOf[core.DunningCampaignID](r.DunningCampaignID),
		PaymentMethodType:         core.PaymentMethodType(deref(r.PaymentMethodType)),
		PaymentMethodID:           optIDOf[core.PaymentMethodID](r.PaymentMethodID),
		ReadyForPaymentProcessing: r.ReadyForPaymentProcessing,
		CreatedAt:                 utc(r.CreatedAt),
		UpdatedAt:                 utc(r.UpdatedAt),
	}, nil
}

// writeInvoices replaces the invoices of x.
func (r paymentRequestRepo) writeInvoices(ctx context.Context, q *db.Queries, x *payment.Request, replace bool) error {
	org, id := x.OrganizationID.UUID(), x.ID.UUID()
	if replace {
		if err := q.DeletePaymentRequestInvoices(ctx, db.DeletePaymentRequestInvoicesParams{OrganizationID: org, PaymentRequestID: id}); err != nil {
			return err
		}
	}
	if len(x.InvoiceIDs) == 0 {
		return nil
	}
	return q.InsertPaymentRequestInvoices(ctx, db.InsertPaymentRequestInvoicesParams{
		OrganizationID: org, PaymentRequestID: id, InvoiceIds: uuids(x.InvoiceIDs),
	})
}

func (r paymentRequestRepo) Create(ctx context.Context, x *payment.Request) error {
	q, err := r.t.queries(ctx, x.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreatePaymentRequest(ctx, paymentRequestParams(x))
	if err := insertErr("payment_request", n, err); err != nil {
		return err
	}
	if err := r.writeInvoices(ctx, q, x, false); err != nil {
		return fmt.Errorf("create payment_request %s: %w", x.ID, mapErr(err))
	}
	return nil
}

func (r paymentRequestRepo) Update(ctx context.Context, x *payment.Request) error {
	q, err := r.t.queries(ctx, x.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() error {
		var err error
		n, err = q.UpdatePaymentRequest(ctx, db.UpdatePaymentRequestParams(paymentRequestParams(x)))
		if err != nil || n == 0 {
			return err
		}
		return r.writeInvoices(ctx, q, x, true)
	})
	return updateErr("payment_request", x.ID, n, err)
}

func (r paymentRequestRepo) Get(ctx context.Context, org core.OrganizationID, id core.PaymentRequestID) (*payment.Request, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetPaymentRequest(ctx, db.GetPaymentRequestParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("payment_request", id, err)
	}
	return paymentRequestOf(paymentRequestRow(row))
}

func (r paymentRequestRepo) List(ctx context.Context, org core.OrganizationID, f payment.RequestFilter, page core.PageRequest) (core.Page[*payment.Request], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*payment.Request]{}, err
	}
	if !f.Order.Valid() {
		return core.Page[*payment.Request]{}, fmt.Errorf("list payment requests: %w: order", core.ErrInvalidValue)
	}
	after, afterAt, lim, recent, err := paymentListArgs(f.Order, page)
	if err != nil {
		return core.Page[*payment.Request]{}, err
	}
	rows, err := q.ListPaymentRequests(ctx, db.ListPaymentRequestsParams{
		OrganizationID: org.UUID(), CustomerID: optUUID(f.CustomerID), PaymentStatus: paymentOptEnum(f.PaymentStatus),
		Currency: currencyCode(f.Currency), BillingEntityIds: uuids(f.BillingEntityIDs),
		After: after, Recent: recent, AfterAt: afterAt, Lim: lim,
	})
	if err != nil {
		return core.Page[*payment.Request]{}, fmt.Errorf("list payment requests: %w", mapErr(err))
	}
	return pageOf(rows, page,
		func(r db.ListPaymentRequestsRow) (*payment.Request, error) {
			return paymentRequestOf(paymentRequestRow(r))
		},
		func(x *payment.Request) string { return paymentListToken(recent, x.CreatedAt, x.ID.UUID()) })
}

func (r paymentRequestRepo) ListByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) ([]*payment.Request, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListPaymentRequestsByInvoice(ctx, db.ListPaymentRequestsByInvoiceParams{OrganizationID: org.UUID(), InvoiceID: invoice.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list payment requests of invoice %s: %w", invoice, mapErr(err))
	}
	return convAll(rows, func(r db.ListPaymentRequestsByInvoiceRow) (*payment.Request, error) {
		return paymentRequestOf(paymentRequestRow(r))
	})
}

// paymentMethodRepo implements payment.MethodRepository.
type paymentMethodRepo struct{ t *txState }

var _ payment.MethodRepository = paymentMethodRepo{}

func paymentMethodParams(m *payment.Method) db.CreatePaymentMethodParams {
	return db.CreatePaymentMethodParams{
		OrganizationID:            m.OrganizationID.UUID(),
		ID:                        m.ID.UUID(),
		CustomerID:                m.CustomerID.UUID(),
		IntegrationID:             optUUID(m.IntegrationID),
		PaymentProviderCustomerID: optUUID(m.PaymentProviderCustomerID),
		ProviderMethodID:          m.ProviderMethodID,
		Details:                   paymentEncodeDetails(m.Details),
		IsDefault:                 m.IsDefault,
		DeletedAt:                 optUTC(m.DeletedAt),
		CreatedAt:                 utc(m.CreatedAt),
		UpdatedAt:                 utc(m.UpdatedAt),
	}
}

func paymentMethodOf(r db.PaymentMethod) (*payment.Method, error) {
	details, err := paymentDecodeDetails(r.Details)
	if err != nil {
		return nil, fmt.Errorf("payment method %s: %w", r.ID, err)
	}
	return &payment.Method{
		ID:                        idOf[core.PaymentMethodID](r.ID),
		OrganizationID:            idOf[core.OrganizationID](r.OrganizationID),
		CustomerID:                idOf[core.CustomerID](r.CustomerID),
		IntegrationID:             optIDOf[core.IntegrationID](r.IntegrationID),
		PaymentProviderCustomerID: optIDOf[core.PaymentProviderCustomerID](r.PaymentProviderCustomerID),
		ProviderMethodID:          r.ProviderMethodID,
		Details:                   details,
		IsDefault:                 r.IsDefault,
		DeletedAt:                 optUTC(r.DeletedAt),
		CreatedAt:                 utc(r.CreatedAt),
		UpdatedAt:                 utc(r.UpdatedAt),
	}, nil
}

func (r paymentMethodRepo) Create(ctx context.Context, m *payment.Method) error {
	q, err := r.t.queries(ctx, m.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreatePaymentMethod(ctx, paymentMethodParams(m))
	return insertErr("payment_method", n, err)
}

func (r paymentMethodRepo) Update(ctx context.Context, m *payment.Method) error {
	q, err := r.t.queries(ctx, m.OrganizationID)
	if err != nil {
		return err
	}
	// At most one live default per customer.
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdatePaymentMethod(ctx, db.UpdatePaymentMethodParams(paymentMethodParams(m)))
		return err
	})
	return updateErr("payment_method", m.ID, n, err)
}

func (r paymentMethodRepo) Get(ctx context.Context, org core.OrganizationID, id core.PaymentMethodID) (*payment.Method, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetPaymentMethod(ctx, db.GetPaymentMethodParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("payment_method", id, err)
	}
	return paymentMethodOf(row)
}

func (r paymentMethodRepo) GetDefault(ctx context.Context, org core.OrganizationID, customer core.CustomerID) (*payment.Method, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetDefaultPaymentMethod(ctx, db.GetDefaultPaymentMethodParams{OrganizationID: org.UUID(), CustomerID: customer.UUID()})
	if err != nil {
		return nil, getErr("payment_method", "default of "+customer.String(), err)
	}
	return paymentMethodOf(row)
}

func (r paymentMethodRepo) GetByProviderMethodID(ctx context.Context, org core.OrganizationID, integration core.IntegrationID, providerMethodID string) (*payment.Method, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetPaymentMethodByProviderMethodID(ctx, db.GetPaymentMethodByProviderMethodIDParams{
		OrganizationID: org.UUID(), IntegrationID: optUUID(integration), ProviderMethodID: providerMethodID,
	})
	if err != nil {
		return nil, getErr("payment_method", providerMethodID, err)
	}
	return paymentMethodOf(row)
}

func (r paymentMethodRepo) ListByCustomer(ctx context.Context, org core.OrganizationID, customer core.CustomerID) ([]*payment.Method, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListPaymentMethodsByCustomer(ctx, db.ListPaymentMethodsByCustomerParams{OrganizationID: org.UUID(), CustomerID: customer.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list payment methods of customer %s: %w", customer, mapErr(err))
	}
	return convAll(rows, paymentMethodOf)
}

// paymentProviderCustomerRepo implements payment.ProviderCustomerRepository.
type paymentProviderCustomerRepo struct{ t *txState }

var _ payment.ProviderCustomerRepository = paymentProviderCustomerRepo{}

func paymentProviderCustomerParams(c *payment.ProviderCustomer) db.CreatePaymentProviderCustomerParams {
	return db.CreatePaymentProviderCustomerParams{
		OrganizationID:         c.OrganizationID.UUID(),
		ID:                     c.ID.UUID(),
		CustomerID:             c.CustomerID.UUID(),
		IntegrationID:          c.IntegrationID.UUID(),
		ProviderCustomerID:     c.ProviderCustomerID,
		SyncWithProvider:       c.SyncWithProvider,
		ProviderPaymentMethods: enumStrings(c.ProviderPaymentMethods),
		DeletedAt:              optUTC(c.DeletedAt),
		CreatedAt:              utc(c.CreatedAt),
		UpdatedAt:              utc(c.UpdatedAt),
	}
}

func paymentProviderCustomerOf(r db.PaymentProviderCustomer) (*payment.ProviderCustomer, error) {
	return &payment.ProviderCustomer{
		ID:                     idOf[core.PaymentProviderCustomerID](r.ID),
		OrganizationID:         idOf[core.OrganizationID](r.OrganizationID),
		CustomerID:             idOf[core.CustomerID](r.CustomerID),
		IntegrationID:          idOf[core.IntegrationID](r.IntegrationID),
		ProviderCustomerID:     r.ProviderCustomerID,
		SyncWithProvider:       r.SyncWithProvider,
		ProviderPaymentMethods: enumsOf[string](r.ProviderPaymentMethods),
		DeletedAt:              optUTC(r.DeletedAt),
		CreatedAt:              utc(r.CreatedAt),
		UpdatedAt:              utc(r.UpdatedAt),
	}, nil
}

func (r paymentProviderCustomerRepo) Create(ctx context.Context, c *payment.ProviderCustomer) error {
	q, err := r.t.queries(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreatePaymentProviderCustomer(ctx, paymentProviderCustomerParams(c))
	return insertErr("payment_provider_customer", n, err)
}

func (r paymentProviderCustomerRepo) Update(ctx context.Context, c *payment.ProviderCustomer) error {
	q, err := r.t.queries(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	// One live row per (customer, connection).
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdatePaymentProviderCustomer(ctx, db.UpdatePaymentProviderCustomerParams(paymentProviderCustomerParams(c)))
		return err
	})
	return updateErr("payment_provider_customer", c.ID, n, err)
}

func (r paymentProviderCustomerRepo) Get(ctx context.Context, org core.OrganizationID, id core.PaymentProviderCustomerID) (*payment.ProviderCustomer, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetPaymentProviderCustomer(ctx, db.GetPaymentProviderCustomerParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("payment_provider_customer", id, err)
	}
	return paymentProviderCustomerOf(row)
}

func (r paymentProviderCustomerRepo) GetByCustomer(ctx context.Context, org core.OrganizationID, customer core.CustomerID, integration core.IntegrationID) (*payment.ProviderCustomer, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetPaymentProviderCustomerByCustomer(ctx, db.GetPaymentProviderCustomerByCustomerParams{
		OrganizationID: org.UUID(), CustomerID: customer.UUID(), IntegrationID: integration.UUID(),
	})
	if err != nil {
		return nil, getErr("payment_provider_customer", customer, err)
	}
	return paymentProviderCustomerOf(row)
}

func (r paymentProviderCustomerRepo) GetByProviderCustomerID(ctx context.Context, org core.OrganizationID, integration core.IntegrationID, providerCustomerID string) (*payment.ProviderCustomer, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetPaymentProviderCustomerByProviderCustomerID(ctx, db.GetPaymentProviderCustomerByProviderCustomerIDParams{
		OrganizationID: org.UUID(), IntegrationID: integration.UUID(), ProviderCustomerID: &providerCustomerID,
	})
	if err != nil {
		return nil, getErr("payment_provider_customer", providerCustomerID, err)
	}
	return paymentProviderCustomerOf(row)
}

func (r paymentProviderCustomerRepo) ListByCustomer(ctx context.Context, org core.OrganizationID, customer core.CustomerID) ([]*payment.ProviderCustomer, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListPaymentProviderCustomersByCustomer(ctx, db.ListPaymentProviderCustomersByCustomerParams{OrganizationID: org.UUID(), CustomerID: customer.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list payment provider customers of customer %s: %w", customer, mapErr(err))
	}
	return convAll(rows, paymentProviderCustomerOf)
}

func (r paymentProviderCustomerRepo) ListByIntegration(ctx context.Context, org core.OrganizationID, integration core.IntegrationID) ([]*payment.ProviderCustomer, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListPaymentProviderCustomersByIntegration(ctx, db.ListPaymentProviderCustomersByIntegrationParams{OrganizationID: org.UUID(), IntegrationID: integration.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list payment provider customers of integration %s: %w", integration, mapErr(err))
	}
	return convAll(rows, paymentProviderCustomerOf)
}

// paymentIntentRepo implements payment.IntentRepository.
type paymentIntentRepo struct{ t *txState }

var _ payment.IntentRepository = paymentIntentRepo{}

func paymentIntentParams(i *payment.Intent) db.CreatePaymentIntentParams {
	return db.CreatePaymentIntentParams{
		OrganizationID:    i.OrganizationID.UUID(),
		ID:                i.ID.UUID(),
		InvoiceID:         i.InvoiceID.UUID(),
		Status:            string(i.Status),
		PaymentUrl:        i.PaymentURL,
		ProviderSessionID: i.ProviderSessionID,
		ExpiresAt:         utc(i.ExpiresAt),
		CreatedAt:         utc(i.CreatedAt),
		UpdatedAt:         utc(i.UpdatedAt),
	}
}

func paymentIntentOf(r db.PaymentIntent) (*payment.Intent, error) {
	return &payment.Intent{
		ID:                idOf[payment.IntentID](r.ID),
		OrganizationID:    idOf[core.OrganizationID](r.OrganizationID),
		InvoiceID:         idOf[core.InvoiceID](r.InvoiceID),
		Status:            payment.IntentStatus(r.Status),
		PaymentURL:        r.PaymentUrl,
		ProviderSessionID: r.ProviderSessionID,
		ExpiresAt:         utc(r.ExpiresAt),
		CreatedAt:         utc(r.CreatedAt),
		UpdatedAt:         utc(r.UpdatedAt),
	}, nil
}

func (r paymentIntentRepo) Create(ctx context.Context, i *payment.Intent) error {
	q, err := r.t.queries(ctx, i.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreatePaymentIntent(ctx, paymentIntentParams(i))
	return insertErr("payment_intent", n, err)
}

func (r paymentIntentRepo) Update(ctx context.Context, i *payment.Intent) error {
	q, err := r.t.queries(ctx, i.OrganizationID)
	if err != nil {
		return err
	}
	// At most one active link per invoice.
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdatePaymentIntent(ctx, db.UpdatePaymentIntentParams(paymentIntentParams(i)))
		return err
	})
	return updateErr("payment_intent", i.ID, n, err)
}

func (r paymentIntentRepo) GetActiveByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) (*payment.Intent, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetActivePaymentIntentByInvoice(ctx, db.GetActivePaymentIntentByInvoiceParams{OrganizationID: org.UUID(), InvoiceID: invoice.UUID()})
	if err != nil {
		return nil, getErr("payment_intent", invoice, err)
	}
	return paymentIntentOf(row)
}

func (r paymentIntentRepo) ListAwaitingExpiration(ctx context.Context, org core.OrganizationID, now time.Time) ([]*payment.Intent, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListPaymentIntentsAwaitingExpiration(ctx, db.ListPaymentIntentsAwaitingExpirationParams{OrganizationID: org.UUID(), Now: utc(now)})
	if err != nil {
		return nil, fmt.Errorf("list payment intents awaiting expiration: %w", mapErr(err))
	}
	return convAll(rows, paymentIntentOf)
}

// inboundWebhookRepo implements payment.InboundWebhookRepository.
type inboundWebhookRepo struct{ t *txState }

var _ payment.InboundWebhookRepository = inboundWebhookRepo{}

func inboundWebhookParams(w *payment.InboundWebhook) db.CreateInboundWebhookParams {
	return db.CreateInboundWebhookParams{
		OrganizationID: w.OrganizationID.UUID(),
		ID:             w.ID.UUID(),
		Source:         w.Source,
		IntegrationID:  optUUID(w.IntegrationID),
		Code:           w.Code,
		EventType:      w.EventType,
		Payload:        rawJSON(w.Payload),
		Signature:      w.Signature,
		Status:         string(w.Status),
		Attempts:       int32(w.Attempts), // attempts are few
		ProcessingAt:   optUTC(w.ProcessingAt),
		CreatedAt:      utc(w.CreatedAt),
		UpdatedAt:      utc(w.UpdatedAt),
	}
}

func inboundWebhookOf(r db.InboundWebhook) (*payment.InboundWebhook, error) {
	return &payment.InboundWebhook{
		ID:             idOf[core.InboundWebhookID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		Source:         r.Source,
		IntegrationID:  optIDOf[core.IntegrationID](r.IntegrationID),
		Code:           r.Code,
		EventType:      r.EventType,
		Payload:        rawJSON(r.Payload),
		Signature:      r.Signature,
		Status:         payment.InboundWebhookStatus(r.Status),
		Attempts:       int(r.Attempts),
		ProcessingAt:   optUTC(r.ProcessingAt),
		CreatedAt:      utc(r.CreatedAt),
		UpdatedAt:      utc(r.UpdatedAt),
	}, nil
}

func (r inboundWebhookRepo) Create(ctx context.Context, w *payment.InboundWebhook) error {
	q, err := r.t.queries(ctx, w.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateInboundWebhook(ctx, inboundWebhookParams(w))
	return insertErr("inbound_webhook", n, err)
}

func (r inboundWebhookRepo) Update(ctx context.Context, w *payment.InboundWebhook) error {
	q, err := r.t.queries(ctx, w.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.UpdateInboundWebhook(ctx, db.UpdateInboundWebhookParams(inboundWebhookParams(w)))
	return updateErr("inbound_webhook", w.ID, n, err)
}

func (r inboundWebhookRepo) Get(ctx context.Context, org core.OrganizationID, id core.InboundWebhookID) (*payment.InboundWebhook, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetInboundWebhook(ctx, db.GetInboundWebhookParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("inbound_webhook", id, err)
	}
	return inboundWebhookOf(row)
}

func (r inboundWebhookRepo) ListRetriable(ctx context.Context, org core.OrganizationID, now time.Time) ([]*payment.InboundWebhook, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListRetriableInboundWebhooks(ctx, db.ListRetriableInboundWebhooksParams{
		OrganizationID: org.UUID(),
		RetryInterval:  paymentInterval(payment.WebhookRetryInterval),
		RetryWindow:    paymentInterval(payment.WebhookRetryWindow),
		Now:            utc(now),
	})
	if err != nil {
		return nil, fmt.Errorf("list retriable inbound webhooks: %w", mapErr(err))
	}
	return convAll(rows, inboundWebhookOf)
}

func (r inboundWebhookRepo) DeleteExpired(ctx context.Context, org core.OrganizationID, now time.Time) (int, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	n, err := q.DeleteExpiredInboundWebhooks(ctx, db.DeleteExpiredInboundWebhooksParams{
		OrganizationID: org.UUID(), Cutoff: utc(now.Add(-payment.WebhookRetention)),
	})
	if err != nil {
		return 0, fmt.Errorf("delete expired inbound webhooks: %w", mapErr(err))
	}
	return int(n), nil
}
