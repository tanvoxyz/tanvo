package deduct

import (
	"context"
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/tax"
)

// CodeAlreadyApplied refuses to apply prepaid credits twice to an invoice.
const CodeAlreadyApplied = "already_applied"

// WalletAmount is what one wallet pays of an invoice.
type WalletAmount struct {
	// Wallet is the paying wallet.
	Wallet *wallet.Wallet
	// Amount is the amount paid, in minor units (possibly fractional
	// before the transaction rounds it).
	Amount money.Decimal
}

// bucketKey groups fees as wallets may pay them: by fee type and billable
// metric, and, for the fees of a charge accepting a target wallet, by the
// wallet code of their group (lago keys them [fee_type, metric,
// target_wallet_code], the code nil when the group names none).
type bucketKey struct {
	feeType core.FeeType
	metric  core.BillableMetricID
	// targeted marks the fees of a charge accepting a target wallet.
	targeted bool
	// targetWalletCode is the wallet the fees' group targets; "" for none.
	targetWalletCode string
}

// paidBy reports whether wallet w may pay the fees of k: only the wallet
// with the targeted code pays targeted fees; other fees follow the
// wallet's fee types and billable metrics.
//
// source: app/services/credits/allocate_prepaid_credits_by_wallets_service.rb:97-111
func (k bucketKey) paidBy(w *wallet.Wallet) bool {
	if k.targetWalletCode != "" {
		return w.Code != nil && *w.Code == k.targetWalletCode
	}
	return w.Pays(k.feeType, k.metric)
}

type bucket struct {
	key  bucketKey
	left money.Decimal
}

// walletsForApplication returns the customer's active wallets in the
// invoice currency with a positive balance, in application order.
func walletsForApplication(ctx context.Context, r port.Repos, inv *invoice.Invoice) ([]*wallet.Wallet, error) {
	all, err := r.Wallets.ListActiveForApplication(ctx, inv.OrganizationID, inv.CustomerID)
	if err != nil {
		return nil, fmt.Errorf("list wallets: %w", err)
	}
	return slices.DeleteFunc(all, func(w *wallet.Wallet) bool {
		return w.BalanceAmount <= 0 || w.Currency.Code() != inv.Currency.Code()
	}), nil
}

// AllocatePrepaidCredits splits total, the invoice amount left after
// credit notes, over wallets (in application order) by fee buckets (lago
// Credits::AllocatePrepaidCreditsByWalletsService, domain §22.7 step 5).
// Each bucket (fee type, billable metric, and the target wallet code of
// the fees of a charge accepting one) is capped by its fees' amount
// after coupons, plus their booked taxes, less their credit-note shares;
// rounding differences of at most one minor unit per bucket go to the
// largest bucket. Each wallet takes min(bucket left, balance left, invoice
// left) from the buckets it may pay. appliedTaxes are the invoice's taxes,
// used to book its tax over the fees.
func AllocatePrepaidCredits(ctx context.Context, r port.Repos, inv *invoice.Invoice, fees []*invoice.Fee, appliedTaxes []*invoice.AppliedTax, wallets []*wallet.Wallet, total int64) ([]WalletAmount, error) {
	if len(wallets) == 0 || total <= 0 {
		return nil, nil
	}
	booked := bookedTaxes(inv, fees, appliedTaxes)
	lookup := newCatalogLookup(r, inv.OrganizationID)
	var buckets []*bucket
	for _, f := range fees {
		net := f.SubTotalExcludingTaxesPreciseAmount()
		if net.IsZero() {
			continue
		}
		capAmount := net.Add(kernel.Cents(booked[f.ID])).Sub(f.PreciseCreditNotesAmount)
		if !capAmount.IsPositive() {
			continue
		}
		m, err := lookup.metric(ctx, f)
		if err != nil {
			return nil, err
		}
		k := bucketKey{feeType: f.Type, metric: m}
		if lookup.acceptsTargetWallet(f) {
			k.targeted, k.targetWalletCode = true, f.GroupedBy[catalog.EventTargetWalletCode]
		}
		i := slices.IndexFunc(buckets, func(b *bucket) bool { return b.key == k })
		if i < 0 {
			buckets = append(buckets, &bucket{key: k, left: capAmount})
			continue
		}
		buckets[i].left = buckets[i].left.Add(capAmount)
	}
	slices.SortStableFunc(buckets, func(a, b *bucket) int { return b.left.Cmp(a.left) })
	if len(buckets) > 0 {
		sum := money.Zero
		for _, b := range buckets {
			sum = sum.Add(b.left)
		}
		diff := kernel.Cents(total).Sub(sum)
		if diff.IsPositive() && diff.LessThanOrEqual(money.NewFromInt(int64(len(buckets)))) {
			buckets[0].left = buckets[0].left.Add(diff)
		}
	}

	invoiceLeft := kernel.Cents(total)
	var out []WalletAmount
	for _, w := range wallets {
		used := money.Zero
		for _, b := range buckets {
			if !b.left.IsPositive() || !b.key.paidBy(w) {
				continue
			}
			balanceLeft := kernel.Cents(w.BalanceAmount).Sub(used)
			if !balanceLeft.IsPositive() {
				continue
			}
			n := money.Min(b.left, balanceLeft, invoiceLeft)
			if !n.IsPositive() {
				continue
			}
			b.left = b.left.Sub(n)
			invoiceLeft = invoiceLeft.Sub(n)
			used = used.Add(n)
		}
		if used.IsPositive() {
			out = append(out, WalletAmount{Wallet: w, Amount: used})
		}
	}
	return out, nil
}

