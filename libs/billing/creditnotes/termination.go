package creditnotes

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// TerminationInput is the automatic credit note of a terminated
// subscription whose base fee was paid in advance (domain §13.3).
type TerminationInput struct {
	// OrganizationID is the organization.
	OrganizationID core.OrganizationID
	// SubscriptionID is the terminated subscription.
	SubscriptionID core.SubscriptionID
	// Reason is why the note is issued; zero means order_change.
	Reason creditnote.Reason
	// Upgrade is true when the subscription is replaced by an upgrade: the
	// termination day is billed by the new subscription.
	Upgrade bool
	// OnTermination is how the unused amount is returned; zero means
	// credit, skip issues nothing.
	OnTermination subscription.TerminationCreditNote
	// Preview computes the credit note without storing anything.
	Preview bool
}

// day is one day.
const day = 24 * time.Hour

// termination is the state of a termination credit note.
type termination struct {
	in      TerminationInput
	env     *kernel.SubscriptionEnv
	dates   *period.Dates
	fee     *invoice.Fee
	st      *invoiceState
	at      time.Time // the termination instant
	nextEnd time.Time // the UTC date of the end of the period
}

// CreateFromTermination issues, in the transaction of r, the credit note
// returning the unused part of the last subscription fee of a terminated
// subscription: the plan's day price times the days left in the period
// after the termination day (after the trial), capped by the fee and what
// earlier notes credited. With refund or offset, the part already paid and
// not used is refunded and the rest credited or offset. It returns nil
// when there is nothing to return, the last fee is zero or its invoice is
// voided. An upgrade with refund or offset returns a *core.RuleError with
// code [CodeUpgradeRefundUnsupported].
func (s *Service) CreateFromTermination(ctx context.Context, r port.Repos, in TerminationInput) (*creditnote.CreditNote, error) {
	if in.OnTermination == "" {
		in.OnTermination = subscription.TerminationCreditNoteCredit
	}
	if in.OnTermination == subscription.TerminationCreditNoteSkip {
		return nil, nil
	}
	if in.Reason == "" {
		in.Reason = creditnote.ReasonOrderChange
	}
	t, err := s.loadTermination(ctx, r, in)
	if err != nil || t == nil {
		return nil, err
	}
	if in.Upgrade && in.OnTermination != subscription.TerminationCreditNoteCredit {
		return nil, rule(CodeUpgradeRefundUnsupported, "upgrade cannot refund or offset the unused subscription")
	}
	base := t.creditableAmount()
	if base.Sign() <= 0 {
		return nil, nil
	}
	total, err := t.adjusted(s.d.IDs, base)
	if err != nil {
		return nil, err
	}
	refund := int64(0)
	if in.OnTermination != subscription.TerminationCreditNoteCredit {
		if refund, err = t.refund(s.d.IDs, total); err != nil {
			return nil, err
		}
	}
	var credit, offset int64
	switch in.OnTermination {
	case subscription.TerminationCreditNoteCredit:
		credit, refund = total, 0
	case subscription.TerminationCreditNoteRefund:
		credit = total - refund
	case subscription.TerminationCreditNoteOffset:
		offset = total - refund
	}
	if credit+refund+offset == 0 {
		return nil, nil
	}
	return s.create(ctx, r, t.st, CreateInput{
		OrganizationID: in.OrganizationID, InvoiceID: t.fee.InvoiceID,
		Items:        []ItemInput{{FeeID: t.fee.ID, Amount: base.Truncate(dbScale)}},
		Reason:       in.Reason,
		CreditAmount: credit, RefundAmount: refund, OffsetAmount: offset,
		Automatic: true, Preview: in.Preview,
	})
}

// loadTermination loads the subscription, its dates at termination and its
// last subscription fee; it returns nil when that fee credits nothing.
func (s *Service) loadTermination(ctx context.Context, r port.Repos, in TerminationInput) (*termination, error) {
	env, err := kernel.LoadSubscriptionEnv(ctx, r, in.OrganizationID, in.SubscriptionID)
	if err != nil {
		return nil, err
	}
	fee, err := lastSubscriptionFee(ctx, r, in.OrganizationID, in.SubscriptionID)
	if err != nil || fee == nil || fee.Amount == 0 || fee.InvoiceID.IsZero() {
		return nil, err
	}
	st, err := lockAndLoad(ctx, r, in.OrganizationID, fee.InvoiceID, in.Preview)
	if err != nil {
		return nil, err
	}
	if st.inv.Status == invoice.StatusVoided {
		return nil, nil
	}
	at := s.d.Now()
	if ta := env.Subscription.TerminatedAt; ta != nil {
		at = *ta
	}
	dates, err := env.Dates(at, period.Billing)
	if err != nil {
		return nil, err
	}
	return &termination{
		in: in, env: env, dates: dates, fee: fee, st: st, at: at,
		nextEnd: utcDate(dates.CurrentPeriodEnd().Add(-time.Microsecond)),
	}, nil
}

