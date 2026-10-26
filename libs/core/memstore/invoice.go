package memstore

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
)

// invoiceData holds the tables of package invoice.
type invoiceData struct {
	invoices       *table[core.InvoiceID, invoice.Invoice]
	subscriptions  *table[core.InvoiceSubscriptionID, invoice.Subscription]
	fees           *table[core.FeeID, invoice.Fee]
	appliedTaxes   *table[core.InvoiceAppliedTaxID, invoice.AppliedTax]
	adjustedFees   *table[core.AdjustedFeeID, invoice.AdjustedFee]
	settlements    *table[core.InvoiceSettlementID, invoice.Settlement]
	customSections *table[core.InvoiceCustomSectionID, invoice.CustomSection]
	sectionCopies  *table[core.AppliedInvoiceCustomSectionID, invoice.AppliedCustomSection]
	errorDetails   *table[core.ErrorDetailID, invoice.ErrorDetail]
	credits        *table[core.InvoiceCreditID, invoice.Credit]
}

func newInvoiceData() *invoiceData {
	return &invoiceData{
		invoices: newTable[core.InvoiceID](func(i invoice.Invoice) invoice.Invoice {
			i.Metadata = slices.Clone(i.Metadata)
			i.SequentialID = invoiceClonePtr(i.SequentialID)
			i.BillingEntitySequentialID = invoiceClonePtr(i.BillingEntitySequentialID)
			i.PaymentDisputeLostAt = invoiceClonePtr(i.PaymentDisputeLostAt)
			i.FinalizedAt = invoiceClonePtr(i.FinalizedAt)
			i.VoidedAt = invoiceClonePtr(i.VoidedAt)
			i.PurchaseOrderNumber = invoiceClonePtr(i.PurchaseOrderNumber)
			i.X402PaymentToken = invoiceClonePtr(i.X402PaymentToken)
			return i
		}),
		subscriptions: newTable[core.InvoiceSubscriptionID, invoice.Subscription](nil),
		fees:          newTable[core.FeeID](invoice.CloneFee),
		appliedTaxes:  newTable[core.InvoiceAppliedTaxID, invoice.AppliedTax](nil),
		adjustedFees: newTable[core.AdjustedFeeID](func(a invoice.AdjustedFee) invoice.AdjustedFee {
			a.GroupedBy = maps.Clone(a.GroupedBy)
			a.InvoiceDisplayName = invoiceClonePtr(a.InvoiceDisplayName)
			return a
		}),
		settlements: newTable[core.InvoiceSettlementID, invoice.Settlement](nil),
		customSections: newTable[core.InvoiceCustomSectionID](func(s invoice.CustomSection) invoice.CustomSection {
			s.DisplayName = invoiceClonePtr(s.DisplayName)
			s.Details = invoiceClonePtr(s.Details)
			s.Description = invoiceClonePtr(s.Description)
			s.DeletedAt = invoiceClonePtr(s.DeletedAt)
			return s
		}),
		sectionCopies: newTable[core.AppliedInvoiceCustomSectionID](func(s invoice.AppliedCustomSection) invoice.AppliedCustomSection {
			s.DisplayName = invoiceClonePtr(s.DisplayName)
			s.Details = invoiceClonePtr(s.Details)
			return s
		}),
		errorDetails: newTable[core.ErrorDetailID](func(e invoice.ErrorDetail) invoice.ErrorDetail {
			e.Details = slices.Clone(e.Details)
			return e
		}),
		credits: newTable[core.InvoiceCreditID, invoice.Credit](nil),
	}
}

func (d *invoiceData) snapshot() *invoiceData {
	return &invoiceData{
		invoices:       d.invoices.snapshot(),
		subscriptions:  d.subscriptions.snapshot(),
		fees:           d.fees.snapshot(),
		appliedTaxes:   d.appliedTaxes.snapshot(),
		adjustedFees:   d.adjustedFees.snapshot(),
		settlements:    d.settlements.snapshot(),
		customSections: d.customSections.snapshot(),
		sectionCopies:  d.sectionCopies.snapshot(),
		errorDetails:   d.errorDetails.snapshot(),
		credits:        d.credits.snapshot(),
	}
}

// invoiceClonePtr returns a copy of *p, or nil.
func invoiceClonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	c := *p
	return &c
}

var (
	_ invoice.Repository              = invoiceRepo{}
	_ invoice.SubscriptionRepository  = invoiceSubscriptionRepo{}
	_ invoice.FeeRepository           = feeRepo{}
	_ invoice.AppliedTaxRepository    = invoiceAppliedTaxRepo{}
	_ invoice.AdjustedFeeRepository   = adjustedFeeRepo{}
	_ invoice.SettlementRepository    = invoiceSettlementRepo{}
	_ invoice.CustomSectionRepository = invoiceCustomSectionRepo{}
	_ invoice.ErrorDetailRepository   = errorDetailRepo{}
	_ invoice.CreditRepository        = invoiceCreditRepo{}
)

// invoiceRepo implements invoice.Repository.
type invoiceRepo struct {
	d  *invoiceData
	st *state // the whole transaction state, for the list filters
}

