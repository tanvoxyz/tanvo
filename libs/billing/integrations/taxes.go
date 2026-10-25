package integrations

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/tax"
)

// Taxes computes, reports and voids the taxes of the customers linked to
// a tax integration (Anrok, Avalara) through their adapters; it
// implements [kernel.TaxProvider] (lago
// Integrations::Aggregator::Taxes). Drafts, previews and current usage
// are quoted; finalized invoices, standalone fees and credit notes are
// reported. A provider's refusal emits customer.tax_provider_error and is
// a [kernel.TaxProviderError]; its rate limits, unavailability and
// contention are [kernel.ErrTaxContention], retried by the job.
type Taxes struct {
	s     *Service
	cache *DraftTaxCache
}

var _ kernel.TaxProvider = (*Taxes)(nil)

// NewTaxes returns the tax provider over the integrations of s; cache
// keeps Anrok's draft answers and may be nil.
func NewTaxes(s *Service, cache *DraftTaxCache) *Taxes {
	t := &Taxes{s: s, cache: cache}
	s.taxes = t
	return t
}

// taxEnv is what a tax call needs, loaded in one transaction.
type taxEnv struct {
	customer *customer.Customer
	ic       *integration.IntegrationCustomer
	integ    *integration.Integration
	conn     integration.Connection
	gw       integration.TaxGateway
	env      *kernel.CustomerEnv
	mapper   *itemMapper
}

// anrok reports an Anrok integration.
func (e *taxEnv) anrok() bool { return e.integ.Provider == integration.ProviderAnrok }

// Enabled reports whether the customer is linked to a tax integration
// (lago Customer#tax_customer).
func (t *Taxes) Enabled(ctx context.Context, c *customer.Customer) (bool, error) {
	var on bool
	err := t.s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		ic, err := taxCustomer(ctx, r, c)
		on = ic != nil
		return err
	})
	return on, err
}

func taxCustomer(ctx context.Context, r port.Repos, c *customer.Customer) (*integration.IntegrationCustomer, error) {
	ics, err := r.IntegrationCustomers.ListByCustomer(ctx, c.OrganizationID, c.ID, integration.CategoryTax)
	if err != nil {
		return nil, fmt.Errorf("load tax integration customer of %s: %w", c.ID, err)
	}
	if len(ics) == 0 {
		return nil, nil
	}
	return ics[0], nil
}

// load loads the tax environment of c in the transaction of r; nil when c
// has no tax integration.
func (t *Taxes) load(ctx context.Context, r port.Repos, c *customer.Customer) (*taxEnv, error) {
	ic, err := taxCustomer(ctx, r, c)
	if err != nil || ic == nil {
		return nil, err
	}
	org := c.OrganizationID
	integ, conn, err := t.s.loadConnection(ctx, r, org, ic.IntegrationID)
	if err != nil {
		return nil, err
	}
	if t.s.gw == nil {
		return nil, fmt.Errorf("tax integration %s: %w", integ.Code, integration.ErrUnsupported)
	}
	gw, err := t.s.gw.TaxGateway(integ.Provider)
	if err != nil {
		return nil, fmt.Errorf("tax integration %s: %w", integ.Code, err)
	}
	be, err := billingEntityOf(ctx, r, c)
	if err != nil {
		return nil, err
	}
	env := kernel.NewCustomerEnv(c, be)
	mapper, err := newItemMapper(ctx, r, org, integ.ID, be.ID)
	if err != nil {
		return nil, err
	}
	return &taxEnv{customer: c, ic: ic, integ: integ, conn: conn, gw: gw, env: env, mapper: mapper}, nil
}

// line is a provider line and the fees it stands for: one fee, or the
// fees of one charge grouped for Anrok.
type line struct {
	fees []*invoice.Fee
}

