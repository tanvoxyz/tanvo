package kernel

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/tax"
)

// ErrTaxProvider wraps a failure of the tax provider; the invoice then
// fails with the tax_error code (domain §17.4).
var ErrTaxProvider = errors.New("billing: tax provider error")

// ErrTaxContention marks a tax provider failure caused by its rate limit
// or by contention on its side (lago
// Integrations::Aggregator::ServerContentionError,
// app/services/integrations/aggregator/taxes/base_service.rb:68): it is
// not wrapped in [ErrTaxProvider], so the invoice is left unchanged and
// the job asking the provider is retried.
var ErrTaxContention = errors.New("billing: tax provider contention")

// TaxProviderError is a failure a tax provider reports, with its code and
// message; the tax_error detail of the invoice records both (lago
// PullTaxesAndApplyService#create_error_detail). Its message classifies
// it as [ErrTaxContention] with [TaxContention].
type TaxProviderError struct {
	// Code is the provider's error code.
	Code string
	// Message is the provider's error message.
	Message string
}

// Error returns the code and the message.
func (e *TaxProviderError) Error() string {
	if e.Message == "" {
		return "tax provider: " + e.Code
	}
	return "tax provider: " + e.Code + ": " + e.Message
}

// Is reports whether e is a contention failure, for target
// [ErrTaxContention].
func (e *TaxProviderError) Is(target error) bool {
	return target == ErrTaxContention && TaxContention(e.Message)
}

// TaxContention reports whether a tax provider's error message tells of
// its rate limit or of contention (lago
// app/services/integrations/aggregator/taxes/base_service.rb:113-115).
func TaxContention(message string) bool {
	return strings.Contains(message, "API limit") || strings.Contains(message, "resource contention")
}

// providerFailure wraps the failure err of a tax provider call: in
// [ErrTaxProvider], unless it is a contention failure.
func providerFailure(err error) error {
	if errors.Is(err, ErrTaxContention) {
		return fmt.Errorf("compute taxes: %w", err)
	}
	return fmt.Errorf("%w: %w", ErrTaxProvider, err)
}

// TaxResolver resolves the taxes of fees (domain §22.10), caching the
// catalog objects it loads. It is bound to one transaction.
type TaxResolver struct {
	r   port.Repos
	org core.OrganizationID
	env *CustomerEnv

	taxes         map[core.TaxID]*catalog.Tax
	plans         map[core.SubscriptionID]*catalog.Plan
	charges       map[core.ChargeID]*catalog.Charge
	fixedCharges  map[core.FixedChargeID]*catalog.FixedCharge
	addOns        map[core.AddOnID]*catalog.AddOn
	commitments   map[core.CommitmentID]*catalog.Commitment
	explicitTaxes map[core.FeeID][]core.TaxID
}

// NewTaxResolver returns a resolver for the fees of the customer env.
func NewTaxResolver(r port.Repos, env *CustomerEnv) *TaxResolver {
	return &TaxResolver{
		r: r, org: env.OrganizationID(), env: env,
		taxes:         map[core.TaxID]*catalog.Tax{},
		plans:         map[core.SubscriptionID]*catalog.Plan{},
		charges:       map[core.ChargeID]*catalog.Charge{},
		fixedCharges:  map[core.FixedChargeID]*catalog.FixedCharge{},
		addOns:        map[core.AddOnID]*catalog.AddOn{},
		commitments:   map[core.CommitmentID]*catalog.Commitment{},
		explicitTaxes: map[core.FeeID][]core.TaxID{},
	}
}

// SetPlan records the plan of a subscription (for instance a plan given
// for a preview), so it is not loaded.
func (t *TaxResolver) SetPlan(s core.SubscriptionID, p *catalog.Plan) { t.plans[s] = p }

// SetExplicit records the explicit taxes of a fee (one-off invoice lines);
// they win even when empty (domain §22.10).
func (t *TaxResolver) SetExplicit(f core.FeeID, taxes []core.TaxID) {
	if taxes == nil {
		taxes = []core.TaxID{}
	}
	t.explicitTaxes[f] = taxes
}

