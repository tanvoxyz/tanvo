package rpc

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/billing/exports"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/export"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/money"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
	exportv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/export/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/export/v1/exportv1connect"
	invoicev1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/invoice/v1"
)

// Security log of data exports (lago DataExports::CreateService
// register_security_log).
const (
	securityLogExport   = "export"
	eventExportCreated  = "export.created"
	exportMemberMessage = "data exports are requested by a member"
)

// DataExports implements DataExportService over libs/billing/exports
// (lago Mutations::DataExports); exports are read through the emailed
// link only, as v1.55.0 has no API reading them.
type DataExports struct {
	exportv1connect.UnimplementedDataExportServiceHandler

	svc *exports.Service
	log auth.SecurityLogger
}

// NewDataExports returns the handler; log records the export.created
// security log, nil records none.
func NewDataExports(svc *exports.Service, log auth.SecurityLogger) *DataExports {
	return &DataExports{svc: svc, log: log}
}

// ExportFilters are the filter parsers of the exports: the invoices and
// credit notes list filters of the export mutations.
func ExportFilters() exports.Filters {
	return exports.Filters{Invoices: exportInvoiceFilter, CreditNotes: exportCreditNoteFilter}
}

var dataExportStatuses = NewEnum(map[export.Status]exportv1.DataExportStatus{
	export.StatusPending:    exportv1.DataExportStatus_DATA_EXPORT_STATUS_PENDING,
	export.StatusProcessing: exportv1.DataExportStatus_DATA_EXPORT_STATUS_PROCESSING,
	export.StatusCompleted:  exportv1.DataExportStatus_DATA_EXPORT_STATUS_COMPLETED,
	export.StatusFailed:     exportv1.DataExportStatus_DATA_EXPORT_STATUS_FAILED,
})

// ExportInvoices implements DataExportService.ExportInvoices (lago
// CreateInvoicesDataExport with resource_type invoices).
func (s *DataExports) ExportInvoices(ctx context.Context, req *exportv1.ExportInvoicesRequest) (*exportv1.ExportInvoicesResponse, error) {
	e, err := s.create(ctx, export.ResourceInvoices, req.GetFilter(), req.GetSearch())
	if err != nil {
		return nil, err
	}
	return exportv1.ExportInvoicesResponse_builder{DataExport: dataExportProto(e)}.Build(), nil
}

// ExportInvoiceFees implements DataExportService.ExportInvoiceFees (lago
// CreateInvoicesDataExport with resource_type invoice_fees).
func (s *DataExports) ExportInvoiceFees(ctx context.Context, req *exportv1.ExportInvoiceFeesRequest) (*exportv1.ExportInvoiceFeesResponse, error) {
	e, err := s.create(ctx, export.ResourceInvoiceFees, req.GetFilter(), req.GetSearch())
	if err != nil {
		return nil, err
	}
	return exportv1.ExportInvoiceFeesResponse_builder{DataExport: dataExportProto(e)}.Build(), nil
}

// ExportCreditNotes implements DataExportService.ExportCreditNotes (lago
// CreateCreditNotesDataExport with resource_type credit_notes).
func (s *DataExports) ExportCreditNotes(ctx context.Context, req *exportv1.ExportCreditNotesRequest) (*exportv1.ExportCreditNotesResponse, error) {
	e, err := s.create(ctx, export.ResourceCreditNotes, req.GetFilter(), req.GetSearch())
	if err != nil {
		return nil, err
	}
	return exportv1.ExportCreditNotesResponse_builder{DataExport: dataExportProto(e)}.Build(), nil
}

// ExportCreditNoteItems implements DataExportService.ExportCreditNoteItems
// (lago CreateCreditNotesDataExport with resource_type credit_note_items).
func (s *DataExports) ExportCreditNoteItems(ctx context.Context, req *exportv1.ExportCreditNoteItemsRequest) (*exportv1.ExportCreditNoteItemsResponse, error) {
	e, err := s.create(ctx, export.ResourceCreditNoteItems, req.GetFilter(), req.GetSearch())
	if err != nil {
		return nil, err
	}
	return exportv1.ExportCreditNoteItemsResponse_builder{DataExport: dataExportProto(e)}.Build(), nil
}

