package exports

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/export"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// InvoiceHeaders are the columns of an invoices export (lago
// DataExports::Csv::Invoices.base_headers plus
// progressive_billing_credit_amount_cents, which every organization has).
var InvoiceHeaders = []string{
	"lago_id", "sequential_id", "partner_billing", "issuing_date",
	"customer_lago_id", "customer_external_id", "customer_name", "customer_email", "customer_country",
	"customer_tax_identification_number", "invoice_number", "purchase_order_number", "invoice_type",
	"payment_status", "status", "file_url", "currency", "fees_amount_cents", "coupons_amount_cents",
	"taxes_amount_cents", "credit_notes_amount_cents", "prepaid_credit_amount_cents", "total_amount_cents",
	"payment_due_date", "payment_dispute_lost_at", "payment_overdue", "total_due_amount_cents",
	"total_paid_amount_cents", "total_offsetted_credit_note_amount_cents",
	"progressive_billing_credit_amount_cents",
}

// InvoiceFeeHeaders are the columns of an invoice fees export (lago
// DataExports::Csv::InvoiceFees.base_headers).
var InvoiceFeeHeaders = []string{
	"invoice_lago_id", "invoice_number", "invoice_issuing_date", "fee_lago_id", "fee_item_type",
	"fee_item_code", "fee_item_name", "fee_item_description", "fee_item_invoice_display_name",
	"fee_item_filter_invoice_display_name", "fee_item_grouped_by", "subscription_external_id",
	"subscription_plan_code", "fee_from_date", "fee_to_date", "fee_amount_currency", "fee_units",
	"fee_precise_unit_amount", "fee_taxes_amount_cents", "fee_total_amount_cents",
}

// CreditNoteHeaders are the columns of a credit notes export (lago
// DataExports::Csv::CreditNotes.base_headers).
var CreditNoteHeaders = []string{
	"lago_id", "sequential_id", "partner_billing", "issuing_date",
	"customer_lago_id", "customer_external_id", "customer_name", "customer_email", "customer_country",
	"customer_tax_identification_number", "number", "invoice_number", "purchase_order_number",
	"credit_status", "refund_status", "reason", "description", "currency", "total_amount_cents",
	"taxes_amount_cents", "sub_total_excluding_taxes_amount_cents", "coupons_adjustment_amount_cents",
	"offset_amount_cents", "credit_amount_cents", "balance_amount_cents", "refund_amount_cents", "file_url",
}

// CreditNoteItemHeaders are the columns of a credit note items export
// (lago DataExports::Csv::CreditNoteItems.base_headers).
var CreditNoteItemHeaders = []string{
	"credit_note_lago_id", "credit_note_number", "credit_note_invoice_number", "credit_note_issuing_date",
	"credit_note_item_lago_id", "credit_note_item_fee_lago_id", "credit_note_item_currency",
	"credit_note_item_amount_cents",
}

// billingEntityCodeHeader is the column added to the invoices and credit
// notes exports of an organization with several billing entities.
const billingEntityCodeHeader = "billing_entity_code"

// header returns the header line of e, without the line break.
func (s *Service) header(ctx context.Context, r port.Repos, e *export.DataExport) (string, error) {
	var cols []string
	switch e.ResourceType {
	case export.ResourceInvoices:
		cols = slices.Clone(InvoiceHeaders)
	case export.ResourceInvoiceFees:
		cols = slices.Clone(InvoiceFeeHeaders)
	case export.ResourceCreditNotes:
		cols = slices.Clone(CreditNoteHeaders)
	case export.ResourceCreditNoteItems:
		cols = slices.Clone(CreditNoteItemHeaders)
	default:
		return "", fmt.Errorf("'%s' resource not supported: %w", e.ResourceType, core.ErrInvalidValue)
	}
	if e.ResourceType == export.ResourceInvoices || e.ResourceType == export.ResourceCreditNotes {
		multi, err := multipleBillingEntities(ctx, r, e.OrganizationID)
		if err != nil {
			return "", err
		}
		if multi {
			cols = append(cols, billingEntityCodeHeader)
		}
	}
	return strings.Join(cols, ","), nil
}

