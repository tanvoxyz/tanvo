package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// Codes of the documents that cannot be built (lago
// Integrations::Aggregator::BasePayload::Failure).
const (
	codeInvalidMapping = "invalid_mapping"
	codeInvoiceMissing = "invoice_missing"
)

// Documents renders the API representation of invoices and credit notes
// sent with them to NetSuite (lago fullInvoicePayload and
// fullCreditNotePayload: V1::InvoiceSerializer and CreditNoteSerializer).
// apps/server implements it with the API's serializers.
type Documents interface {
	// InvoiceDocument returns the API representation of inv.
	InvoiceDocument(ctx context.Context, inv *invoice.Invoice) (json.RawMessage, error)
	// CreditNoteDocument returns the API representation of cn.
	CreditNoteDocument(ctx context.Context, cn *creditnote.CreditNote) (json.RawMessage, error)
}

// WithDocuments sets the renderer of the API representations sent to
// NetSuite; without it they are left out.
func WithDocuments(d Documents) Option { return func(s *Service) { s.docs = d } }

// humanize is ActiveSupport's String#humanize of a failure code
// ("invalid_mapping" → "Invalid mapping").
func humanize(code string) string {
	s := strings.ReplaceAll(strings.TrimSuffix(code, "_id"), "_", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + strings.ToLower(s[1:])
}

// defaultLink returns the customer's default link of category cat, else
// its first (lago customer.integration_customers.<kind>.first).
func defaultLink(ctx context.Context, r port.Repos, c *customer.Customer, cat integration.Category) (*integration.IntegrationCustomer, error) {
	ics, err := r.IntegrationCustomers.ListByCustomer(ctx, c.OrganizationID, c.ID, cat)
	if err != nil {
		return nil, fmt.Errorf("load %s links of customer %s: %w", cat, c.ID, err)
	}
	if len(ics) == 0 {
		return nil, nil
	}
	if i := slices.IndexFunc(ics, func(ic *integration.IntegrationCustomer) bool { return ic.IsDefault }); i >= 0 {
		return ics[i], nil
	}
	return ics[0], nil
}

// docEnv is what an accounting or CRM sync needs, loaded in one
// transaction.
type docEnv struct {
	c      *customer.Customer
	ic     *integration.IntegrationCustomer
	i      *integration.Integration
	conn   integration.Connection
	slug   string
	mapper *itemMapper
	names  *feeNames
}

// loadDoc loads the customer c's link of category cat; nil when it has
// none.
func (s *Service) loadDoc(ctx context.Context, r port.Repos, c *customer.Customer, cat integration.Category) (*docEnv, error) {
	ic, err := defaultLink(ctx, r, c, cat)
	if err != nil || ic == nil {
		return nil, err
	}
	i, conn, err := s.loadConnection(ctx, r, c.OrganizationID, ic.IntegrationID)
	if err != nil {
		return nil, err
	}
	be, err := billingEntityOf(ctx, r, c)
	if err != nil {
		return nil, err
	}
	m, err := newItemMapper(ctx, r, c.OrganizationID, i.ID, be.ID)
	if err != nil {
		return nil, err
	}
	o, err := r.Organizations.Get(ctx, c.OrganizationID)
	if err != nil {
		return nil, fmt.Errorf("load organization %s: %w", c.OrganizationID, err)
	}
	return &docEnv{c: c, ic: ic, i: i, conn: conn, slug: o.Slug, mapper: m, names: newFeeNames(r, c.OrganizationID, m)}, nil
}

// items returns the collection mappings of a document.
func (e *docEnv) items() integration.DocumentItems {
	m := e.mapper
	out := integration.DocumentItems{
		Coupon:        m.collection(integration.CollectionMappingTypeCoupon),
		PrepaidCredit: m.collection(integration.CollectionMappingTypePrepaidCredit),
		CreditNote:    m.collection(integration.CollectionMappingTypeCreditNote),
		Account:       m.collection(integration.CollectionMappingTypeAccount),
	}
	if c := integration.LookupCollection(m.collections, m.be, integration.CollectionMappingTypeTax); c != nil {
		out.Tax = &integration.TaxItem{Nexus: deref(c.TaxNexus), Type: deref(c.TaxType), Code: deref(c.TaxCode)}
	}
	if c := integration.LookupCollection(m.collections, m.be, integration.CollectionMappingTypeCurrencies); c != nil {
		out.Currencies = c.Currencies
	}
	return out
}

// accountingFee returns the neutral fee of f. A credit fee maps as the
// prepaid credit item (lago base_payload.rb#item: credit_item).
func (e *docEnv) accountingFee(ctx context.Context, f *invoice.Fee) (integration.AccountingFee, error) {
	var (
		it  *integration.ExternalItem
		err error
	)
	if f.Type == core.FeeTypeCredit {
		it = e.mapper.collection(integration.CollectionMappingTypePrepaidCredit)
	} else if it, err = e.mapper.fee(ctx, f); err != nil {
		return integration.AccountingFee{}, err
	}
	refs, err := e.names.refs(ctx, f)
	if err != nil {
		return integration.AccountingFee{}, err
	}
	from, to := f.Properties.From, f.Properties.To
	if f.Type == core.FeeTypeCharge {
		from, to = f.Properties.ChargesFrom, f.Properties.ChargesTo
	}
	return integration.AccountingFee{
		ID: f.ID, Type: f.Type, Item: it,
		InvoiceName: refs.InvoiceName(f), FilterDisplayName: refs.FilterDisplayName(", "),
		GroupedByDisplay: invoice.GroupedByDisplay(f), ItemName: refs.ItemName(f), ItemSource: itemSource(refs, f),
		Units: f.Units, PreciseUnitAmount: f.PreciseUnitAmount, AmountCents: f.Amount,
		TaxesAmountCents: f.TaxesAmount, TaxesRate: f.TaxesRate, From: from, To: to, CreatedAt: f.CreatedAt,
	}, nil
}

// itemSource is lago Fee#item_source (app/models/fee.rb:149-156).
func itemSource(r invoice.FeeRefs, f *invoice.Fee) string {
	switch f.Type {
	case core.FeeTypeFixedCharge, core.FeeTypeAddOn:
		if r.AddOn != nil {
			return r.AddOn.Code
		}
	case core.FeeTypeCredit:
		return "consumed_credits"
	}
	if r.Plan != nil && r.Plan.Code != "" {
		return r.Plan.Code
	}
	if r.Metric != nil {
		return r.Metric.Code
	}
	return ""
}

// accountingInvoice returns the neutral document of inv.
func (s *Service) accountingInvoice(ctx context.Context, r port.Repos, e *docEnv, inv *invoice.Invoice) (*integration.AccountingInvoice, error) {
	org := inv.OrganizationID
	fees, err := r.Fees.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return nil, fmt.Errorf("load fees of invoice %s: %w", inv.ID, err)
	}
	// Every fee, oldest first: NetSuite keeps the positive ones
	// (invoices/payloads/base_payload.rb:40-46), Xero all of them
	// (invoices/payloads/xero.rb:43-45).
	slices.SortStableFunc(fees, func(a, b *invoice.Fee) int { return a.CreatedAt.Compare(b.CreatedAt) })
	out := &integration.AccountingInvoice{
		ID: inv.ID, Number: inv.Number, ExternalCustomerID: e.ic.ExternalCustomerID,
		IssuingDate: inv.IssuingDate, PaymentDueDate: inv.PaymentDueDate, Currency: inv.Currency,
		PurchaseOrderNumber: deref(inv.PurchaseOrderNumber), URL: s.invoiceURL(e.slug, inv),
		TaxesAmountCents: inv.TaxesAmount, TaxesRate: inv.TaxesRate, CouponsAmountCents: inv.CouponsAmount,
		PrepaidCreditAmountCents: inv.PrepaidCreditAmount, ProgressiveBillingCreditAmountCents: inv.ProgressiveBillingCreditAmount,
		CreditNotesAmountCents: inv.CreditNotesAmount, Items: e.items(),
	}
	for _, f := range fees {
		af, err := e.accountingFee(ctx, f)
		if err != nil {
			return nil, err
		}
		out.Fees = append(out.Fees, af)
	}
	subs, err := r.InvoiceSubscriptions.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return nil, fmt.Errorf("load subscriptions of invoice %s: %w", inv.ID, err)
	}
	for _, is := range subs {
		sub, err := e.names.subscription(ctx, is.SubscriptionID)
		if err != nil {
			return nil, err
		}
		p, err := e.names.plan(ctx, sub.PlanID)
		if err != nil {
			return nil, err
		}
		out.PlanCodes = append(out.PlanCodes, p.Code)
	}
	credits, err := r.Credits.ListByInvoice(ctx, org, inv.ID)
	if err != nil {
		return nil, fmt.Errorf("load credits of invoice %s: %w", inv.ID, err)
	}
	for _, cr := range credits {
		switch {
		case !cr.AppliedCouponID.IsZero():
			name, err := couponName(ctx, r, org, cr.AppliedCouponID)
			if err != nil {
				return nil, err
			}
			out.CouponNames = append(out.CouponNames, name)
		case !cr.ProgressiveBillingInvoiceID.IsZero():
			pi, err := r.Invoices.Get(ctx, org, cr.ProgressiveBillingInvoiceID)
			if err != nil {
				return nil, fmt.Errorf("load invoice %s: %w", cr.ProgressiveBillingInvoiceID, err)
			}
			out.ProgressiveBillingNames = append(out.ProgressiveBillingNames, pi.Number)
		case !cr.CreditNoteID.IsZero():
			cn, err := r.CreditNotes.Get(ctx, org, cr.CreditNoteID)
			if err != nil {
				return nil, fmt.Errorf("load credit note %s: %w", cr.CreditNoteID, err)
			}
			ci, err := r.Invoices.Get(ctx, org, cn.InvoiceID)
			if err != nil {
				return nil, fmt.Errorf("load invoice %s: %w", cn.InvoiceID, err)
			}
			out.CreditNoteNames = append(out.CreditNoteNames, ci.Number)
		}
	}
	if s.docs != nil {
		if out.Document, err = s.docs.InvoiceDocument(ctx, inv); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// couponName is the name of the coupon of an applied coupon (lago
// Credit#item_name).
func couponName(ctx context.Context, r port.Repos, org core.OrganizationID, id core.AppliedCouponID) (string, error) {
	ac, err := r.AppliedCoupons.Get(ctx, org, id)
	if err != nil {
		return "", fmt.Errorf("load applied coupon %s: %w", id, err)
	}
	c, err := r.Coupons.Get(ctx, org, ac.CouponID)
	if err != nil {
		return "", fmt.Errorf("load coupon %s: %w", ac.CouponID, err)
	}
	return c.Name, nil
}

// invoiceURL is lago Invoice#web_url.
func (s *Service) invoiceURL(slug string, inv *invoice.Invoice) string {
	return s.link(slug, "customer", inv.CustomerID.String(), "invoice", inv.ID.String(), "overview")
}

// syncDoc is one accounting document to create.
type syncDoc struct {
	rt       integration.ResourceType
	syncable fmt.Stringer
	// failRaise lists the HTTP statuses re-raised for a retry (credit notes
	// and payments: 500 and 424); nil re-raises every 5xx and 424 but a
	// rejected login (invoices).
	failRaise []int
	// payloadFails reports whether a document that cannot be built fails
	// the job without retry (invoices) instead of succeeding (credit
	// notes); payments retry it.
	payloadFails bool
}

// timedOut marks err, a call of i that got no answer, [ErrSyncTimeout]
// (lago Net::ReadTimeout); other errors are returned unchanged.
func timedOut(i *integration.Integration, err error) error {
	pe, ok := integration.AsProviderError(err)
	if !ok || pe.HTTPStatus != 0 || pe.Kind != integration.KindUnavailable {
		return err
	}
	if i.Provider == integration.ProviderNetsuite {
		return fmt.Errorf("%w: %w: %w", ErrSyncTimeout, errNetSuiteTimeout, err)
	}
	return fmt.Errorf("%w: %w", ErrSyncTimeout, err)
}

// syncFailure handles the failure err of an accounting sync of e (lago
// aggregator/{invoices,credit_notes,payments}/create_service.rb rescues).
func (s *Service) syncFailure(ctx context.Context, e *docEnv, d syncDoc, err error) error {
	switch {
	case errors.Is(err, integration.ErrRequestLimit), errors.Is(err, context.Canceled):
		return err
	case errors.Is(err, integration.ErrNoMapping):
		if err := s.emitCustomerError(ctx, e.c, e.i, ProviderErrorDetails{ErrorCode: codeInvalidMapping, Message: humanize(codeInvalidMapping)}); err != nil {
			return err
		}
		if d.payloadFails {
			return fmt.Errorf("integration %s: sync %s: %s", e.i.Code, d.rt, codeInvalidMapping)
		}
		return nil
	}
	pe, ok := integration.AsProviderError(err)
	if !ok {
		return err
	}
	if pe.HTTPStatus == 0 && pe.Kind == integration.KindUnavailable {
		// Net::ReadTimeout escapes the services' rescues.
		return timedOut(e.i, err)
	}
	det := errorDetails(err)
	if err := s.emitCustomerError(ctx, e.c, e.i, det); err != nil {
		return err
	}
	if det.ErrorCode == codeValidationError {
		return nil
	}
	retried := slices.Contains(d.failRaise, pe.HTTPStatus)
	if d.failRaise == nil {
		retried = (pe.HTTPStatus >= 500 || pe.HTTPStatus == 424) && pe.Code != invalidLoginAttempt
	}
	if retried {
		return fmt.Errorf("%w: %w", ErrSyncRetryable, err)
	}
	if d.rt == integration.ResourceTypeInvoice {
		// non_retryable_failure!: discard_on.
		return fmt.Errorf("integration %s: sync %s %s: %s: %s", e.i.Code, d.rt, d.syncable, det.ErrorCode, det.Message)
	}
	return nil
}

// syncInvoice creates the finalized invoice id at its customer's
// accounting provider (lago Integrations::Aggregator::Invoices::CreateJob
// and CreateService). With findFirst or on a retry, NetSuite is first
// searched for an invoice of the same number an earlier attempt created
// (ReconcileService).
func (s *Service) syncInvoice(ctx context.Context, org core.OrganizationID, a kernel.SyncInvoiceArgs, retry bool) error {
	var (
		e   *docEnv
		doc *integration.AccountingInvoice
		inv *invoice.Invoice
	)
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if inv, err = r.Invoices.Get(ctx, org, a.InvoiceID); err != nil {
			return fmt.Errorf("load invoice %s: %w", a.InvoiceID, err)
		}
		c, err := r.Customers.Get(ctx, org, inv.CustomerID)
		if err != nil {
			return fmt.Errorf("load customer %s: %w", inv.CustomerID, err)
		}
		if e, err = s.loadDoc(ctx, r, c, integration.CategoryAccounting); err != nil || e == nil {
			return err
		}
		if !e.i.SyncInvoices || inv.Status != invoice.StatusFinalized {
			e = nil
			return nil
		}
		if done, err := r.IntegrationResources.Find(ctx, org, e.i.ID, integration.ResourceTypeInvoice, inv.ID.UUID()); err == nil && done != nil {
			e = nil
			return nil
		} else if err != nil && !errors.Is(err, core.ErrNotFound) {
			return fmt.Errorf("find synced invoice %s: %w", inv.ID, err)
		}
		doc, err = s.accountingInvoice(ctx, r, e, inv)
		return err
	})
	if err != nil {
		if errors.Is(err, integration.ErrNoMapping) && e != nil {
			return s.syncFailure(ctx, e, syncDoc{rt: integration.ResourceTypeInvoice, syncable: inv.ID, payloadFails: true}, err)
		}
		return err
	}
	if e == nil {
		return nil
	}
	g, err := s.accounting(e.i)
	if err != nil {
		return err
	}
	if a.FindFirst || retry {
		if f, ok := g.(integration.InvoiceFinder); ok {
			id, found, err := f.FindInvoice(ctx, e.conn, inv.Number)
			if err != nil {
				return retriedFailure(timedOut(e.i, err))
			}
			if found {
				return s.recorded(ctx, e.i, integration.ResourceTypeInvoice, inv.ID.UUID(), id)
			}
		}
	}
	id, err := g.SyncInvoice(ctx, e.conn, doc)
	if err != nil {
		return s.syncFailure(ctx, e, syncDoc{rt: integration.ResourceTypeInvoice, syncable: inv.ID, payloadFails: true}, err)
	}
	return s.recorded(ctx, e.i, integration.ResourceTypeInvoice, inv.ID.UUID(), id)
}