// create starts an export for the calling member and records the
// export.created security log.
func (s *DataExports) create(ctx context.Context, resource export.Resource, filter, search string) (*export.DataExport, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	var member core.MembershipID
	if p.Membership != nil {
		member = p.Membership.ID
	}
	if member.IsZero() {
		return nil, Error(ctx, core.NewRuleError(exports.CodeMembershipRequired, exportMemberMessage))
	}
	var record func(context.Context, *export.DataExport) error
	if s.log != nil {
		var user core.UserID
		if p.User != nil {
			user = p.User.ID
		}
		// Recorded in the export's transaction: the nested InTx of the
		// logger joins it.
		record = func(ctx context.Context, _ *export.DataExport) error {
			return s.log.Record(ctx, auth.SecurityEvent{
				OrganizationID: p.OrganizationID, UserID: user, Type: securityLogExport, Event: eventExportCreated,
				Resources: map[string]any{
					"export_type":    string(resource),
					"resource_query": map[string]any{"filter": filter, "search": search},
				},
			})
		}
	}
	q := exports.Query{Filter: filter, Search: search}
	e, err := s.svc.Create(ctx, p.OrganizationID, member, resource, q, record)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return e, nil
}

// dataExportProto converts an export: its ID and status, the fields of
// lago's DataExport type (types/data_exports/object.rb).
func dataExportProto(e *export.DataExport) *exportv1.DataExport {
	return exportv1.DataExport_builder{
		Id:     new(e.ID.String()),
		Status: new(dataExportStatuses.Proto(e.Status)),
	}.Build()
}

// exportInvoiceFields are the fields of the invoices export filter (lago
// Types::DataExports::Invoices::FiltersInput); issuing_date and amount
// also take >= and <=.
var exportInvoiceFields = []string{
	"status", "payment_status", "invoice_type", "currency", "billing_entity_id", "customer_external_id",
	"payment_overdue", "payment_dispute_lost", "self_billed", "purchase_order_number", "issuing_date", "amount",
}

