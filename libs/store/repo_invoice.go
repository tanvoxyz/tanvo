package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindInvoice binds the repositories of the invoice aggregates (the
// invoice custom sections are bound with the organization's).
func (t *txState) bindInvoice(r *port.Repos) {
	r.Invoices = invoiceRepo{t}
	r.InvoiceSubscriptions = invoiceSubscriptionRepo{t}
	r.Fees = feeRepo{t}
	r.InvoiceAppliedTaxes = invoiceAppliedTaxRepo{t}
	r.AdjustedFees = adjustedFeeRepo{t}
	r.InvoiceSettlements = invoiceSettlementRepo{t}
	r.ErrorDetails = invoiceErrorDetailRepo{t}
	r.Credits = invoiceCreditRepo{t}
	r.AppliedInvoiceCustomSections = appliedCustomSectionRepo{t}
}

// invoiceDateCol returns the date column of d (NULL for the zero date).
func invoiceDateCol(d core.Date) *time.Time {
	if d.IsZero() {
		return nil
	}
	t := d.Midnight(time.UTC)
	return &t
}

// invoiceDateOf converts a nullable date column.
func invoiceDateOf(t *time.Time) core.Date {
	if t == nil {
		return core.Date{}
	}
	y, m, d := t.Date()
	return core.Date{Year: y, Month: m, Day: d}
}

// invoiceMapJSON encodes a string map as a jsonb object (never null).
func invoiceMapJSON(m map[string]string) []byte {
	if len(m) == 0 {
		return []byte("{}")
	}
	b, _ := json.Marshal(m) // a string map always encodes
	return b
}

// invoiceMapOf decodes a jsonb object of strings (empty to nil).
func invoiceMapOf(b []byte) (map[string]string, error) {
	var m map[string]string
	if err := decodeJSON(b, &m); err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, nil
	}
	return m, nil
}

// invoiceStatusErr maps the status read of a delete that is only allowed
// for drafts: a missing row is core.ErrNotFound and another status
// core.ErrConflict.
func invoiceStatusErr(entity string, key any, status string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound(entity, key)
	}
	if err != nil {
		return fmt.Errorf("%s %v: %w", entity, key, mapErr(err))
	}
	if status != "draft" {
		return conflict(entity, "not_draft")
	}
	return nil
}

// invoiceRepo implements invoice.Repository.
type invoiceRepo struct{ t *txState }

var _ invoice.Repository = invoiceRepo{}

func invoiceParams(inv *invoice.Invoice) db.CreateInvoiceParams {
	p := db.CreateInvoiceParams{
		OrganizationID:                 inv.OrganizationID.UUID(),
		ID:                             inv.ID.UUID(),
		CustomerID:                     inv.CustomerID.UUID(),
		BillingEntityID:                inv.BillingEntityID.UUID(),
		InvoiceType:                    string(inv.Type),
		Status:                         string(inv.Status),
		PaymentStatus:                  string(inv.PaymentStatus),
		TaxStatus:                      optString(string(inv.TaxStatus)),
		Number:                         inv.Number,
		SequentialID:                   inv.SequentialID,
		BillingEntitySequentialID:      inv.BillingEntitySequentialID,
		Currency:                       inv.Currency.Code(),
		FeesAmount:                     inv.FeesAmount,
		ProgressiveBillingCreditAmount: inv.ProgressiveBillingCreditAmount,
		CouponsAmount:                  inv.CouponsAmount,
		SubTotalExcludingTaxesAmount:   inv.SubTotalExcludingTaxesAmount,
		TaxesAmount:                    inv.TaxesAmount,
		SubTotalIncludingTaxesAmount:   inv.SubTotalIncludingTaxesAmount,
		CreditNotesAmount:              inv.CreditNotesAmount,
		PrepaidCreditAmount:            inv.PrepaidCreditAmount,
		PrepaidGrantedCreditAmount:     inv.PrepaidGrantedCreditAmount,
		PrepaidPurchasedCreditAmount:   inv.PrepaidPurchasedCreditAmount,
		TotalAmount:                    inv.TotalAmount,
		TotalPaidAmount:                inv.TotalPaidAmount,
		OffsettedAmount:                inv.OffsettedAmount,
		IssuingDate:                    invoiceDateCol(inv.IssuingDate),
		PaymentDueDate:                 invoiceDateCol(inv.PaymentDueDate),
		ExpectedFinalizationDate:       invoiceDateCol(inv.ExpectedFinalizationDate),
		AppliedGracePeriodDays:         int32(inv.AppliedGracePeriodDays), // validated small
		NetPaymentTermDays:             int32(inv.NetPaymentTermDays),     // validated small
		PaymentOverdue:                 inv.PaymentOverdue,
		PaymentDisputeLostAt:           optUTC(inv.PaymentDisputeLostAt),
		SelfBilled:                     inv.SelfBilled,
		VoidedInvoiceID:                optUUID(inv.VoidedInvoiceID),
		SkipAutomaticPayment:           inv.SkipAutomaticPayment,
		PaymentAttempts:                int32(min(inv.PaymentAttempts, math.MaxInt32)), // a counter of attempts
		PaymentMethodType:              optString(string(inv.PaymentMethodType)),
		PaymentMethodID:                optUUID(inv.PaymentMethodID),
		PurchaseOrderNumber:            inv.PurchaseOrderNumber,
		X402ConnectionID:               optUUID(inv.X402ConnectionID),
		X402PaymentToken:               inv.X402PaymentToken,
		Metadata:                       encodeMetadata(inv.Metadata),
		File:                           optString(string(inv.File)),
		XmlFile:                        optString(string(inv.XMLFile)),
		ReadyForPaymentProcessing:      inv.ReadyForPaymentProcessing,
		RefreshRequested:               inv.RefreshRequested,
		FinalizedAt:                    optUTC(inv.FinalizedAt),
		VoidedAt:                       optUTC(inv.VoidedAt),
		CreatedAt:                      utc(inv.CreatedAt),
		UpdatedAt:                      utc(inv.UpdatedAt),
		TaxesRate:                      inv.TaxesRate,
	}
	return p
}

func invoiceOf(r db.Invoice) (*invoice.Invoice, error) {
	cur, err := mustCurrency(r.Currency)
	if err != nil {
		return nil, fmt.Errorf("invoice %s: %w", r.ID, err)
	}
	md, err := decodeMetadata(r.Metadata)
	if err != nil {
		return nil, fmt.Errorf("invoice %s: %w", r.ID, err)
	}
	inv := &invoice.Invoice{
		ID:                             idOf[core.InvoiceID](r.ID),
		OrganizationID:                 idOf[core.OrganizationID](r.OrganizationID),
		CustomerID:                     idOf[core.CustomerID](r.CustomerID),
		BillingEntityID:                idOf[core.BillingEntityID](r.BillingEntityID),
		Type:                           invoice.Type(r.InvoiceType),
		Status:                         invoice.Status(r.Status),
		PaymentStatus:                  core.PaymentStatus(r.PaymentStatus),
		TaxStatus:                      invoice.TaxStatus(deref(r.TaxStatus)),
		Number:                         r.Number,
		SequentialID:                   r.SequentialID,
		BillingEntitySequentialID:      r.BillingEntitySequentialID,
		Currency:                       cur,
		FeesAmount:                     r.FeesAmount,
		ProgressiveBillingCreditAmount: r.ProgressiveBillingCreditAmount,
		CouponsAmount:                  r.CouponsAmount,
		SubTotalExcludingTaxesAmount:   r.SubTotalExcludingTaxesAmount,
		TaxesAmount:                    r.TaxesAmount,
		TaxesRate:                      r.TaxesRate,
		SubTotalIncludingTaxesAmount:   r.SubTotalIncludingTaxesAmount,
		CreditNotesAmount:              r.CreditNotesAmount,
		PrepaidCreditAmount:            r.PrepaidCreditAmount,
		PrepaidGrantedCreditAmount:     r.PrepaidGrantedCreditAmount,
		PrepaidPurchasedCreditAmount:   r.PrepaidPurchasedCreditAmount,
		TotalAmount:                    r.TotalAmount,
		TotalPaidAmount:                r.TotalPaidAmount,
		OffsettedAmount:                r.OffsettedAmount,
		IssuingDate:                    invoiceDateOf(r.IssuingDate),
		PaymentDueDate:                 invoiceDateOf(r.PaymentDueDate),
		ExpectedFinalizationDate:       invoiceDateOf(r.ExpectedFinalizationDate),
		AppliedGracePeriodDays:         int(r.AppliedGracePeriodDays),
		NetPaymentTermDays:             int(r.NetPaymentTermDays),
		PaymentOverdue:                 r.PaymentOverdue,
		PaymentDisputeLostAt:           optUTC(r.PaymentDisputeLostAt),
		SelfBilled:                     r.SelfBilled,
		VoidedInvoiceID:                optIDOf[core.InvoiceID](r.VoidedInvoiceID),
		SkipAutomaticPayment:           r.SkipAutomaticPayment,
		PaymentAttempts:                int(r.PaymentAttempts),
		PaymentMethodType:              core.PaymentMethodType(deref(r.PaymentMethodType)),
		PaymentMethodID:                optIDOf[core.PaymentMethodID](r.PaymentMethodID),
		PurchaseOrderNumber:            r.PurchaseOrderNumber,
		X402ConnectionID:               optIDOf[core.X402ConnectionID](r.X402ConnectionID),
		X402PaymentToken:               r.X402PaymentToken,
		Metadata:                       md,
		File:                           core.BlobRef(deref(r.File)),
		XMLFile:                        core.BlobRef(deref(r.XmlFile)),
		ReadyForPaymentProcessing:      r.ReadyForPaymentProcessing,
		RefreshRequested:               r.RefreshRequested,
		FinalizedAt:                    optUTC(r.FinalizedAt),
		VoidedAt:                       optUTC(r.VoidedAt),
		CreatedAt:                      utc(r.CreatedAt),
		UpdatedAt:                      utc(r.UpdatedAt),
	}
	return inv, nil
}