// multipleBillingEntities reports whether org has more than one billing
// entity, archived ones included (lago org_has_multiple_billing_entities?).
func multipleBillingEntities(ctx context.Context, r port.Repos, org core.OrganizationID) (bool, error) {
	p, err := r.BillingEntities.List(ctx, org, organization.BillingEntityFilter{IncludeArchived: true}, core.PageRequest{Size: 2})
	if err != nil {
		return false, fmt.Errorf("list billing entities: %w", err)
	}
	return len(p.Items) > 1, nil
}

// render returns the CSV lines of part p of e.
func (s *Service) render(ctx context.Context, r port.Repos, e *export.DataExport, p *export.Part) (string, error) {
	l := &loader{s: s, r: r, org: e.OrganizationID}
	w := &csvWriter{}
	var err error
	switch e.ResourceType {
	case export.ResourceInvoices:
		err = s.renderInvoices(ctx, l, w, p.ObjectIDs)
	case export.ResourceInvoiceFees:
		err = s.renderInvoiceFees(ctx, l, w, p.ObjectIDs)
	case export.ResourceCreditNotes:
		err = s.renderCreditNotes(ctx, l, w, p.ObjectIDs)
	case export.ResourceCreditNoteItems:
		err = s.renderCreditNoteItems(ctx, l, w, p.ObjectIDs)
	default:
		err = fmt.Errorf("'%s' resource not supported: %w", e.ResourceType, core.ErrInvalidValue)
	}
	if err != nil {
		return "", err
	}
	return w.String(), nil
}

// invoices loads the invoices of ids, in the order of ids.
func (l *loader) invoices(ctx context.Context, ids []uuid.UUID) ([]*invoice.Invoice, error) {
	out := make([]*invoice.Invoice, 0, len(ids))
	for _, id := range ids {
		inv, err := l.invoice(ctx, idOf[core.InvoiceID](id))
		if err != nil {
			return nil, err
		}
		if inv != nil {
			out = append(out, inv)
		}
	}
	return out, nil
}

// renderInvoices writes a row per invoice (lago Csv::Invoices#serialize_item).
func (s *Service) renderInvoices(ctx context.Context, l *loader, w *csvWriter, ids []uuid.UUID) error {
	multi, err := multipleBillingEntities(ctx, l.r, l.org)
	if err != nil {
		return err
	}
	invs, err := l.invoices(ctx, ids)
	if err != nil {
		return err
	}
	for _, inv := range invs {
		c, err := l.customer(ctx, inv.CustomerID)
		if err != nil {
			return err
		}
		fileURL, err := l.fileURL(ctx, inv.File)
		if err != nil {
			return err
		}
		row := []*string{
			str(inv.ID.String()), optInt(inv.SequentialID), boolean(inv.SelfBilled), date(inv.IssuingDate),
		}
		row = append(row, customerCells(c)...)
		row = append(row,
			str(inv.Number), inv.PurchaseOrderNumber, str(string(inv.Type)),
			str(string(inv.PaymentStatus)), str(string(inv.Status)), fileURL, str(inv.Currency.Code()),
			integer(inv.FeesAmount), integer(inv.CouponsAmount), integer(inv.TaxesAmount),
			integer(inv.CreditNotesAmount), integer(inv.PrepaidCreditAmount), integer(inv.TotalAmount),
			date(inv.PaymentDueDate), rubyTime(inv.PaymentDisputeLostAt), boolean(inv.PaymentOverdue),
			integer(inv.TotalDueAmount()), integer(inv.TotalPaidAmount), integer(inv.OffsettedAmount),
			integer(inv.ProgressiveBillingCreditAmount),
		)
		if multi {
			code, err := l.billingEntityCode(ctx, inv.BillingEntityID)
			if err != nil {
				return err
			}
			row = append(row, str(code))
		}
		w.Write(row)
	}
	return nil
}

// customerCells are the customer columns of the invoices and credit notes
// exports; a missing customer leaves them empty.
func customerCells(c *customer.Customer) []*string {
	if c == nil {
		return make([]*string, 6)
	}
	return []*string{
		str(c.ID.String()), str(c.ExternalID), c.Name, c.Email, nonEmpty(c.BillingAddress.Country),
		c.TaxIdentificationNumber,
	}
}

