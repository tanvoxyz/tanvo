package memstore

import (
	"context"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
)

// walletData holds the tables of package wallet.
type walletData struct {
	wallets      *table[core.WalletID, wallet.Wallet]
	transactions *table[core.WalletTransactionID, wallet.Transaction]
	consumptions *table[core.WalletTransactionConsumptionID, wallet.Consumption]
	rules        *table[core.RecurringTransactionRuleID, wallet.RecurringTransactionRule]
}

func newWalletData() *walletData {
	return &walletData{
		wallets: newTable[core.WalletID](func(w wallet.Wallet) wallet.Wallet {
			w.AllowedFeeTypes = slices.Clone(w.AllowedFeeTypes)
			w.BillableMetricIDs = slices.Clone(w.BillableMetricIDs)
			return w
		}),
		transactions: newTable[core.WalletTransactionID](func(t wallet.Transaction) wallet.Transaction {
			t.Metadata = slices.Clone(t.Metadata)
			if t.RemainingAmount != nil {
				r := *t.RemainingAmount
				t.RemainingAmount = &r
			}
			return t
		}),
		consumptions: newTable[core.WalletTransactionConsumptionID, wallet.Consumption](nil),
		rules: newTable[core.RecurringTransactionRuleID](func(r wallet.RecurringTransactionRule) wallet.RecurringTransactionRule {
			r.TransactionMetadata = slices.Clone(r.TransactionMetadata)
			return r
		}),
	}
}

func (d *walletData) snapshot() *walletData {
	return &walletData{
		wallets:      d.wallets.snapshot(),
		transactions: d.transactions.snapshot(),
		consumptions: d.consumptions.snapshot(),
		rules:        d.rules.snapshot(),
	}
}

// walletRepo implements wallet.Repository.
type walletRepo struct{ d *walletData }

var _ wallet.Repository = walletRepo{}

func (r walletRepo) check(w *wallet.Wallet) error {
	if w.Code == nil || !w.Active() {
		return nil
	}
	if r.d.wallets.any(func(x *wallet.Wallet) bool {
		return x.ID != w.ID && x.OrganizationID == w.OrganizationID && x.CustomerID == w.CustomerID &&
			x.Active() && x.Code != nil && *x.Code == *w.Code
	}) {
		return conflict("wallet", "code")
	}
	return nil
}

func (r walletRepo) Create(_ context.Context, w *wallet.Wallet) error {
	if r.d.wallets.has(w.ID) {
		return conflict("wallet", "id")
	}
	if err := r.check(w); err != nil {
		return err
	}
	r.d.wallets.put(w.ID, *w)
	return nil
}

func (r walletRepo) Update(_ context.Context, w *wallet.Wallet) error {
	cur, ok := r.d.wallets.get(w.ID)
	if !ok || cur.OrganizationID != w.OrganizationID {
		return notFound("wallet", w.ID)
	}
	if err := r.check(w); err != nil {
		return err
	}
	r.d.wallets.put(w.ID, *w)
	return nil
}

func (r walletRepo) Get(_ context.Context, org core.OrganizationID, id core.WalletID) (*wallet.Wallet, error) {
	w, ok := r.d.wallets.get(id)
	if !ok || w.OrganizationID != org {
		return nil, notFound("wallet", id)
	}
	return w, nil
}

func (r walletRepo) GetByCode(_ context.Context, org core.OrganizationID, customer core.CustomerID, code string) (*wallet.Wallet, error) {
	w, ok := r.d.wallets.first(func(x *wallet.Wallet) bool {
		return x.OrganizationID == org && x.CustomerID == customer && x.Active() && x.Code != nil && *x.Code == code
	})
	if !ok {
		return nil, notFound("wallet", stringer(code))
	}
	return w, nil
}

func (r walletRepo) List(_ context.Context, org core.OrganizationID, f wallet.Filter, page core.PageRequest) (core.Page[*wallet.Wallet], error) {
	rows := r.d.wallets.find(func(x *wallet.Wallet) bool {
		return x.OrganizationID == org &&
			(f.CustomerID.IsZero() || x.CustomerID == f.CustomerID) &&
			(f.Status == "" || x.Status == f.Status)
	})
	return paginate(rows, func(x *wallet.Wallet) core.WalletID { return x.ID }, page)
}