func (r invoiceRepo) Create(ctx context.Context, inv *invoice.Invoice) error {
	q, err := r.t.queries(ctx, inv.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateInvoice(ctx, invoiceParams(inv))
	return insertErr("invoice", n, err)
}

func (r invoiceRepo) Update(ctx context.Context, inv *invoice.Invoice) error {
	q, err := r.t.queries(ctx, inv.OrganizationID)
	if err != nil {
		return err
	}
	// The number is unique: guard the update so that a conflict keeps the
	// transaction usable.
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateInvoice(ctx, db.UpdateInvoiceParams(invoiceParams(inv)))
		return err
	})
	return updateErr("invoice", inv.ID, n, err)
}

func (r invoiceRepo) Get(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (*invoice.Invoice, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetInvoice(ctx, db.GetInvoiceParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("invoice", id, err)
	}
	return invoiceOf(row)
}

func (r invoiceRepo) GetByNumber(ctx context.Context, org core.OrganizationID, number string) (*invoice.Invoice, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetInvoiceByNumber(ctx, db.GetInvoiceByNumberParams{OrganizationID: org.UUID(), Number: number})
	if err != nil {
		return nil, getErr("invoice", number, err)
	}
	return invoiceOf(row)
}

func (r invoiceRepo) GetRegenerated(ctx context.Context, org core.OrganizationID, voided core.InvoiceID) (*invoice.Invoice, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetRegeneratedInvoice(ctx, db.GetRegeneratedInvoiceParams{OrganizationID: org.UUID(), VoidedInvoiceID: new(voided.UUID())})
	if err != nil {
		return nil, getErr("invoice", voided, err)
	}
	return invoiceOf(row)
}

func (r invoiceRepo) List(ctx context.Context, org core.OrganizationID, f invoice.Filter, page core.PageRequest) (core.Page[*invoice.Invoice], error) {
	if err := f.Validate(); err != nil {
		return core.Page[*invoice.Invoice]{}, fmt.Errorf("list invoices: %w", err)
	}
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*invoice.Invoice]{}, err
	}
	var (
		after       *uuid.UUID
		afterIssued *time.Time
		lim         int32
	)
	recent := f.Order == core.OrderRecent
	if recent {
		after, afterIssued, lim, err = invoiceRecentArgs(page)
	} else {
		after, lim, err = pageArgs(page)
	}
	if err != nil {
		return core.Page[*invoice.Invoice]{}, err
	}
	var searchID *uuid.UUID
	if id, ok := invoice.SearchID(core.SearchTerm(f.Search)); ok {
		searchID = new(id.UUID())
	}
	var currency *string
	if !f.Currency.IsZero() {
		currency = new(f.Currency.Code())
	}
	present, absent := metadataFilterArgs(f.Metadata)
	rows, err := q.ListInvoices(ctx, db.ListInvoicesParams{
		OrganizationID:      org.UUID(),
		IncludeGenerating:   f.IncludeGenerating,
		IncludeUnlisted:     f.IncludeUnlisted,
		CustomerID:          optUUID(f.CustomerID),
		CustomerExternalID:  optString(f.CustomerExternalID),
		BillingEntityID:     optUUID(f.BillingEntityID),
		BillingEntityIds:    uuids(f.BillingEntityIDs),
		Currency:            currency,
		Statuses:            enumStrings(f.Statuses),
		PaymentStatuses:     enumStrings(f.PaymentStatuses),
		Types:               enumStrings(f.Types),
		Ids:                 uuids(f.IDs),
		SubscriptionIds:     uuids(f.SubscriptionIDs),
		IssuingDateFrom:     invoiceDateCol(f.IssuingDateFrom),
		IssuingDateTo:       invoiceDateCol(f.IssuingDateTo),
		AmountFrom:          f.AmountFrom,
		AmountTo:            f.AmountTo,
		PaymentDisputeLost:  f.PaymentDisputeLost,
		PaymentOverdue:      f.PaymentOverdue,
		PartiallyPaid:       f.PartiallyPaid,
		PositiveDueAmount:   f.PositiveDueAmount,
		SelfBilled:          f.SelfBilled,
		PurchaseOrderNumber: optString(f.PurchaseOrderNumber),
		Settlements:         enumStrings(f.Settlements),
		MetadataPresent:     present,
		MetadataAbsent:      absent,
		Search:              searchArg(f.Search),
		SearchID:            searchID,
		SearchCustomers:     f.SearchesCustomers(),
		After:               after,
		Recent:              recent,
		AfterIssuingDate:    afterIssued,
		Lim:                 lim,
	})
	if err != nil {
		return core.Page[*invoice.Invoice]{}, fmt.Errorf("list invoices: %w", mapErr(err))
	}
	if recent {
		return pageOf(rows, page, invoiceOf, invoiceRecentToken)
	}
	return pageOf(rows, page, invoiceOf, func(inv *invoice.Invoice) string { return inv.ID.String() })
}

// invoiceRecentArgs returns the cursor (ID and issuing date, nil for none)
// and the row limit of a page of the invoice list by issuing date.
func invoiceRecentArgs(p core.PageRequest) (*uuid.UUID, *time.Time, int32, error) {
	lim := int32(p.Limit()) + 1 // Limit is at most core.MaxPageSize
	if p.Token == "" {
		return nil, nil, lim, nil
	}
	c, err := core.DecodeCursor(p.Token)
	if err != nil {
		return nil, nil, 0, err
	}
	var issued *time.Time
	if c.Key != "" {
		d, err := core.ParseDate(c.Key)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("%w: page token", core.ErrInvalidValue)
		}
		issued = invoiceDateCol(d)
	}
	return &c.ID, issued, lim, nil
}

// invoiceRecentToken returns the page token after inv in the invoice list
// by issuing date.
func invoiceRecentToken(inv *invoice.Invoice) string {
	c := core.Cursor{ID: inv.ID.UUID()}
	if !inv.IssuingDate.IsZero() {
		c.Key = inv.IssuingDate.String()
	}
	return c.Encode()
}