// bookedTaxes returns the share of the invoice's tax booked on each fee.
func bookedTaxes(inv *invoice.Invoice, fees []*invoice.Fee, appliedTaxes []*invoice.AppliedTax) map[core.FeeID]int64 {
	ordered := slices.Clone(fees)
	slices.SortStableFunc(ordered, func(a, b *invoice.Fee) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return a.ID.Compare(b.ID)
	})
	in := tax.Invoice{TaxesAmountCents: inv.TaxesAmount}
	for _, t := range appliedTaxes {
		row := tax.InvoiceAppliedTax{
			TaxCode: t.TaxCode, TaxName: t.TaxName, TaxRate: t.TaxRate, Provider: t.TaxID.IsZero(),
			AmountCents: t.Amount, FeesAmountCents: t.FeesAmount, TaxableBaseAmountCents: t.TaxableBaseAmount,
		}
		if t.TaxDescription != nil {
			row.TaxDescription = *t.TaxDescription
		}
		in.AppliedTaxes = append(in.AppliedTaxes, row)
	}
	for _, f := range ordered {
		in.Fees = append(in.Fees, kernel.TaxFeeOf(f))
	}
	out := make(map[core.FeeID]int64, len(ordered))
	for i, b := range tax.BookTaxes(in) {
		out[ordered[i].ID] = b.AmountCents
	}
	return out
}

// applyPrepaidCredits runs step 5 (lago Credits::AppliedPrepaidCreditsService)
// under the customer's prepaid_credits lock: one settled outbound invoiced
// transaction per wallet used, traceable consumption, balance decrease and
// the granted/purchased split. remaining is the invoice total left; the
// amount applied is returned.
func (s *Service) applyPrepaidCredits(ctx context.Context, r port.Repos, inv *invoice.Invoice, fees []*invoice.Fee, appliedTaxes []*invoice.AppliedTax, remaining int64, opts Options, out *AfterTaxes) (int64, error) {
	org := inv.OrganizationID
	wallets, err := walletsForApplication(ctx, r, inv)
	if err != nil {
		return 0, err
	}
	if !opts.Preview {
		txs, err := r.WalletTransactions.ListByInvoice(ctx, org, inv.ID)
		if err != nil {
			return 0, fmt.Errorf("list wallet transactions of invoice %s: %w", inv.ID, err)
		}
		if slices.ContainsFunc(txs, func(t *wallet.Transaction) bool {
			return slices.ContainsFunc(wallets, func(w *wallet.Wallet) bool { return w.ID == t.WalletID })
		}) {
			return 0, core.NewRuleError(CodeAlreadyApplied, "prepaid credits already applied")
		}
	}
	if len(wallets) == 0 || remaining <= 0 {
		return 0, nil
	}
	if !opts.Preview {
		if err := kernel.LockCustomer(ctx, r, org, inv.CustomerID, customer.LockScopePrepaidCredits); err != nil {
			return 0, err
		}
		// Reload the wallets now that the lock is held.
		if wallets, err = walletsForApplication(ctx, r, inv); err != nil {
			return 0, err
		}
	}
	if appliedTaxes == nil {
		if appliedTaxes, err = r.InvoiceAppliedTaxes.ListByInvoice(ctx, org, inv.ID); err != nil {
			return 0, fmt.Errorf("list applied taxes of invoice %s: %w", inv.ID, err)
		}
	}
	amounts, err := AllocatePrepaidCredits(ctx, r, inv, fees, appliedTaxes, wallets, remaining)
	if err != nil {
		return 0, err
	}
	now := s.d.Now()
	var total int64
	for _, wa := range amounts {
		w := wa.Wallet
		credit, err := wallet.CreditFromMinor(wa.Amount, w.Currency, w.RateAmount)
		if err != nil {
			return 0, err
		}
		if credit.Amount = min(credit.Amount, w.BalanceAmount, remaining-total); credit.Amount <= 0 {
			continue
		}
		credit.Credits = wallet.CreditsFromMinor(credit.Amount, w.Currency, w.RateAmount)
		be, err := kernel.WalletBillingEntity(ctx, r, w)
		if err != nil {
			return 0, err
		}
		t := &wallet.Transaction{
			ID: core.NewID[core.WalletTransactionID](s.d.IDs), OrganizationID: org, WalletID: w.ID,
			TransactionType: wallet.TransactionTypeOutbound, TransactionStatus: wallet.TransactionStatusInvoiced,
			Status: wallet.SettlementStatusSettled, Source: wallet.TransactionSourceManual, Currency: w.Currency,
			Amount: credit.Amount, CreditAmount: credit.Credits, Priority: wallet.MaxPriority, InvoiceID: inv.ID,
			BillingEntityID: be, SettledAt: &now, CreatedAt: now, UpdatedAt: now,
		}
		if !opts.Preview {
			if err := s.consumeWallet(ctx, r, w, t); err != nil {
				return 0, err
			}
		}
		total += t.Amount
		out.WalletTransactions = append(out.WalletTransactions, t)
	}
	inv.PrepaidCreditAmount += total
	if !opts.Preview && total > 0 {
		if err := s.splitPrepaidCredit(ctx, r, inv, out); err != nil {
			return 0, err
		}
	}
	return total, nil
}