// Explicit returns the explicit taxes recorded for the fee f with
// [TaxResolver.SetExplicit], and whether any were.
func (t *TaxResolver) Explicit(f core.FeeID) ([]core.TaxID, bool) {
	taxes, ok := t.explicitTaxes[f]
	return taxes, ok
}

// Candidates returns the tax lists the fee f can inherit.
func (t *TaxResolver) Candidates(ctx context.Context, f *invoice.Fee) (catalog.TaxCandidates, error) {
	c := catalog.TaxCandidates{Customer: t.env.Customer.TaxIDs, BillingEntity: t.env.BillingEntity.TaxIDs}
	if ex, ok := t.explicitTaxes[f.ID]; ok {
		if len(ex) == 0 {
			// Explicitly untaxed: no level applies.
			return catalog.TaxCandidates{}, nil
		}
		c.Explicit = ex
	}
	// A product fee bills a contract rate card, which may have no plan and
	// never takes its taxes.
	if !f.SubscriptionID.IsZero() && f.Type != core.FeeTypeProduct {
		p, err := t.plan(ctx, f.SubscriptionID)
		if err != nil {
			return c, err
		}
		c.Plan = p.TaxIDs
	}
	if !f.ChargeID.IsZero() {
		ch, err := t.charge(ctx, f.ChargeID)
		if err != nil {
			return c, err
		}
		c.Charge = ch.TaxIDs
	}
	if !f.FixedChargeID.IsZero() {
		fc, err := t.fixedCharge(ctx, f.FixedChargeID)
		if err != nil {
			return c, err
		}
		c.FixedCharge = fc.TaxIDs
		if !fc.AddOnID.IsZero() && f.AddOnID.IsZero() {
			a, err := t.addOn(ctx, fc.AddOnID)
			if err != nil {
				return c, err
			}
			c.AddOn = a.TaxIDs
		}
	}
	if !f.AddOnID.IsZero() {
		a, err := t.addOn(ctx, f.AddOnID)
		if err != nil {
			return c, err
		}
		c.AddOn = a.TaxIDs
	}
	if !f.CommitmentID.IsZero() {
		cm, err := t.commitment(ctx, f.CommitmentID)
		if err != nil {
			return c, err
		}
		c.Commitment = cm.TaxIDs
	}
	return c, nil
}

// Resolve returns the taxes that apply to the fee f.
func (t *TaxResolver) Resolve(ctx context.Context, f *invoice.Fee) ([]*catalog.Tax, error) {
	c, err := t.Candidates(ctx, f)
	if err != nil {
		return nil, err
	}
	ids, _ := catalog.ResolveTaxes(f.Type, c)
	out := make([]*catalog.Tax, 0, len(ids))
	for _, id := range ids {
		tx, err := t.Tax(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, tx)
	}
	return out, nil
}

// Tax loads the tax id.
func (t *TaxResolver) Tax(ctx context.Context, id core.TaxID) (*catalog.Tax, error) {
	if tx, ok := t.taxes[id]; ok {
		return tx, nil
	}
	tx, err := t.r.Taxes.Get(ctx, t.org, id)
	if err != nil {
		return nil, fmt.Errorf("load tax %s: %w", id, err)
	}
	t.taxes[id] = tx
	return tx, nil
}

// byCode returns a loaded tax with the code, or nil.
func (t *TaxResolver) byCode(code string) *catalog.Tax {
	for _, tx := range t.taxes {
		if tx.Code == code {
			return tx
		}
	}
	return nil
}

