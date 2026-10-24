package rpc

import (
	"context"
	"fmt"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/money"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
	creditnotev1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/creditnote/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/creditnote/v1/creditnotev1connect"
)

// CreditNotes serves tanvo.creditnote.v1.CreditNoteService over the
// billing library's credit note service (lago
// Api::V1::CreditNotesController). ResendCreditNote stays Unimplemented:
// the server sends no document emails. RetryCreditNoteSync needs the
// integrations service ([CreditNotes.WithIntegrations]).
type CreditNotes struct {
	creditnotev1connect.UnimplementedCreditNoteServiceHandler

	svc *creditnotes.Service
	// integ syncs the credit notes with the integrations; nil leaves
	// RetryCreditNoteSync Unimplemented.
	integ *integrations.Service
}

var _ creditnotev1connect.CreditNoteServiceHandler = (*CreditNotes)(nil)

// NewCreditNotes returns the service over svc.
func NewCreditNotes(svc *creditnotes.Service) *CreditNotes { return &CreditNotes{svc: svc} }

var (
	cnStatuses = NewEnum(map[creditnote.Status]creditnotev1.CreditNoteStatus{
		creditnote.StatusDraft:     creditnotev1.CreditNoteStatus_CREDIT_NOTE_STATUS_DRAFT,
		creditnote.StatusFinalized: creditnotev1.CreditNoteStatus_CREDIT_NOTE_STATUS_FINALIZED,
	})
	cnCreditStatuses = NewEnum(map[creditnote.CreditStatus]creditnotev1.CreditStatus{
		creditnote.CreditStatusAvailable: creditnotev1.CreditStatus_CREDIT_STATUS_AVAILABLE,
		creditnote.CreditStatusConsumed:  creditnotev1.CreditStatus_CREDIT_STATUS_CONSUMED,
		creditnote.CreditStatusVoided:    creditnotev1.CreditStatus_CREDIT_STATUS_VOIDED,
	})
	cnRefundStatuses = NewEnum(map[core.RefundStatus]commonv1.RefundStatus{
		core.RefundStatusPending:   commonv1.RefundStatus_REFUND_STATUS_PENDING,
		core.RefundStatusSucceeded: commonv1.RefundStatus_REFUND_STATUS_SUCCEEDED,
		core.RefundStatusFailed:    commonv1.RefundStatus_REFUND_STATUS_FAILED,
	})
	cnReasons = NewEnum(map[creditnote.Reason]creditnotev1.CreditNoteReason{
		creditnote.ReasonDuplicatedCharge:      creditnotev1.CreditNoteReason_CREDIT_NOTE_REASON_DUPLICATED_CHARGE,
		creditnote.ReasonProductUnsatisfactory: creditnotev1.CreditNoteReason_CREDIT_NOTE_REASON_PRODUCT_UNSATISFACTORY,
		creditnote.ReasonOrderChange:           creditnotev1.CreditNoteReason_CREDIT_NOTE_REASON_ORDER_CHANGE,
		creditnote.ReasonOrderCancellation:     creditnotev1.CreditNoteReason_CREDIT_NOTE_REASON_ORDER_CANCELLATION,
		creditnote.ReasonFraudulentCharge:      creditnotev1.CreditNoteReason_CREDIT_NOTE_REASON_FRAUDULENT_CHARGE,
		creditnote.ReasonOther:                 creditnotev1.CreditNoteReason_CREDIT_NOTE_REASON_OTHER,
	})
)

// cnFilterFields are the fields ListCreditNotes filters on; issuing_date
// and amount also take >= and <=.
var cnFilterFields = []string{
	"credit_status", "refund_status", "reason", "currency", "billing_entity_id", "self_billed",
	"invoice_number", "purchase_order_number", "type", "issuing_date", "amount",
}

// cnUpdateFields are the update_mask paths of UpdateCreditNote.
var cnUpdateFields = []string{"refund_status", "metadata"}