func (r walletRepo) ListActiveForApplication(_ context.Context, org core.OrganizationID, customer core.CustomerID) ([]*wallet.Wallet, error) {
	rows := r.d.wallets.find(func(x *wallet.Wallet) bool {
		return x.OrganizationID == org && x.CustomerID == customer && x.Active()
	})
	slices.SortStableFunc(rows, wallet.CompareForApplication)
	return rows, nil
}

func (r walletRepo) CountActive(_ context.Context, org core.OrganizationID, customer core.CustomerID) (int, error) {
	return r.d.wallets.count(func(x *wallet.Wallet) bool {
		return x.OrganizationID == org && x.CustomerID == customer && x.Active()
	}), nil
}

func (r walletRepo) ListExpired(_ context.Context, org core.OrganizationID, now time.Time) ([]*wallet.Wallet, error) {
	return r.d.wallets.find(func(x *wallet.Wallet) bool {
		return x.OrganizationID == org && x.Active() && x.Expired(now)
	}), nil
}

// walletTransactionRepo implements wallet.TransactionRepository.
type walletTransactionRepo struct{ d *walletData }

var _ wallet.TransactionRepository = walletTransactionRepo{}

func (r walletTransactionRepo) Create(_ context.Context, t *wallet.Transaction) error {
	if r.d.transactions.has(t.ID) {
		return conflict("wallet_transaction", "id")
	}
	r.d.transactions.put(t.ID, *t)
	return nil
}

func (r walletTransactionRepo) Update(_ context.Context, t *wallet.Transaction) error {
	cur, ok := r.d.transactions.get(t.ID)
	if !ok || cur.OrganizationID != t.OrganizationID {
		return notFound("wallet_transaction", t.ID)
	}
	r.d.transactions.put(t.ID, *t)
	return nil
}

func (r walletTransactionRepo) Get(_ context.Context, org core.OrganizationID, id core.WalletTransactionID) (*wallet.Transaction, error) {
	t, ok := r.d.transactions.get(id)
	if !ok || t.OrganizationID != org {
		return nil, notFound("wallet_transaction", id)
	}
	return t, nil
}

func (r walletTransactionRepo) List(_ context.Context, org core.OrganizationID, f wallet.TransactionFilter, page core.PageRequest) (core.Page[*wallet.Transaction], error) {
	rows := r.d.transactions.find(func(x *wallet.Transaction) bool {
		return x.OrganizationID == org &&
			(f.WalletID.IsZero() || x.WalletID == f.WalletID) &&
			(f.InvoiceID.IsZero() || x.InvoiceID == f.InvoiceID) &&
			(f.TransactionType == "" || x.TransactionType == f.TransactionType) &&
			(f.TransactionStatus == "" || x.TransactionStatus == f.TransactionStatus) &&
			(f.Status == "" || x.Status == f.Status)
	})
	return paginate(rows, func(x *wallet.Transaction) core.WalletTransactionID { return x.ID }, page)
}

func (r walletTransactionRepo) ListByInvoice(_ context.Context, org core.OrganizationID, invoice core.InvoiceID) ([]*wallet.Transaction, error) {
	return r.d.transactions.find(func(x *wallet.Transaction) bool {
		return x.OrganizationID == org && x.InvoiceID == invoice
	}), nil
}

func (r walletTransactionRepo) GetPendingPurchase(_ context.Context, org core.OrganizationID, rule core.RecurringTransactionRuleID) (*wallet.Transaction, error) {
	t, ok := r.d.transactions.first(func(x *wallet.Transaction) bool {
		return x.OrganizationID == org && x.RecurringTransactionRuleID == rule &&
			x.TransactionStatus == wallet.TransactionStatusPurchased && x.Status == wallet.SettlementStatusPending
	})
	if !ok {
		return nil, notFound("wallet_transaction", stringer("pending purchase of "+rule.String()))
	}
	return t, nil
}

func (r walletTransactionRepo) ListAvailableInbound(_ context.Context, org core.OrganizationID, w core.WalletID) ([]*wallet.Transaction, error) {
	rows := r.d.transactions.find(func(x *wallet.Transaction) bool {
		return x.OrganizationID == org && x.WalletID == w && x.Available()
	})
	slices.SortStableFunc(rows, wallet.CompareForConsumption)
	return rows, nil
}