// renderInvoiceFees writes a row per fee of each invoice (lago
// Csv::InvoiceFees#serialize_item).
func (s *Service) renderInvoiceFees(ctx context.Context, l *loader, w *csvWriter, ids []uuid.UUID) error {
	invs, err := l.invoices(ctx, ids)
	if err != nil {
		return err
	}
	for _, inv := range invs {
		iss, err := l.r.InvoiceSubscriptions.ListByInvoice(ctx, l.org, inv.ID)
		if err != nil {
			return fmt.Errorf("list invoice subscriptions of %s: %w", inv.ID, err)
		}
		bySub := make(map[core.SubscriptionID]*invoice.Subscription, len(iss))
		for _, is := range iss {
			if _, ok := bySub[is.SubscriptionID]; !ok {
				bySub[is.SubscriptionID] = is
			}
		}
		loc, err := l.location(ctx, inv.CustomerID)
		if err != nil {
			return err
		}
		fees, err := l.r.Fees.ListByInvoice(ctx, l.org, inv.ID)
		if err != nil {
			return fmt.Errorf("list fees of invoice %s: %w", inv.ID, err)
		}
		for _, f := range fees {
			refs, err := l.feeRefs(ctx, f)
			if err != nil {
				return err
			}
			from, to, err := l.feeDates(ctx, f, bySub[f.SubscriptionID], refs.Plan, loc)
			if err != nil {
				return err
			}
			var extID, planCode *string
			if refs.Subscription != nil {
				extID = str(refs.Subscription.ExternalID)
			}
			if refs.Plan != nil {
				planCode = str(refs.Plan.Code)
			}
			w.Write([]*string{
				str(inv.ID.String()), str(inv.Number), date(inv.IssuingDate), str(f.ID.String()),
				str(string(f.Type)), str(refs.ItemCode(f)), str(refs.ItemName(f)), nonEmpty(refs.ItemDescription(f)),
				str(refs.InvoiceName(f)), nonEmpty(refs.FilterDisplayName(", ")), rubyHash(f.GroupedBy),
				extID, planCode, from, to, str(f.Currency.Code()), decimal(f.Units),
				decimal(f.PreciseUnitAmount), integer(f.TaxesAmount), integer(f.Amount + f.TaxesAmount),
			})
		}
	}
	return nil
}

// feeDates are the fee_from_date and fee_to_date of f in loc (lago
// Csv::InvoiceFees#fee_period_dates with ResolveFeeBillingPeriodService):
// an add-on fee shows its stored dates; any other fee its billing period
// when its subscription is on the invoice, else nothing. Period ends are
// exclusive, so the last day is the one before the end.
func (l *loader) feeDates(ctx context.Context, f *invoice.Fee, is *invoice.Subscription, plan *catalog.Plan, loc *time.Location) (*string, *string, error) {
	if f.Type == core.FeeTypeAddOn {
		return utcDate(f.Properties.From), utcDate(f.Properties.To), nil
	}
	if is == nil {
		return nil, nil, nil
	}
	p, err := l.billingPeriod(ctx, f, is, plan)
	if err != nil {
		return nil, nil, err
	}
	return localDate(p.From, loc, false), localDate(p.To, loc, true), nil
}

// billingPeriod is the period fee f bills (lago
// Csv::ResolveFeeBillingPeriodService#billing_period).
func (l *loader) billingPeriod(ctx context.Context, f *invoice.Fee, is *invoice.Subscription, plan *catalog.Plan) (period.Period, error) {
	props := f.Properties
	own := func(from, to time.Time) (period.Period, bool) {
		return period.Period{From: from, To: to}, !from.IsZero() && !to.IsZero()
	}
	switch f.Type {
	case core.FeeTypeSubscription:
		if p, ok := own(props.From, props.To); ok {
			return p, nil
		}
		return is.Period, nil
	case core.FeeTypeFixedCharge:
		if p, ok := own(props.FixedChargesFrom, props.FixedChargesTo); ok {
			return p, nil
		}
		return is.FixedChargesPeriod, nil
	case core.FeeTypeCommitment:
		if p, ok := own(props.From, props.To); ok {
			return p, nil
		}
		if plan == nil || !plan.PayInAdvance {
			return is.Period, nil
		}
		return l.previousPeriod(ctx, is)
	case core.FeeTypeCharge:
		if f.PayInAdvance {
			if p, ok := own(props.ChargesFrom, props.ChargesTo); ok {
				return p, nil
			}
		}
		return is.ChargesPeriod, nil
	}
	return is.ChargesPeriod, nil
}