// recorded records the provider's record id of a synced document.
func (s *Service) recorded(ctx context.Context, i *integration.Integration, rt integration.ResourceType, syncable [16]byte, id string) error {
	if id == "" {
		return nil
	}
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		return s.recordResource(ctx, r, i, rt, syncable, id)
	})
}

// syncCreditNote creates the finalized credit note id at its customer's
// accounting provider (lago Integrations::Aggregator::CreditNotes::
// CreateJob and CreateService).
func (s *Service) syncCreditNote(ctx context.Context, org core.OrganizationID, id core.CreditNoteID) error {
	var (
		e   *docEnv
		doc *integration.AccountingCreditNote
	)
	sd := syncDoc{rt: integration.ResourceTypeCreditNote, syncable: id, failRaise: []int{500, 424}}
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		cn, err := r.CreditNotes.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load credit note %s: %w", id, err)
		}
		c, err := r.Customers.Get(ctx, org, cn.CustomerID)
		if err != nil {
			return fmt.Errorf("load customer %s: %w", cn.CustomerID, err)
		}
		if e, err = s.loadDoc(ctx, r, c, integration.CategoryAccounting); err != nil || e == nil {
			return err
		}
		if !e.i.SyncCreditNotes || cn.Status != creditnote.StatusFinalized {
			e = nil
			return nil
		}
		if done, err := r.IntegrationResources.Find(ctx, org, e.i.ID, integration.ResourceTypeCreditNote, cn.ID.UUID()); err == nil && done != nil {
			e = nil
			return nil
		} else if err != nil && !errors.Is(err, core.ErrNotFound) {
			return fmt.Errorf("find synced credit note %s: %w", cn.ID, err)
		}
		doc, err = s.accountingCreditNote(ctx, r, e, cn)
		return err
	})
	if err != nil {
		if errors.Is(err, integration.ErrNoMapping) && e != nil {
			return s.syncFailure(ctx, e, sd, err)
		}
		return err
	}
	if e == nil {
		return nil
	}
	g, err := s.accounting(e.i)
	if err != nil {
		return err
	}
	ext, err := g.SyncCreditNote(ctx, e.conn, doc)
	if err != nil {
		return s.syncFailure(ctx, e, sd, err)
	}
	return s.recorded(ctx, e.i, integration.ResourceTypeCreditNote, id.UUID(), ext)
}

