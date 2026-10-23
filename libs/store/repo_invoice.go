package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		PaymentMethodType:              optString(string(inv.PaymentMethodType)),
		PaymentMethodID:                optUUID(inv.PaymentMethodID),
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
		PaymentMethodType:              core.PaymentMethodType(deref(r.PaymentMethodType)),
		PaymentMethodID:                optIDOf[core.PaymentMethodID](r.PaymentMethodID),
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

func (r invoiceRepo) List(ctx context.Context, org core.OrganizationID, f invoice.Filter, page core.PageRequest) (core.Page[*invoice.Invoice], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*invoice.Invoice]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*invoice.Invoice]{}, err
	}
	rows, err := q.ListInvoices(ctx, db.ListInvoicesParams{
		OrganizationID:    org.UUID(),
		IncludeGenerating: f.IncludeGenerating,
		CustomerID:        optUUID(f.CustomerID),
		BillingEntityID:   optUUID(f.BillingEntityID),
		Statuses:          enumStrings(f.Statuses),
		PaymentStatuses:   enumStrings(f.PaymentStatuses),
		Types:             enumStrings(f.Types),
		Ids:               uuids(f.IDs),
		After:             after,
		Lim:               lim,
	})
	if err != nil {
		return core.Page[*invoice.Invoice]{}, fmt.Errorf("list invoices: %w", mapErr(err))
	}
	return pageOf(rows, page, invoiceOf, func(inv *invoice.Invoice) string { return inv.ID.String() })
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
	row, err := q.FindInvoiceSubscription(ctx, db.FindInvoiceSubscriptionParams{
		OrganizationID:  org.UUID(),
		SubscriptionID:  k.SubscriptionID.UUID(),
		InvoicingReason: string(k.Reason),
		FromDatetime:    utc(k.PeriodStart),
	})
	if err != nil {
		return nil, getErr("invoice_subscription", k.SubscriptionID.String()+"/"+string(k.Reason), err)
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
	if err := q.DeleteInvoiceSubscriptionsByInvoice(ctx, db.DeleteInvoiceSubscriptionsByInvoiceParams{
		OrganizationID: org.UUID(), InvoiceID: inv.UUID(),
	}); err != nil {
		return fmt.Errorf("delete invoice subscriptions: %w", mapErr(err))
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
		TrueUpParentFeeID:              optUUID(f.TrueUpParentFeeID),
		PricingUnitUsage:               unitUsage,
		AppliedTaxes:                   taxesJSON,
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
			From:                utc(r.FromDatetime),
			To:                  utc(r.ToDatetime),
			ChargesFrom:         utc(r.ChargesFromDatetime),
			ChargesTo:           utc(r.ChargesToDatetime),
			Timestamp:           utc(r.BillingTimestamp),
			ChargesDurationDays: int(r.ChargesDurationDays),
		},
		TrueUpParentFeeID:              optIDOf[core.FeeID](r.TrueUpParentFeeID),
		PricingUnitUsage:               unitUsage,
		PayInAdvanceEventID:            optIDOf[core.EventID](r.PayInAdvanceEventID),
		PayInAdvanceEventTransactionID: deref(r.PayInAdvanceEventTransactionID),
		SucceededAt:                    optUTC(r.SucceededAt),
		FailedAt:                       optUTC(r.FailedAt),
		RefundedAt:                     optUTC(r.RefundedAt),
		CreatedAt:                      utc(r.CreatedAt),
		UpdatedAt:                      utc(r.UpdatedAt),
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
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*invoice.Fee]{}, err
	}
	rows, err := q.ListFees(ctx, db.ListFeesParams{
		OrganizationID: org.UUID(),
		InvoiceID:      optUUID(f.InvoiceID),
		SubscriptionID: optUUID(f.SubscriptionID),
		ChargeID:       optUUID(f.ChargeID),
		Types:          enumStrings(f.Types),
		After:          after,
		Lim:            lim,
	})
	if err != nil {
		return core.Page[*invoice.Fee]{}, fmt.Errorf("list fees: %w", mapErr(err))
	}
	return pageOf(rows, page, feeOf, func(f *invoice.Fee) string { return f.ID.String() })
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
	u := inv.UUID()
	if err := q.DeleteFeesByInvoice(ctx, db.DeleteFeesByInvoiceParams{OrganizationID: org.UUID(), InvoiceID: &u}); err != nil {
		return fmt.Errorf("delete fees: %w", mapErr(err))
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
		ID:             idOf[core.InvoiceAppliedTaxID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		InvoiceID:      idOf[core.InvoiceID](r.InvoiceID),
		TaxID:          optIDOf[core.TaxID](r.TaxID),
		TaxCode:        r.TaxCode,
		TaxName:        r.TaxName,
		TaxDescription: r.TaxDescription,
		TaxRate:        r.TaxRate,
		FeesAmount:     r.FeesAmount,
		Amount:         r.Amount,
		PreciseAmount:  r.PreciseAmount,
		Currency:       cur,
		CreatedAt:      utc(r.CreatedAt),
	}
	return t, nil
}

func (r invoiceAppliedTaxRepo) ReplaceForInvoice(ctx context.Context, org core.OrganizationID, inv core.InvoiceID, taxes []*invoice.AppliedTax) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	if err := q.DeleteInvoiceAppliedTaxes(ctx, db.DeleteInvoiceAppliedTaxesParams{OrganizationID: org.UUID(), InvoiceID: inv.UUID()}); err != nil {
		return fmt.Errorf("replace invoice applied taxes: %w", mapErr(err))
	}
	for _, t := range taxes {
		p := db.CreateInvoiceAppliedTaxParams{
			OrganizationID: org.UUID(),
			ID:             t.ID.UUID(),
			InvoiceID:      inv.UUID(),
			TaxID:          optUUID(t.TaxID),
			TaxCode:        t.TaxCode,
			TaxName:        t.TaxName,
			TaxDescription: t.TaxDescription,
			FeesAmount:     t.FeesAmount,
			Amount:         t.Amount,
			Currency:       t.Currency.Code(),
			CreatedAt:      utc(t.CreatedAt),
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
		OrganizationID:     a.OrganizationID.UUID(),
		ID:                 a.ID.UUID(),
		InvoiceID:          a.InvoiceID.UUID(),
		FeeID:              optUUID(a.FeeID),
		SubscriptionID:     optUUID(a.SubscriptionID),
		ChargeID:           optUUID(a.ChargeID),
		ChargeFilterID:     optUUID(a.ChargeFilterID),
		FixedChargeID:      optUUID(a.FixedChargeID),
		FeeType:            string(a.FeeType),
		GroupedBy:          invoiceMapJSON(a.GroupedBy),
		Mode:               string(a.Mode),
		InvoiceDisplayName: a.InvoiceDisplayName,
		CreatedAt:          utc(a.CreatedAt),
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