// previousPeriod is the period of the latest other invoice subscription of
// the same subscription starting no later than is and billing a
// subscription fee, or the zero period (lago
// InvoiceSubscription#previous_invoice_subscription).
func (l *loader) previousPeriod(ctx context.Context, is *invoice.Subscription) (period.Period, error) {
	all, err := l.r.InvoiceSubscriptions.ListBySubscription(ctx, l.org, is.SubscriptionID)
	if err != nil {
		return period.Period{}, fmt.Errorf("list invoice subscriptions of %s: %w", is.SubscriptionID, err)
	}
	all = slices.DeleteFunc(slices.Clone(all), func(o *invoice.Subscription) bool {
		return o.ID == is.ID || o.Period.From.After(is.Period.From)
	})
	slices.SortStableFunc(all, func(a, b *invoice.Subscription) int { return b.Period.From.Compare(a.Period.From) })
	for _, o := range all {
		fees, err := l.r.Fees.ListByInvoice(ctx, l.org, o.InvoiceID)
		if err != nil {
			return period.Period{}, fmt.Errorf("list fees of invoice %s: %w", o.InvoiceID, err)
		}
		if slices.ContainsFunc(fees, func(f *invoice.Fee) bool {
			return f.Type == core.FeeTypeSubscription && f.SubscriptionID == o.SubscriptionID
		}) {
			return o.Period, nil
		}
	}
	return period.Period{}, nil
}

// renderCreditNotes writes a row per credit note (lago
// Csv::CreditNotes#serialize_item).
func (s *Service) renderCreditNotes(ctx context.Context, l *loader, w *csvWriter, ids []uuid.UUID) error {
	multi, err := multipleBillingEntities(ctx, l.r, l.org)
	if err != nil {
		return err
	}
	for _, id := range ids {
		cn, err := l.creditNote(ctx, idOf[core.CreditNoteID](id))
		if err != nil || cn == nil {
			return err
		}
		inv, err := l.invoice(ctx, cn.InvoiceID)
		if err != nil {
			return err
		}
		c, err := l.customer(ctx, cn.CustomerID)
		if err != nil {
			return err
		}
		fileURL, err := l.fileURL(ctx, cn.File)
		if err != nil {
			return err
		}
		var selfBilled, invNumber, po *string
		if inv != nil {
			selfBilled, invNumber, po = boolean(inv.SelfBilled), str(inv.Number), inv.PurchaseOrderNumber
		}
		row := []*string{str(cn.ID.String()), integer(cn.SequentialID), selfBilled, date(cn.IssuingDate)}
		row = append(row, customerCells(c)...)
		row = append(row,
			str(cn.Number), invNumber, po, nonEmpty(string(cn.CreditStatus)), nonEmpty(string(cn.RefundStatus)),
			str(string(cn.Reason)), cn.Description, str(cn.Currency.Code()), integer(cn.TotalAmount),
			integer(cn.TaxesAmount), integer(cn.SubTotalExcludingTaxesAmount), integer(cn.CouponsAdjustmentAmount),
			integer(cn.OffsetAmount), integer(cn.CreditAmount), integer(cn.BalanceAmount), integer(cn.RefundAmount),
			fileURL,
		)
		if multi {
			var code *string
			if inv != nil {
				c, err := l.billingEntityCode(ctx, inv.BillingEntityID)
				if err != nil {
					return err
				}
				code = str(c)
			}
			row = append(row, code)
		}
		w.Write(row)
	}
	return nil
}

