package rpc

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/money"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
	invoicev1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/invoice/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/invoice/v1/invoicev1connect"
)

// Invoices serves tanvo.invoice.v1.InvoiceService over the billing
// library's invoicing service (lago Api::V1::InvoicesController and the
// invoice mutations), with the payments service for updates.
// ResendInvoice stays unimplemented: no mailer is wired. RetryInvoiceSync
// and SetInvoiceSalesforceId need the integrations service
// ([Invoices.WithIntegrations]).
type Invoices struct {
	invoicev1connect.UnimplementedInvoiceServiceHandler

	svc *invoicing.Service
	pay *payments.Service
	// integ syncs the invoices with the integrations; nil leaves the sync
	// RPCs Unimplemented.
	integ *integrations.Service
}

var _ invoicev1connect.InvoiceServiceHandler = (*Invoices)(nil)

// NewInvoices returns the service over svc and pay.
func NewInvoices(svc *invoicing.Service, pay *payments.Service) *Invoices {
	return &Invoices{svc: svc, pay: pay}
}

// invFilterFields are the equality fields of the ListInvoices filter.
var invFilterFields = []string{
	"status", "payment_status", "invoice_type", "currency", "billing_entity_id",
	"payment_overdue", "payment_dispute_lost", "self_billed", "partially_paid",
	"purchase_order_number", "settlement",
}

// CreateInvoice implements InvoiceService.CreateInvoice: a one-off invoice
// of add-ons (lago Invoices::CreateOneOffService). unit_amount is in
// currency units, at most to the currency's minor unit.
func (s *Invoices) CreateInvoice(ctx context.Context, req *invoicev1.CreateInvoiceRequest) (*invoicev1.CreateInvoiceResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	in := invoicing.OneOffRequest{
		OrganizationID:            p.OrganizationID,
		SkipAutomaticPayment:      req.GetSkipAutomaticPayment(),
		SkipInvoiceCustomSections: req.GetSkipInvoiceCustomSections(),
	}
	if req.HasPurchaseOrderNumber() {
		in.PurchaseOrderNumber = new(req.GetPurchaseOrderNumber())
	}
	c, err := ParseRef("customer_id", req.GetCustomerId())
	if err != nil {
		return nil, err
	}
	switch {
	case c.Code != "":
		return nil, InvalidArgument("invalid reference", []FieldViolation{{
			Field: "customer_id", Reason: core.CodeInvalid, Description: "a customer has no code",
		}})
	case c.ExternalID != "":
		in.CustomerExternalID = c.ExternalID
	default:
		if in.CustomerID, err = ParseID[core.CustomerID]("customer_id", c.ID.String()); err != nil {
			return nil, err
		}
	}
	if v := req.GetCurrency(); v != "" {
		if in.Currency, err = money.LookupCurrency(v); err != nil {
			return nil, InvalidArgument("invalid currency", []FieldViolation{{Field: "currency", Reason: core.CodeInvalidCurrency}})
		}
	}
	if req.HasBillingEntityId() {
		if in.BillingEntity, err = CatalogRef("billing_entity_id", req.GetBillingEntityId()); err != nil {
			return nil, err
		}
	}
	if req.HasPaymentMethodType() {
		in.PaymentMethodType = invPaymentMethodTypes.Core(req.GetPaymentMethodType())
	}
	if in.PaymentMethodID, err = ParseID[core.PaymentMethodID]("payment_method_id", req.GetPaymentMethodId()); err != nil {
		return nil, err
	}
	if in.InvoiceCustomSections, err = CatalogRefs("invoice_custom_section_ids", req.GetInvoiceCustomSectionIds()); err != nil {
		return nil, err
	}
	for i, b := range req.GetFees() {
		field := fmt.Sprintf("fees[%d]", i)
		f := invoicing.OneOffRequestFee{}
		if f.AddOn, err = CatalogRef(field+".add_on_id", b.GetAddOnId()); err != nil {
			return nil, err
		}
		if b.HasUnits() {
			d, err := ParseDecimal(field+".units", b.GetUnits())
			if err != nil {
				return nil, err
			}
			f.Units = &d
		}
		if b.HasUnitAmount() {
			d, err := ParseDecimal(field+".unit_amount", b.GetUnitAmount())
			if err != nil {
				return nil, err
			}
			f.UnitAmount = &d
		}
		if b.HasInvoiceDisplayName() {
			f.InvoiceDisplayName = new(b.GetInvoiceDisplayName())
		}
		if b.HasDescription() {
			f.Description = new(b.GetDescription())
		}
		if b.HasPeriod() {
			pr := b.GetPeriod()
			if !pr.HasFrom() || !pr.HasTo() {
				return nil, InvalidArgument("invalid period", []FieldViolation{{Field: field + ".period", Reason: core.CodeInvalid}})
			}
			from, to := pr.GetFrom().AsTime(), pr.GetTo().AsTime()
			f.From, f.To = &from, &to
		}
		if f.Taxes, err = CatalogRefs(field+".tax_ids", b.GetTaxIds()); err != nil {
			return nil, err
		}
		in.Fees = append(in.Fees, f)
	}
	v, err := s.svc.CreateOneOffRequest(ctx, in)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return invoicev1.CreateInvoiceResponse_builder{Invoice: invoiceProto(v)}.Build(), nil
}