// invoiceRows converts the rows of a list query.
func invoiceRows(what string, rows []db.Invoice, err error) ([]*invoice.Invoice, error) {
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, mapErr(err))
	}
	return convAll(rows, invoiceOf)
}

func (r invoiceRepo) ListByIDs(ctx context.Context, org core.OrganizationID, ids []core.InvoiceID) ([]*invoice.Invoice, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListInvoicesByIDs(ctx, db.ListInvoicesByIDsParams{OrganizationID: org.UUID(), Ids: uuids(ids)})
	return invoiceRows("list invoices", rows, err)
}

func (r invoiceRepo) ListDraftsToFinalize(ctx context.Context, org core.OrganizationID, date core.Date) ([]*invoice.Invoice, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListInvoiceDraftsToFinalize(ctx, db.ListInvoiceDraftsToFinalizeParams{OrganizationID: org.UUID(), Date: date.Midnight(time.UTC)})
	return invoiceRows("list drafts to finalize", rows, err)
}

func (r invoiceRepo) ListRefreshRequested(ctx context.Context, org core.OrganizationID) ([]*invoice.Invoice, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListInvoicesRefreshRequested(ctx, org.UUID())
	return invoiceRows("list refresh requested", rows, err)
}

func (r invoiceRepo) RequestRefreshByMetric(ctx context.Context, org core.OrganizationID, metric core.BillableMetricID) (int, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	n, err := q.RequestInvoiceRefreshByMetric(ctx, db.RequestInvoiceRefreshByMetricParams{OrganizationID: org.UUID(), BillableMetricID: metric.UUID()})
	if err != nil {
		return 0, fmt.Errorf("request refresh of drafts charging metric %s: %w", metric, mapErr(err))
	}
	return int(n), nil
}

func (r invoiceRepo) ListOverdueCandidates(ctx context.Context, org core.OrganizationID, date core.Date) ([]*invoice.Invoice, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListInvoiceOverdueCandidates(ctx, db.ListInvoiceOverdueCandidatesParams{OrganizationID: org.UUID(), Date: date.Midnight(time.UTC)})
	return invoiceRows("list overdue candidates", rows, err)
}

func (r invoiceRepo) NextSequentialID(ctx context.Context, org core.OrganizationID, customer core.CustomerID, billingEntity core.BillingEntityID) (int64, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	n, err := q.MaxInvoiceSequentialID(ctx, db.MaxInvoiceSequentialIDParams{
		OrganizationID: org.UUID(), CustomerID: customer.UUID(), BillingEntityID: billingEntity.UUID(),
	})
	if err != nil {
		return 0, fmt.Errorf("next invoice sequential id: %w", mapErr(err))
	}
	return n + 1, nil
}

func (r invoiceRepo) NextBillingEntitySequentialID(ctx context.Context, org core.OrganizationID, billingEntity core.BillingEntityID) (int64, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	n, err := q.MaxInvoiceBillingEntitySequentialID(ctx, db.MaxInvoiceBillingEntitySequentialIDParams{
		OrganizationID: org.UUID(), BillingEntityID: billingEntity.UUID(),
	})
	if err != nil {
		return 0, fmt.Errorf("next billing entity sequential id: %w", mapErr(err))
	}
	return n + 1, nil
}

func (r invoiceRepo) DeleteDraft(ctx context.Context, org core.OrganizationID, id core.InvoiceID) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	status, err := q.GetInvoiceStatus(ctx, db.GetInvoiceStatusParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err := invoiceStatusErr("invoice", id, status, err); err != nil {
		return err
	}
	if err := r.t.trackInvoice(ctx, q, org, id); err != nil {
		return err
	}
	// The draft's own rows cascade; a row of another aggregate still
	// pointing at it (a payment, a wallet transaction) refuses the delete.
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.DeleteDraftInvoice(ctx, db.DeleteDraftInvoiceParams{OrganizationID: org.UUID(), ID: id.UUID()})
		return err
	})
	return updateErr("invoice", id, n, err)
}

// invoiceSubscriptionRepo implements invoice.SubscriptionRepository.
type invoiceSubscriptionRepo struct{ t *txState }

var _ invoice.SubscriptionRepository = invoiceSubscriptionRepo{}

func invoiceSubscriptionOf(r db.InvoiceSubscription) (*invoice.Subscription, error) {
	return &invoice.Subscription{
		ID:                 idOf[core.InvoiceSubscriptionID](r.ID),
		OrganizationID:     idOf[core.OrganizationID](r.OrganizationID),
		InvoiceID:          idOf[core.InvoiceID](r.InvoiceID),
		SubscriptionID:     idOf[core.SubscriptionID](r.SubscriptionID),
		Reason:             invoice.InvoicingReason(r.InvoicingReason),
		Period:             period.Period{From: utc(r.FromDatetime), To: utc(r.ToDatetime)},
		ChargesPeriod:      period.Period{From: utc(r.ChargesFromDatetime), To: utc(r.ChargesToDatetime)},
		FixedChargesPeriod: period.Period{From: utc(r.FixedChargesFromDatetime), To: utc(r.FixedChargesToDatetime)},
		Timestamp:          utc(r.BillingTimestamp),
		Recurring:          r.Recurring,
		CreatedAt:          utc(r.CreatedAt),

		RegeneratedInvoiceID: optIDOf[core.InvoiceID](r.RegeneratedInvoiceID),
	}, nil
}

func (r invoiceSubscriptionRepo) Create(ctx context.Context, s *invoice.Subscription) error {
	q, err := r.t.queries(ctx, s.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateInvoiceSubscription(ctx, db.CreateInvoiceSubscriptionParams{
		OrganizationID:           s.OrganizationID.UUID(),
		ID:                       s.ID.UUID(),
		InvoiceID:                s.InvoiceID.UUID(),
		SubscriptionID:           s.SubscriptionID.UUID(),
		InvoicingReason:          string(s.Reason),
		FromDatetime:             utc(s.Period.From),
		ToDatetime:               utc(s.Period.To),
		ChargesFromDatetime:      utc(s.ChargesPeriod.From),
		ChargesToDatetime:        utc(s.ChargesPeriod.To),
		FixedChargesFromDatetime: utc(s.FixedChargesPeriod.From),
		FixedChargesToDatetime:   utc(s.FixedChargesPeriod.To),
		BillingTimestamp:         utc(s.Timestamp),
		Recurring:                s.Recurring,
		RegeneratedInvoiceID:     optUUID(s.RegeneratedInvoiceID),
		CreatedAt:                utc(s.CreatedAt),
	})
	return insertErr("invoice_subscription", n, err)
}

// invoiceSubscriptionRows converts the rows of a list query.
func invoiceSubscriptionRows(rows []db.InvoiceSubscription, err error) ([]*invoice.Subscription, error) {
	if err != nil {
		return nil, fmt.Errorf("list invoice subscriptions: %w", mapErr(err))
	}
	return convAll(rows, invoiceSubscriptionOf)
}

func (r invoiceSubscriptionRepo) ListByInvoice(ctx context.Context, org core.OrganizationID, inv core.InvoiceID) ([]*invoice.Subscription, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	return invoiceSubscriptionRows(q.ListInvoiceSubscriptionsByInvoice(ctx, db.ListInvoiceSubscriptionsByInvoiceParams{
		OrganizationID: org.UUID(), InvoiceID: inv.UUID(),
	}))
}

func (r invoiceSubscriptionRepo) ListBySubscription(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID) ([]*invoice.Subscription, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	return invoiceSubscriptionRows(q.ListInvoiceSubscriptionsBySubscription(ctx, db.ListInvoiceSubscriptionsBySubscriptionParams{
		OrganizationID: org.UUID(), SubscriptionID: sub.UUID(),
	}))
}

func (r invoiceSubscriptionRepo) Find(ctx context.Context, org core.OrganizationID, k invoice.IdempotencyKey) (*invoice.Subscription, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	var row db.InvoiceSubscription
	switch k.Kind {
	case invoice.KeyCharges:
		row, err = q.FindInvoiceSubscriptionByCharges(ctx, db.FindInvoiceSubscriptionByChargesParams{
			OrganizationID: org.UUID(), SubscriptionID: k.SubscriptionID.UUID(),
			FromDatetime: utc(k.Period.From), ToDatetime: utc(k.Period.To),
		})
	case invoice.KeyFixedCharges:
		row, err = q.FindInvoiceSubscriptionByFixedCharges(ctx, db.FindInvoiceSubscriptionByFixedChargesParams{
			OrganizationID: org.UUID(), SubscriptionID: k.SubscriptionID.UUID(),
			FromDatetime: utc(k.Period.From), ToDatetime: utc(k.Period.To),
		})
	case invoice.KeyStarting, invoice.KeyTerminating:
		reason := invoice.InvoicingReasonSubscriptionStarting
		if k.Kind == invoice.KeyTerminating {
			reason = invoice.InvoicingReasonSubscriptionTerminating
		}
		row, err = q.FindInvoiceSubscriptionByReason(ctx, db.FindInvoiceSubscriptionByReasonParams{
			OrganizationID: org.UUID(), SubscriptionID: k.SubscriptionID.UUID(), InvoicingReason: string(reason),
		})
	default:
		return nil, fmt.Errorf("find invoice subscription: %w: key kind %d", core.ErrInvalidValue, k.Kind)
	}
	if err != nil {
		return nil, getErr("invoice_subscription", fmt.Sprintf("%s/%d", k.SubscriptionID, k.Kind), err)
	}
	return invoiceSubscriptionOf(row)
}

func (r invoiceSubscriptionRepo) Last(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID) (*invoice.Subscription, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.LastInvoiceSubscription(ctx, db.LastInvoiceSubscriptionParams{OrganizationID: org.UUID(), SubscriptionID: sub.UUID()})
	if err != nil {
		return nil, getErr("invoice_subscription", sub, err)
	}
	return invoiceSubscriptionOf(row)
}

func (r invoiceSubscriptionRepo) DeleteByInvoice(ctx context.Context, org core.OrganizationID, inv core.InvoiceID) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	if err := r.t.trackInvoice(ctx, q, org, inv); err != nil {
		return err
	}
	if err := q.DeleteInvoiceSubscriptionsByInvoice(ctx, db.DeleteInvoiceSubscriptionsByInvoiceParams{
		OrganizationID: org.UUID(), InvoiceID: inv.UUID(),
	}); err != nil {
		return fmt.Errorf("delete invoice subscriptions: %w", mapErr(err))
	}
	return nil
}