func (r invoiceRepo) checkNumber(inv *invoice.Invoice) error {
	// The x402 payment token is unique across all organizations.
	if inv.X402PaymentToken != nil && r.d.invoices.any(func(x *invoice.Invoice) bool {
		return x.ID != inv.ID && x.X402PaymentToken != nil && *x.X402PaymentToken == *inv.X402PaymentToken
	}) {
		return conflict("invoice", "x402_payment_token")
	}
	if inv.Number == "" || strings.HasSuffix(inv.Number, "-DRAFT") {
		return nil
	}
	if r.d.invoices.any(func(x *invoice.Invoice) bool {
		return x.ID != inv.ID && x.OrganizationID == inv.OrganizationID && x.Number == inv.Number
	}) {
		return conflict("invoice", "number")
	}
	return nil
}

func (r invoiceRepo) Create(_ context.Context, inv *invoice.Invoice) error {
	if r.d.invoices.has(inv.ID) {
		return conflict("invoice", "id")
	}
	if err := r.checkNumber(inv); err != nil {
		return err
	}
	r.d.invoices.put(inv.ID, *inv)
	return nil
}

func (r invoiceRepo) Update(_ context.Context, inv *invoice.Invoice) error {
	cur, ok := r.d.invoices.get(inv.ID)
	if !ok || cur.OrganizationID != inv.OrganizationID {
		return notFound("invoice", inv.ID)
	}
	if err := r.checkNumber(inv); err != nil {
		return err
	}
	r.d.invoices.put(inv.ID, *inv)
	return nil
}

func (r invoiceRepo) Get(_ context.Context, org core.OrganizationID, id core.InvoiceID) (*invoice.Invoice, error) {
	inv, ok := r.d.invoices.get(id)
	if !ok || inv.OrganizationID != org {
		return nil, notFound("invoice", id)
	}
	return inv, nil
}

func (r invoiceRepo) GetByNumber(_ context.Context, org core.OrganizationID, number string) (*invoice.Invoice, error) {
	inv, ok := r.d.invoices.first(func(x *invoice.Invoice) bool { return x.OrganizationID == org && x.Number == number })
	if !ok {
		return nil, notFound("invoice", stringer(number))
	}
	return inv, nil
}

func (r invoiceRepo) GetRegenerated(_ context.Context, org core.OrganizationID, voided core.InvoiceID) (*invoice.Invoice, error) {
	inv, ok := r.d.invoices.first(func(x *invoice.Invoice) bool { return x.OrganizationID == org && x.VoidedInvoiceID == voided })
	if !ok {
		return nil, notFound("invoice", voided)
	}
	return inv, nil
}

func (r invoiceRepo) List(_ context.Context, org core.OrganizationID, f invoice.Filter, page core.PageRequest) (core.Page[*invoice.Invoice], error) {
	if err := f.Validate(); err != nil {
		return core.Page[*invoice.Invoice]{}, fmt.Errorf("list invoices: %w", err)
	}
	term := strings.ToLower(core.SearchTerm(f.Search))
	searchID, byID := invoice.SearchID(core.SearchTerm(f.Search))
	rows := r.d.invoices.find(func(x *invoice.Invoice) bool {
		if x.OrganizationID != org ||
			!f.Lists(x.Status) ||
			!f.CustomerID.IsZero() && x.CustomerID != f.CustomerID ||
			!f.BillingEntityID.IsZero() && x.BillingEntityID != f.BillingEntityID ||
			len(f.BillingEntityIDs) > 0 && !slices.Contains(f.BillingEntityIDs, x.BillingEntityID) ||
			!f.Currency.IsZero() && x.Currency.Code() != f.Currency.Code() ||
			len(f.Statuses) > 0 && !slices.Contains(f.Statuses, x.Status) ||
			len(f.PaymentStatuses) > 0 && !slices.Contains(f.PaymentStatuses, x.PaymentStatus) ||
			len(f.Types) > 0 && !slices.Contains(f.Types, x.Type) ||
			len(f.IDs) > 0 && !slices.Contains(f.IDs, x.ID) ||
			!f.IssuingDateFrom.IsZero() && (x.IssuingDate.IsZero() || x.IssuingDate.Before(f.IssuingDateFrom)) ||
			!f.IssuingDateTo.IsZero() && (x.IssuingDate.IsZero() || x.IssuingDate.After(f.IssuingDateTo)) ||
			f.AmountFrom != nil && x.TotalAmount < *f.AmountFrom ||
			f.AmountTo != nil && x.TotalAmount > *f.AmountTo ||
			f.PaymentDisputeLost != nil && (x.PaymentDisputeLostAt != nil) != *f.PaymentDisputeLost ||
			f.PaymentOverdue != nil && x.PaymentOverdue != *f.PaymentOverdue ||
			f.PartiallyPaid != nil && (x.TotalAmount > x.TotalPaidAmount && x.TotalPaidAmount > 0) != *f.PartiallyPaid ||
			f.PositiveDueAmount != nil && (x.TotalAmount-x.TotalPaidAmount > 0) != *f.PositiveDueAmount ||
			f.SelfBilled != nil && x.SelfBilled != *f.SelfBilled ||
			f.PurchaseOrderNumber != "" && !invoicePurchaseOrderIs(x, f.PurchaseOrderNumber) ||
			!f.Metadata.Matches(x.Metadata) {
			return false
		}
		if f.CustomerExternalID != "" && !r.st.customer.customers.any(func(c *customer.Customer) bool {
			return c.OrganizationID == org && c.ID == x.CustomerID && c.ExternalID == f.CustomerExternalID
		}) {
			return false
		}
		if len(f.SubscriptionIDs) > 0 && !r.d.subscriptions.any(func(s *invoice.Subscription) bool {
			return s.OrganizationID == org && s.InvoiceID == x.ID && slices.Contains(f.SubscriptionIDs, s.SubscriptionID)
		}) {
			return false
		}
		if len(f.Settlements) > 0 && !r.d.settlements.any(func(s *invoice.Settlement) bool {
			return s.OrganizationID == org && s.InvoiceID == x.ID && slices.Contains(f.Settlements, s.Type)
		}) {
			return false
		}
		return term == "" || byID && x.ID == searchID || strings.Contains(strings.ToLower(r.searchTerms(x, f)), term)
	})
	if f.Order == core.OrderRecent {
		if page.Token != "" {
			c, err := core.DecodeCursor(page.Token)
			if err != nil {
				return core.Page[*invoice.Invoice]{}, err
			}
			if _, err := invoiceCursorDate(c); err != nil {
				return core.Page[*invoice.Invoice]{}, err
			}
		}
		slices.SortStableFunc(rows, func(a, b *invoice.Invoice) int {
			if c := invoiceCompareIssuing(b.IssuingDate, a.IssuingDate); c != 0 {
				return c
			}
			return b.ID.Compare(a.ID)
		})
		return paginateSorted(rows, invoiceCursor, func(x *invoice.Invoice, c core.Cursor) bool {
			d, _ := invoiceCursorDate(c) // checked above
			if cmp := invoiceCompareIssuing(x.IssuingDate, d); cmp != 0 {
				return cmp < 0
			}
			return x.ID.UUID().String() < c.ID.String()
		}, page)
	}
	return paginate(rows, func(x *invoice.Invoice) core.InvoiceID { return x.ID }, page)
}