// GetInvoice implements InvoiceService.GetInvoice (lago
// InvoicesController#show): a visible invoice with its fees.
func (s *Invoices) GetInvoice(ctx context.Context, req *invoicev1.GetInvoiceRequest) (*invoicev1.GetInvoiceResponse, error) {
	p, id, err := s.target(ctx, req.GetInvoiceId())
	if err != nil {
		return nil, err
	}
	v, err := s.svc.View(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return invoicev1.GetInvoiceResponse_builder{Invoice: invoiceProto(v)}.Build(), nil
}

// ListInvoices implements InvoiceService.ListInvoices (lago InvoicesQuery
// through InvoiceIndex): the visible invoices, latest issued first, with
// their subscriptions and without fees.
func (s *Invoices) ListInvoices(ctx context.Context, req *invoicev1.ListInvoicesRequest) (*invoicev1.ListInvoicesResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	f, err := invListFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	f.Order = core.OrderRecent
	f.Search = req.GetSearch()
	if v := req.GetCustomerId(); v != "" {
		c, err := ParseRef("customer_id", v)
		if err != nil {
			return nil, err
		}
		switch {
		case c.Code != "":
			return nil, InvalidArgument("invalid reference", []FieldViolation{{
				Field: "customer_id", Reason: core.CodeInvalid, Description: "a customer has no code",
			}})
		case c.ExternalID != "":
			f.CustomerExternalID = c.ExternalID
		default:
			if f.CustomerID, err = ParseID[core.CustomerID]("customer_id", c.ID.String()); err != nil {
				return nil, err
			}
		}
	}
	page, err := s.svc.ListViews(ctx, p.OrganizationID, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return invoicev1.ListInvoicesResponse_builder{
		Invoices:      Items(page, invoiceProto),
		NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// UpdateInvoice implements InvoiceService.UpdateInvoice: the payment
// status and metadata (lago Invoices::UpdateService).
func (s *Invoices) UpdateInvoice(ctx context.Context, req *invoicev1.UpdateInvoiceRequest) (*invoicev1.UpdateInvoiceResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetInvoice()
	paths, err := UpdatePaths(req.GetUpdateMask(), []string{"payment_status", "metadata"}, func(path string) bool {
		switch path {
		case "payment_status":
			return b.HasPaymentStatus()
		case "metadata":
			return len(b.GetMetadata()) > 0
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.InvoiceID]("invoice.id", b.GetId())
	if err != nil {
		return nil, err
	}
	var in payments.InvoiceUpdate
	if slices.Contains(paths, "payment_status") {
		st := invPaymentStatuses.Core(b.GetPaymentStatus())
		if st == "" {
			return nil, InvalidArgument("invalid invoice", []FieldViolation{{Field: "invoice.payment_status", Reason: core.CodeInvalid}})
		}
		in.PaymentStatus = &st
	}
	if slices.Contains(paths, "metadata") {
		md := FromMetadata(b.GetMetadata())
		in.Metadata = &md
	}
	if in.PaymentStatus != nil || in.Metadata != nil {
		if _, err := s.pay.UpdateInvoice(ctx, p.OrganizationID, id, in); err != nil {
			return nil, invError(ctx, err)
		}
	}
	v, err := s.svc.View(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return invoicev1.UpdateInvoiceResponse_builder{Invoice: invoiceProto(v)}.Build(), nil
}

// DeleteInvoice implements InvoiceService.DeleteInvoice: a draft (lago
// Invoices::DeleteService).
func (s *Invoices) DeleteInvoice(ctx context.Context, req *invoicev1.DeleteInvoiceRequest) (*invoicev1.DeleteInvoiceResponse, error) {
	p, id, err := s.target(ctx, req.GetInvoiceId())
	if err != nil {
		return nil, err
	}
	if err := s.svc.Delete(ctx, p.OrganizationID, id); err != nil {
		return nil, Error(ctx, err)
	}
	return &invoicev1.DeleteInvoiceResponse{}, nil
}

// RefreshInvoice implements InvoiceService.RefreshInvoice (lago
// Invoices::RefreshDraftService).
func (s *Invoices) RefreshInvoice(ctx context.Context, req *invoicev1.RefreshInvoiceRequest) (*invoicev1.RefreshInvoiceResponse, error) {
	p, id, err := s.target(ctx, req.GetInvoiceId())
	if err != nil {
		return nil, err
	}
	v, err := s.svc.Refresh(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return invoicev1.RefreshInvoiceResponse_builder{Invoice: invoiceProto(v)}.Build(), nil
}

// FinalizeInvoice implements InvoiceService.FinalizeInvoice: a draft
// (lago Invoices::RefreshDraftAndFinalizeService).
func (s *Invoices) FinalizeInvoice(ctx context.Context, req *invoicev1.FinalizeInvoiceRequest) (*invoicev1.FinalizeInvoiceResponse, error) {
	p, id, err := s.target(ctx, req.GetInvoiceId())
	if err != nil {
		return nil, err
	}
	v, err := s.svc.Finalize(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return invoicev1.FinalizeInvoiceResponse_builder{Invoice: invoiceProto(v)}.Build(), nil
}

// FinalizeAllInvoices implements InvoiceService.FinalizeAllInvoices: the
// finalization of every draft is queued (lago FinalizeAllInvoices
// mutation, Invoices::FinalizeBatchService#call_async).
func (s *Invoices) FinalizeAllInvoices(ctx context.Context, _ *invoicev1.FinalizeAllInvoicesRequest) (*invoicev1.FinalizeAllInvoicesResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	n, err := s.svc.FinalizeAll(ctx, p.OrganizationID)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return invoicev1.FinalizeAllInvoicesResponse_builder{QueuedCount: new(int32(min(n, 1<<31-1)))}.Build(), nil
}

// VoidInvoice implements InvoiceService.VoidInvoice (lago
// Invoices::VoidService): credit_amount and refund_amount are in the
// invoice's currency units.
func (s *Invoices) VoidInvoice(ctx context.Context, req *invoicev1.VoidInvoiceRequest) (*invoicev1.VoidInvoiceResponse, error) {
	p, id, err := s.target(ctx, req.GetInvoiceId())
	if err != nil {
		return nil, err
	}
	in := invoicing.VoidInput{GenerateCreditNote: req.GetGenerateCreditNote()}
	if req.HasCreditAmount() || req.HasRefundAmount() {
		cur, err := s.svc.View(ctx, p.OrganizationID, id)
		if err != nil {
			return nil, Error(ctx, err)
		}
		c := cur.Invoice.Currency
		credit, err := walletMinor("credit_amount", req.HasCreditAmount(), req.GetCreditAmount(), c)
		if err != nil {
			return nil, err
		}
		refund, err := walletMinor("refund_amount", req.HasRefundAmount(), req.GetRefundAmount(), c)
		if err != nil {
			return nil, err
		}
		if credit != nil {
			in.CreditAmount = *credit
		}
		if refund != nil {
			in.RefundAmount = *refund
		}
	}
	v, err := s.svc.VoidView(ctx, p.OrganizationID, id, in)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return invoicev1.VoidInvoiceResponse_builder{Invoice: invoiceProto(v)}.Build(), nil
}

// RegenerateInvoice implements InvoiceService.RegenerateInvoice (lago
// regenerateFromVoided mutation, Invoices::RegenerateFromVoidedService):
// only the fees listed are regenerated; unit_amount is in currency units.
// validate_only previews the regeneration of the invoice as it stands
// (lago Invoices::BuildRegenerationPreviewService), ignoring fees and
// purchase_order_number. fixed_charge_id, add_on_id and description are
// accepted and ignored, as lago ignores them.
func (s *Invoices) RegenerateInvoice(ctx context.Context, req *invoicev1.RegenerateInvoiceRequest) (*invoicev1.RegenerateInvoiceResponse, error) {
	p, id, err := s.target(ctx, req.GetInvoiceId())
	if err != nil {
		return nil, err
	}
	if req.GetValidateOnly() {
		v, err := s.svc.RegenerationPreview(ctx, p.OrganizationID, id)
		if err != nil {
			return nil, Error(ctx, err)
		}
		return invoicev1.RegenerateInvoiceResponse_builder{Invoice: invoiceProto(v)}.Build(), nil
	}
	var in invoicing.RegenerateInput
	if req.HasPurchaseOrderNumber() {
		in.PurchaseOrderNumber = new(req.GetPurchaseOrderNumber())
	}
	for i, b := range req.GetFees() {
		field := fmt.Sprintf("fees[%d]", i)
		var f invoicing.RegeneratedFee
		if f.FeeID, err = ParseID[core.FeeID](field+".fee_id", b.GetFeeId()); err != nil {
			return nil, err
		}
		if f.SubscriptionID, err = ParseID[core.SubscriptionID](field+".subscription_id", b.GetSubscriptionId()); err != nil {
			return nil, err
		}
		if f.ChargeID, err = ParseID[core.ChargeID](field+".charge_id", b.GetChargeId()); err != nil {
			return nil, err
		}
		if f.ChargeFilterID, err = ParseID[core.ChargeFilterID](field+".charge_filter_id", b.GetChargeFilterId()); err != nil {
			return nil, err
		}
		if b.HasUnits() {
			d, err := ParseDecimal(field+".units", b.GetUnits())
			if err != nil {
				return nil, err
			}
			f.Units = &d
		}
		if b.HasUnitAmount() {
			d, err := ParseDecimal(field+".unit_amount", b.GetUnitAmount())
			if err != nil {
				return nil, err
			}
			f.UnitAmount = &d
		}
		if b.HasInvoiceDisplayName() {
			f.InvoiceDisplayName = new(b.GetInvoiceDisplayName())
		}
		in.Fees = append(in.Fees, f)
	}
	v, err := s.svc.RegenerateView(ctx, p.OrganizationID, id, in)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return invoicev1.RegenerateInvoiceResponse_builder{Invoice: invoiceProto(v)}.Build(), nil
}

// RetryInvoice implements InvoiceService.RetryInvoice: a failed invoice
// is reopened with its taxes pending and their pull is queued (lago
// Invoices::RetryService).
func (s *Invoices) RetryInvoice(ctx context.Context, req *invoicev1.RetryInvoiceRequest) (*invoicev1.RetryInvoiceResponse, error) {
	p, id, err := s.target(ctx, req.GetInvoiceId())
	if err != nil {
		return nil, err
	}
	v, err := s.svc.RetryView(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return invoicev1.RetryInvoiceResponse_builder{Invoice: invoiceProto(v)}.Build(), nil
}

// RetryAllInvoices implements InvoiceService.RetryAllInvoices: the retry
// of every failed invoice is queued (lago RetryAllInvoices mutation,
// Invoices::RetryBatchService#call_async).
func (s *Invoices) RetryAllInvoices(ctx context.Context, _ *invoicev1.RetryAllInvoicesRequest) (*invoicev1.RetryAllInvoicesResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	n, err := s.svc.RetryAll(ctx, p.OrganizationID)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return invoicev1.RetryAllInvoicesResponse_builder{QueuedCount: new(int32(min(n, 1<<31-1)))}.Build(), nil
}

// RetryAllInvoicePayments implements
// InvoiceService.RetryAllInvoicePayments: the payment retry of every
// finalized invoice with a pending or failed payment is queued (lago
// RetryAllInvoicePayments mutation,
// Invoices::Payments::RetryBatchService#call_async).
func (s *Invoices) RetryAllInvoicePayments(ctx context.Context, _ *invoicev1.RetryAllInvoicePaymentsRequest) (*invoicev1.RetryAllInvoicePaymentsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	n, err := s.svc.RetryAllPayments(ctx, p.OrganizationID)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return invoicev1.RetryAllInvoicePaymentsResponse_builder{QueuedCount: new(int32(min(n, 1<<31-1)))}.Build(), nil
}

// RetryInvoicePayment implements InvoiceService.RetryInvoicePayment (lago
// Invoices::Payments::RetryService), with the payment method params
// overriding the invoice's payment method.
func (s *Invoices) RetryInvoicePayment(ctx context.Context, req *invoicev1.RetryInvoicePaymentRequest) (*invoicev1.RetryInvoicePaymentResponse, error) {
	p, id, err := s.target(ctx, req.GetInvoiceId())
	if err != nil {
		return nil, err
	}
	var pm invoicing.PaymentMethodParams
	if req.HasPaymentMethodType() {
		pm.Type = invPaymentMethodTypes.Core(req.GetPaymentMethodType())
	}
	if pm.ID, err = ParseID[core.PaymentMethodID]("payment_method_id", req.GetPaymentMethodId()); err != nil {
		return nil, err
	}
	if err := s.svc.RetryPayment(ctx, p.OrganizationID, id, pm); err != nil {
		return nil, Error(ctx, err)
	}
	return &invoicev1.RetryInvoicePaymentResponse{}, nil
}

// LoseInvoiceDispute implements InvoiceService.LoseInvoiceDispute (lago
// Invoices::LoseDisputeService).
func (s *Invoices) LoseInvoiceDispute(ctx context.Context, req *invoicev1.LoseInvoiceDisputeRequest) (*invoicev1.LoseInvoiceDisputeResponse, error) {
	p, id, err := s.target(ctx, req.GetInvoiceId())
	if err != nil {
		return nil, err
	}
	v, err := s.svc.LoseDispute(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return invoicev1.LoseInvoiceDisputeResponse_builder{Invoice: invoiceProto(v)}.Build(), nil
}

// DownloadInvoice implements InvoiceService.DownloadInvoice: when the
// finalized invoice has no PDF yet its rendering is queued and the
// response is empty, to be called again (lago download_pdf's head :ok). A
// rendered document is returned with its media type; its signed URL needs
// a blob store, which is not wired.
func (s *Invoices) DownloadInvoice(ctx context.Context, req *invoicev1.DownloadInvoiceRequest) (*invoicev1.DownloadInvoiceResponse, error) {
	p, id, err := s.target(ctx, req.GetInvoiceId())
	if err != nil {
		return nil, err
	}
	inv, ready, err := s.svc.Download(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := invoicev1.DownloadInvoiceResponse_builder{}
	if ready {
		out.Pdf = commonv1.Document_builder{ContentType: new("application/pdf")}.Build()
		if !inv.XMLFile.IsZero() {
			out.Xml = commonv1.Document_builder{ContentType: new("application/xml")}.Build()
		}
	}
	return out.Build(), nil
}

// GenerateInvoicePaymentUrl implements
// InvoiceService.GenerateInvoicePaymentUrl (lago
// Invoices::Payments::GeneratePaymentUrlService): the checkout link of the
// invoice's payment intent at its provider connection.
func (s *Invoices) GenerateInvoicePaymentUrl(ctx context.Context, req *invoicev1.GenerateInvoicePaymentUrlRequest) (*invoicev1.GenerateInvoicePaymentUrlResponse, error) {
	p, id, err := s.target(ctx, req.GetInvoiceId())
	if err != nil {
		return nil, err
	}
	u, err := s.svc.PaymentURL(ctx, p.OrganizationID, id)
	if errors.Is(err, invoicing.ErrPaymentURLUnavailable) {
		return nil, connect.NewError(connect.CodeUnimplemented, "payment URLs are not available")
	}
	if err != nil {
		return nil, Error(ctx, err)
	}
	return invoicev1.GenerateInvoicePaymentUrlResponse_builder{PaymentUrl: new(u)}.Build(), nil
}

// target returns the caller and the invoice ID of a request.
func (s *Invoices) target(ctx context.Context, raw string) (auth.Principal, core.InvoiceID, error) {
	p, err := principal(ctx)
	if err != nil {
		return p, core.InvoiceID{}, err
	}
	id, err := ParseID[core.InvoiceID]("invoice_id", raw)
	return p, id, err
}

// invError maps the errors of an update, whose field paths are the
// invoice's, onto the request's invoice message.
func invError(ctx context.Context, err error) error {
	if v, ok := validationViolations("invoice.", err); ok {
		return withReason(InvalidArgument("invalid invoice", v), ReasonValidationErrors)
	}
	return Error(ctx, err)
}

// invListFilter parses the filter of ListInvoices: equalities on
// invFilterFields, ranges on issuing_date (YYYY-MM-DD) and amount (minor
// units), and metadata.<key> equalities.
func invListFilter(s string) (invoice.Filter, error) {
	var f invoice.Filter
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
		switch {
		case ok && (r.Field == "issuing_date" || r.Field == "amount"):
			if err := invRange(&f, r); err != nil {
				return f, err
			}
		case ok && strings.HasPrefix(r.Field, "metadata."):
			key := strings.TrimPrefix(r.Field, "metadata.")
			if r.Op != CmpEq || key == "" {
				return f, filterError("metadata takes `metadata.<key> = value` restrictions")
			}
			if f.Metadata == nil {
				f.Metadata = core.MetadataFilter{}
			}
			if _, dup := f.Metadata[key]; dup {
				return f, filterError(fmt.Sprintf("field %q is restricted twice", r.Field))
			}
			f.Metadata[key] = r.Value
		default:
			rest = append(rest, t)
		}
	}
	eq, err := Equalities(rest, invFilterFields...)
	if err != nil {
		return f, err
	}
	statuses, err := FilterEnum[invoicev1.InvoiceStatus]("status", eq["status"], invoicev1.InvoiceStatus(0).Descriptor())
	if err != nil {
		return f, err
	}
	for _, v := range statuses {
		st := invStatuses.Core(v)
		if !(invoice.Filter{}).Lists(st) {
			return f, filterError(fmt.Sprintf("field \"status\": %s invoices are not listed", st))
		}
		f.Statuses = append(f.Statuses, st)
	}
	pays, err := FilterEnum[commonv1.PaymentStatus]("payment_status", eq["payment_status"], commonv1.PaymentStatus(0).Descriptor())
	if err != nil {
		return f, err
	}
	for _, v := range pays {
		f.PaymentStatuses = append(f.PaymentStatuses, invPaymentStatuses.Core(v))
	}
	types, err := FilterEnum[invoicev1.InvoiceType]("invoice_type", eq["invoice_type"], invoicev1.InvoiceType(0).Descriptor())
	if err != nil {
		return f, err
	}
	for _, v := range types {
		f.Types = append(f.Types, invTypes.Core(v))
	}
	settlements, err := FilterEnum[invoicev1.SettlementType]("settlement", eq["settlement"], invoicev1.SettlementType(0).Descriptor())
	if err != nil {
		return f, err
	}
	for _, v := range settlements {
		f.Settlements = append(f.Settlements, invSettlementTypes.Core(v))
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
	for _, b := range []struct {
		field string
		dst   **bool
	}{
		{"payment_overdue", &f.PaymentOverdue},
		{"payment_dispute_lost", &f.PaymentDisputeLost},
		{"self_billed", &f.SelfBilled},
		{"partially_paid", &f.PartiallyPaid},
	} {
		if *b.dst, err = FilterBool(b.field, eq[b.field]); err != nil {
			return f, err
		}
	}
	if f.PurchaseOrderNumber, err = FilterSingle("purchase_order_number", eq["purchase_order_number"]); err != nil {
		return f, err
	}
	return f, nil
}

// invRange applies an issuing_date (YYYY-MM-DD) or amount (minor units)
// restriction with =, >= or <=.
func invRange(f *invoice.Filter, r Restriction) error {
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
