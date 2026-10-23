package billing_test

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// walScenario returns a scenario at now whose billing entity has the
// invoice grace period of graceDays.
func walScenario(t *testing.T, now time.Time, graceDays int) *scenario {
	t.Helper()
	s := newScenario(t, now)
	if graceDays > 0 {
		s.UpdateBillingEntity(func(b *organization.BillingEntity) { b.InvoiceGracePeriodDays = graceDays })
	}
	return s
}

// walDec parses a decimal.
func walDec(v string) *money.Decimal {
	d := money.MustParse(v)
	return &d
}

// walCreate creates a wallet of c through the wallets service (lago
// create_wallet): USD, rate 1 unless set, then drains the jobs.
func (s *scenario) walCreate(c *customer.Customer, in wallets.CreateInput) *wallet.Wallet {
	s.T.Helper()
	in.CustomerID = c.ID
	if in.Currency.IsZero() {
		in.Currency = billingtest.USD
	}
	if in.RateAmount.IsZero() {
		in.RateAmount = money.One
	}
	w, err := s.E.Wallets.Create(s.Ctx, s.OrgID(), in)
	s.must(err)
	s.drain()
	return s.ReloadWallet(w.ID)
}

// walTopUp creates wallet transactions (lago create_wallet_transaction),
// then drains the jobs.
func (s *scenario) walTopUp(in wallets.TransactionInput) []*wallet.Transaction {
	s.T.Helper()
	out, err := s.E.Wallets.CreateTransactions(s.Ctx, s.OrgID(), in)
	s.must(err)
	s.drain()
	return out
}

// walTransactions returns the transactions of wallet w, oldest first.
func (s *scenario) walTransactions(w *wallet.Wallet) []*wallet.Transaction {
	s.T.Helper()
	return billingtest.Read(s.Harness, func(ctx context.Context, r port.Repos) ([]*wallet.Transaction, error) {
		return kernel.All(func(p core.PageRequest) (core.Page[*wallet.Transaction], error) {
			return r.WalletTransactions.List(ctx, s.OrgID(), wallet.TransactionFilter{WalletID: w.ID}, p)
		})
	})
}

// walSubscribe subscribes c to p now through the lifecycle service (lago
// create_subscription), then drains the jobs.
func (s *scenario) walSubscribe(c *customer.Customer, p *catalog.Plan, opts ...func(*subscription.Subscription)) *subscription.Subscription {
	s.T.Helper()
	id := core.NewID[core.SubscriptionID](s.IDs)
	sub := subscription.New(s.OrgID(), id, c.ID, p.ID, "sub_"+id.String()[30:], s.Now(), subscription.PlanFacts{PayInAdvance: p.PayInAdvance}, s.Now())
	for _, o := range opts {
		o(sub)
	}
	out, err := s.E.Lifecycle.Create(s.Ctx, sub)
	s.must(err)
	s.drain()
	return billingtest.Read(s.Harness, func(ctx context.Context, r port.Repos) (*subscription.Subscription, error) {
		return r.Subscriptions.Get(ctx, s.OrgID(), out.ID)
	})
}

// walSubInvoices returns the invoices of c billing subscription sub,
// oldest first.
func (s *scenario) walSubInvoices(c *customer.Customer, sub *subscription.Subscription) []*invoice.Invoice {
	s.T.Helper()
	var out []*invoice.Invoice
	for _, inv := range s.Invoices(c.ID) {
		subs := billingtest.Read(s.Harness, func(ctx context.Context, r port.Repos) ([]*invoice.Subscription, error) {
			return r.InvoiceSubscriptions.ListByInvoice(ctx, s.OrgID(), inv.ID)
		})
		for _, is := range subs {
			if is.SubscriptionID == sub.ID {
				out = append(out, inv)
				break
			}
		}
	}
	return out
}

// walBillingEntityTax creates a tax of rate applied to the billing entity.
func (s *scenario) walBillingEntityTax(rate string) *catalog.Tax {
	s.T.Helper()
	t := s.Tax("vat_"+rate, rate)
	s.UpdateBillingEntity(func(b *organization.BillingEntity) { b.TaxIDs = append(b.TaxIDs, t.ID) })
	return t
}

// walBalances are the balances of a wallet: amounts in minor units,
// credits as decimals.
type walBalances struct {
	Balance, OngoingBalance, OngoingUsage        int64
	Credits, CreditsOngoing, CreditsOngoingUsage string
}