func (r invoiceSubscriptionRepo) MarkRegenerated(ctx context.Context, org core.OrganizationID, inv, regenerated core.InvoiceID) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	if err := q.MarkInvoiceSubscriptionsRegenerated(ctx, db.MarkInvoiceSubscriptionsRegeneratedParams{
		OrganizationID: org.UUID(), InvoiceID: inv.UUID(), RegeneratedInvoiceID: optUUID(regenerated),
	}); err != nil {
		return fmt.Errorf("mark invoice subscriptions regenerated: %w", mapErr(err))
	}
	return nil
}

// feeRepo implements invoice.FeeRepository.
type feeRepo struct{ t *txState }

var _ invoice.FeeRepository = feeRepo{}

// feeAppliedTaxJSON is the stored form of one invoice.FeeAppliedTax in the
// applied_taxes array of a fee.
type feeAppliedTaxJSON struct {
	ID             uuid.UUID     `json:"id"`
	TaxID          *uuid.UUID    `json:"tax_id,omitempty"`
	TaxCode        string        `json:"tax_code"`
	TaxName        string        `json:"tax_name"`
	TaxDescription *string       `json:"tax_description,omitempty"`
	TaxRate        money.Decimal `json:"tax_rate"`
	Amount         int64         `json:"amount"`
	PreciseAmount  money.Decimal `json:"precise_amount"`
}

// feePresentationBreakdownJSON is an element of the stored
// presentation_breakdowns array of a fee.
type feePresentationBreakdownJSON struct {
	PresentationBy map[string]*string `json:"presentation_by"`
	Units          money.Decimal      `json:"units"`
}

// feePricingUnitUsageJSON is the stored form of invoice.PricingUnitUsage.
type feePricingUnitUsageJSON struct {
	PricingUnitID     uuid.UUID     `json:"pricing_unit_id"`
	ShortName         string        `json:"short_name"`
	ConversionRate    money.Decimal `json:"conversion_rate"`
	Amount            money.Decimal `json:"amount"`
	PreciseAmount     money.Decimal `json:"precise_amount"`
	UnitAmount        money.Decimal `json:"unit_amount"`
	PreciseUnitAmount money.Decimal `json:"precise_unit_amount"`
}

func feeParams(f *invoice.Fee) (db.CreateFeeParams, error) {
	taxes := make([]feeAppliedTaxJSON, len(f.AppliedTaxes))
	for i, t := range f.AppliedTaxes {
		taxes[i] = feeAppliedTaxJSON{
			ID: t.ID.UUID(), TaxID: optUUID(t.TaxID), TaxCode: t.TaxCode, TaxName: t.TaxName,
			TaxDescription: t.TaxDescription, TaxRate: t.TaxRate, Amount: t.Amount, PreciseAmount: t.PreciseAmount,
		}
	}
	taxesJSON, err := encodeJSON(taxes)
	if err != nil {
		return db.CreateFeeParams{}, fmt.Errorf("fee %s: applied taxes: %w", f.ID, err)
	}
	breakdowns := make([]feePresentationBreakdownJSON, len(f.PresentationBreakdowns))
	for i, b := range f.PresentationBreakdowns {
		breakdowns[i] = feePresentationBreakdownJSON{PresentationBy: b.PresentationBy, Units: b.Units}
		if breakdowns[i].PresentationBy == nil {
			breakdowns[i].PresentationBy = map[string]*string{}
		}
	}
	breakdownsJSON, err := encodeJSON(breakdowns)
	if err != nil {
		return db.CreateFeeParams{}, fmt.Errorf("fee %s: presentation breakdowns: %w", f.ID, err)
	}
	var unitUsage []byte
	if u := f.PricingUnitUsage; u != nil {
		if unitUsage, err = encodeJSON(feePricingUnitUsageJSON{
			PricingUnitID: u.PricingUnitID.UUID(), ShortName: u.ShortName, ConversionRate: u.ConversionRate,
			Amount: u.Amount, PreciseAmount: u.PreciseAmount, UnitAmount: u.UnitAmount, PreciseUnitAmount: u.PreciseUnitAmount,
		}); err != nil {
			return db.CreateFeeParams{}, fmt.Errorf("fee %s: pricing unit usage: %w", f.ID, err)
		}
	}
	p := db.CreateFeeParams{
		OrganizationID:                 f.OrganizationID.UUID(),
		ID:                             f.ID.UUID(),
		InvoiceID:                      optUUID(f.InvoiceID),
		SubscriptionID:                 optUUID(f.SubscriptionID),
		FeeType:                        string(f.Type),
		ChargeID:                       optUUID(f.ChargeID),
		ChargeFilterID:                 optUUID(f.ChargeFilterID),
		FixedChargeID:                  optUUID(f.FixedChargeID),
		AddOnID:                        optUUID(f.AddOnID),
		CommitmentID:                   optUUID(f.CommitmentID),
		WalletTransactionID:            optUUID(f.WalletTransactionID),
		SubscriptionRateCardID:         optUUID(f.SubscriptionRateCardID),
		ProductID:                      optUUID(f.ProductID),
		ProductFilterID:                optUUID(f.ProductFilterID),
		RateCardRateID:                 optUUID(f.RateCardRateID),
		RatePhaseID:                    optUUID(f.RatePhaseID),
		GroupedBy:                      invoiceMapJSON(f.GroupedBy),
		InvoiceDisplayName:             f.InvoiceDisplayName,
		Description:                    f.Description,
		Amount:                         f.Amount,
		UnitAmount:                     f.UnitAmount,
		EventsCount:                    f.EventsCount,
		AmountDetails:                  rawJSON(f.AmountDetails),
		TaxesAmount:                    f.TaxesAmount,
		Currency:                       f.Currency.Code(),
		PayInAdvance:                   f.PayInAdvance,
		Invoiceable:                    f.Invoiceable,
		PaymentStatus:                  optString(string(f.PaymentStatus)),
		FromDatetime:                   utc(f.Properties.From),
		ToDatetime:                     utc(f.Properties.To),
		ChargesFromDatetime:            utc(f.Properties.ChargesFrom),
		ChargesToDatetime:              utc(f.Properties.ChargesTo),
		BillingTimestamp:               utc(f.Properties.Timestamp),
		ChargesDurationDays:            int32(f.Properties.ChargesDurationDays), // a period length in days
		FixedChargesFromDatetime:       utc(f.Properties.FixedChargesFrom),
		FixedChargesToDatetime:         utc(f.Properties.FixedChargesTo),
		FixedChargesDurationDays:       int32(f.Properties.FixedChargesDurationDays), // a period length in days
		TrueUpParentFeeID:              optUUID(f.TrueUpParentFeeID),
		OriginalFeeID:                  optUUID(f.OriginalFeeID),
		PricingUnitUsage:               unitUsage,
		AppliedTaxes:                   taxesJSON,
		PresentationBreakdowns:         breakdownsJSON,
		PayInAdvanceEventID:            optUUID(f.PayInAdvanceEventID),
		PayInAdvanceEventTransactionID: optString(f.PayInAdvanceEventTransactionID),
		SucceededAt:                    optUTC(f.SucceededAt),
		FailedAt:                       optUTC(f.FailedAt),
		RefundedAt:                     optUTC(f.RefundedAt),
		CreatedAt:                      utc(f.CreatedAt),
		UpdatedAt:                      utc(f.UpdatedAt),
	}
	p.PreciseAmount = f.PreciseAmount
	p.PreciseUnitAmount = f.PreciseUnitAmount
	p.Units = f.Units
	p.TotalAggregatedUnits = f.TotalAggregatedUnits
	p.TaxesPreciseAmount = f.TaxesPreciseAmount
	p.TaxesRate = f.TaxesRate
	p.TaxesBaseRate = f.TaxesBaseRate
	p.PreciseCouponsAmount = f.PreciseCouponsAmount
	p.PreciseCreditNotesAmount = f.PreciseCreditNotesAmount
	return p, nil
}