// renderCreditNoteItems writes a row per item of each credit note (lago
// Csv::CreditNoteItems#serialize_item).
func (s *Service) renderCreditNoteItems(ctx context.Context, l *loader, w *csvWriter, ids []uuid.UUID) error {
	for _, id := range ids {
		cn, err := l.creditNote(ctx, idOf[core.CreditNoteID](id))
		if err != nil {
			return err
		}
		if cn == nil {
			continue
		}
		inv, err := l.invoice(ctx, cn.InvoiceID)
		if err != nil {
			return err
		}
		var invNumber *string
		if inv != nil {
			invNumber = str(inv.Number)
		}
		items := cn.Items
		if items == nil {
			if items, err = l.r.CreditNotes.ListItemsByCreditNote(ctx, l.org, cn.ID); err != nil {
				return fmt.Errorf("list items of credit note %s: %w", cn.ID, err)
			}
		}
		for _, it := range items {
			var feeID *string
			if !it.FeeID.IsZero() {
				feeID = str(it.FeeID.String())
			}
			w.Write([]*string{
				str(cn.ID.String()), str(cn.Number), invNumber, date(cn.IssuingDate), str(it.ID.String()),
				feeID, str(cn.Currency.Code()), integer(it.Amount),
			})
		}
	}
	return nil
}

// loader loads and caches the records the rows of one part show.
type loader struct {
	s   *Service
	r   port.Repos
	org core.OrganizationID

	invs      map[core.InvoiceID]*invoice.Invoice
	customers map[core.CustomerID]*customer.Customer
	entities  map[core.BillingEntityID]*organization.BillingEntity
	subs      map[core.SubscriptionID]*subscription.Subscription
	plans     map[core.PlanID]*catalog.Plan
	charges   map[core.ChargeID]*catalog.Charge
	metrics   map[core.BillableMetricID]*catalog.BillableMetric
	addOns    map[core.AddOnID]*catalog.AddOn
	fixed     map[core.FixedChargeID]*catalog.FixedCharge
}

