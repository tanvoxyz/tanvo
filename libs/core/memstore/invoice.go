package memstore

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
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
	errorDetails   *table[core.ErrorDetailID, invoice.ErrorDetail]
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
			return s
		}),
		errorDetails: newTable[core.ErrorDetailID](func(e invoice.ErrorDetail) invoice.ErrorDetail {
			e.Details = slices.Clone(e.Details)
			return e
		}),
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
		errorDetails:   d.errorDetails.snapshot(),
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
)

// invoiceRepo implements invoice.Repository.
type invoiceRepo struct{ d *invoiceData }

func (r invoiceRepo) checkNumber(inv *invoice.Invoice) error {
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

func (r invoiceRepo) List(_ context.Context, org core.OrganizationID, f invoice.Filter, page core.PageRequest) (core.Page[*invoice.Invoice], error) {
	rows := r.d.invoices.find(func(x *invoice.Invoice) bool {
		return x.OrganizationID == org &&
			(f.IncludeGenerating || x.Visible()) &&
			(f.CustomerID.IsZero() || x.CustomerID == f.CustomerID) &&
			(f.BillingEntityID.IsZero() || x.BillingEntityID == f.BillingEntityID) &&
			(len(f.Statuses) == 0 || slices.Contains(f.Statuses, x.Status)) &&
			(len(f.PaymentStatuses) == 0 || slices.Contains(f.PaymentStatuses, x.PaymentStatus)) &&
			(len(f.Types) == 0 || slices.Contains(f.Types, x.Type)) &&
			(len(f.IDs) == 0 || slices.Contains(f.IDs, x.ID))
	})
	return paginate(rows, func(x *invoice.Invoice) core.InvoiceID { return x.ID }, page)
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
	r.d.invoices.del(id)
	return nil
}

// invoiceSubscriptionRepo implements invoice.SubscriptionRepository.
type invoiceSubscriptionRepo struct{ d *invoiceData }

func (r invoiceSubscriptionRepo) Create(_ context.Context, s *invoice.Subscription) error {
	if r.d.subscriptions.has(s.ID) {
		return conflict("invoice_subscription", "id")
	}
	if k, ok := s.Key(); ok && r.d.subscriptions.any(func(x *invoice.Subscription) bool {
		xk, xok := x.Key()
		return xok && x.OrganizationID == s.OrganizationID && xk == k
	}) {
		return conflict("invoice_subscription", "idempotency_key")
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
		xk, _ := x.Key()
		return x.OrganizationID == org && xk == k
	})
	if !ok {
		return nil, notFound("invoice_subscription", stringer(k.SubscriptionID.String()+"/"+string(k.Reason)))
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

// feeRepo implements invoice.FeeRepository.
type feeRepo struct{ d *invoiceData }

func (r feeRepo) Create(_ context.Context, f *invoice.Fee) error {
	if r.d.fees.has(f.ID) {
		return conflict("fee", "id")
	}
	r.d.fees.put(f.ID, *f)
	return nil
}

func (r feeRepo) Update(_ context.Context, f *invoice.Fee) error {
	cur, ok := r.d.fees.get(f.ID)
	if !ok || cur.OrganizationID != f.OrganizationID {
		return notFound("fee", f.ID)
	}
	r.d.fees.put(f.ID, *f)
	return nil
}

func (r feeRepo) Get(_ context.Context, org core.OrganizationID, id core.FeeID) (*invoice.Fee, error) {
	f, ok := r.d.fees.get(id)
	if !ok || f.OrganizationID != org {
		return nil, notFound("fee", id)
	}
	return f, nil
}

func (r feeRepo) List(_ context.Context, org core.OrganizationID, f invoice.FeeFilter, page core.PageRequest) (core.Page[*invoice.Fee], error) {
	rows := r.d.fees.find(func(x *invoice.Fee) bool {
		return x.OrganizationID == org &&
			(f.InvoiceID.IsZero() || x.InvoiceID == f.InvoiceID) &&
			(f.SubscriptionID.IsZero() || x.SubscriptionID == f.SubscriptionID) &&
			(f.ChargeID.IsZero() || x.ChargeID == f.ChargeID) &&
			(len(f.Types) == 0 || slices.Contains(f.Types, x.Type))
	})
	return paginate(rows, func(x *invoice.Fee) core.FeeID { return x.ID }, page)
}

func (r feeRepo) ListByInvoice(_ context.Context, org core.OrganizationID, inv core.InvoiceID) ([]*invoice.Fee, error) {
	return r.d.fees.find(func(x *invoice.Fee) bool { return x.OrganizationID == org && x.InvoiceID == inv }), nil
}

func (r feeRepo) ListBySubscriptionPeriod(_ context.Context, org core.OrganizationID, sub core.SubscriptionID, from time.Time) ([]*invoice.Fee, error) {
	return r.d.fees.find(func(x *invoice.Fee) bool {
		return x.OrganizationID == org && x.SubscriptionID == sub && x.Properties.From.Equal(from)
	}), nil
}

func (r feeRepo) ListUnbilledInAdvance(_ context.Context, org core.OrganizationID, before time.Time) ([]*invoice.Fee, error) {
	return r.d.fees.find(func(x *invoice.Fee) bool {
		return x.OrganizationID == org && x.InvoiceID.IsZero() && x.PayInAdvance && x.Invoiceable &&
			x.PaymentStatus == invoice.FeePaymentStatusSucceeded && x.CreatedAt.Before(before)
	}), nil
}

func (r feeRepo) ListByCharge(_ context.Context, org core.OrganizationID, charge core.ChargeID) ([]*invoice.Fee, error) {
	return r.d.fees.find(func(x *invoice.Fee) bool { return x.OrganizationID == org && x.ChargeID == charge }), nil
}

func (r feeRepo) ListByEventTransaction(_ context.Context, org core.OrganizationID, sub core.SubscriptionID, transactionID string) ([]*invoice.Fee, error) {
	return r.d.fees.find(func(x *invoice.Fee) bool {
		return x.OrganizationID == org && x.SubscriptionID == sub && x.PayInAdvanceEventTransactionID == transactionID
	}), nil
}

func (r feeRepo) DeleteByInvoice(_ context.Context, org core.OrganizationID, inv core.InvoiceID) error {
	r.d.fees.deleteWhere(func(x *invoice.Fee) bool { return x.OrganizationID == org && x.InvoiceID == inv })
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
		return x.ID != s.ID && x.OrganizationID == s.OrganizationID && x.Code == s.Code
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
	if !ok || cur.OrganizationID != s.OrganizationID {
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
	if !ok || s.OrganizationID != org {
		return nil, notFound("invoice_custom_section", id)
	}
	return s, nil
}

func (r invoiceCustomSectionRepo) GetByCode(_ context.Context, org core.OrganizationID, code string) (*invoice.CustomSection, error) {
	s, ok := r.d.customSections.first(func(x *invoice.CustomSection) bool { return x.OrganizationID == org && x.Code == code })
	if !ok {
		return nil, notFound("invoice_custom_section", stringer(code))
	}
	return s, nil
}

func (r invoiceCustomSectionRepo) ListByIDs(_ context.Context, org core.OrganizationID, ids []core.InvoiceCustomSectionID) ([]*invoice.CustomSection, error) {
	return r.d.customSections.find(func(x *invoice.CustomSection) bool { return x.OrganizationID == org && slices.Contains(ids, x.ID) }), nil
}

func (r invoiceCustomSectionRepo) List(_ context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*invoice.CustomSection], error) {
	rows := r.d.customSections.find(func(x *invoice.CustomSection) bool { return x.OrganizationID == org })
	return paginate(rows, func(x *invoice.CustomSection) core.InvoiceCustomSectionID { return x.ID }, page)
}

func (r invoiceCustomSectionRepo) Delete(_ context.Context, org core.OrganizationID, id core.InvoiceCustomSectionID) error {
	s, ok := r.d.customSections.get(id)
	if !ok || s.OrganizationID != org {
		return notFound("invoice_custom_section", id)
	}
	r.d.customSections.del(id)
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