func feeOf(r db.Fee) (*invoice.Fee, error) {
	cur, err := mustCurrency(r.Currency)
	if err != nil {
		return nil, fmt.Errorf("fee %s: %w", r.ID, err)
	}
	groupedBy, err := invoiceMapOf(r.GroupedBy)
	if err != nil {
		return nil, fmt.Errorf("fee %s: grouped by: %w", r.ID, err)
	}
	var taxes []feeAppliedTaxJSON
	if err := decodeJSON(r.AppliedTaxes, &taxes); err != nil {
		return nil, fmt.Errorf("fee %s: applied taxes: %w", r.ID, err)
	}
	var breakdowns []feePresentationBreakdownJSON
	if err := decodeJSON(r.PresentationBreakdowns, &breakdowns); err != nil {
		return nil, fmt.Errorf("fee %s: presentation breakdowns: %w", r.ID, err)
	}
	var unitUsage *invoice.PricingUnitUsage
	if len(r.PricingUnitUsage) > 0 && string(r.PricingUnitUsage) != "null" {
		var u feePricingUnitUsageJSON
		if err := decodeJSON(r.PricingUnitUsage, &u); err != nil {
			return nil, fmt.Errorf("fee %s: pricing unit usage: %w", r.ID, err)
		}
		unitUsage = &invoice.PricingUnitUsage{
			PricingUnitID: idOf[core.PricingUnitID](u.PricingUnitID), ShortName: u.ShortName, ConversionRate: u.ConversionRate,
			Amount: u.Amount, PreciseAmount: u.PreciseAmount, UnitAmount: u.UnitAmount, PreciseUnitAmount: u.PreciseUnitAmount,
		}
	}
	f := &invoice.Fee{
		ID:                       idOf[core.FeeID](r.ID),
		OrganizationID:           idOf[core.OrganizationID](r.OrganizationID),
		InvoiceID:                optIDOf[core.InvoiceID](r.InvoiceID),
		SubscriptionID:           optIDOf[core.SubscriptionID](r.SubscriptionID),
		Type:                     core.FeeType(r.FeeType),
		ChargeID:                 optIDOf[core.ChargeID](r.ChargeID),
		ChargeFilterID:           optIDOf[core.ChargeFilterID](r.ChargeFilterID),
		FixedChargeID:            optIDOf[core.FixedChargeID](r.FixedChargeID),
		AddOnID:                  optIDOf[core.AddOnID](r.AddOnID),
		CommitmentID:             optIDOf[core.CommitmentID](r.CommitmentID),
		WalletTransactionID:      optIDOf[core.WalletTransactionID](r.WalletTransactionID),
		SubscriptionRateCardID:   optIDOf[core.SubscriptionRateCardID](r.SubscriptionRateCardID),
		ProductID:                optIDOf[core.ProductID](r.ProductID),
		ProductFilterID:          optIDOf[core.ProductFilterID](r.ProductFilterID),
		RateCardRateID:           optIDOf[core.RateCardRateID](r.RateCardRateID),
		RatePhaseID:              optIDOf[core.RatePhaseID](r.RatePhaseID),
		GroupedBy:                groupedBy,
		InvoiceDisplayName:       r.InvoiceDisplayName,
		Description:              r.Description,
		Amount:                   r.Amount,
		PreciseAmount:            r.PreciseAmount,
		UnitAmount:               r.UnitAmount,
		PreciseUnitAmount:        r.PreciseUnitAmount,
		Units:                    r.Units,
		TotalAggregatedUnits:     r.TotalAggregatedUnits,
		EventsCount:              r.EventsCount,
		AmountDetails:            rawJSON(r.AmountDetails),
		TaxesAmount:              r.TaxesAmount,
		TaxesPreciseAmount:       r.TaxesPreciseAmount,
		TaxesRate:                r.TaxesRate,
		TaxesBaseRate:            r.TaxesBaseRate,
		PreciseCouponsAmount:     r.PreciseCouponsAmount,
		PreciseCreditNotesAmount: r.PreciseCreditNotesAmount,
		Currency:                 cur,
		PayInAdvance:             r.PayInAdvance,
		Invoiceable:              r.Invoiceable,
		PaymentStatus:            invoice.FeePaymentStatus(deref(r.PaymentStatus)),
		Properties: invoice.FeeProperties{
			From:                     utc(r.FromDatetime),
			To:                       utc(r.ToDatetime),
			ChargesFrom:              utc(r.ChargesFromDatetime),
			ChargesTo:                utc(r.ChargesToDatetime),
			Timestamp:                utc(r.BillingTimestamp),
			ChargesDurationDays:      int(r.ChargesDurationDays),
			FixedChargesFrom:         utc(r.FixedChargesFromDatetime),
			FixedChargesTo:           utc(r.FixedChargesToDatetime),
			FixedChargesDurationDays: int(r.FixedChargesDurationDays),
		},
		TrueUpParentFeeID:              optIDOf[core.FeeID](r.TrueUpParentFeeID),
		OriginalFeeID:                  optIDOf[core.FeeID](r.OriginalFeeID),
		PricingUnitUsage:               unitUsage,
		PayInAdvanceEventID:            optIDOf[core.EventID](r.PayInAdvanceEventID),
		PayInAdvanceEventTransactionID: deref(r.PayInAdvanceEventTransactionID),
		SucceededAt:                    optUTC(r.SucceededAt),
		FailedAt:                       optUTC(r.FailedAt),
		RefundedAt:                     optUTC(r.RefundedAt),
		CreatedAt:                      utc(r.CreatedAt),
		UpdatedAt:                      utc(r.UpdatedAt),
		DeletedAt:                      optUTC(r.DeletedAt),
	}
	if len(breakdowns) > 0 {
		f.PresentationBreakdowns = make([]invoice.PresentationBreakdown, len(breakdowns))
		for i, b := range breakdowns {
			f.PresentationBreakdowns[i] = invoice.PresentationBreakdown{PresentationBy: b.PresentationBy, Units: b.Units}
		}
	}
	if len(taxes) > 0 {
		f.AppliedTaxes = make([]invoice.FeeAppliedTax, len(taxes))
		for i, t := range taxes {
			f.AppliedTaxes[i] = invoice.FeeAppliedTax{
				ID: idOf[core.FeeAppliedTaxID](t.ID), TaxID: optIDOf[core.TaxID](t.TaxID), TaxCode: t.TaxCode, TaxName: t.TaxName,
				TaxDescription: t.TaxDescription, TaxRate: t.TaxRate, Amount: t.Amount, PreciseAmount: t.PreciseAmount,
			}
		}
	}
	return f, nil
}