// cached returns the value of id in *m, loading it with get on a miss; a
// missing record is nil.
func cached[K comparable, V any](ctx context.Context, m *map[K]*V, id K, get func(context.Context, K) (*V, error)) (*V, error) {
	if *m == nil {
		*m = map[K]*V{}
	}
	if v, ok := (*m)[id]; ok {
		return v, nil
	}
	v, err := get(ctx, id)
	if errors.Is(err, core.ErrNotFound) {
		v, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	(*m)[id] = v
	return v, nil
}

func (l *loader) invoice(ctx context.Context, id core.InvoiceID) (*invoice.Invoice, error) {
	if id.IsZero() {
		return nil, nil
	}
	return cached(ctx, &l.invs, id, func(ctx context.Context, id core.InvoiceID) (*invoice.Invoice, error) {
		return l.r.Invoices.Get(ctx, l.org, id)
	})
}

func (l *loader) creditNote(ctx context.Context, id core.CreditNoteID) (*creditnote.CreditNote, error) {
	cn, err := l.r.CreditNotes.Get(ctx, l.org, id)
	if errors.Is(err, core.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get credit note %s: %w", id, err)
	}
	return cn, nil
}

func (l *loader) customer(ctx context.Context, id core.CustomerID) (*customer.Customer, error) {
	if id.IsZero() {
		return nil, nil
	}
	return cached(ctx, &l.customers, id, func(ctx context.Context, id core.CustomerID) (*customer.Customer, error) {
		return l.r.Customers.Get(ctx, l.org, id)
	})
}

func (l *loader) billingEntity(ctx context.Context, id core.BillingEntityID) (*organization.BillingEntity, error) {
	if id.IsZero() {
		return nil, nil
	}
	return cached(ctx, &l.entities, id, func(ctx context.Context, id core.BillingEntityID) (*organization.BillingEntity, error) {
		return l.r.BillingEntities.Get(ctx, l.org, id)
	})
}

// billingEntityCode returns the code of billing entity id, "" if unknown.
func (l *loader) billingEntityCode(ctx context.Context, id core.BillingEntityID) (string, error) {
	be, err := l.billingEntity(ctx, id)
	if err != nil || be == nil {
		return "", err
	}
	return be.Code, nil
}

// location returns the applicable time zone of customer id: its own, its
// billing entity's, else UTC.
func (l *loader) location(ctx context.Context, id core.CustomerID) (*time.Location, error) {
	c, err := l.customer(ctx, id)
	if err != nil || c == nil {
		return time.UTC, err
	}
	be, err := l.billingEntity(ctx, c.BillingEntityID)
	if err != nil {
		return nil, err
	}
	var d customer.Defaults
	if be != nil {
		d = kernel.DefaultsOf(be)
	}
	if loc := c.ApplicableTimezone(d); loc != nil {
		return loc, nil
	}
	return time.UTC, nil
}

// feeRefs loads the objects the names of f are read from.
func (l *loader) feeRefs(ctx context.Context, f *invoice.Fee) (invoice.FeeRefs, error) {
	var (
		refs invoice.FeeRefs
		err  error
	)
	if !f.SubscriptionID.IsZero() {
		if refs.Subscription, err = cached(ctx, &l.subs, f.SubscriptionID, func(ctx context.Context, id core.SubscriptionID) (*subscription.Subscription, error) {
			return l.r.Subscriptions.Get(ctx, l.org, id)
		}); err != nil {
			return refs, fmt.Errorf("get subscription %s: %w", f.SubscriptionID, err)
		}
		if refs.Subscription != nil {
			if refs.Plan, err = l.plan(ctx, refs.Subscription.PlanID); err != nil {
				return refs, err
			}
		}
	}
	switch f.Type {
	case core.FeeTypeCharge:
		if f.ChargeID.IsZero() {
			break
		}
		if refs.Charge, err = cached(ctx, &l.charges, f.ChargeID, func(ctx context.Context, id core.ChargeID) (*catalog.Charge, error) {
			return l.r.Charges.Get(ctx, l.org, id)
		}); err != nil {
			return refs, fmt.Errorf("get charge %s: %w", f.ChargeID, err)
		}
		if refs.Charge != nil {
			if refs.Metric, err = cached(ctx, &l.metrics, refs.Charge.BillableMetricID, func(ctx context.Context, id core.BillableMetricID) (*catalog.BillableMetric, error) {
				ms, err := l.r.Metrics.ListByIDs(ctx, l.org, []core.BillableMetricID{id})
				if err != nil || len(ms) == 0 {
					return nil, err
				}
				return ms[0], nil
			}); err != nil {
				return refs, fmt.Errorf("get metric %s: %w", refs.Charge.BillableMetricID, err)
			}
		}
		if !f.ChargeFilterID.IsZero() {
			cf, err := l.r.Charges.GetFilter(ctx, l.org, f.ChargeFilterID)
			switch {
			case err == nil:
				refs.ChargeFilter = cf
			case !errors.Is(err, core.ErrNotFound):
				return refs, fmt.Errorf("get charge filter %s: %w", f.ChargeFilterID, err)
			}
		}
	case core.FeeTypeAddOn:
		if refs.AddOn, err = l.addOn(ctx, f.AddOnID); err != nil {
			return refs, err
		}
	case core.FeeTypeFixedCharge:
		if f.FixedChargeID.IsZero() {
			break
		}
		if refs.FixedCharge, err = cached(ctx, &l.fixed, f.FixedChargeID, func(ctx context.Context, id core.FixedChargeID) (*catalog.FixedCharge, error) {
			return l.r.FixedCharges.Get(ctx, l.org, id)
		}); err != nil {
			return refs, fmt.Errorf("get fixed charge %s: %w", f.FixedChargeID, err)
		}
		if refs.FixedCharge != nil {
			if refs.AddOn, err = l.addOn(ctx, refs.FixedCharge.AddOnID); err != nil {
				return refs, err
			}
		}
	case core.FeeTypeCredit:
		if !f.WalletTransactionID.IsZero() {
			wt, err := l.r.WalletTransactions.Get(ctx, l.org, f.WalletTransactionID)
			switch {
			case err == nil:
				refs.WalletTransaction = wt
			case !errors.Is(err, core.ErrNotFound):
				return refs, fmt.Errorf("get wallet transaction %s: %w", f.WalletTransactionID, err)
			}
		}
	}
	return refs, nil
}

func (l *loader) plan(ctx context.Context, id core.PlanID) (*catalog.Plan, error) {
	p, err := cached(ctx, &l.plans, id, func(ctx context.Context, id core.PlanID) (*catalog.Plan, error) {
		ps, err := l.r.Plans.ListByIDs(ctx, l.org, []core.PlanID{id})
		if err != nil || len(ps) == 0 {
			return nil, err
		}
		return ps[0], nil
	})
	if err != nil {
		return nil, fmt.Errorf("get plan %s: %w", id, err)
	}
	return p, nil
}

func (l *loader) addOn(ctx context.Context, id core.AddOnID) (*catalog.AddOn, error) {
	if id.IsZero() {
		return nil, nil
	}
	a, err := cached(ctx, &l.addOns, id, func(ctx context.Context, id core.AddOnID) (*catalog.AddOn, error) {
		return l.r.AddOns.Get(ctx, l.org, id)
	})
	if err != nil {
		return nil, fmt.Errorf("get add-on %s: %w", id, err)
	}
	return a, nil
}

// fileURL returns the link of a document's file, nil without one or
// without a blob store.
func (l *loader) fileURL(ctx context.Context, ref core.BlobRef) (*string, error) {
	if ref.IsZero() || l.s.blobs == nil {
		return nil, nil
	}
	u, err := l.s.blobs.URL(ctx, l.org, ref)
	if err != nil {
		return nil, fmt.Errorf("file url: %w", err)
	}
	return str(u), nil
}

// csvWriter writes rows as Ruby's CSV does: a nil cell is empty, an empty
// string is "", and a cell with a comma, quote or line break is quoted.
type csvWriter struct{ b strings.Builder }

// Write appends a row.
func (w *csvWriter) Write(row []*string) {
	for i, c := range row {
		if i > 0 {
			w.b.WriteByte(',')
		}
		if c == nil {
			continue
		}
		v := *c
		if v == "" || strings.ContainsAny(v, ",\"\r\n") {
			w.b.WriteByte('"')
			w.b.WriteString(strings.ReplaceAll(v, `"`, `""`))
			w.b.WriteByte('"')
			continue
		}
		w.b.WriteString(v)
	}
	w.b.WriteByte('\n')
}

// String returns the written rows.
func (w *csvWriter) String() string { return w.b.String() }

func str(s string) *string { return &s }

// nonEmpty is nil for "".
func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func integer[T ~int64 | ~int](n T) *string { return str(strconv.FormatInt(int64(n), 10)) }

func optInt(n *int64) *string {
	if n == nil {
		return nil
	}
	return integer(*n)
}

func boolean(b bool) *string { return str(strconv.FormatBool(b)) }

// date is a date in ISO 8601, nil for the zero date.
func date(d core.Date) *string {
	if d.IsZero() {
		return nil
	}
	return str(d.String())
}

// rubyTime is an instant as Ruby's Time#to_s in UTC.
func rubyTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	return str(t.UTC().Format("2006-01-02 15:04:05 UTC"))
}