// accountingCreditNote returns the neutral document of cn.
func (s *Service) accountingCreditNote(ctx context.Context, r port.Repos, e *docEnv, cn *creditnote.CreditNote) (*integration.AccountingCreditNote, error) {
	org := cn.OrganizationID
	out := &integration.AccountingCreditNote{
		ID: cn.ID, Number: cn.Number, ExternalCustomerID: e.ic.ExternalCustomerID, IssuingDate: cn.IssuingDate,
		Currency: cn.Currency, TaxesAmountCents: cn.TaxesAmount, TaxesRate: cn.TaxesRate,
		CouponsAdjustmentAmountCents: cn.CouponsAdjustmentAmount, Collections: e.items(),
	}
	for _, it := range cn.Items {
		f, err := r.Fees.Get(ctx, org, it.FeeID)
		if err != nil {
			return nil, fmt.Errorf("load fee %s: %w", it.FeeID, err)
		}
		af, err := e.accountingFee(ctx, f)
		if err != nil {
			return nil, err
		}
		if af.Item == nil {
			return nil, fmt.Errorf("fee %s: %w", f.ID, integration.ErrNoMapping)
		}
		out.Items = append(out.Items, integration.AccountingCreditNoteItem{
			ID: it.ID.String(), Fee: af, AmountCents: it.Amount, CreatedAt: cn.CreatedAt,
		})
	}
	if cn.CouponsAdjustmentAmount > 0 {
		// The coupon line names the coupons credited on the invoice (lago
		// credit_notes/payloads/netsuite.rb:152).
		credits, err := r.Credits.ListByInvoice(ctx, org, cn.InvoiceID)
		if err != nil {
			return nil, fmt.Errorf("load credits of invoice %s: %w", cn.InvoiceID, err)
		}
		for _, cr := range credits {
			if cr.AppliedCouponID.IsZero() {
				continue
			}
			name, err := couponName(ctx, r, org, cr.AppliedCouponID)
			if err != nil {
				return nil, err
			}
			out.CouponNames = append(out.CouponNames, name)
		}
	}
	if s.docs != nil {
		var err error
		if out.Document, err = s.docs.CreditNoteDocument(ctx, cn); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// EnqueuePaymentSync enqueues, in the transaction of r, the sync of the
// succeeded payment p of the finalized invoice inv to the customer's
// accounting provider when it syncs payments (lago
// Payment#should_sync_payment?); it implements payments.PaymentSync.
func (s *Service) EnqueuePaymentSync(ctx context.Context, r port.Repos, p *payment.Payment, inv *invoice.Invoice) error {
	if p.Payable.Type != payment.PayableTypeInvoice || inv == nil || inv.Status != invoice.StatusFinalized || p.Status != payment.StateSucceeded {
		return nil
	}
	c, err := r.Customers.Get(ctx, inv.OrganizationID, inv.CustomerID)
	if err != nil {
		return fmt.Errorf("load customer %s: %w", inv.CustomerID, err)
	}
	on, err := syncs(ctx, r, c, integration.CategoryAccounting, func(i *integration.Integration) bool { return i.SyncPayments })
	if err != nil || !on {
		return err
	}
	return kernel.Enqueue(ctx, r, p.OrganizationID, kernel.JobSyncPayment, kernel.PaymentArgs{PaymentID: p.ID}, "payment:"+p.ID.String()+":accounting", time.Time{})
}

// syncs reports whether c has a link of category cat whose integration
// satisfies on.
func syncs(ctx context.Context, r port.Repos, c *customer.Customer, cat integration.Category, on func(*integration.Integration) bool) (bool, error) {
	ics, err := r.IntegrationCustomers.ListByCustomer(ctx, c.OrganizationID, c.ID, cat)
	if err != nil {
		return false, fmt.Errorf("load %s links of customer %s: %w", cat, c.ID, err)
	}
	for _, ic := range ics {
		i, err := r.Integrations.Get(ctx, c.OrganizationID, ic.IntegrationID)
		if err != nil {
			return false, fmt.Errorf("load integration %s: %w", ic.IntegrationID, err)
		}
		if on(i) {
			return true, nil
		}
	}
	return false, nil
}

// syncPayment creates the succeeded payment id at its customer's
// accounting provider (lago Integrations::Aggregator::Payments::CreateJob
// and CreateService). A payment whose invoice is not synced yet is
// retried (invoice_missing).
func (s *Service) syncPayment(ctx context.Context, org core.OrganizationID, id core.PaymentID) error {
	var (
		e   *docEnv
		doc *integration.AccountingPayment
	)
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := r.Payments.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load payment %s: %w", id, err)
		}
		if p.Payable.Type != payment.PayableTypeInvoice {
			return nil
		}
		inv, err := r.Invoices.Get(ctx, org, p.Payable.InvoiceID)
		if err != nil {
			return fmt.Errorf("load invoice %s: %w", p.Payable.InvoiceID, err)
		}
		c, err := r.Customers.Get(ctx, org, inv.CustomerID)
		if err != nil {
			return fmt.Errorf("load customer %s: %w", inv.CustomerID, err)
		}
		if e, err = s.loadDoc(ctx, r, c, integration.CategoryAccounting); err != nil || e == nil {
			return err
		}
		if !e.i.SyncPayments || inv.Status != invoice.StatusFinalized {
			e = nil
			return nil
		}
		if done, err := r.IntegrationResources.Find(ctx, org, e.i.ID, integration.ResourceTypePayment, p.ID.UUID()); err == nil && done != nil {
			e = nil
			return nil
		} else if err != nil && !errors.Is(err, core.ErrNotFound) {
			return fmt.Errorf("find synced payment %s: %w", p.ID, err)
		}
		ir, err := r.IntegrationResources.Find(ctx, org, e.i.ID, integration.ResourceTypeInvoice, inv.ID.UUID())
		if errors.Is(err, core.ErrNotFound) || err == nil && ir == nil {
			return fmt.Errorf("payment %s: %s: %w", p.ID, codeInvoiceMissing, ErrSyncPayload)
		} else if err != nil {
			return fmt.Errorf("find synced invoice %s: %w", inv.ID, err)
		}
		doc = &integration.AccountingPayment{
			ID: p.ID, InvoiceExternalID: ir.ExternalID, ExternalCustomerID: e.ic.ExternalCustomerID,
			Account:     e.mapper.collection(integration.CollectionMappingTypeAccount),
			AmountCents: p.Amount, Currency: p.Currency, CreatedAt: p.CreatedAt,
		}
		return nil
	})
	if err != nil || e == nil {
		return err
	}
	g, err := s.accounting(e.i)
	if err != nil {
		return err
	}
	ext, err := g.SyncPayment(ctx, e.conn, doc)
	if err != nil {
		return s.syncFailure(ctx, e, syncDoc{rt: integration.ResourceTypePayment, syncable: id, failRaise: []int{500, 424}}, err)
	}
	return s.recorded(ctx, e.i, integration.ResourceTypePayment, id.UUID(), ext)
}