// walExpect reloads w and checks its balances.
func (s *scenario) walExpect(w *wallet.Wallet, want walBalances) {
	s.T.Helper()
	got := s.ReloadWallet(w.ID)
	ints := []struct {
		name      string
		got, want int64
	}{
		{"balance", got.BalanceAmount, want.Balance},
		{"ongoing balance", got.OngoingBalanceAmount, want.OngoingBalance},
		{"ongoing usage balance", got.OngoingUsageBalanceAmount, want.OngoingUsage},
	}
	for _, c := range ints {
		if c.got != c.want {
			s.T.Errorf("wallet %s %s: got = %d, want = %d", walName(got), c.name, c.got, c.want)
		}
	}
	decs := []struct {
		name string
		got  money.Decimal
		want string
	}{
		{"credits balance", got.CreditsBalance, want.Credits},
		{"credits ongoing balance", got.CreditsOngoingBalance, want.CreditsOngoing},
		{"credits ongoing usage balance", got.CreditsOngoingUsageBalance, want.CreditsOngoingUsage},
	}
	for _, c := range decs {
		if c.want == "" {
			continue
		}
		if !c.got.Equal(money.MustParse(c.want)) {
			s.T.Errorf("wallet %s %s: got = %s, want = %s", walName(got), c.name, c.got, c.want)
		}
	}
}

// walName names a wallet in failure messages.
func walName(w *wallet.Wallet) string {
	if w.Name != nil {
		return *w.Name
	}
	return w.ID.String()
}

// walTx is the expected shape of a wallet transaction.
type walTx struct {
	Source  wallet.TransactionSource
	Type    wallet.TransactionType
	Status  wallet.TransactionStatus
	Settled wallet.SettlementStatus
	Amount  string // in major units
	Credits string
}

// walExpectTx checks t against want.
func (s *scenario) walExpectTx(t *wallet.Transaction, want walTx) {
	s.T.Helper()
	if t == nil {
		s.T.Errorf("transaction: got = nil, want = %+v", want)
		return
	}
	if want.Source != "" && t.Source != want.Source {
		s.T.Errorf("transaction source: got = %s, want = %s", t.Source, want.Source)
	}
	if want.Type != "" && t.TransactionType != want.Type {
		s.T.Errorf("transaction type: got = %s, want = %s", t.TransactionType, want.Type)
	}
	if want.Status != "" && t.TransactionStatus != want.Status {
		s.T.Errorf("transaction status: got = %s, want = %s", t.TransactionStatus, want.Status)
	}
	if want.Settled != "" && t.Status != want.Settled {
		s.T.Errorf("transaction settlement: got = %s, want = %s", t.Status, want.Settled)
	}
	if want.Amount != "" {
		a := t.Currency.FromMinor(t.Amount)
		if !a.Equal(money.MustParse(want.Amount)) {
			s.T.Errorf("transaction amount: got = %s, want = %s", a, want.Amount)
		}
	}
	if want.Credits != "" && !t.CreditAmount.Equal(money.MustParse(want.Credits)) {
		s.T.Errorf("transaction credits: got = %s, want = %s", t.CreditAmount, want.Credits)
	}
}

// walTopUpTx is a settled granted manual inbound transaction.
func walTopUpTx(amount, credits string) walTx {
	return walTx{
		Source: wallet.TransactionSourceManual, Type: wallet.TransactionTypeInbound,
		Status: wallet.TransactionStatusGranted, Settled: wallet.SettlementStatusSettled, Amount: amount, Credits: credits,
	}
}

// walInvoicedTx is a settled invoiced manual outbound transaction.
func walInvoicedTx(amount, credits string) walTx {
	return walTx{
		Source: wallet.TransactionSourceManual, Type: wallet.TransactionTypeOutbound,
		Status: wallet.TransactionStatusInvoiced, Settled: wallet.SettlementStatusSettled, Amount: amount, Credits: credits,
	}
}

// walSettle runs the delayed jobs as well, as lago's
// perform_all_enqueued_jobs does, then drains.
func (s *scenario) walSettle() {
	s.T.Helper()
	for range 100 {
		s.drain()
		if len(s.future) == 0 {
			return
		}
		for i := range s.future {
			s.future[i].RunAt = time.Time{}
		}
	}
	s.T.Fatalf("delayed jobs never settle")
}