// ComputeTaxes asks the provider for the taxes of the request's fees
// (lago Taxes::Invoices::CreateDraftService for a draft, CreateService
// otherwise). Fees without a taxable amount are left out, unless none has
// one: the first then stands for the invoice. For Anrok the fees of one
// charge are one line, whose taxes are split back over them.
func (t *Taxes) ComputeTaxes(ctx context.Context, req kernel.TaxRequest) ([]tax.ProviderTaxResult, error) {
	if len(req.Fees) == 0 {
		// invoices/base_service.rb:69-74.
		return []tax.ProviderTaxResult{}, nil
	}
	var (
		e     *taxEnv
		lines []line
		tx    *integration.TaxTransaction
	)
	err := t.s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if e, err = t.load(ctx, r, req.Customer); err != nil || e == nil {
			return err
		}
		lines = e.group(taxableFees(req.Fees))
		tx, err = t.invoiceTransaction(ctx, e, req, lines)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("tax request: %w", err)
	}
	if e == nil {
		return []tax.ProviderTaxResult{}, nil
	}
	var res *integration.TaxResult
	if req.Draft {
		res, err = t.quote(ctx, e, tx)
	} else {
		res, err = e.gw.ReportTaxes(ctx, e.conn, tx)
	}
	if err != nil {
		return nil, t.failure(ctx, e, err)
	}
	if !req.Draft {
		// create_service.rb:21-22.
		if err := t.reported(ctx, e, integration.ResourceTypeInvoice, invoiceID(req.Invoice).UUID(), res); err != nil {
			return nil, err
		}
	}
	return splitResults(res, lines), nil
}

// quote quotes tx, through the draft cache for Anrok (lago
// create_draft_service.rb:16-20).
func (t *Taxes) quote(ctx context.Context, e *taxEnv, tx *integration.TaxTransaction) (*integration.TaxResult, error) {
	key := ""
	if e.anrok() {
		key = t.cache.key(e.integ, tx)
	}
	if res, ok := t.cache.get(ctx, key, tx); ok {
		return res, nil
	}
	res, err := e.gw.QuoteTaxes(ctx, e.conn, tx)
	if err != nil {
		return nil, err
	}
	t.cache.put(ctx, key, res)
	return res, nil
}

// taxableFees returns the fees in creation order (then by ID, then as
// given) that have a taxable amount, or the first fee when none has
// (lago invoices/base_service.rb:27-37).
func taxableFees(fs []*invoice.Fee) []*invoice.Fee {
	ordered := slices.Clone(fs)
	slices.SortStableFunc(ordered, func(a, b *invoice.Fee) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID.String(), b.ID.String()))
	})
	out := slices.DeleteFunc(slices.Clone(ordered), func(f *invoice.Fee) bool { return !kernel.TaxFeeOf(f).Taxable() })
	if len(out) == 0 {
		return ordered[:1]
	}
	return out
}

// group makes the provider lines of fs: for Anrok, which rounds taxes
// once per transaction, the fees of one charge are one line placed where
// the charge first appears; for Avalara, which rounds per line, every fee
// is its own line (lago invoices/base_service.rb:39-50,
// charge_fee_group.rb).
func (e *taxEnv) group(fs []*invoice.Fee) []line {
	if !e.anrok() {
		out := make([]line, len(fs))
		for i, f := range fs {
			out[i] = line{fees: []*invoice.Fee{f}}
		}
		return out
	}
	ids := make([]string, len(fs))
	for i, f := range fs {
		if f.Type == core.FeeTypeCharge {
			ids[i] = f.ChargeID.String()
		}
	}
	groups := tax.GroupFeesByCharge(ids)
	out := make([]line, len(groups))
	for i, g := range groups {
		for _, n := range g {
			out[i].fees = append(out[i].fees, fs[n])
		}
	}
	return out
}

// grouped reports a line standing for several fees of one charge.
func (l line) grouped() bool { return len(l.fees) > 1 }

// key is the line's item_key and item_id: the fee, or the charge of
// grouped fees.
func (l line) key() string {
	if l.grouped() {
		return l.fees[0].ChargeID.String()
	}
	return l.fees[0].ID.String()
}

// subTotal is the sum of the fees' amounts less their coupons.
func (l line) subTotal() money.Decimal {
	sum := money.Zero
	for _, f := range l.fees {
		sum = sum.Add(kernel.TaxFeeOf(f).SubTotalExcludingTaxesAmountCents())
	}
	return sum
}