func (t *TaxResolver) plan(ctx context.Context, s core.SubscriptionID) (*catalog.Plan, error) {
	if p, ok := t.plans[s]; ok {
		return p, nil
	}
	sub, err := t.r.Subscriptions.Get(ctx, t.org, s)
	if err != nil {
		return nil, fmt.Errorf("load subscription %s: %w", s, err)
	}
	p, err := t.r.Plans.Get(ctx, t.org, sub.PlanID)
	if err != nil {
		return nil, fmt.Errorf("load plan %s: %w", sub.PlanID, err)
	}
	t.plans[s] = p
	return p, nil
}

func (t *TaxResolver) charge(ctx context.Context, id core.ChargeID) (*catalog.Charge, error) {
	return cached(ctx, t.charges, id, func(ctx context.Context) (*catalog.Charge, error) { return t.r.Charges.Get(ctx, t.org, id) })
}

func (t *TaxResolver) fixedCharge(ctx context.Context, id core.FixedChargeID) (*catalog.FixedCharge, error) {
	return cached(ctx, t.fixedCharges, id, func(ctx context.Context) (*catalog.FixedCharge, error) {
		return t.r.FixedCharges.Get(ctx, t.org, id)
	})
}

func (t *TaxResolver) addOn(ctx context.Context, id core.AddOnID) (*catalog.AddOn, error) {
	return cached(ctx, t.addOns, id, func(ctx context.Context) (*catalog.AddOn, error) { return t.r.AddOns.Get(ctx, t.org, id) })
}

func (t *TaxResolver) commitment(ctx context.Context, id core.CommitmentID) (*catalog.Commitment, error) {
	return cached(ctx, t.commitments, id, func(ctx context.Context) (*catalog.Commitment, error) {
		return t.r.Commitments.Get(ctx, t.org, id)
	})
}

func cached[K comparable, V any](ctx context.Context, m map[K]*V, k K, load func(context.Context) (*V, error)) (*V, error) {
	if v, ok := m[k]; ok {
		return v, nil
	}
	v, err := load(ctx)
	if err != nil {
		return nil, fmt.Errorf("load %v: %w", k, err)
	}
	m[k] = v
	return v, nil
}

// TaxOf converts a catalog tax to the libs/tax view.
func TaxOf(t *catalog.Tax) tax.Tax {
	out := tax.Tax{Code: t.Code, Name: t.Name, Rate: t.Rate}
	if t.Description != nil {
		out.Description = *t.Description
	}
	return out
}

// TaxFeeOf converts a fee to the libs/tax view, keeping its taxes.
func TaxFeeOf(f *invoice.Fee) tax.Fee {
	out := tax.Fee{
		Type:                      tax.FeeType(f.Type),
		AmountCents:               f.Amount,
		PreciseAmountCents:        f.PreciseAmount,
		PreciseCouponsAmountCents: f.PreciseCouponsAmount,
	}
	if len(f.AppliedTaxes) > 0 {
		out.TaxesAmountCents = f.TaxesAmount
		out.TaxesPreciseAmountCents = f.TaxesPreciseAmount
		out.TaxesRate = f.TaxesRate
		out.TaxesBaseRate = f.TaxesBaseRate
		for _, a := range f.AppliedTaxes {
			row := tax.FeeAppliedTax{
				TaxCode: a.TaxCode, TaxName: a.TaxName, TaxRate: a.TaxRate,
				Provider: a.TaxID.IsZero(), AmountCents: a.Amount, PreciseAmountCents: a.PreciseAmount,
			}
			if a.TaxDescription != nil {
				row.TaxDescription = *a.TaxDescription
			}
			out.AppliedTaxes = append(out.AppliedTaxes, row)
		}
	}
	return out
}