// exportInvoiceFilter parses the filter of ExportInvoices and
// ExportInvoiceFees; the search is applied by the export.
func exportInvoiceFilter(q exports.Query) (invoice.Filter, error) {
	var f invoice.Filter
	e, err := ParseFilter(q.Filter)
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
		if err := exportInvoiceRange(&f, r); err != nil {
			return f, err
		}
	}
	eq, err := Equalities(rest, exportInvoiceFields...)
	if err != nil {
		return f, err
	}
	for _, field := range []string{"issuing_date", "amount"} {
		if _, ok := eq[field]; ok {
			return f, filterError(fmt.Sprintf("field %q is compared with =, >= or <=", field))
		}
	}
	statuses, err := FilterEnum[invoicev1.InvoiceStatus]("status", eq["status"], invoicev1.InvoiceStatus(0).Descriptor())
	if err != nil {
		return f, err
	}
	for _, v := range statuses {
		st := invStatuses.Core(v)
		if !slices.Contains(invoice.ListedStatuses(), st) {
			return f, filterError(fmt.Sprintf("field \"status\": %s invoices are not listed", st))
		}
		f.Statuses = append(f.Statuses, st)
	}
	pays, err := FilterEnum[commonv1.PaymentStatus]("payment_status", eq["payment_status"], commonv1.PaymentStatus(0).Descriptor())
	if err != nil {
		return f, err
	}
	for _, v := range pays {
		f.PaymentStatuses = append(f.PaymentStatuses, payStatuses.Core(v))
	}
	types, err := FilterEnum[invoicev1.InvoiceType]("invoice_type", eq["invoice_type"], invoicev1.InvoiceType(0).Descriptor())
	if err != nil {
		return f, err
	}
	for _, v := range types {
		f.Types = append(f.Types, invTypes.Core(v))
	}
	if v, err := FilterSingle("currency", eq["currency"]); err != nil {
		return f, err
	} else if v != "" {
		if f.Currency, err = money.LookupCurrency(strings.ToUpper(v)); err != nil {
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
	if f.CustomerExternalID, err = FilterSingle("customer_external_id", eq["customer_external_id"]); err != nil {
		return f, err
	}
	if f.PurchaseOrderNumber, err = FilterSingle("purchase_order_number", eq["purchase_order_number"]); err != nil {
		return f, err
	}
	if f.PaymentOverdue, err = FilterBool("payment_overdue", eq["payment_overdue"]); err != nil {
		return f, err
	}
	if f.PaymentDisputeLost, err = FilterBool("payment_dispute_lost", eq["payment_dispute_lost"]); err != nil {
		return f, err
	}
	if f.SelfBilled, err = FilterBool("self_billed", eq["self_billed"]); err != nil {
		return f, err
	}
	return f, nil
}

// exportInvoiceRange applies an issuing_date (YYYY-MM-DD) or amount (minor
// units) restriction with =, >= or <= (lago issuing_date_from/to and
// amount_from/to).
func exportInvoiceRange(f *invoice.Filter, r Restriction) error {
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
	n, err := strconv.ParseInt(r.Value, 10, 64)
	if err != nil {
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

// exportCreditNoteFilter parses the filter of ExportCreditNotes and
// ExportCreditNoteItems: the ListCreditNotes filter plus customer_id (an ID
// or `external_id:<id>`) and customer_external_id (lago
// Types::DataExports::CreditNotes::FiltersInput).
func exportCreditNoteFilter(q exports.Query) (creditnote.Filter, error) {
	e, err := ParseFilter(q.Filter)
	if err != nil {
		return creditnote.Filter{}, err
	}
	terms := []FilterExpr{}
	switch t := e.(type) {
	case nil:
	case FilterAnd:
		terms = t
	default:
		terms = []FilterExpr{t}
	}
	var (
		rest     []string
		customer = map[string]string{}
	)
	for _, t := range terms {
		r, ok := t.(Restriction)
		if ok && (r.Field == "customer_id" || r.Field == "customer_external_id") {
			if r.Op != CmpEq {
				return creditnote.Filter{}, filterError(fmt.Sprintf("field %q is compared with =", r.Field))
			}
			if _, dup := customer[r.Field]; dup {
				return creditnote.Filter{}, filterError(fmt.Sprintf("field %q is restricted twice", r.Field))
			}
			customer[r.Field] = r.Value
			continue
		}
		s, err := filterText(t)
		if err != nil {
			return creditnote.Filter{}, err
		}
		rest = append(rest, s)
	}
	f, err := cnListFilter(strings.Join(rest, " AND "), billingEntityIDs)
	if err != nil {
		return f, err
	}
	f.CustomerExternalID = customer["customer_external_id"]
	if v, ok := customer["customer_id"]; ok {
		if ext, ok := strings.CutPrefix(v, "external_id:"); ok {
			if f.CustomerExternalID != "" && f.CustomerExternalID != ext {
				return f, filterError("customer_id and customer_external_id name different customers")
			}
			f.CustomerExternalID = ext
		} else if f.CustomerID, err = core.ParseID[core.CustomerID](v); err != nil {
			return f, filterError(fmt.Sprintf("field \"customer_id\": invalid ID %q", v))
		}
	}
	return f, nil
}

// filterText renders a parsed filter back to AIP-160 text.
func filterText(e FilterExpr) (string, error) {
	switch t := e.(type) {
	case Restriction:
		v := t.Value
		if t.Quoted || strings.ContainsAny(v, " \t()\"'") || v == "" {
			if strings.Contains(v, `"`) {
				if strings.Contains(v, "'") {
					return "", filterError("a value holds both quote characters")
				}
				v = "'" + v + "'"
			} else {
				v = `"` + v + `"`
			}
		}
		return t.Field + " " + string(t.Op) + " " + v, nil
	case FilterNot:
		s, err := filterText(t.X)
		return "NOT (" + s + ")", err
	case FilterOr, FilterAnd:
		var (
			parts []FilterExpr
			sep   string
		)
		if or, ok := t.(FilterOr); ok {
			parts, sep = or, " OR "
		} else {
			parts, sep = t.(FilterAnd), " AND "
		}
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			s, err := filterText(p)
			if err != nil {
				return "", err
			}
			out = append(out, s)
		}
		if sep == " OR " {
			return strings.Join(out, sep), nil
		}
		return "(" + strings.Join(out, sep) + ")", nil
	}
	return "", filterError("unsupported filter")
}