// feeNames loads the objects the names of fees are read from, caching
// them (lago Fee#invoice_name, #item_name and #item_source).
type feeNames struct {
	r       port.Repos
	org     core.OrganizationID
	m       *itemMapper
	metrics map[core.BillableMetricID]*catalog.BillableMetric
	addOns  map[core.AddOnID]*catalog.AddOn
	subs    map[core.SubscriptionID]*subscription.Subscription
	plans   map[core.PlanID]*catalog.Plan
}

func newFeeNames(r port.Repos, org core.OrganizationID, m *itemMapper) *feeNames {
	return &feeNames{
		r: r, org: org, m: m,
		metrics: map[core.BillableMetricID]*catalog.BillableMetric{}, addOns: map[core.AddOnID]*catalog.AddOn{},
		subs: map[core.SubscriptionID]*subscription.Subscription{}, plans: map[core.PlanID]*catalog.Plan{},
	}
}

func (n *feeNames) subscription(ctx context.Context, id core.SubscriptionID) (*subscription.Subscription, error) {
	if s, ok := n.subs[id]; ok {
		return s, nil
	}
	s, err := n.r.Subscriptions.Get(ctx, n.org, id)
	if err != nil {
		return nil, fmt.Errorf("load subscription %s: %w", id, err)
	}
	n.subs[id] = s
	return s, nil
}