// lastSubscriptionFee returns the latest subscription fee of a
// subscription, or nil.
func lastSubscriptionFee(ctx context.Context, r port.Repos, org core.OrganizationID, sub core.SubscriptionID) (*invoice.Fee, error) {
	var last *invoice.Fee
	page := core.PageRequest{}
	for {
		p, err := r.Fees.List(ctx, org, invoice.FeeFilter{SubscriptionID: sub, Types: []core.FeeType{core.FeeTypeSubscription}}, page)
		if err != nil {
			return nil, fmt.Errorf("load subscription fees of %s: %w", sub, err)
		}
		for _, f := range p.Items {
			if last == nil || !f.CreatedAt.Before(last.CreatedAt) {
				last = f
			}
		}
		if p.NextToken == "" {
			return last, nil
		}
		page.Token = p.NextToken
	}
}

// dayPrice is the price of one day of the period, from the plan amount the
// fee was computed with.
func (t *termination) dayPrice() money.Decimal {
	amount := t.env.Plan.Amount
	var details struct {
		PlanAmountCents *int64 `json:"plan_amount_cents"`
	}
	if len(t.fee.AmountDetails) > 0 && json.Unmarshal(t.fee.AmountDetails, &details) == nil && details.PlanAmountCents != nil {
		amount = *details.PlanAmountCents
	}
	return t.dates.DayPrice(money.NewFromInt(amount))
}

// terminationDay is the UTC date of the end of the local termination day.
func (t *termination) terminationDay() time.Time {
	end := period.EndOfDay(t.at, t.env.Location()).Add(-time.Microsecond)
	return utcDate(end)
}

// trialEnd returns the UTC date the trial ends, if the plan has one.
func (t *termination) trialEnd() (time.Time, bool) {
	end, ok := t.env.Period().TrialEnd()
	if !ok {
		return time.Time{}, false
	}
	return utcDate(end), true
}

// remainingDays is the number of days of the period left after the
// termination day.
func (t *termination) remainingDays() int64 {
	from := t.terminationDay()
	if t.in.Upgrade {
		from = from.Add(-day)
	}
	if trial, ok := t.trialEnd(); ok && !trial.Before(from) {
		if trial.After(t.nextEnd) {
			from = t.nextEnd
		} else {
			from = trial.Add(-day)
		}
	}
	return max(0, days(from, t.nextEnd))
}

// usedDays is the number of days of the period used up to the termination
// day, after the trial.
func (t *termination) usedDays() int64 {
	p, ok := t.dates.SubscriptionPeriod()
	if !ok {
		return 0
	}
	from := utcDate(p.From)
	if trial, ok := t.trialEnd(); ok && trial.After(from) {
		from = trial
	}
	to := t.terminationDay()
	if to.After(t.nextEnd) {
		to = t.nextEnd
	}
	return max(0, days(from, to)+1)
}

// creditableAmount is the unused amount of the fee left to credit.
func (t *termination) creditableAmount() money.Decimal {
	amount := t.dayPrice().Mul(money.NewFromInt(t.remainingDays()))
	if amount.Sign() <= 0 {
		return money.Zero
	}
	if fee := money.NewFromInt(t.fee.Amount); amount.Cmp(fee) > 0 {
		amount = fee
	}
	amount = amount.Sub(money.NewFromInt(t.st.credited[t.fee.ID]))
	if amount.Sign() <= 0 {
		return money.Zero
	}
	return amount
}

// adjusted is amount of the fee after its coupons and with its taxes,
// rounded to minor units.
func (t *termination) adjusted(ids core.IDGenerator, amount money.Decimal) (int64, error) {
	precise := amount.Truncate(dbScale)
	cn := &creditnote.CreditNote{Items: []creditnote.Item{{FeeID: t.fee.ID, PreciseAmount: precise}}}
	res, err := t.st.applyTaxes(ids, cn, false)
	if err != nil {
		return 0, err
	}
	return kernel.RoundCents(precise.Sub(res.PreciseCouponsAdjustmentAmountCents).Add(res.PreciseTaxesAmountCents)), nil
}

// refund is the part of total already paid and not used.
func (t *termination) refund(ids core.IDGenerator, total int64) (int64, error) {
	used, err := t.adjusted(ids, t.dayPrice().Mul(money.NewFromInt(t.usedDays())))
	if err != nil {
		return 0, err
	}
	inv := t.st.inv
	if inv.SubTotalIncludingTaxesAmount == 0 {
		return 0, nil
	}
	// lago Fee#sub_total_excluding_taxes_precise_amount_cents: the precise
	// amount less the coupons (create_from_termination.rb:192).
	feeAmount := t.fee.PreciseAmount.Sub(t.fee.PreciseCouponsAmount).Add(t.fee.TaxesPreciseAmount)
	paid := feeAmount.Div(money.NewFromInt(inv.SubTotalIncludingTaxesAmount)).Mul(money.NewFromInt(inv.TotalPaidAmount))
	potential := paid.Sub(money.NewFromInt(used))
	if potential.Sign() <= 0 {
		return 0, nil
	}
	if t := money.NewFromInt(total); potential.Cmp(t) > 0 {
		potential = t
	}
	return kernel.RoundCents(potential), nil
}

// utcDate is the midnight UTC of the UTC date of t.
func utcDate(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// days is the number of days from from to to, both UTC dates.
func days(from, to time.Time) int64 { return int64(to.Sub(from) / day) }