// units is the sum of the fees' units.
func (l line) units() money.Decimal {
	sum := money.Zero
	for _, f := range l.fees {
		sum = sum.Add(f.Units)
	}
	return sum
}

// truncated is d without its fraction (Ruby to_i).
func truncated(d money.Decimal) int64 {
	v, _ := d.Truncate(0).Int64()
	return v
}

// invoiceTransaction builds the transaction of an invoice or of
// standalone fees (lago taxes/invoices/payloads/anrok.rb, avalara.rb and
// create_service.rb:44-56). Standalone fees have no invoice: they are
// reported under a random ID on the customer's current date (lago
// Fees::ApplyProviderTaxesToStandaloneFeesService#fake_invoice).
func (t *Taxes) invoiceTransaction(ctx context.Context, e *taxEnv, req kernel.TaxRequest, lines []line) (*integration.TaxTransaction, error) {
	inv := req.Invoice
	tx := e.transaction()
	switch {
	case inv == nil || inv.ID.IsZero():
		tx.IssuingDate = e.env.Today(t.s.d.Now())
		if inv != nil {
			tx.Currency = inv.Currency
		} else {
			tx.Currency = req.Fees[0].Currency
		}
		if !req.Draft {
			tx.ID = core.NewID[core.InvoiceID](t.s.d.IDs).String()
		}
	default:
		tx.IssuingDate, tx.Currency = inv.IssuingDate, inv.Currency
		if !req.Draft {
			tx.ID = inv.ID.String()
			// Avalara: a voided invoice reported again is a return
			// (create_service.rb:51-53, payloads/avalara.rb:63-64).
			tx.Return = !e.anrok() && inv.Status == invoice.StatusVoided
		}
	}
	for _, l := range lines {
		item, err := e.mapper.fee(ctx, l.fees[0])
		if err != nil {
			return nil, err
		}
		amount := truncated(l.subTotal())
		if tx.Return {
			amount = -amount
		}
		tx.Lines = append(tx.Lines, integration.TaxLine{
			ItemKey: l.key(), ItemID: l.key(), ItemCode: externalID(item), AmountCents: amount, Units: l.units(),
		})
	}
	return tx, nil
}

// transaction returns a transaction of the customer of e, without lines.
func (e *taxEnv) transaction() *integration.TaxTransaction {
	c := e.customer
	tc := integration.TaxCustomer{
		LinkedID: e.ic.ExternalCustomerID, ExternalID: c.ExternalID,
		Address: c.EffectiveShippingAddress(),
	}
	if c.Name != nil {
		tc.Name = *c.Name
	}
	if c.TaxIdentificationNumber != nil {
		tc.TaxNumber = *c.TaxIdentificationNumber
	}
	return &integration.TaxTransaction{Customer: tc, Origin: e.env.BillingEntity.Address}
}

func externalID(it *integration.ExternalItem) string {
	if it == nil {
		return ""
	}
	return it.ExternalID
}

func invoiceID(inv *invoice.Invoice) core.InvoiceID {
	if inv == nil {
		return core.InvoiceID{}
	}
	return inv.ID
}

// splitResults converts the provider's lines into fee results keyed by
// fee ID, splitting the taxes of grouped lines over their fees in
// proportion to their taxable amounts (lago invoices/base_service.rb:57-67,
// charge_fee_group.rb:45-74).
func splitResults(res *integration.TaxResult, lines []line) []tax.ProviderTaxResult {
	byKey := make(map[string]line, len(lines))
	for _, l := range lines {
		byKey[l.key()] = l
	}
	out := make([]tax.ProviderTaxResult, 0, len(res.Lines))
	for _, rl := range res.Lines {
		r := providerResult(rl)
		l, ok := byKey[rl.ItemKey]
		if !ok {
			l, ok = byKey[rl.ItemID]
		}
		if !ok || !l.grouped() {
			out = append(out, r)
			continue
		}
		items := make([]tax.ProviderItem, len(l.fees))
		for i, f := range l.fees {
			items[i] = tax.ProviderItem{ItemKey: f.ID.String(), ItemID: f.ID.String(), Fee: kernel.TaxFeeOf(f)}
		}
		out = append(out, tax.SplitProviderTaxes(r, items)...)
	}
	return out
}