// invoicePurchaseOrderIs reports whether the purchase order number of x
// equals number, compared case-insensitively.
func invoicePurchaseOrderIs(x *invoice.Invoice, number string) bool {
	return x.PurchaseOrderNumber != nil && strings.EqualFold(*x.PurchaseOrderNumber, number)
}

// invoiceCompareIssuing compares issuing dates with a missing date after
// every date (PostgreSQL sorts NULL first in descending order).
func invoiceCompareIssuing(a, b core.Date) int {
	switch {
	case a.IsZero() && b.IsZero():
		return 0
	case a.IsZero():
		return 1
	case b.IsZero():
		return -1
	default:
		return a.Compare(b)
	}
}

// invoiceCursorDate returns the issuing date of a cursor (zero for an
// invoice without one); a malformed date fails with core.ErrInvalidValue.
func invoiceCursorDate(c core.Cursor) (core.Date, error) {
	if c.Key == "" {
		return core.Date{}, nil
	}
	d, err := core.ParseDate(c.Key)
	if err != nil {
		return core.Date{}, fmt.Errorf("%w: page token", core.ErrInvalidValue)
	}
	return d, nil
}

// invoiceCursor is the position of x in the list by issuing date.
func invoiceCursor(x *invoice.Invoice) core.Cursor {
	c := core.Cursor{ID: x.ID.UUID()}
	if !x.IssuingDate.IsZero() {
		c.Key = x.IssuingDate.String()
	}
	return c
}

// searchTerms returns the text the search term of f is looked for in: the
// number, then (unless f filters by customer) the customer's fields,
// joined by spaces.
func (r invoiceRepo) searchTerms(x *invoice.Invoice, f invoice.Filter) string {
	parts := []string{x.Number}
	if !f.SearchesCustomers() {
		return x.Number
	}
	if x.PurchaseOrderNumber != nil {
		parts = append(parts, *x.PurchaseOrderNumber)
	}
	if c, ok := r.st.customer.customers.get(x.CustomerID); ok {
		for _, p := range []*string{c.Name, c.Firstname, c.Lastname, c.LegalName, &c.ExternalID, c.Email} {
			if p != nil {
				parts = append(parts, *p)
			}
		}
	}
	return strings.Join(parts, " ")
}

func (r invoiceRepo) ListByIDs(_ context.Context, org core.OrganizationID, ids []core.InvoiceID) ([]*invoice.Invoice, error) {
	return r.d.invoices.find(func(x *invoice.Invoice) bool { return x.OrganizationID == org && slices.Contains(ids, x.ID) }), nil
}

func (r invoiceRepo) ListDraftsToFinalize(_ context.Context, org core.OrganizationID, date core.Date) ([]*invoice.Invoice, error) {
	return r.d.invoices.find(func(x *invoice.Invoice) bool { return x.OrganizationID == org && x.FinalizationDue(date) }), nil
}

func (r invoiceRepo) ListRefreshRequested(_ context.Context, org core.OrganizationID) ([]*invoice.Invoice, error) {
	return r.d.invoices.find(func(x *invoice.Invoice) bool {
		return x.OrganizationID == org && x.Status == invoice.StatusDraft && x.RefreshRequested
	}), nil
}