// consumeWallet stores the outbound transaction t of wallet w, records the
// consumption of a traceable wallet and decreases its balance.
func (s *Service) consumeWallet(ctx context.Context, r port.Repos, w *wallet.Wallet, t *wallet.Transaction) error {
	if err := r.WalletTransactions.Create(ctx, t); err != nil {
		return fmt.Errorf("create wallet transaction: %w", err)
	}
	if w.Traceable {
		if err := kernel.TrackWalletConsumption(ctx, s.d, r, t, core.WalletTransactionID{}); err != nil {
			return err
		}
	}
	if err := kernel.DecreaseWalletBalance(ctx, s.d, r, w, t); err != nil {
		return err
	}
	if err := kernel.Emit(ctx, s.d, t.OrganizationID, webhook.EventTypeWalletTransactionCreated, "wallet_transaction", kernel.ObjectID(t.ID), t); err != nil {
		return err
	}
	// source: app/services/credits/applied_prepaid_credits_service.rb:58
	return s.d.LogWalletTransaction(ctx, r, auditlog.ActivityWalletTransactionCreated, nil, t, w)
}

// splitPrepaidCredit sets the granted and purchased parts of the prepaid
// credit when every wallet of the customer is traceable.
func (s *Service) splitPrepaidCredit(ctx context.Context, r port.Repos, inv *invoice.Invoice, out *AfterTaxes) error {
	org := inv.OrganizationID
	all, err := kernel.All(func(p core.PageRequest) (core.Page[*wallet.Wallet], error) {
		return r.Wallets.List(ctx, org, wallet.Filter{CustomerID: inv.CustomerID}, p)
	})
	if err != nil {
		return fmt.Errorf("list wallets: %w", err)
	}
	if slices.ContainsFunc(all, func(w *wallet.Wallet) bool { return !w.Traceable }) {
		return nil
	}
	var granted, purchased int64
	for _, t := range out.WalletTransactions {
		cs, err := r.WalletTransactions.ListConsumptionsByOutbound(ctx, org, t.ID)
		if err != nil {
			return fmt.Errorf("list consumptions of wallet transaction %s: %w", t.ID, err)
		}
		for _, c := range cs {
			in, err := r.WalletTransactions.Get(ctx, org, c.InboundTransactionID)
			if err != nil {
				return fmt.Errorf("get wallet transaction %s: %w", c.InboundTransactionID, err)
			}
			if in.TransactionStatus == wallet.TransactionStatusGranted {
				granted += c.ConsumedAmount
			} else {
				purchased += c.ConsumedAmount
			}
		}
	}
	if granted > 0 {
		inv.PrepaidGrantedCreditAmount = granted
		out.PrepaidGrantedCredit = granted
	}
	if purchased > 0 {
		inv.PrepaidPurchasedCreditAmount = purchased
		out.PrepaidPurchasedCredit = purchased
	}
	return nil
}