// providerResult normalizes one line of the provider (lago
// taxes/base_service.rb:44-110).
func providerResult(l integration.TaxLineResult) tax.ProviderTaxResult {
	raw := make([]tax.ProviderBreakdownLine, len(l.Breakdown))
	for i, b := range l.Breakdown {
		raw[i] = tax.ProviderBreakdownLine{Name: b.Name, Reason: b.Reason, Type: b.Type, Rate: b.Rate, TaxAmount: b.TaxAmount}
	}
	var toPay int64
	if l.TaxAmountCents != nil {
		toPay = *l.TaxAmountCents
	}
	return tax.ProviderTaxResult{
		ItemKey: l.ItemKey, ItemID: l.ItemID, ItemCode: l.ItemCode,
		AmountCents: l.AmountCents, TaxAmountCents: l.TaxAmountCents,
		Breakdown: tax.NormalizeProviderBreakdown(raw, toPay),
	}
}

// reported records a reported transaction: the customer's external ID
// when its link has none, and, for Avalara invoices and every credit
// note, the provider's transaction (lago taxes/base_service.rb:41-46,
// invoices/create_service.rb:21-22, credit_notes/create_service.rb:23-24).
func (t *Taxes) reported(ctx context.Context, e *taxEnv, rt integration.ResourceType, syncable uuid.UUID, res *integration.TaxResult) error {
	return t.s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if e.ic.ExternalCustomerID == "" {
			ic, err := r.IntegrationCustomers.Get(ctx, e.ic.OrganizationID, e.ic.ID)
			if err != nil {
				return fmt.Errorf("load integration customer %s: %w", e.ic.ID, err)
			}
			if ic.ExternalCustomerID == "" {
				ic.ExternalCustomerID, ic.UpdatedAt = e.customer.ExternalID, t.s.d.Now()
				if err := r.IntegrationCustomers.Update(ctx, ic); err != nil {
					return fmt.Errorf("update integration customer %s: %w", ic.ID, err)
				}
			}
			e.ic = ic
		}
		if res.TransactionID == "" || syncable == uuid.Nil || (rt == integration.ResourceTypeInvoice && e.anrok()) {
			return nil
		}
		return t.s.recordResource(ctx, r, e.integ, rt, syncable, res.TransactionID)
	})
}

// failure classifies the failure err of a call for e: a retryable failure
// is [kernel.ErrTaxContention]; a refusal of the request's content emits
// customer.tax_provider_error and, like an authentication failure, is a
// [kernel.TaxProviderError] with the provider's code and message (lago
// taxes/base_service.rb:66-77 and the HttpError rescues of the services).
func (t *Taxes) failure(ctx context.Context, e *taxEnv, err error) error {
	pe, ok := integration.AsProviderError(err)
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%w: %w", kernel.ErrTaxContention, err)
	case !ok:
		return err
	case pe.Retryable():
		return fmt.Errorf("%w: %w", kernel.ErrTaxContention, err)
	}
	out := &kernel.TaxProviderError{Code: cmp.Or(pe.Code, string(pe.Kind)), Message: pe.Message}
	if pe.Rejected() && !kernel.TaxContention(pe.Message) {
		if err := t.emitError(ctx, e, out); err != nil {
			return err
		}
	}
	return out
}

// TaxProviderCustomerError is the payload of customer.tax_provider_error
// (lago V1::Integrations::Taxes::CustomerErrorSerializer).
type TaxProviderCustomerError struct {
	// CustomerID is the customer.
	CustomerID core.CustomerID `json:"customer_id"`
	// ExternalCustomerID is the customer's external ID.
	ExternalCustomerID string `json:"external_customer_id"`
	// TaxProvider is the vendor.
	TaxProvider integration.Provider `json:"tax_provider"`
	// TaxProviderCode is the integration's code.
	TaxProviderCode string `json:"tax_provider_code"`
	// ProviderError is the provider's error.
	ProviderError ProviderErrorDetails `json:"provider_error"`
}