func (r invoiceRepo) RequestRefreshByMetric(_ context.Context, org core.OrganizationID, metric core.BillableMetricID) (int, error) {
	plans := map[core.PlanID]bool{}
	for _, c := range r.st.catalog.charges.find(func(c *catalog.Charge) bool {
		return c.OrganizationID == org && c.BillableMetricID == metric
	}) {
		plans[c.PlanID] = true
	}
	billed := map[core.InvoiceID]bool{}
	for _, is := range r.d.subscriptions.find(func(is *invoice.Subscription) bool { return is.OrganizationID == org }) {
		if s, ok := r.st.subscription.subscriptions.get(is.SubscriptionID); ok && plans[s.PlanID] {
			billed[is.InvoiceID] = true
		}
	}
	n := 0
	for _, x := range r.d.invoices.find(func(x *invoice.Invoice) bool {
		return x.OrganizationID == org && x.Status == invoice.StatusDraft && billed[x.ID]
	}) {
		x.RefreshRequested = true
		r.d.invoices.put(x.ID, *x)
		n++
	}
	return n, nil
}

func (r invoiceRepo) ListOverdueCandidates(_ context.Context, org core.OrganizationID, date core.Date) ([]*invoice.Invoice, error) {
	return r.d.invoices.find(func(x *invoice.Invoice) bool { return x.OrganizationID == org && x.OverdueCandidate(date) }), nil
}

func (r invoiceRepo) NextSequentialID(_ context.Context, org core.OrganizationID, customer core.CustomerID, billingEntity core.BillingEntityID) (int64, error) {
	var n int64
	for _, x := range r.d.invoices.find(func(x *invoice.Invoice) bool {
		return x.OrganizationID == org && x.CustomerID == customer && x.BillingEntityID == billingEntity && x.SequentialID != nil
	}) {
		n = max(n, *x.SequentialID)
	}
	return n + 1, nil
}

func (r invoiceRepo) NextBillingEntitySequentialID(_ context.Context, org core.OrganizationID, billingEntity core.BillingEntityID) (int64, error) {
	var n int64
	for _, x := range r.d.invoices.find(func(x *invoice.Invoice) bool {
		return x.OrganizationID == org && x.BillingEntityID == billingEntity && !x.SelfBilled && x.BillingEntitySequentialID != nil
	}) {
		n = max(n, *x.BillingEntitySequentialID)
	}
	return n + 1, nil
}

func (r invoiceRepo) DeleteDraft(_ context.Context, org core.OrganizationID, id core.InvoiceID) error {
	inv, ok := r.d.invoices.get(id)
	if !ok || inv.OrganizationID != org {
		return notFound("invoice", id)
	}
	if !inv.Deletable() {
		return conflict("invoice", "not_draft")
	}
	return r.st.deleteInvoice(org, id)
}

// invoiceSubscriptionRepo implements invoice.SubscriptionRepository.
type invoiceSubscriptionRepo struct{ d *invoiceData }

func (r invoiceSubscriptionRepo) Create(_ context.Context, s *invoice.Subscription) error {
	if r.d.subscriptions.has(s.ID) {
		return conflict("invoice_subscription", "id")
	}
	for _, k := range s.Keys() {
		if r.d.subscriptions.any(func(x *invoice.Subscription) bool {
			return x.OrganizationID == s.OrganizationID && x.Has(k)
		}) {
			return conflict("invoice_subscription", "idempotency_key")
		}
	}
	r.d.subscriptions.put(s.ID, *s)
	return nil
}

func (r invoiceSubscriptionRepo) ListByInvoice(_ context.Context, org core.OrganizationID, inv core.InvoiceID) ([]*invoice.Subscription, error) {
	return r.d.subscriptions.find(func(x *invoice.Subscription) bool { return x.OrganizationID == org && x.InvoiceID == inv }), nil
}

func (r invoiceSubscriptionRepo) ListBySubscription(_ context.Context, org core.OrganizationID, sub core.SubscriptionID) ([]*invoice.Subscription, error) {
	return r.d.subscriptions.find(func(x *invoice.Subscription) bool { return x.OrganizationID == org && x.SubscriptionID == sub }), nil
}

func (r invoiceSubscriptionRepo) Find(_ context.Context, org core.OrganizationID, k invoice.IdempotencyKey) (*invoice.Subscription, error) {
	s, ok := r.d.subscriptions.first(func(x *invoice.Subscription) bool {
		return x.OrganizationID == org && x.Has(k)
	})
	if !ok {
		return nil, notFound("invoice_subscription", stringer(fmt.Sprintf("%s/%d", k.SubscriptionID, k.Kind)))
	}
	return s, nil
}

func (r invoiceSubscriptionRepo) Last(_ context.Context, org core.OrganizationID, sub core.SubscriptionID) (*invoice.Subscription, error) {
	rows := r.d.subscriptions.find(func(x *invoice.Subscription) bool { return x.OrganizationID == org && x.SubscriptionID == sub })
	if len(rows) == 0 {
		return nil, notFound("invoice_subscription", sub)
	}
	return slices.MaxFunc(rows, func(a, b *invoice.Subscription) int {
		if c := a.Period.From.Compare(b.Period.From); c != 0 {
			return c
		}
		if c := a.Timestamp.Compare(b.Timestamp); c != 0 {
			return c
		}
		return a.ID.Compare(b.ID)
	}), nil
}

func (r invoiceSubscriptionRepo) DeleteByInvoice(_ context.Context, org core.OrganizationID, inv core.InvoiceID) error {
	r.d.subscriptions.deleteWhere(func(x *invoice.Subscription) bool { return x.OrganizationID == org && x.InvoiceID == inv })
	return nil
}