// feeRows converts the rows of a list query.
func feeRows(rows []db.Fee, err error) ([]*invoice.Fee, error) {
	if err != nil {
		return nil, fmt.Errorf("list fees: %w", mapErr(err))
	}
	return convAll(rows, feeOf)
}

func (r feeRepo) Create(ctx context.Context, f *invoice.Fee) error {
	q, err := r.t.queries(ctx, f.OrganizationID)
	if err != nil {
		return err
	}
	p, err := feeParams(f)
	if err != nil {
		return err
	}
	n, err := q.CreateFee(ctx, p)
	return insertErr("fee", n, err)
}

func (r feeRepo) Update(ctx context.Context, f *invoice.Fee) error {
	q, err := r.t.queries(ctx, f.OrganizationID)
	if err != nil {
		return err
	}
	p, err := feeParams(f)
	if err != nil {
		return err
	}
	n, err := q.UpdateFee(ctx, db.UpdateFeeParams(p))
	return updateErr("fee", f.ID, n, err)
}

func (r feeRepo) Get(ctx context.Context, org core.OrganizationID, id core.FeeID) (*invoice.Fee, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetFee(ctx, db.GetFeeParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("fee", id, err)
	}
	return feeOf(row)
}

func (r feeRepo) List(ctx context.Context, org core.OrganizationID, f invoice.FeeFilter, page core.PageRequest) (core.Page[*invoice.Fee], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*invoice.Fee]{}, err
	}
	if err := f.Validate(); err != nil {
		return core.Page[*invoice.Fee]{}, err
	}
	afterAt, after, lim, err := logPageArgs(page)
	if err != nil {
		return core.Page[*invoice.Fee]{}, err
	}
	rows, err := q.ListFees(ctx, db.ListFeesParams{
		OrganizationID:         org.UUID(),
		InvoiceID:              optUUID(f.InvoiceID),
		SubscriptionID:         optUUID(f.SubscriptionID),
		ChargeID:               optUUID(f.ChargeID),
		Types:                  enumStrings(f.Types),
		PaymentStatuses:        enumStrings(f.PaymentStatuses),
		Currency:               currencyCode(f.Currency),
		EventTransactionID:     optString(f.EventTransactionID),
		CustomerID:             optUUID(f.CustomerID),
		CustomerExternalID:     optString(f.CustomerExternalID),
		SubscriptionExternalID: optString(f.SubscriptionExternalID),
		BillableMetricCode:     optString(f.BillableMetricCode),
		CreatedFrom:            optInstant(f.CreatedFrom),
		CreatedTo:              optInstant(f.CreatedTo),
		SucceededFrom:          optInstant(f.SucceededFrom),
		SucceededTo:            optInstant(f.SucceededTo),
		FailedFrom:             optInstant(f.FailedFrom),
		FailedTo:               optInstant(f.FailedTo),
		RefundedFrom:           optInstant(f.RefundedFrom),
		RefundedTo:             optInstant(f.RefundedTo),
		AfterAt:                afterAt,
		After:                  after,
		Lim:                    lim,
	})
	if err != nil {
		return core.Page[*invoice.Fee]{}, fmt.Errorf("list fees: %w", mapErr(err))
	}
	return pageOf(rows, page, feeOf, func(f *invoice.Fee) string { return logToken(f.CreatedAt, f.ID.UUID()) })
}

func (r feeRepo) Discard(ctx context.Context, org core.OrganizationID, id core.FeeID, at time.Time) error {
	f, err := r.Get(ctx, org, id)
	if err != nil {
		return err
	}
	if !f.InvoiceID.IsZero() {
		return fmt.Errorf("fee %s: attached to an invoice: %w", id, core.ErrConflict)
	}
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	n, err := q.DiscardUninvoicedFee(ctx, db.DiscardUninvoicedFeeParams{
		OrganizationID: org.UUID(), ID: id.UUID(), DeletedAt: new(at.UTC()),
	})
	if err != nil {
		return fmt.Errorf("discard fee %s: %w", id, mapErr(err))
	}
	if n == 0 {
		return fmt.Errorf("fee %s: %w", id, core.ErrNotFound)
	}
	return nil
}

// optInstant returns t in UTC, or nil when t is zero.
func optInstant(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func (r feeRepo) ListByInvoice(ctx context.Context, org core.OrganizationID, inv core.InvoiceID) ([]*invoice.Fee, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	u := inv.UUID()
	return feeRows(q.ListFeesByInvoice(ctx, db.ListFeesByInvoiceParams{OrganizationID: org.UUID(), InvoiceID: &u}))
}

func (r feeRepo) ListBySubscriptionPeriod(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID, from time.Time) ([]*invoice.Fee, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	u := sub.UUID()
	return feeRows(q.ListFeesBySubscriptionPeriod(ctx, db.ListFeesBySubscriptionPeriodParams{
		OrganizationID: org.UUID(), SubscriptionID: &u, FromDatetime: utc(from),
	}))
}

func (r feeRepo) ListUnbilledInAdvance(ctx context.Context, org core.OrganizationID, before time.Time) ([]*invoice.Fee, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	return feeRows(q.ListFeesUnbilledInAdvance(ctx, db.ListFeesUnbilledInAdvanceParams{OrganizationID: org.UUID(), Before: utc(before)}))
}

func (r feeRepo) ListByCharge(ctx context.Context, org core.OrganizationID, charge core.ChargeID) ([]*invoice.Fee, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	u := charge.UUID()
	return feeRows(q.ListFeesByCharge(ctx, db.ListFeesByChargeParams{OrganizationID: org.UUID(), ChargeID: &u}))
}

func (r feeRepo) ListByEventTransaction(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID, transactionID string) ([]*invoice.Fee, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	u := sub.UUID()
	return feeRows(q.ListFeesByEventTransaction(ctx, db.ListFeesByEventTransactionParams{
		OrganizationID: org.UUID(), SubscriptionID: &u, TransactionID: &transactionID,
	}))
}

func (r feeRepo) DeleteByInvoice(ctx context.Context, org core.OrganizationID, inv core.InvoiceID) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	if err := r.t.trackInvoice(ctx, q, org, inv); err != nil {
		return err
	}
	u := inv.UUID()
	// A fee still credited by a credit note item violates a foreign key:
	// guard it so that the transaction stays usable.
	if err := r.t.guard(ctx, func() error {
		return q.DeleteFeesByInvoice(ctx, db.DeleteFeesByInvoiceParams{OrganizationID: org.UUID(), InvoiceID: &u})
	}); err != nil {
		return fmt.Errorf("delete fees: %w", err)
	}
	return nil
}

// invoiceAppliedTaxRepo implements invoice.AppliedTaxRepository.
type invoiceAppliedTaxRepo struct{ t *txState }

var _ invoice.AppliedTaxRepository = invoiceAppliedTaxRepo{}