// walRefresh is lago's recalculate_wallet_balances: it refreshes the
// ongoing balances of c's wallets and runs every job, delayed ones
// included.
func (s *scenario) walRefresh(c *customer.Customer) {
	s.T.Helper()
	s.performWalletRefresh(c.ID)
	s.walSettle()
}

// walSetTraceable changes whether w records consumptions (lago
// wallet.update!(traceable:)).
func (s *scenario) walSetTraceable(w *wallet.Wallet, traceable bool) {
	s.T.Helper()
	w = s.ReloadWallet(w.ID)
	w.Traceable = traceable
	s.Do(func(ctx context.Context, r port.Repos) error { return r.Wallets.Update(ctx, w) })
}

// walUpdateTx changes a stored wallet transaction.
func (s *scenario) walUpdateTx(t *wallet.Transaction, fn func(*wallet.Transaction)) {
	s.T.Helper()
	t = s.ReloadWalletTransaction(t.ID)
	fn(t)
	s.Do(func(ctx context.Context, r port.Repos) error { return r.WalletTransactions.Update(ctx, t) })
}

// walFundings returns the consumptions funding the outbound transaction t
// (lago GET /wallet_transactions/:id/fundings).
func (s *scenario) walFundings(t *wallet.Transaction) []*wallet.Consumption {
	s.T.Helper()
	return billingtest.Read(s.Harness, func(ctx context.Context, r port.Repos) ([]*wallet.Consumption, error) {
		return r.WalletTransactions.ListConsumptionsByOutbound(ctx, s.OrgID(), t.ID)
	})
}

// walConsumptions returns the consumptions of the inbound transaction t
// (lago GET /wallet_transactions/:id/consumptions).
func (s *scenario) walConsumptions(t *wallet.Transaction) []*wallet.Consumption {
	s.T.Helper()
	return billingtest.Read(s.Harness, func(ctx context.Context, r port.Repos) ([]*wallet.Consumption, error) {
		return r.WalletTransactions.ListConsumptionsByInbound(ctx, s.OrgID(), t.ID)
	})
}

// walOutbound returns the outbound transactions of w with status, oldest
// first.
func (s *scenario) walOutbound(w *wallet.Wallet, status wallet.TransactionStatus) []*wallet.Transaction {
	s.T.Helper()
	var out []*wallet.Transaction
	for _, t := range s.walTransactions(w) {
		if t.TransactionType == wallet.TransactionTypeOutbound && t.TransactionStatus == status {
			out = append(out, t)
		}
	}
	return out
}

// walInbound returns the inbound transactions of w, oldest first.
func (s *scenario) walInbound(w *wallet.Wallet) []*wallet.Transaction {
	s.T.Helper()
	var out []*wallet.Transaction
	for _, t := range s.walTransactions(w) {
		if t.TransactionType == wallet.TransactionTypeInbound {
			out = append(out, t)
		}
	}
	return out
}

// walRemaining returns the remaining amount of a stored inbound
// transaction, -1 when it has none.
func (s *scenario) walRemaining(t *wallet.Transaction) int64 {
	s.T.Helper()
	t = s.ReloadWalletTransaction(t.ID)
	if t.RemainingAmount == nil {
		return -1
	}
	return *t.RemainingAmount
}

// walExpectFundings checks that the outbound transaction t is funded by
// the inbound transactions of want, with the amounts in minor units.
func (s *scenario) walExpectFundings(t *wallet.Transaction, want map[core.WalletTransactionID]int64) {
	s.T.Helper()
	fs := s.walFundings(t)
	if len(fs) != len(want) {
		s.T.Errorf("fundings: got = %d, want = %d", len(fs), len(want))
	}
	for _, f := range fs {
		if w, ok := want[f.InboundTransactionID]; !ok || w != f.ConsumedAmount {
			s.T.Errorf("funding by %s: got = %d, want = %d", f.InboundTransactionID, f.ConsumedAmount, w)
		}
	}
}

// walChargeFees returns the fees of charge id.
func (s *scenario) walChargeFees(id core.ChargeID) []*invoice.Fee {
	s.T.Helper()
	var out []*invoice.Fee
	s.tx(func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = r.Fees.ListByCharge(ctx, s.OrgID(), id)
		return err
	})
	return out
}