func (r invoiceSubscriptionRepo) MarkRegenerated(_ context.Context, org core.OrganizationID, inv, regenerated core.InvoiceID) error {
	for _, x := range r.d.subscriptions.find(func(x *invoice.Subscription) bool { return x.OrganizationID == org && x.InvoiceID == inv }) {
		x.RegeneratedInvoiceID = regenerated
		r.d.subscriptions.put(x.ID, *x)
	}
	return nil
}

// feeRepo implements invoice.FeeRepository.
type feeRepo struct {
	d  *invoiceData
	st *state // the whole transaction state, for the reference checks
}

func (r feeRepo) Create(_ context.Context, f *invoice.Fee) error {
	if r.d.fees.has(f.ID) {
		return conflict("fee", "id")
	}
	if r.inAdvanceTaken(f) {
		return conflict("fee", "pay_in_advance_event_transaction_id")
	}
	r.d.fees.put(f.ID, *f)
	return nil
}

func (r feeRepo) Update(_ context.Context, f *invoice.Fee) error {
	cur, ok := r.d.fees.get(f.ID)
	if !ok || cur.OrganizationID != f.OrganizationID || cur.DeletedAt != nil {
		return notFound("fee", f.ID)
	}
	if r.inAdvanceTaken(f) {
		return conflict("fee", "pay_in_advance_event_transaction_id")
	}
	r.d.fees.put(f.ID, *f)
	return nil
}

// inAdvanceTaken reports whether another live fee holds f's pay-in-advance
// key: its event and charge (and filter), or its event and contract rate
// card (the unique indexes fees_in_advance_*_event_key).
func (r feeRepo) inAdvanceTaken(f *invoice.Fee) bool {
	k, ok := inAdvanceKey(f)
	return ok && r.d.fees.any(func(x *invoice.Fee) bool {
		xk, ok := inAdvanceKey(x)
		return ok && x.ID != f.ID && x.OrganizationID == f.OrganizationID && xk == k
	})
}

type feeInAdvanceKey struct {
	subscription core.SubscriptionID
	transaction  string
	charge       core.ChargeID
	filter       core.ChargeFilterID
	card         core.SubscriptionRateCardID
}

func inAdvanceKey(f *invoice.Fee) (feeInAdvanceKey, bool) {
	if !f.PayInAdvance || f.PayInAdvanceEventTransactionID == "" || !f.OriginalFeeID.IsZero() || f.DeletedAt != nil {
		return feeInAdvanceKey{}, false
	}
	k := feeInAdvanceKey{subscription: f.SubscriptionID, transaction: f.PayInAdvanceEventTransactionID}
	switch {
	case !f.ChargeID.IsZero():
		k.charge, k.filter = f.ChargeID, f.ChargeFilterID
	case !f.SubscriptionRateCardID.IsZero():
		k.card = f.SubscriptionRateCardID
	default:
		return feeInAdvanceKey{}, false
	}
	// A NULL subscription is distinct from every other in PostgreSQL.
	return k, !f.SubscriptionID.IsZero()
}

func (r feeRepo) Get(_ context.Context, org core.OrganizationID, id core.FeeID) (*invoice.Fee, error) {
	f, ok := r.d.fees.get(id)
	if !ok || f.OrganizationID != org || f.DeletedAt != nil {
		return nil, notFound("fee", id)
	}
	return f, nil
}

func (r feeRepo) List(_ context.Context, org core.OrganizationID, f invoice.FeeFilter, page core.PageRequest) (core.Page[*invoice.Fee], error) {
	if err := f.Validate(); err != nil {
		return core.Page[*invoice.Fee]{}, fmt.Errorf("list fees: %w", err)
	}
	rows := r.d.fees.find(func(x *invoice.Fee) bool {
		return x.OrganizationID == org && x.DeletedAt == nil &&
			(f.InvoiceID.IsZero() || x.InvoiceID == f.InvoiceID) &&
			(f.SubscriptionID.IsZero() || x.SubscriptionID == f.SubscriptionID) &&
			(f.ChargeID.IsZero() || x.ChargeID == f.ChargeID) &&
			(len(f.Types) == 0 || slices.Contains(f.Types, x.Type)) &&
			(len(f.PaymentStatuses) == 0 || slices.Contains(f.PaymentStatuses, x.PaymentStatus)) &&
			(f.Currency.IsZero() || x.Currency == f.Currency) &&
			(f.EventTransactionID == "" || x.PayInAdvanceEventTransactionID == f.EventTransactionID) &&
			r.feeCustomerMatches(x, f) &&
			r.feeSubscriptionMatches(x, f) &&
			r.feeMetricMatches(x, f) &&
			inTimeRange(&x.CreatedAt, f.CreatedFrom, f.CreatedTo) &&
			inTimeRange(x.SucceededAt, f.SucceededFrom, f.SucceededTo) &&
			inTimeRange(x.FailedAt, f.FailedFrom, f.FailedTo) &&
			inTimeRange(x.RefundedAt, f.RefundedFrom, f.RefundedTo)
	})
	// Oldest first, then by ID (fees_query.rb:33).
	return paginateOldest(rows, func(x *invoice.Fee) (time.Time, uuid.UUID) { return x.CreatedAt, x.ID.UUID() }, page)
}