func invoiceAppliedTaxOf(r db.InvoiceAppliedTax) (*invoice.AppliedTax, error) {
	cur, err := mustCurrency(r.Currency)
	if err != nil {
		return nil, fmt.Errorf("invoice applied tax %s: %w", r.ID, err)
	}
	t := &invoice.AppliedTax{
		ID:                idOf[core.InvoiceAppliedTaxID](r.ID),
		OrganizationID:    idOf[core.OrganizationID](r.OrganizationID),
		InvoiceID:         idOf[core.InvoiceID](r.InvoiceID),
		TaxID:             optIDOf[core.TaxID](r.TaxID),
		TaxCode:           r.TaxCode,
		TaxName:           r.TaxName,
		TaxDescription:    r.TaxDescription,
		TaxRate:           r.TaxRate,
		FeesAmount:        r.FeesAmount,
		TaxableBaseAmount: r.TaxableBaseAmount,
		Amount:            r.Amount,
		PreciseAmount:     r.PreciseAmount,
		Currency:          cur,
		CreatedAt:         utc(r.CreatedAt),
	}
	return t, nil
}

func (r invoiceAppliedTaxRepo) ReplaceForInvoice(ctx context.Context, org core.OrganizationID, inv core.InvoiceID, taxes []*invoice.AppliedTax) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	if err := r.t.trackInvoice(ctx, q, org, inv); err != nil {
		return err
	}
	if err := q.DeleteInvoiceAppliedTaxes(ctx, db.DeleteInvoiceAppliedTaxesParams{OrganizationID: org.UUID(), InvoiceID: inv.UUID()}); err != nil {
		return fmt.Errorf("replace invoice applied taxes: %w", mapErr(err))
	}
	for _, t := range taxes {
		p := db.CreateInvoiceAppliedTaxParams{
			OrganizationID:    org.UUID(),
			ID:                t.ID.UUID(),
			InvoiceID:         inv.UUID(),
			TaxID:             optUUID(t.TaxID),
			TaxCode:           t.TaxCode,
			TaxName:           t.TaxName,
			TaxDescription:    t.TaxDescription,
			FeesAmount:        t.FeesAmount,
			TaxableBaseAmount: t.TaxableBaseAmount,
			Amount:            t.Amount,
			Currency:          t.Currency.Code(),
			CreatedAt:         utc(t.CreatedAt),
		}
		p.TaxRate = t.TaxRate
		p.PreciseAmount = t.PreciseAmount
		if err := q.CreateInvoiceAppliedTax(ctx, p); err != nil {
			return fmt.Errorf("replace invoice applied taxes: %w", mapErr(err))
		}
	}
	return nil
}

func (r invoiceAppliedTaxRepo) ListByInvoice(ctx context.Context, org core.OrganizationID, inv core.InvoiceID) ([]*invoice.AppliedTax, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListInvoiceAppliedTaxes(ctx, db.ListInvoiceAppliedTaxesParams{OrganizationID: org.UUID(), InvoiceID: inv.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list invoice applied taxes: %w", mapErr(err))
	}
	return convAll(rows, invoiceAppliedTaxOf)
}

// adjustedFeeRepo implements invoice.AdjustedFeeRepository.
type adjustedFeeRepo struct{ t *txState }

var _ invoice.AdjustedFeeRepository = adjustedFeeRepo{}

func adjustedFeeOf(r db.AdjustedFee) (*invoice.AdjustedFee, error) {
	groupedBy, err := invoiceMapOf(r.GroupedBy)
	if err != nil {
		return nil, fmt.Errorf("adjusted fee %s: grouped by: %w", r.ID, err)
	}
	a := &invoice.AdjustedFee{
		ID:                 idOf[core.AdjustedFeeID](r.ID),
		OrganizationID:     idOf[core.OrganizationID](r.OrganizationID),
		InvoiceID:          idOf[core.InvoiceID](r.InvoiceID),
		FeeID:              optIDOf[core.FeeID](r.FeeID),
		SubscriptionID:     optIDOf[core.SubscriptionID](r.SubscriptionID),
		ChargeID:           optIDOf[core.ChargeID](r.ChargeID),
		ChargeFilterID:     optIDOf[core.ChargeFilterID](r.ChargeFilterID),
		FixedChargeID:      optIDOf[core.FixedChargeID](r.FixedChargeID),
		FeeType:            core.FeeType(r.FeeType),
		GroupedBy:          groupedBy,
		ChargesFrom:        utc(r.ChargesFromDatetime),
		ChargesTo:          utc(r.ChargesToDatetime),
		FixedChargesFrom:   utc(r.FixedChargesFromDatetime),
		FixedChargesTo:     utc(r.FixedChargesToDatetime),
		Mode:               invoice.AdjustmentMode(r.Mode),
		InvoiceDisplayName: r.InvoiceDisplayName,
		Units:              r.Units,
		UnitPreciseAmount:  r.UnitPreciseAmount,
		CreatedAt:          utc(r.CreatedAt),
	}
	return a, nil
}

func (r adjustedFeeRepo) Create(ctx context.Context, a *invoice.AdjustedFee) error {
	q, err := r.t.queries(ctx, a.OrganizationID)
	if err != nil {
		return err
	}
	p := db.CreateAdjustedFeeParams{
		OrganizationID:           a.OrganizationID.UUID(),
		ID:                       a.ID.UUID(),
		InvoiceID:                a.InvoiceID.UUID(),
		FeeID:                    optUUID(a.FeeID),
		SubscriptionID:           optUUID(a.SubscriptionID),
		ChargeID:                 optUUID(a.ChargeID),
		ChargeFilterID:           optUUID(a.ChargeFilterID),
		FixedChargeID:            optUUID(a.FixedChargeID),
		FeeType:                  string(a.FeeType),
		GroupedBy:                invoiceMapJSON(a.GroupedBy),
		ChargesFromDatetime:      utc(a.ChargesFrom),
		ChargesToDatetime:        utc(a.ChargesTo),
		FixedChargesFromDatetime: utc(a.FixedChargesFrom),
		FixedChargesToDatetime:   utc(a.FixedChargesTo),
		Mode:                     string(a.Mode),
		InvoiceDisplayName:       a.InvoiceDisplayName,
		CreatedAt:                utc(a.CreatedAt),
	}
	p.Units = a.Units
	p.UnitPreciseAmount = a.UnitPreciseAmount
	n, err := q.CreateAdjustedFee(ctx, p)
	return insertErr("adjusted_fee", n, err)
}

func (r adjustedFeeRepo) ListByInvoice(ctx context.Context, org core.OrganizationID, inv core.InvoiceID) ([]*invoice.AdjustedFee, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListAdjustedFeesByInvoice(ctx, db.ListAdjustedFeesByInvoiceParams{OrganizationID: org.UUID(), InvoiceID: inv.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list adjusted fees: %w", mapErr(err))
	}
	return convAll(rows, adjustedFeeOf)
}

func (r adjustedFeeRepo) Delete(ctx context.Context, org core.OrganizationID, id core.AdjustedFeeID) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	n, err := q.DeleteAdjustedFee(ctx, db.DeleteAdjustedFeeParams{OrganizationID: org.UUID(), ID: id.UUID()})
	return updateErr("adjusted_fee", id, n, err)
}

func (r adjustedFeeRepo) DeleteByInvoice(ctx context.Context, org core.OrganizationID, inv core.InvoiceID) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	if err := q.DeleteAdjustedFeesByInvoice(ctx, db.DeleteAdjustedFeesByInvoiceParams{OrganizationID: org.UUID(), InvoiceID: inv.UUID()}); err != nil {
		return fmt.Errorf("delete adjusted fees: %w", mapErr(err))
	}
	return nil
}

// invoiceSettlementRepo implements invoice.SettlementRepository.
type invoiceSettlementRepo struct{ t *txState }

var _ invoice.SettlementRepository = invoiceSettlementRepo{}