// ProviderErrorDetails is a provider's error in the integration
// webhooks.
type ProviderErrorDetails struct {
	// Message is the provider's message.
	Message string `json:"message"`
	// ErrorCode is the provider's code.
	ErrorCode string `json:"error_code"`
}

// emitError emits customer.tax_provider_error (lago
// aggregator/base_service.rb:131-142). In a preview, the customer and the
// event live in the rolled-back transaction, so no event is delivered
// (lago taxes/base_service.rb:73: not in preview mode).
func (t *Taxes) emitError(ctx context.Context, e *taxEnv, pe *kernel.TaxProviderError) error {
	c := e.customer
	payload := TaxProviderCustomerError{
		CustomerID: c.ID, ExternalCustomerID: c.ExternalID,
		TaxProvider: e.integ.Provider, TaxProviderCode: e.integ.Code,
		ProviderError: ProviderErrorDetails{Message: pe.Message, ErrorCode: pe.Code},
	}
	return kernel.Emit(ctx, t.s.d, c.OrganizationID, webhook.EventTypeCustomerTaxProviderError, "tax_provider_customer_error", c.ID.UUID(), payload)
}

// VoidInvoice voids the reported transaction of the voided invoice inv
// (lago Invoices::ProviderTaxes::VoidService). An invoice never reported
// (without fees nor recorded transaction) is skipped. A transaction Anrok
// froze for filing is negated under "<id>_voided"; one Avalara locked is
// reported again as a return. A refusal is a [kernel.TaxProviderError],
// stored by the caller as a tax_voiding_error.
func (t *Taxes) VoidInvoice(ctx context.Context, c *customer.Customer, inv *invoice.Invoice) error {
	var (
		e    *taxEnv
		fees []*invoice.Fee
		skip bool
	)
	err := t.s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if e, err = t.load(ctx, r, c); err != nil || e == nil {
			return err
		}
		if fees, err = r.Fees.ListByInvoice(ctx, inv.OrganizationID, inv.ID); err != nil {
			return fmt.Errorf("load fees of invoice %s: %w", inv.ID, err)
		}
		if len(fees) > 0 {
			return nil
		}
		// void_service.rb:39-45.
		_, err = r.IntegrationResources.Find(ctx, inv.OrganizationID, e.integ.ID, integration.ResourceTypeInvoice, inv.ID.UUID())
		switch {
		case errors.Is(err, core.ErrNotFound):
			skip = true
			return nil
		default:
			return err
		}
	})
	if err != nil || e == nil || skip {
		return err
	}
	err = e.gw.VoidTaxes(ctx, e.conn, inv.ID.String())
	pe, _ := integration.AsProviderError(err)
	switch {
	case err == nil:
		return nil
	case pe != nil && pe.Kind == integration.KindLocked && e.anrok():
		// provider_taxes/void_service.rb:21-27,71-75: negate_service.rb.
		neg, ok := e.gw.(integration.TaxNegator)
		if !ok {
			return fmt.Errorf("negate taxes of invoice %s: %w", inv.ID, integration.ErrUnsupported)
		}
		if err := neg.NegateTaxes(ctx, e.conn, inv.ID.String(), inv.ID.String()+"_voided"); err != nil {
			return t.failure(ctx, e, err)
		}
		return nil
	case pe != nil && pe.Kind == integration.KindLocked:
		// provider_taxes/void_service.rb:27-33,57-63,77-81.
		voided := *inv
		voided.Status = invoice.StatusVoided
		_, err := t.ComputeTaxes(ctx, kernel.TaxRequest{Customer: c, Invoice: &voided, Fees: fees})
		return err
	default:
		return t.failure(ctx, e, err)
	}
}