// inTimeRange reports whether t is in [from, to] (zero bounds unset); a
// nil t is in no bounded range.
func inTimeRange(t *time.Time, from, to time.Time) bool {
	if from.IsZero() && to.IsZero() {
		return true
	}
	return t != nil && (from.IsZero() || !t.Before(from)) && (to.IsZero() || !t.After(to))
}

// feeCustomerMatches applies the customer filters: the customer of the
// fee's invoice, else of its subscription.
func (r feeRepo) feeCustomerMatches(x *invoice.Fee, f invoice.FeeFilter) bool {
	if f.CustomerID.IsZero() && f.CustomerExternalID == "" {
		return true
	}
	var id core.CustomerID
	if !x.InvoiceID.IsZero() {
		inv, ok := r.d.invoices.get(x.InvoiceID)
		if !ok {
			return false
		}
		id = inv.CustomerID
	} else {
		sub, ok := r.st.subscription.subscriptions.get(x.SubscriptionID)
		if !ok {
			return false
		}
		id = sub.CustomerID
	}
	if !f.CustomerID.IsZero() && id != f.CustomerID {
		return false
	}
	if f.CustomerExternalID != "" {
		c, ok := r.st.customer.customers.get(id)
		if !ok || c.DeletedAt != nil || c.ExternalID != f.CustomerExternalID {
			return false
		}
	}
	return true
}

// feeSubscriptionMatches applies SubscriptionExternalID.
func (r feeRepo) feeSubscriptionMatches(x *invoice.Fee, f invoice.FeeFilter) bool {
	if f.SubscriptionExternalID == "" {
		return true
	}
	sub, ok := r.st.subscription.subscriptions.get(x.SubscriptionID)
	return ok && sub.ExternalID == f.SubscriptionExternalID
}

// feeMetricMatches applies BillableMetricCode.
func (r feeRepo) feeMetricMatches(x *invoice.Fee, f invoice.FeeFilter) bool {
	if f.BillableMetricCode == "" {
		return true
	}
	c, ok := r.st.catalog.charges.get(x.ChargeID)
	if !ok {
		return false
	}
	m, ok := r.st.catalog.metrics.get(c.BillableMetricID)
	return ok && m.Code == f.BillableMetricCode
}

func (r feeRepo) Discard(_ context.Context, org core.OrganizationID, id core.FeeID, at time.Time) error {
	f, ok := r.d.fees.get(id)
	if !ok || f.OrganizationID != org || f.DeletedAt != nil {
		return notFound("fee", id)
	}
	if !f.InvoiceID.IsZero() {
		return conflict("fee", "attached to an invoice")
	}
	at = at.UTC()
	f.DeletedAt, f.UpdatedAt = &at, at
	r.d.fees.put(id, *f)
	return nil
}

func (r feeRepo) ListByInvoice(_ context.Context, org core.OrganizationID, inv core.InvoiceID) ([]*invoice.Fee, error) {
	return r.d.fees.find(func(x *invoice.Fee) bool {
		return x.OrganizationID == org && x.DeletedAt == nil && x.InvoiceID == inv
	}), nil
}

func (r feeRepo) ListBySubscriptionPeriod(_ context.Context, org core.OrganizationID, sub core.SubscriptionID, from time.Time) ([]*invoice.Fee, error) {
	return r.d.fees.find(func(x *invoice.Fee) bool {
		return x.OrganizationID == org && x.DeletedAt == nil && x.SubscriptionID == sub && x.Properties.From.Equal(from)
	}), nil
}

func (r feeRepo) ListUnbilledInAdvance(_ context.Context, org core.OrganizationID, before time.Time) ([]*invoice.Fee, error) {
	return r.d.fees.find(func(x *invoice.Fee) bool {
		return x.OrganizationID == org && x.DeletedAt == nil && x.InvoiceID.IsZero() && x.PayInAdvance && x.Invoiceable &&
			x.PaymentStatus == invoice.FeePaymentStatusSucceeded && x.CreatedAt.Before(before)
	}), nil
}

func (r feeRepo) ListByCharge(_ context.Context, org core.OrganizationID, charge core.ChargeID) ([]*invoice.Fee, error) {
	return r.d.fees.find(func(x *invoice.Fee) bool {
		return x.OrganizationID == org && x.DeletedAt == nil && x.ChargeID == charge
	}), nil
}

func (r feeRepo) ListByEventTransaction(_ context.Context, org core.OrganizationID, sub core.SubscriptionID, transactionID string) ([]*invoice.Fee, error) {
	return r.d.fees.find(func(x *invoice.Fee) bool {
		return x.OrganizationID == org && x.DeletedAt == nil && x.SubscriptionID == sub && x.PayInAdvanceEventTransactionID == transactionID
	}), nil
}

func (r feeRepo) DeleteByInvoice(_ context.Context, org core.OrganizationID, inv core.InvoiceID) error {
	gone := r.st.feesWithTrueUps(func(x *invoice.Fee) bool { return x.OrganizationID == org && x.InvoiceID == inv })
	if err := r.st.checkFeesDeletable(gone, nil); err != nil {
		return err
	}
	r.d.fees.deleteWhere(func(x *invoice.Fee) bool { return gone[x.ID] })
	return nil
}