// CreateCreditNote implements CreditNoteService.CreateCreditNote (lago
// CreditNotes::CreateService) on a visible invoice. Item amounts and the
// credit, refund and offset amounts are in the invoice's currency.
func (s *CreditNotes) CreateCreditNote(ctx context.Context, req *creditnotev1.CreateCreditNoteRequest) (*creditnotev1.CreateCreditNoteResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetCreditNote()
	invID, err := ParseID[core.InvoiceID]("credit_note.invoice_id", b.GetInvoiceId())
	if err != nil {
		return nil, err
	}
	c, err := s.svc.InvoiceCurrency(ctx, p.OrganizationID, invID)
	if err != nil {
		return nil, Error(ctx, err)
	}
	items, err := cnItems("credit_note.items", b.GetItems(), c)
	if err != nil {
		return nil, err
	}
	in := creditnotes.CreateInput{
		OrganizationID: p.OrganizationID,
		InvoiceID:      invID,
		Items:          items,
		Reason:         cnReasons.Core(b.GetReason()),
		Description:    Opt(b.HasDescription(), b.GetDescription()),
		Metadata:       FromMetadata(b.GetMetadata()),
	}
	for _, a := range []struct {
		field string
		m     *commonv1.Money
		dst   *int64
	}{
		{"credit_note.credit_amount", b.GetCreditAmount(), &in.CreditAmount},
		{"credit_note.refund_amount", b.GetRefundAmount(), &in.RefundAmount},
		{"credit_note.offset_amount", b.GetOffsetAmount(), &in.OffsetAmount},
	} {
		if *a.dst, err = cnAmount(a.field, a.m, c); err != nil {
			return nil, err
		}
	}
	cn, err := s.svc.Create(ctx, in)
	if err != nil {
		return nil, cnError(ctx, err)
	}
	return creditnotev1.CreateCreditNoteResponse_builder{CreditNote: creditNote(cn)}.Build(), nil
}

// EstimateCreditNote implements CreditNoteService.EstimateCreditNote (lago
// CreditNotes::EstimateService): nothing is stored.
func (s *CreditNotes) EstimateCreditNote(ctx context.Context, req *creditnotev1.EstimateCreditNoteRequest) (*creditnotev1.EstimateCreditNoteResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	invID, err := ParseID[core.InvoiceID]("invoice_id", req.GetInvoiceId())
	if err != nil {
		return nil, err
	}
	c, err := s.svc.InvoiceCurrency(ctx, p.OrganizationID, invID)
	if err != nil {
		return nil, Error(ctx, err)
	}
	items, err := cnItems("items", req.GetItems(), c)
	if err != nil {
		return nil, err
	}
	est, err := s.svc.EstimateMaximums(ctx, p.OrganizationID, invID, items)
	if err != nil {
		if v, ok := validationViolations("", err); ok {
			return nil, withReason(InvalidArgument("invalid credit note", v), ReasonValidationErrors)
		}
		return nil, Error(ctx, err)
	}
	return creditnotev1.EstimateCreditNoteResponse_builder{
		CreditNote:           creditNote(est.CreditNote),
		MaxCreditableAmount:  Money(est.MaxCreditable, c),
		MaxRefundableAmount:  Money(est.MaxRefundable, c),
		MaxOffsettableAmount: Money(est.MaxOffsettable, c),
	}.Build(), nil
}

// GetCreditNote implements CreditNoteService.GetCreditNote: drafts are not
// found.
func (s *CreditNotes) GetCreditNote(ctx context.Context, req *creditnotev1.GetCreditNoteRequest) (*creditnotev1.GetCreditNoteResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.CreditNoteID]("credit_note_id", req.GetCreditNoteId())
	if err != nil {
		return nil, err
	}
	cn, err := s.svc.Get(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return creditnotev1.GetCreditNoteResponse_builder{CreditNote: creditNote(cn)}.Build(), nil
}