func (r walletTransactionRepo) CreateConsumption(_ context.Context, c *wallet.Consumption) error {
	if r.d.consumptions.has(c.ID) {
		return conflict("wallet_transaction_consumption", "id")
	}
	if r.d.consumptions.any(func(x *wallet.Consumption) bool {
		return x.InboundTransactionID == c.InboundTransactionID && x.OutboundTransactionID == c.OutboundTransactionID
	}) {
		return conflict("wallet_transaction_consumption", "transactions")
	}
	r.d.consumptions.put(c.ID, *c)
	return nil
}

func (r walletTransactionRepo) ListConsumptionsByOutbound(_ context.Context, org core.OrganizationID, outbound core.WalletTransactionID) ([]*wallet.Consumption, error) {
	return r.d.consumptions.find(func(x *wallet.Consumption) bool {
		return x.OrganizationID == org && x.OutboundTransactionID == outbound
	}), nil
}

func (r walletTransactionRepo) ListConsumptionsByInbound(_ context.Context, org core.OrganizationID, inbound core.WalletTransactionID) ([]*wallet.Consumption, error) {
	return r.d.consumptions.find(func(x *wallet.Consumption) bool {
		return x.OrganizationID == org && x.InboundTransactionID == inbound
	}), nil
}

// recurringRuleRepo implements wallet.RecurringTransactionRuleRepository.
type recurringRuleRepo struct{ d *walletData }

var _ wallet.RecurringTransactionRuleRepository = recurringRuleRepo{}

func (r recurringRuleRepo) Create(_ context.Context, x *wallet.RecurringTransactionRule) error {
	if r.d.rules.has(x.ID) {
		return conflict("recurring_transaction_rule", "id")
	}
	r.d.rules.put(x.ID, *x)
	return nil
}

func (r recurringRuleRepo) Update(_ context.Context, x *wallet.RecurringTransactionRule) error {
	cur, ok := r.d.rules.get(x.ID)
	if !ok || cur.OrganizationID != x.OrganizationID {
		return notFound("recurring_transaction_rule", x.ID)
	}
	r.d.rules.put(x.ID, *x)
	return nil
}

func (r recurringRuleRepo) Get(_ context.Context, org core.OrganizationID, id core.RecurringTransactionRuleID) (*wallet.RecurringTransactionRule, error) {
	x, ok := r.d.rules.get(id)
	if !ok || x.OrganizationID != org {
		return nil, notFound("recurring_transaction_rule", id)
	}
	return x, nil
}

func (r recurringRuleRepo) Delete(_ context.Context, org core.OrganizationID, id core.RecurringTransactionRuleID) error {
	x, ok := r.d.rules.get(id)
	if !ok || x.OrganizationID != org {
		return notFound("recurring_transaction_rule", id)
	}
	r.d.rules.del(id)
	return nil
}

func (r recurringRuleRepo) ListByWallet(_ context.Context, org core.OrganizationID, w core.WalletID) ([]*wallet.RecurringTransactionRule, error) {
	return r.d.rules.find(func(x *wallet.RecurringTransactionRule) bool {
		return x.OrganizationID == org && x.WalletID == w
	}), nil
}

func (r recurringRuleRepo) ListActiveInterval(_ context.Context, org core.OrganizationID, now time.Time) ([]*wallet.RecurringTransactionRule, error) {
	return r.d.rules.find(func(x *wallet.RecurringTransactionRule) bool {
		return x.OrganizationID == org && x.Trigger == wallet.RecurringTriggerInterval && x.CurrentlyActive(now)
	}), nil
}

func (r recurringRuleRepo) ListActiveThreshold(_ context.Context, org core.OrganizationID, w core.WalletID, now time.Time) ([]*wallet.RecurringTransactionRule, error) {
	return r.d.rules.find(func(x *wallet.RecurringTransactionRule) bool {
		return x.OrganizationID == org && x.WalletID == w && x.Trigger == wallet.RecurringTriggerThreshold && x.CurrentlyActive(now)
	}), nil
}

func (r recurringRuleRepo) ListEligibleForTermination(_ context.Context, org core.OrganizationID, now time.Time) ([]*wallet.RecurringTransactionRule, error) {
	return r.d.rules.find(func(x *wallet.RecurringTransactionRule) bool {
		return x.OrganizationID == org && x.EligibleForTermination(now)
	}), nil
}