// invoiceAppliedTaxRepo implements invoice.AppliedTaxRepository.
type invoiceAppliedTaxRepo struct{ d *invoiceData }

func (r invoiceAppliedTaxRepo) ReplaceForInvoice(_ context.Context, org core.OrganizationID, inv core.InvoiceID, taxes []*invoice.AppliedTax) error {
	r.d.appliedTaxes.deleteWhere(func(x *invoice.AppliedTax) bool { return x.OrganizationID == org && x.InvoiceID == inv })
	for _, t := range taxes {
		c := *t
		c.OrganizationID, c.InvoiceID = org, inv
		r.d.appliedTaxes.put(c.ID, c)
	}
	return nil
}

func (r invoiceAppliedTaxRepo) ListByInvoice(_ context.Context, org core.OrganizationID, inv core.InvoiceID) ([]*invoice.AppliedTax, error) {
	return r.d.appliedTaxes.find(func(x *invoice.AppliedTax) bool { return x.OrganizationID == org && x.InvoiceID == inv }), nil
}

// adjustedFeeRepo implements invoice.AdjustedFeeRepository.
type adjustedFeeRepo struct{ d *invoiceData }

func (r adjustedFeeRepo) Create(_ context.Context, a *invoice.AdjustedFee) error {
	if r.d.adjustedFees.has(a.ID) {
		return conflict("adjusted_fee", "id")
	}
	if !a.FeeID.IsZero() && r.d.adjustedFees.any(func(x *invoice.AdjustedFee) bool {
		return x.OrganizationID == a.OrganizationID && x.FeeID == a.FeeID
	}) {
		return conflict("adjusted_fee", "fee_id")
	}
	r.d.adjustedFees.put(a.ID, *a)
	return nil
}

func (r adjustedFeeRepo) ListByInvoice(_ context.Context, org core.OrganizationID, inv core.InvoiceID) ([]*invoice.AdjustedFee, error) {
	return r.d.adjustedFees.find(func(x *invoice.AdjustedFee) bool { return x.OrganizationID == org && x.InvoiceID == inv }), nil
}

func (r adjustedFeeRepo) Delete(_ context.Context, org core.OrganizationID, id core.AdjustedFeeID) error {
	a, ok := r.d.adjustedFees.get(id)
	if !ok || a.OrganizationID != org {
		return notFound("adjusted_fee", id)
	}
	r.d.adjustedFees.del(id)
	return nil
}

func (r adjustedFeeRepo) DeleteByInvoice(_ context.Context, org core.OrganizationID, inv core.InvoiceID) error {
	r.d.adjustedFees.deleteWhere(func(x *invoice.AdjustedFee) bool { return x.OrganizationID == org && x.InvoiceID == inv })
	return nil
}

// invoiceSettlementRepo implements invoice.SettlementRepository.
type invoiceSettlementRepo struct{ d *invoiceData }

func (r invoiceSettlementRepo) Create(_ context.Context, s *invoice.Settlement) error {
	if r.d.settlements.has(s.ID) {
		return conflict("invoice_settlement", "id")
	}
	r.d.settlements.put(s.ID, *s)
	return nil
}

func (r invoiceSettlementRepo) ListByInvoice(_ context.Context, org core.OrganizationID, inv core.InvoiceID) ([]*invoice.Settlement, error) {
	return r.d.settlements.find(func(x *invoice.Settlement) bool { return x.OrganizationID == org && x.InvoiceID == inv }), nil
}

// invoiceCustomSectionRepo implements invoice.CustomSectionRepository.
type invoiceCustomSectionRepo struct{ d *invoiceData }

func (r invoiceCustomSectionRepo) checkCode(s *invoice.CustomSection) error {
	if r.d.customSections.any(func(x *invoice.CustomSection) bool {
		return x.ID != s.ID && x.OrganizationID == s.OrganizationID && x.Code == s.Code && x.DeletedAt == nil && s.DeletedAt == nil
	}) {
		return conflict("invoice_custom_section", "code")
	}
	return nil
}

func (r invoiceCustomSectionRepo) Create(_ context.Context, s *invoice.CustomSection) error {
	if r.d.customSections.has(s.ID) {
		return conflict("invoice_custom_section", "id")
	}
	if err := r.checkCode(s); err != nil {
		return err
	}
	r.d.customSections.put(s.ID, *s)
	return nil
}

func (r invoiceCustomSectionRepo) Update(_ context.Context, s *invoice.CustomSection) error {
	cur, ok := r.d.customSections.get(s.ID)
	if !ok || cur.OrganizationID != s.OrganizationID || cur.DeletedAt != nil {
		return notFound("invoice_custom_section", s.ID)
	}
	if err := r.checkCode(s); err != nil {
		return err
	}
	r.d.customSections.put(s.ID, *s)
	return nil
}

func (r invoiceCustomSectionRepo) Get(_ context.Context, org core.OrganizationID, id core.InvoiceCustomSectionID) (*invoice.CustomSection, error) {
	s, ok := r.d.customSections.get(id)
	if !ok || s.OrganizationID != org || s.DeletedAt != nil {
		return nil, notFound("invoice_custom_section", id)
	}
	return s, nil
}