// ApplyFeeTaxes copies computed fee taxes onto f. Taxes are matched back
// to catalog taxes by code; provider taxes have no tax ID.
func (t *TaxResolver) ApplyFeeTaxes(ids core.IDGenerator, f *invoice.Fee, ft tax.FeeTaxes) {
	f.TaxesAmount = ft.TaxesAmountCents
	f.TaxesPreciseAmount = ft.TaxesPreciseAmountCents
	f.TaxesRate = ft.TaxesRate
	f.TaxesBaseRate = ft.TaxesBaseRate
	f.AppliedTaxes = f.AppliedTaxes[:0]
	for _, a := range ft.AppliedTaxes {
		row := invoice.FeeAppliedTax{
			ID:      core.NewID[core.FeeAppliedTaxID](ids),
			TaxCode: a.TaxCode, TaxName: a.TaxName, TaxRate: a.TaxRate,
			Amount: a.AmountCents, PreciseAmount: a.PreciseAmountCents,
		}
		if a.TaxDescription != "" {
			d := a.TaxDescription
			row.TaxDescription = &d
		}
		if !a.Provider {
			if tx := t.byCode(a.TaxCode); tx != nil {
				row.TaxID = tx.ID
			}
		}
		f.AppliedTaxes = append(f.AppliedTaxes, row)
	}
}

// TaxInput is the input of [TaxResolver.Compute].
type TaxInput struct {
	// Invoice is the invoice being computed (its ID, currency and status
	// are read).
	Invoice *invoice.Invoice
	// Fees are the invoice's fees, in creation order.
	Fees []*invoice.Fee
	// Deductions are the deductions before and after taxes; Taxes is
	// ignored (computed).
	Deductions invoice.Deductions
	// SkipFeeTaxes keeps the fees' taxes (advance charges invoices).
	SkipFeeTaxes bool
	// AggregateFeeTaxes sums the fees' own taxes (advance charges
	// invoices, domain §22.9).
	AggregateFeeTaxes bool
	// Draft is true when the invoice stays a draft (the provider must not
	// record the transaction).
	Draft bool
	// Inline calls the provider at once, possibly inside the caller's
	// transaction, as lago does for previews, current usage and standalone
	// in-advance fees (app/services/fees/create_pay_in_advance_service.rb:29).
	// Otherwise the invoice's taxes are deferred: the invoice waits for
	// them and a job asks the provider outside the transaction (see
	// [ErrTaxesPending], [TaxCall]). Current usage computed for the
	// wallets' ongoing balances runs under no lock
	// (wallets.Service.RefreshOngoingBalances).
	Inline bool
	// LocalOnly applies the organization's own taxes even to a customer
	// taxed by a provider (billing segment invoices, whose taxes lago
	// computes without provider taxes, Invoices::ComputeAmountsFromFees).
	LocalOnly bool
}

// TaxOutcome is the result of [TaxResolver.Compute].
type TaxOutcome struct {
	// AppliedTaxes are the invoice-level taxes.
	AppliedTaxes []*invoice.AppliedTax
	// Totals are the invoice totals.
	Totals invoice.Totals
	// TaxesRate is the effective rate in percent.
	TaxesRate money.Decimal
	// Provider is true when a tax provider computed the taxes.
	Provider bool
}