// utcDate is the UTC date of t, nil for the zero instant.
func utcDate(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	return str(t.UTC().Format(time.DateOnly))
}

// localDate is the date of t in loc, nil for the zero instant; end takes
// the day before an exclusive end.
func localDate(t time.Time, loc *time.Location, end bool) *string {
	if t.IsZero() {
		return nil
	}
	if end {
		t = t.Add(-time.Nanosecond)
	}
	return str(t.In(loc).Format(time.DateOnly))
}

// decimal is d as Ruby's BigDecimal#to_s with ActiveSupport: plain
// notation without trailing zeros and with at least one decimal
// ("100.0", "10.5").
func decimal(d money.Decimal) *string {
	s := d.String()
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "-0" {
		s = "0"
	}
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return &s
}

// idOf wraps u as an identifier of type I.
func idOf[I core.Identifier[I]](u uuid.UUID) I { return core.MustParseID[I](u.String()) }

// rubyHash is a string map as Ruby's Hash#inspect, keys in stored order
// (shorter keys first, then bytewise, as jsonb keeps them).
func rubyHash(m map[string]string) *string {
	keys := slices.SortedFunc(maps.Keys(m), func(a, b string) int {
		return cmp.Or(cmp.Compare(len(a), len(b)), strings.Compare(a, b))
	})
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, strconv.Quote(k)+" => "+strconv.Quote(m[k]))
	}
	return str("{" + strings.Join(parts, ", ") + "}")
}