func (r invoiceCustomSectionRepo) GetByCode(_ context.Context, org core.OrganizationID, code string) (*invoice.CustomSection, error) {
	s, ok := r.d.customSections.first(func(x *invoice.CustomSection) bool {
		return x.OrganizationID == org && x.Code == code && x.DeletedAt == nil
	})
	if !ok {
		return nil, notFound("invoice_custom_section", stringer(code))
	}
	return s, nil
}

func (r invoiceCustomSectionRepo) ListByIDs(_ context.Context, org core.OrganizationID, ids []core.InvoiceCustomSectionID) ([]*invoice.CustomSection, error) {
	return r.d.customSections.find(func(x *invoice.CustomSection) bool {
		return x.OrganizationID == org && x.DeletedAt == nil && slices.Contains(ids, x.ID)
	}), nil
}

func (r invoiceCustomSectionRepo) List(_ context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*invoice.CustomSection], error) {
	rows := r.d.customSections.find(func(x *invoice.CustomSection) bool { return x.OrganizationID == org && x.DeletedAt == nil })
	return paginate(rows, func(x *invoice.CustomSection) core.InvoiceCustomSectionID { return x.ID }, page)
}

func (r invoiceCustomSectionRepo) ListManual(_ context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*invoice.CustomSection], error) {
	rows := r.d.customSections.find(func(x *invoice.CustomSection) bool {
		return x.OrganizationID == org && x.SectionType == invoice.CustomSectionTypeManual && x.DeletedAt == nil
	})
	return paginateByName(rows, func(x *invoice.CustomSection) nameKey { return nameKey{x.Name, x.CreatedAt, x.ID.UUID()} }, page)
}

// Delete soft-deletes the section; the selections keep its ID, which no
// read resolves any more (the ports do not document the cascade).
func (r invoiceCustomSectionRepo) Delete(_ context.Context, org core.OrganizationID, id core.InvoiceCustomSectionID, at time.Time) error {
	s, ok := r.d.customSections.get(id)
	if !ok || s.OrganizationID != org || s.DeletedAt != nil {
		return notFound("invoice_custom_section", id)
	}
	s.DeletedAt = &at
	s.UpdatedAt = at
	r.d.customSections.put(id, *s)
	return nil
}

// errorDetailRepo implements invoice.ErrorDetailRepository.
type errorDetailRepo struct{ d *invoiceData }

func (r errorDetailRepo) Create(_ context.Context, e *invoice.ErrorDetail) error {
	if r.d.errorDetails.has(e.ID) {
		return conflict("error_detail", "id")
	}
	r.d.errorDetails.put(e.ID, *e)
	return nil
}

func (r errorDetailRepo) ListByInvoice(_ context.Context, org core.OrganizationID, inv core.InvoiceID) ([]*invoice.ErrorDetail, error) {
	return r.d.errorDetails.find(func(x *invoice.ErrorDetail) bool { return x.OrganizationID == org && x.InvoiceID == inv }), nil
}

func (r errorDetailRepo) DeleteByInvoice(_ context.Context, org core.OrganizationID, inv core.InvoiceID, code invoice.ErrorCode) error {
	r.d.errorDetails.deleteWhere(func(x *invoice.ErrorDetail) bool {
		return x.OrganizationID == org && x.InvoiceID == inv && (code == "" || x.Code == code)
	})
	return nil
}

// invoiceCreditRepo implements invoice.CreditRepository.
type invoiceCreditRepo struct{ d *invoiceData }

func (r invoiceCreditRepo) Create(_ context.Context, c *invoice.Credit) error {
	if r.d.credits.has(c.ID) {
		return conflict("credit", "id")
	}
	r.d.credits.put(c.ID, *c)
	return nil
}

func (r invoiceCreditRepo) list(pred func(*invoice.Credit) bool) []*invoice.Credit {
	out := r.d.credits.find(pred)
	slices.SortStableFunc(out, func(a, b *invoice.Credit) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out
}

func (r invoiceCreditRepo) ListByInvoice(_ context.Context, org core.OrganizationID, inv core.InvoiceID) ([]*invoice.Credit, error) {
	return r.list(func(c *invoice.Credit) bool { return c.OrganizationID == org && c.InvoiceID == inv }), nil
}

func (r invoiceCreditRepo) ListByAppliedCoupon(_ context.Context, org core.OrganizationID, applied core.AppliedCouponID) ([]*invoice.Credit, error) {
	return r.list(func(c *invoice.Credit) bool { return c.OrganizationID == org && c.AppliedCouponID == applied }), nil
}

func (r invoiceCreditRepo) ListByCreditNote(_ context.Context, org core.OrganizationID, cn core.CreditNoteID) ([]*invoice.Credit, error) {
	return r.list(func(c *invoice.Credit) bool { return c.OrganizationID == org && c.CreditNoteID == cn }), nil
}

func (r invoiceCreditRepo) ListByProgressiveBillingInvoice(_ context.Context, org core.OrganizationID, inv core.InvoiceID) ([]*invoice.Credit, error) {
	return r.list(func(c *invoice.Credit) bool {
		return c.OrganizationID == org && c.ProgressiveBillingInvoiceID == inv
	}), nil
}

func (r invoiceCreditRepo) DeleteByInvoice(_ context.Context, org core.OrganizationID, inv core.InvoiceID) error {
	r.d.credits.deleteWhere(func(c *invoice.Credit) bool { return c.OrganizationID == org && c.InvoiceID == inv })
	return nil
}