// Compute resolves and applies the taxes of the fees (provider or
// internal), then computes the invoice-level taxes and the totals. It
// updates the fees in memory; the caller stores them.
func (t *TaxResolver) Compute(ctx context.Context, d *Deps, in TaxInput) (TaxOutcome, error) {
	useProvider := false
	var results map[string]*tax.ProviderTaxResult
	if d.Taxes != nil && !in.SkipFeeTaxes && !in.AggregateFeeTaxes && !in.LocalOnly && len(in.Fees) > 0 {
		on, err := d.Taxes.Enabled(ctx, t.env.Customer)
		if err != nil {
			return TaxOutcome{}, fmt.Errorf("%w: %w", ErrTaxProvider, err)
		}
		if on {
			req := TaxRequest{Customer: t.env.Customer, Invoice: in.Invoice, Fees: in.Fees, Draft: in.Draft}
			var res []tax.ProviderTaxResult
			switch c := taxCallFrom(ctx); {
			case in.Inline:
				res, err = d.Taxes.ComputeTaxes(ctx, req)
			case c == nil:
				// An invoice's taxes are deferred: the invoice waits for
				// them, pulled by a job.
				return TaxOutcome{}, ErrTaxesPending
			default:
				var fatal error
				if res, err, fatal = c.answer(req); fatal != nil {
					return TaxOutcome{}, fatal
				}
			}
			if err != nil {
				return TaxOutcome{}, providerFailure(err)
			}
			useProvider = true
			results = make(map[string]*tax.ProviderTaxResult, len(res))
			for i := range res {
				results[res[i].ItemID] = &res[i]
			}
		}
	}
	fees := make([]tax.InvoiceFee, len(in.Fees))
	for i, f := range in.Fees {
		fees[i].Fee = TaxFeeOf(f)
		if in.SkipFeeTaxes || in.AggregateFeeTaxes || len(f.AppliedTaxes) > 0 {
			continue
		}
		if useProvider {
			fees[i].ProviderResult = results[f.ID.String()]
			continue
		}
		taxes, err := t.Resolve(ctx, f)
		if err != nil {
			return TaxOutcome{}, err
		}
		for _, tx := range taxes {
			fees[i].Taxes = append(fees[i].Taxes, TaxOf(tx))
		}
	}
	var amounts tax.InvoiceAmounts
	if in.AggregateFeeTaxes {
		plain := make([]tax.Fee, len(fees))
		for i := range fees {
			plain[i] = fees[i].Fee
		}
		amounts = tax.AggregateInvoiceTaxes(plain)
	} else {
		var err error
		amounts, err = tax.ComputeInvoiceAmounts(tax.InvoiceAmountsInput{
			Fees:                                fees,
			UseProviderTaxes:                    useProvider,
			SkipFeeTaxes:                        in.SkipFeeTaxes,
			CouponsAmountCents:                  in.Deductions.Coupons,
			ProgressiveBillingCreditAmountCents: in.Deductions.ProgressiveBillingCredit,
			CreditNotesAmountCents:              in.Deductions.CreditNotes,
		})
		if err != nil {
			if useProvider {
				return TaxOutcome{}, fmt.Errorf("%w: %w", ErrTaxProvider, err)
			}
			return TaxOutcome{}, fmt.Errorf("compute invoice taxes: %w", err)
		}
	}
	feeAmounts := make([]int64, len(in.Fees))
	for i, f := range in.Fees {
		if !in.SkipFeeTaxes && !in.AggregateFeeTaxes && len(f.AppliedTaxes) == 0 {
			t.ApplyFeeTaxes(d.IDs, f, amounts.Fees[i].FeeTaxes)
		}
		feeAmounts[i] = f.Amount
	}
	ded := in.Deductions
	ded.Taxes = amounts.TaxesAmountCents
	out := TaxOutcome{Totals: invoice.ComputeTotals(feeAmounts, ded), TaxesRate: amounts.TaxesRate, Provider: useProvider}
	for _, a := range amounts.AppliedTaxes {
		row := &invoice.AppliedTax{
			ID:                core.NewID[core.InvoiceAppliedTaxID](d.IDs),
			OrganizationID:    t.org,
			InvoiceID:         in.Invoice.ID,
			TaxCode:           a.TaxCode,
			TaxName:           a.TaxName,
			TaxRate:           a.TaxRate,
			FeesAmount:        a.FeesAmountCents,
			TaxableBaseAmount: a.TaxableBaseAmountCents,
			Amount:            a.AmountCents,
			PreciseAmount:     money.NewFromInt(a.AmountCents),
			Currency:          in.Invoice.Currency,
			CreatedAt:         d.Now(),
		}
		if a.TaxDescription != "" {
			desc := a.TaxDescription
			row.TaxDescription = &desc
		}
		if !a.Provider {
			if tx := t.byCode(a.TaxCode); tx != nil {
				row.TaxID = tx.ID
			}
		}
		out.AppliedTaxes = append(out.AppliedTaxes, row)
	}
	return out, nil
}