// ReportCreditNote reports the credit note cn of the invoice inv as a
// return under "cn_<id>" and records the provider's transaction (lago
// Taxes::CreditNotes::CreateService). Every provider failure but
// contention is a [kernel.TaxProviderError] with the provider's code, as
// lago's HTTP error rescue makes every failed response a service failure
// (create_service.rb:29-34); a refusal of the content also emits
// customer.tax_provider_error (taxes/base_service.rb:66-71). Contention
// fails with [kernel.ErrTaxContention] (base_service.rb:68).
func (t *Taxes) ReportCreditNote(ctx context.Context, c *customer.Customer, inv *invoice.Invoice, cn *creditnote.CreditNote) error {
	var (
		e  *taxEnv
		tx *integration.TaxTransaction
	)
	err := t.s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if e, err = t.load(ctx, r, c); err != nil || e == nil {
			return err
		}
		tx, err = t.creditNoteTransaction(ctx, r, e, inv, cn)
		return err
	})
	if err != nil || e == nil {
		return err
	}
	res, err := e.gw.ReportTaxes(ctx, e.conn, tx)
	if err != nil {
		ferr := t.failure(ctx, e, err)
		pe, ok := integration.AsProviderError(err)
		if !ok || pe.Kind == integration.KindContention {
			return ferr
		}
		if tpe := (*kernel.TaxProviderError)(nil); errors.As(ferr, &tpe) {
			return tpe
		}
		return &kernel.TaxProviderError{Code: cmp.Or(pe.Code, string(pe.Kind)), Message: pe.Message}
	}
	return t.reported(ctx, e, integration.ResourceTypeCreditNote, cn.ID.UUID(), res)
}

// creditNoteTransaction builds the return of a credit note (lago
// taxes/credit_notes/payloads/anrok.rb and avalara.rb): each item credits
// its share of its fee's taxable amount, rounded and negated; for Anrok
// the items of one charge are one line identified by the charge, and the
// taxes are computed at the invoice's issuing date.
func (t *Taxes) creditNoteTransaction(ctx context.Context, r port.Repos, e *taxEnv, inv *invoice.Invoice, cn *creditnote.CreditNote) (*integration.TaxTransaction, error) {
	tx := e.transaction()
	tx.ID, tx.Return, tx.IssuingDate, tx.Currency = "cn_"+cn.ID.String(), true, cn.IssuingDate, cn.Currency
	if e.anrok() {
		tx.TaxDate = inv.IssuingDate
	}
	type item struct {
		fee      *invoice.Fee
		subTotal money.Decimal
	}
	var items []item
	var chargeIDs []string
	for _, it := range cn.Items {
		f, err := r.Fees.Get(ctx, cn.OrganizationID, it.FeeID)
		if err != nil {
			return nil, fmt.Errorf("load fee %s: %w", it.FeeID, err)
		}
		items = append(items, item{fee: f, subTotal: itemSubTotal(it, f)})
		id := ""
		if e.anrok() && f.Type == core.FeeTypeCharge {
			id = f.ChargeID.String()
		}
		chargeIDs = append(chargeIDs, id)
	}
	for _, g := range tax.GroupFeesByCharge(chargeIDs) {
		f := items[g[0]].fee
		sum := money.Zero
		for _, n := range g {
			sum = sum.Add(items[n].subTotal)
		}
		code, err := e.mapper.fee(ctx, f)
		if err != nil {
			return nil, err
		}
		var id string
		if e.anrok() && f.Type == core.FeeTypeCharge {
			// anrok.rb:63-71: the charge, not the metric.
			id = f.ChargeID.String()
		} else if id, err = e.mapper.itemID(ctx, f); err != nil {
			return nil, err
		}
		amount, _ := sum.Round(0, money.HalfUp).Int64()
		tx.Lines = append(tx.Lines, integration.TaxLine{ItemID: id, ItemCode: externalID(code), AmountCents: -amount, Units: f.Units})
	}
	return tx, nil
}

// itemSubTotal is the credited share of the fee's taxable amount (lago
// CreditNoteItem#sub_total_excluding_taxes_amount_cents).
func itemSubTotal(it creditnote.Item, f *invoice.Fee) money.Decimal {
	if it.Amount == 0 || f.Amount == 0 {
		return money.Zero
	}
	share := money.NewFromInt(it.Amount).Div(money.NewFromInt(f.Amount))
	return share.Mul(kernel.TaxFeeOf(f).SubTotalExcludingTaxesAmountCents())
}