func (n *feeNames) plan(ctx context.Context, id core.PlanID) (*catalog.Plan, error) {
	if p, ok := n.plans[id]; ok {
		return p, nil
	}
	p, err := n.r.Plans.Get(ctx, n.org, id)
	if err != nil {
		return nil, fmt.Errorf("load plan %s: %w", id, err)
	}
	n.plans[id] = p
	return p, nil
}

func (n *feeNames) addOn(ctx context.Context, id core.AddOnID) (*catalog.AddOn, error) {
	if a, ok := n.addOns[id]; ok {
		return a, nil
	}
	a, err := n.r.AddOns.Get(ctx, n.org, id)
	if err != nil {
		return nil, fmt.Errorf("load add-on %s: %w", id, err)
	}
	n.addOns[id] = a
	return a, nil
}

// refs loads the name references of f.
func (n *feeNames) refs(ctx context.Context, f *invoice.Fee) (invoice.FeeRefs, error) {
	var (
		refs invoice.FeeRefs
		err  error
	)
	if !f.SubscriptionID.IsZero() {
		if refs.Subscription, err = n.subscription(ctx, f.SubscriptionID); err != nil {
			return refs, err
		}
		if refs.Plan, err = n.plan(ctx, refs.Subscription.PlanID); err != nil {
			return refs, err
		}
	}
	switch f.Type {
	case core.FeeTypeCharge:
		if refs.Charge, err = n.m.charge(ctx, f.ChargeID); err != nil {
			return refs, err
		}
		m, ok := n.metrics[refs.Charge.BillableMetricID]
		if !ok {
			ms, err := n.r.Metrics.ListByIDs(ctx, n.org, []core.BillableMetricID{refs.Charge.BillableMetricID})
			if err != nil {
				return refs, fmt.Errorf("load metric %s: %w", refs.Charge.BillableMetricID, err)
			}
			if len(ms) > 0 {
				m = ms[0]
			}
			n.metrics[refs.Charge.BillableMetricID] = m
		}
		refs.Metric = m
		if !f.ChargeFilterID.IsZero() {
			cf, err := n.r.Charges.GetFilter(ctx, n.org, f.ChargeFilterID)
			switch {
			case err == nil:
				refs.ChargeFilter = cf
			case !errors.Is(err, core.ErrNotFound):
				return refs, fmt.Errorf("load charge filter %s: %w", f.ChargeFilterID, err)
			}
		}
	case core.FeeTypeAddOn:
		if refs.AddOn, err = n.addOn(ctx, f.AddOnID); err != nil {
			return refs, err
		}
	case core.FeeTypeFixedCharge:
		a, err := n.m.fixedChargeAddOn(ctx, f)
		if err != nil {
			return refs, err
		}
		if fc, ok := n.m.fixed[f.FixedChargeID]; ok {
			refs.FixedCharge = fc
		}
		if refs.AddOn, err = n.addOn(ctx, a); err != nil {
			return refs, err
		}
	case core.FeeTypeCredit:
		if !f.WalletTransactionID.IsZero() {
			wt, err := n.r.WalletTransactions.Get(ctx, n.org, f.WalletTransactionID)
			switch {
			case err == nil:
				refs.WalletTransaction = wt
			case !errors.Is(err, core.ErrNotFound):
				return refs, fmt.Errorf("load wallet transaction %s: %w", f.WalletTransactionID, err)
			}
		}
	}
	return refs, nil
}
