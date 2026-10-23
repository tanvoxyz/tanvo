package wallets_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

var (
	eur = money.MustCurrency("EUR")
	usd = billingtest.USD
	t0  = billingtest.Date(2024, time.March, 5, 10)
)

// fakeUsage is a [wallets.UsageSource] returning fixed fees per customer.
type fakeUsage map[core.CustomerID][]wallets.UsageFee

func (f fakeUsage) OngoingUsage(_ context.Context, _ port.Repos, c *customer.Customer) ([]wallets.UsageFee, error) {
	return f[c.ID], nil
}

func setup(t *testing.T, now time.Time) (*billingtest.Harness, *wallets.Service) {
	t.Helper()
	h := billingtest.New(t, now)
	return h, wallets.New(h.Deps)
}

// runJobs runs the queued wallet jobs until none is left and returns the
// kinds of the other jobs.
func runJobs(t *testing.T, h *billingtest.Harness, s *wallets.Service) []string {
	t.Helper()
	var others []string
	for range 20 {
		jobs := h.Store.TakeJobs()
		if len(jobs) == 0 {
			return others
		}
		for _, j := range jobs {
			ok, err := s.HandleJob(h.Ctx, j)
			if err != nil {
				t.Fatalf("job %s: %v", j.Kind, err)
			}
			if !ok {
				others = append(others, j.Kind)
			}
		}
	}
	t.Fatalf("jobs keep being enqueued")
	return nil
}

// codes returns the "field/code" pairs of a validation error, or the code
// of a rule error.
func codes(err error) []string {
	var ve *core.ValidationError
	if errors.As(err, &ve) {
		out := make([]string, 0, len(ve.Errors))
		for _, fe := range ve.Errors {
			out = append(out, fe.Field+"/"+fe.Code)
		}
		return out
	}
	var re *core.RuleError
	if errors.As(err, &re) {
		return []string{re.Code}
	}
	if err != nil {
		return []string{err.Error()}
	}
	return nil
}

func wantCodes(t *testing.T, err error, want ...string) {
	t.Helper()
	if got := codes(err); !slices.Equal(got, want) {
		t.Errorf("errors: got = %v, want = %v", got, want)
	}
}

func dec(s string) *money.Decimal {
	d := money.MustParse(s)
	return &d
}

func ptr[T any](v T) *T { return &v }

func eq(a, b money.Decimal) bool { return a.Cmp(b) == 0 }

func wantDec(t *testing.T, name string, got money.Decimal, want string) {
	t.Helper()
	if !eq(got, money.MustParse(want)) {
		t.Errorf("%s: got = %s, want = %s", name, got, want)
	}
}

func transactions(h *billingtest.Harness, w core.WalletID) []*wallet.Transaction {
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) ([]*wallet.Transaction, error) {
		p, err := r.WalletTransactions.List(ctx, h.OrgID(), wallet.TransactionFilter{WalletID: w}, core.PageRequest{Size: 1000})
		return p.Items, err
	})
}

func rules(h *billingtest.Harness, w core.WalletID) []*wallet.RecurringTransactionRule {
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) ([]*wallet.RecurringTransactionRule, error) {
		return r.RecurringTransactionRules.ListByWallet(ctx, h.OrgID(), w)
	})
}

func activeRules(h *billingtest.Harness, w core.WalletID) []*wallet.RecurringTransactionRule {
	var out []*wallet.RecurringTransactionRule
	for _, r := range rules(h, w) {
		if r.Status == wallet.RuleStatusActive {
			out = append(out, r)
		}
	}
	return out
}

// addRule stores a rule on wallet w.
func addRule(h *billingtest.Harness, w *wallet.Wallet, opts ...func(*wallet.RecurringTransactionRule)) *wallet.RecurringTransactionRule {
	h.T.Helper()
	r := &wallet.RecurringTransactionRule{
		ID: core.NewID[core.RecurringTransactionRuleID](h.IDs), OrganizationID: h.OrgID(), WalletID: w.ID,
		Trigger: wallet.RecurringTriggerInterval, Interval: period.Monthly, Method: wallet.RecurringMethodFixed,
		PaidCredits: money.MustParse("10"), GrantedCredits: money.MustParse("10"),
		Status: wallet.RuleStatusActive, CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	for _, o := range opts {
		o(r)
	}
	h.Do(func(ctx context.Context, rp port.Repos) error { return rp.RecurringTransactionRules.Create(ctx, r) })
	return r
}

// addTransaction stores a transaction on wallet w.
func addTransaction(h *billingtest.Harness, w *wallet.Wallet, opts ...func(*wallet.Transaction)) *wallet.Transaction {
	h.T.Helper()
	t := &wallet.Transaction{
		ID: core.NewID[core.WalletTransactionID](h.IDs), OrganizationID: h.OrgID(), WalletID: w.ID,
		TransactionType: wallet.TransactionTypeInbound, TransactionStatus: wallet.TransactionStatusPurchased,
		Status: wallet.SettlementStatusSettled, Source: wallet.TransactionSourceManual, Currency: w.Currency,
		Amount: 1000, CreditAmount: money.MustParse("10"), Priority: w.Priority, CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	for _, o := range opts {
		o(t)
	}
	h.Do(func(ctx context.Context, r port.Repos) error { return r.WalletTransactions.Create(ctx, t) })
	return t
}

func countWebhooks(h *billingtest.Harness, typ webhook.EventType) int {
	n := 0
	for _, w := range h.Webhooks() {
		if w == typ {
			n++
		}
	}
	return n
}

// terminated terminates a wallet fixture at at.
func terminated(at time.Time) func(*wallet.Wallet) {
	return func(w *wallet.Wallet) {
		if err := w.Terminate(at); err != nil {
			panic(err)
		}
	}
}