func invoiceSettlementOf(r db.InvoiceSettlement) (*invoice.Settlement, error) {
	cur, err := mustCurrency(r.Currency)
	if err != nil {
		return nil, fmt.Errorf("invoice settlement %s: %w", r.ID, err)
	}
	return &invoice.Settlement{
		ID:             idOf[core.InvoiceSettlementID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		InvoiceID:      idOf[core.InvoiceID](r.InvoiceID),
		Type:           invoice.SettlementType(r.SettlementType),
		PaymentID:      optIDOf[core.PaymentID](r.PaymentID),
		CreditNoteID:   optIDOf[core.CreditNoteID](r.CreditNoteID),
		Amount:         r.Amount,
		Currency:       cur,
		CreatedAt:      utc(r.CreatedAt),
	}, nil
}

func (r invoiceSettlementRepo) Create(ctx context.Context, s *invoice.Settlement) error {
	q, err := r.t.queries(ctx, s.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateInvoiceSettlement(ctx, db.CreateInvoiceSettlementParams{
		OrganizationID: s.OrganizationID.UUID(),
		ID:             s.ID.UUID(),
		InvoiceID:      s.InvoiceID.UUID(),
		SettlementType: string(s.Type),
		PaymentID:      optUUID(s.PaymentID),
		CreditNoteID:   optUUID(s.CreditNoteID),
		Amount:         s.Amount,
		Currency:       s.Currency.Code(),
		CreatedAt:      utc(s.CreatedAt),
	})
	return insertErr("invoice_settlement", n, err)
}

func (r invoiceSettlementRepo) ListByInvoice(ctx context.Context, org core.OrganizationID, inv core.InvoiceID) ([]*invoice.Settlement, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListInvoiceSettlementsByInvoice(ctx, db.ListInvoiceSettlementsByInvoiceParams{OrganizationID: org.UUID(), InvoiceID: inv.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list invoice settlements: %w", mapErr(err))
	}
	return convAll(rows, invoiceSettlementOf)
}

// invoiceErrorDetailRepo implements invoice.ErrorDetailRepository.
type invoiceErrorDetailRepo struct{ t *txState }

var _ invoice.ErrorDetailRepository = invoiceErrorDetailRepo{}

func invoiceErrorDetailOf(r db.InvoiceErrorDetail) (*invoice.ErrorDetail, error) {
	return &invoice.ErrorDetail{
		ID:             idOf[core.ErrorDetailID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		InvoiceID:      idOf[core.InvoiceID](r.InvoiceID),
		Code:           invoice.ErrorCode(r.ErrorCode),
		Details:        rawJSON(r.Details),
		CreatedAt:      utc(r.CreatedAt),
	}, nil
}

func (r invoiceErrorDetailRepo) Create(ctx context.Context, e *invoice.ErrorDetail) error {
	q, err := r.t.queries(ctx, e.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateInvoiceErrorDetail(ctx, db.CreateInvoiceErrorDetailParams{
		OrganizationID: e.OrganizationID.UUID(),
		ID:             e.ID.UUID(),
		InvoiceID:      e.InvoiceID.UUID(),
		ErrorCode:      string(e.Code),
		Details:        rawJSON(e.Details),
		CreatedAt:      utc(e.CreatedAt),
	})
	return insertErr("error_detail", n, err)
}

func (r invoiceErrorDetailRepo) ListByInvoice(ctx context.Context, org core.OrganizationID, inv core.InvoiceID) ([]*invoice.ErrorDetail, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListInvoiceErrorDetailsByInvoice(ctx, db.ListInvoiceErrorDetailsByInvoiceParams{OrganizationID: org.UUID(), InvoiceID: inv.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list error details: %w", mapErr(err))
	}
	return convAll(rows, invoiceErrorDetailOf)
}

func (r invoiceErrorDetailRepo) DeleteByInvoice(ctx context.Context, org core.OrganizationID, inv core.InvoiceID, code invoice.ErrorCode) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	if err := q.DeleteInvoiceErrorDetailsByInvoice(ctx, db.DeleteInvoiceErrorDetailsByInvoiceParams{
		OrganizationID: org.UUID(), InvoiceID: inv.UUID(), ErrorCode: string(code),
	}); err != nil {
		return fmt.Errorf("delete error details: %w", mapErr(err))
	}
	return nil
}

// invoiceCreditRepo implements invoice.CreditRepository.
type invoiceCreditRepo struct{ t *txState }

var _ invoice.CreditRepository = invoiceCreditRepo{}

func invoiceCreditOf(r db.InvoiceCredit) (*invoice.Credit, error) {
	cur, err := mustCurrency(r.Currency)
	if err != nil {
		return nil, fmt.Errorf("invoice credit %s: %w", r.ID, err)
	}
	return &invoice.Credit{
		ID:                          idOf[core.InvoiceCreditID](r.ID),
		OrganizationID:              idOf[core.OrganizationID](r.OrganizationID),
		InvoiceID:                   idOf[core.InvoiceID](r.InvoiceID),
		AppliedCouponID:             optIDOf[core.AppliedCouponID](r.AppliedCouponID),
		CreditNoteID:                optIDOf[core.CreditNoteID](r.CreditNoteID),
		ProgressiveBillingInvoiceID: optIDOf[core.InvoiceID](r.ProgressiveBillingInvoiceID),
		Amount:                      r.Amount,
		Currency:                    cur,
		BeforeTaxes:                 r.BeforeTaxes,
		CreatedAt:                   utc(r.CreatedAt),
	}, nil
}

func (r invoiceCreditRepo) Create(ctx context.Context, c *invoice.Credit) error {
	q, err := r.t.queries(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateInvoiceCredit(ctx, db.CreateInvoiceCreditParams{
		OrganizationID:              c.OrganizationID.UUID(),
		ID:                          c.ID.UUID(),
		InvoiceID:                   c.InvoiceID.UUID(),
		AppliedCouponID:             optUUID(c.AppliedCouponID),
		CreditNoteID:                optUUID(c.CreditNoteID),
		ProgressiveBillingInvoiceID: optUUID(c.ProgressiveBillingInvoiceID),
		Amount:                      c.Amount,
		Currency:                    c.Currency.Code(),
		BeforeTaxes:                 c.BeforeTaxes,
		CreatedAt:                   utc(c.CreatedAt),
	})
	return insertErr("credit", n, err)
}

// creditRows converts the rows of a list query.
func creditRows(rows []db.InvoiceCredit, err error) ([]*invoice.Credit, error) {
	if err != nil {
		return nil, fmt.Errorf("list invoice credits: %w", mapErr(err))
	}
	return convAll(rows, invoiceCreditOf)
}

func (r invoiceCreditRepo) ListByInvoice(ctx context.Context, org core.OrganizationID, inv core.InvoiceID) ([]*invoice.Credit, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	return creditRows(q.ListInvoiceCreditsByInvoice(ctx, db.ListInvoiceCreditsByInvoiceParams{OrganizationID: org.UUID(), InvoiceID: inv.UUID()}))
}

func (r invoiceCreditRepo) ListByAppliedCoupon(ctx context.Context, org core.OrganizationID, applied core.AppliedCouponID) ([]*invoice.Credit, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	id := applied.UUID()
	return creditRows(q.ListInvoiceCreditsByAppliedCoupon(ctx, db.ListInvoiceCreditsByAppliedCouponParams{OrganizationID: org.UUID(), AppliedCouponID: &id}))
}

func (r invoiceCreditRepo) ListByCreditNote(ctx context.Context, org core.OrganizationID, cn core.CreditNoteID) ([]*invoice.Credit, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	id := cn.UUID()
	return creditRows(q.ListInvoiceCreditsByCreditNote(ctx, db.ListInvoiceCreditsByCreditNoteParams{OrganizationID: org.UUID(), CreditNoteID: &id}))
}

func (r invoiceCreditRepo) ListByProgressiveBillingInvoice(ctx context.Context, org core.OrganizationID, inv core.InvoiceID) ([]*invoice.Credit, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	id := inv.UUID()
	return creditRows(q.ListInvoiceCreditsByProgressiveBillingInvoice(ctx, db.ListInvoiceCreditsByProgressiveBillingInvoiceParams{
		OrganizationID: org.UUID(), ProgressiveBillingInvoiceID: &id,
	}))
}

func (r invoiceCreditRepo) DeleteByInvoice(ctx context.Context, org core.OrganizationID, inv core.InvoiceID) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	if err := q.DeleteInvoiceCreditsByInvoice(ctx, db.DeleteInvoiceCreditsByInvoiceParams{OrganizationID: org.UUID(), InvoiceID: inv.UUID()}); err != nil {
		return fmt.Errorf("delete invoice credits: %w", mapErr(err))
	}
	return nil
}