// ListCreditNotes implements CreditNoteService.ListCreditNotes: the
// finalized credit notes, newest first (lago CreditNotesQuery). amount
// compares the total amount in minor units, as lago amount_from and
// amount_to do.
func (s *CreditNotes) ListCreditNotes(ctx context.Context, req *creditnotev1.ListCreditNotesRequest) (*creditnotev1.ListCreditNotesResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	f, err := cnListFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	if c := req.GetCustomerId(); c != "" {
		ref, err := ParseRef("customer_id", c)
		if err != nil {
			return nil, err
		}
		switch {
		case ref.Code != "":
			return nil, InvalidArgument("invalid reference", []FieldViolation{{
				Field: "customer_id", Reason: core.CodeInvalid, Description: "this resource has no code",
			}})
		case ref.ExternalID != "":
			f.CustomerExternalID = ref.ExternalID
		default:
			if f.CustomerID, err = ParseID[core.CustomerID]("customer_id", c); err != nil {
				return nil, err
			}
		}
	}
	f.Search = req.GetSearch()
	page, err := s.svc.List(ctx, p.OrganizationID, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return creditnotev1.ListCreditNotesResponse_builder{
		CreditNotes:   Items(page, creditNote),
		NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// UpdateCreditNote implements CreditNoteService.UpdateCreditNote: the
// refund status and the metadata, merged key by key into the stored
// metadata (lago CreditNotes::UpdateService with partial_metadata).
func (s *CreditNotes) UpdateCreditNote(ctx context.Context, req *creditnotev1.UpdateCreditNoteRequest) (*creditnotev1.UpdateCreditNoteResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetCreditNote()
	paths, err := UpdatePaths(req.GetUpdateMask(), cnUpdateFields, func(path string) bool {
		switch path {
		case "refund_status":
			return b.HasRefundStatus()
		case "metadata":
			return len(b.GetMetadata()) > 0
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.CreditNoteID]("credit_note.id", b.GetId())
	if err != nil {
		return nil, err
	}
	in := creditnotes.UpdateInput{MergeMetadata: true}
	for _, path := range paths {
		switch path {
		case "refund_status":
			st := cnRefundStatuses.Core(b.GetRefundStatus())
			if st == "" {
				return nil, InvalidArgument("invalid credit note", []FieldViolation{{Field: "credit_note.refund_status", Reason: core.CodeInvalid}})
			}
			in.RefundStatus = &st
		case "metadata":
			md := FromMetadata(b.GetMetadata())
			in.Metadata = &md
		}
	}
	cn, err := s.svc.Update(ctx, p.OrganizationID, id, in)
	if err != nil {
		return nil, cnError(ctx, err)
	}
	return creditnotev1.UpdateCreditNoteResponse_builder{CreditNote: creditNote(cn)}.Build(), nil
}

// VoidCreditNote implements CreditNoteService.VoidCreditNote (lago
// CreditNotes::VoidService).
func (s *CreditNotes) VoidCreditNote(ctx context.Context, req *creditnotev1.VoidCreditNoteRequest) (*creditnotev1.VoidCreditNoteResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.CreditNoteID]("credit_note_id", req.GetCreditNoteId())
	if err != nil {
		return nil, err
	}
	cn, err := s.svc.Void(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return creditnotev1.VoidCreditNoteResponse_builder{CreditNote: creditNote(cn)}.Build(), nil
}

// DownloadCreditNote implements CreditNoteService.DownloadCreditNote: when
// the credit note has no document yet it enqueues its rendering and
// returns an empty response, to be called again (lago download_pdf's
// head :ok). A rendered document is returned with its media type; its
// URL is left empty until the server signs blob store URLs.
func (s *CreditNotes) DownloadCreditNote(ctx context.Context, req *creditnotev1.DownloadCreditNoteRequest) (*creditnotev1.DownloadCreditNoteResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.CreditNoteID]("credit_note_id", req.GetCreditNoteId())
	if err != nil {
		return nil, err
	}
	cn, ready, err := s.svc.Download(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := creditnotev1.DownloadCreditNoteResponse_builder{}
	if ready {
		out.Pdf = commonv1.Document_builder{ContentType: new("application/pdf")}.Build()
		if !cn.XMLFile.IsZero() {
			out.Xml = commonv1.Document_builder{ContentType: new("application/xml")}.Build()
		}
	}
	return out.Build(), nil
}

// cnItems converts the items of a request, whose amounts are in major
// units of c, to service items in minor units.
func cnItems(field string, items []*creditnotev1.CreditNoteItem, c money.Currency) ([]creditnotes.ItemInput, error) {
	out := make([]creditnotes.ItemInput, 0, len(items))
	for i, it := range items {
		f := fmt.Sprintf("%s[%d]", field, i)
		fee, err := ParseID[core.FeeID](f+".fee_id", it.GetFeeId())
		if err != nil {
			return nil, err
		}
		d, err := ParseDecimal(f+".amount", it.GetAmount())
		if err != nil {
			return nil, err
		}
		minor := d.Mul(c.SubunitToUnit())
		if !minor.IsInteger() {
			return nil, InvalidArgument("invalid decimal", []FieldViolation{{
				Field: f + ".amount", Reason: core.CodeInvalid,
				Description: fmt.Sprintf("must have at most %d decimals", c.Exponent()),
			}})
		}
		out = append(out, creditnotes.ItemInput{FeeID: fee, Amount: minor})
	}
	return out, nil
}

// cnAmount converts an amount of a request, in currency c, to minor
// units; an absent amount is zero.
func cnAmount(field string, m *commonv1.Money, c money.Currency) (int64, error) {
	if m == nil {
		return 0, nil
	}
	minor, mc, err := FromMoney(field, m)
	if err != nil {
		return 0, err
	}
	if mc != c {
		return 0, InvalidArgument("invalid money", []FieldViolation{{
			Field: field + ".currency", Reason: core.CodeInvalidCurrency,
			Description: "must be the invoice's currency " + c.Code(),
		}})
	}
	return minor, nil
}

// cnListFilter parses the filter of ListCreditNotes: equalities on
// cnFilterFields, and ranges on issuing_date and amount.
func cnListFilter(s string) (creditnote.Filter, error) {
	var f creditnote.Filter
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
		if !ok || (r.Field != "issuing_date" && r.Field != "amount") {
			rest = append(rest, t)
			continue
		}
		if err := cnRange(&f, r); err != nil {
			return f, err
		}
	}
	eq, err := Equalities(rest, cnFilterFields...)
	if err != nil {
		return f, err
	}
	for _, field := range []string{"issuing_date", "amount"} {
		if _, ok := eq[field]; ok {
			return f, filterError(fmt.Sprintf("field %q is compared with =, >= or <=", field))
		}
	}
	credit, err := FilterEnum[creditnotev1.CreditStatus]("credit_status", eq["credit_status"], creditnotev1.CreditStatus(0).Descriptor())
	if err != nil {
		return f, err
	}
	for _, v := range credit {
		f.CreditStatuses = append(f.CreditStatuses, cnCreditStatuses.Core(v))
	}
	refund, err := FilterEnum[commonv1.RefundStatus]("refund_status", eq["refund_status"], commonv1.RefundStatus(0).Descriptor())
	if err != nil {
		return f, err
	}
	for _, v := range refund {
		f.RefundStatuses = append(f.RefundStatuses, cnRefundStatuses.Core(v))
	}
	reasons, err := FilterEnum[creditnotev1.CreditNoteReason]("reason", eq["reason"], creditnotev1.CreditNoteReason(0).Descriptor())
	if err != nil {
		return f, err
	}
	for _, v := range reasons {
		f.Reasons = append(f.Reasons, cnReasons.Core(v))
	}
	for _, v := range eq["type"] {
		t := creditnote.Type(strings.ToLower(v))
		if !t.Valid() {
			return f, filterError(fmt.Sprintf("field \"type\": unknown value %q", v))
		}
		f.Types = append(f.Types, t)
	}
	if v, err := FilterSingle("currency", eq["currency"]); err != nil {
		return f, err
	} else if v != "" {
		if f.Currency, err = money.LookupCurrency(v); err != nil {
			return f, filterError(fmt.Sprintf("field \"currency\": unknown currency %q", v))
		}
	}
	for _, v := range eq["billing_entity_id"] {
		id, err := core.ParseID[core.BillingEntityID](v)
		if err != nil {
			return f, filterError(fmt.Sprintf("field \"billing_entity_id\": invalid ID %q", v))
		}
		f.BillingEntityIDs = append(f.BillingEntityIDs, id)
	}
	if f.SelfBilled, err = FilterBool("self_billed", eq["self_billed"]); err != nil {
		return f, err
	}
	if f.InvoiceNumber, err = FilterSingle("invoice_number", eq["invoice_number"]); err != nil {
		return f, err
	}
	if f.PurchaseOrderNumber, err = FilterSingle("purchase_order_number", eq["purchase_order_number"]); err != nil {
		return f, err
	}
	return f, nil
}

// cnRange applies an issuing_date (YYYY-MM-DD) or amount (minor units)
// restriction with =, >= or <=.
func cnRange(f *creditnote.Filter, r Restriction) error {
	from, to := r.Op == CmpEq || r.Op == CmpGe, r.Op == CmpEq || r.Op == CmpLe
	if !from && !to {
		return filterError(fmt.Sprintf("field %q is compared with =, >= or <=", r.Field))
	}
	if r.Field == "issuing_date" {
		d, err := core.ParseDate(r.Value)
		if err != nil {
			return filterError(fmt.Sprintf("field \"issuing_date\": invalid date %q", r.Value))
		}
		if from {
			f.IssuingDateFrom = d
		}
		if to {
			f.IssuingDateTo = d
		}
		return nil
	}
	d, err := money.Parse(r.Value)
	n, ok := d.Int64()
	if err != nil || !d.IsInteger() || !ok {
		return filterError(fmt.Sprintf("field \"amount\": invalid amount %q, in minor units", r.Value))
	}
	if from {
		f.AmountFrom = &n
	}
	if to {
		f.AmountTo = &n
	}
	return nil
}

// cnError maps the service errors, whose field paths are the credit
// note's, onto the request's credit_note message.
func cnError(ctx context.Context, err error) error {
	if v, ok := validationViolations("credit_note.", err); ok {
		return withReason(InvalidArgument("invalid credit note", v), ReasonValidationErrors)
	}
	return Error(ctx, err)
}

// creditNote converts a credit note to its proto message; its amounts are
// in its currency.
func creditNote(cn *creditnote.CreditNote) *creditnotev1.CreditNote {
	c := cn.Currency
	items := make([]*creditnotev1.CreditNoteItem, len(cn.Items))
	for i, it := range cn.Items {
		items[i] = creditnotev1.CreditNoteItem_builder{
			FeeId:         new(it.FeeID.String()),
			Amount:        new(c.FromMinor(it.Amount).StringFixed(c.Exponent())),
			PreciseAmount: new(Decimal(c.MinorToUnits(it.PreciseAmount))),
		}.Build()
	}
	taxes := make([]*creditnotev1.CreditNoteAppliedTax, len(cn.AppliedTaxes))
	for i, t := range cn.AppliedTaxes {
		taxes[i] = creditnotev1.CreditNoteAppliedTax_builder{
			TaxId:      new(t.TaxID.String()),
			TaxCode:    new(t.TaxCode),
			TaxName:    new(t.TaxName),
			TaxRate:    new(Decimal(t.TaxRate)),
			BaseAmount: Money(t.BaseAmount, c),
			Amount:     Money(t.Amount, c),
		}.Build()
	}
	b := creditnotev1.CreditNote_builder{
		Id:                           new(cn.ID.String()),
		InvoiceId:                    new(cn.InvoiceID.String()),
		CustomerId:                   new(cn.CustomerID.String()),
		Number:                       new(cn.Number),
		Status:                       new(cnStatuses.Proto(cn.Status)),
		Reason:                       new(cnReasons.Proto(cn.Reason)),
		Description:                  cn.Description,
		CreditAmount:                 Money(cn.CreditAmount, c),
		RefundAmount:                 Money(cn.RefundAmount, c),
		OffsetAmount:                 Money(cn.OffsetAmount, c),
		BalanceAmount:                Money(cn.BalanceAmount, c),
		SubTotalExcludingTaxesAmount: Money(cn.SubTotalExcludingTaxesAmount, c),
		CouponsAdjustmentAmount:      Money(cn.CouponsAdjustmentAmount, c),
		TaxesAmount:                  Money(cn.TaxesAmount, c),
		TaxesRate:                    new(Decimal(cn.TaxesRate)),
		TotalAmount:                  Money(cn.TotalAmount, c),
		Items:                        items,
		AppliedTaxes:                 taxes,
		Metadata:                     Metadata(cn.Metadata),
		FileAvailable:                new(!cn.File.IsZero()),
		XmlFileAvailable:             new(!cn.XMLFile.IsZero()),
		RefundedAt:                   TimestampPtr(cn.RefundedAt),
		VoidedAt:                     TimestampPtr(cn.VoidedAt),
		CreatedAt:                    Timestamp(cn.CreatedAt),
		UpdatedAt:                    Timestamp(cn.UpdatedAt),
	}
	if !cn.IssuingDate.IsZero() {
		b.IssuingDate = Date(cn.IssuingDate)
	}
	if cn.CreditStatus != "" {
		b.CreditStatus = new(cnCreditStatuses.Proto(cn.CreditStatus))
	}
	if cn.RefundStatus != "" {
		b.RefundStatus = new(cnRefundStatuses.Proto(cn.RefundStatus))
	}
	for _, e := range cn.ErrorDetails {
		b.Errors = append(b.Errors, creditnotev1.CreditNoteError_builder{
			Code:      new(cnErrorCodes.Proto(e.Code)),
			Message:   new(string(e.Details)),
			CreatedAt: Timestamp(e.CreatedAt),
		}.Build())
	}
	return b.Build()
}

// cnErrorCodes maps the credit note error codes.
var cnErrorCodes = NewEnum(map[creditnote.ErrorCode]creditnotev1.CreditNoteErrorCode{
	creditnote.ErrorCodeTaxError: creditnotev1.CreditNoteErrorCode_CREDIT_NOTE_ERROR_CODE_TAX_ERROR,
})
