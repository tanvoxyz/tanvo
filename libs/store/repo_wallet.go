package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/period"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindWallet binds the repositories of the wallet aggregates.
func (t *txState) bindWallet(r *port.Repos) {
	r.Wallets = walletRepo{t}
	r.WalletTransactions = walletTransactionRepo{t}
	r.RecurringTransactionRules = recurringTransactionRuleRepo{t}
}

// walletEnum returns the text of an optional enumeration column ("" to
// NULL).
func walletEnum[E ~string](e E) *string { return optString(string(e)) }

// walletRepo implements wallet.Repository.
type walletRepo struct{ t *txState }

var _ wallet.Repository = walletRepo{}

func walletParams(w *wallet.Wallet) db.CreateWalletParams {
	return db.CreateWalletParams{
		OrganizationID:                   w.OrganizationID.UUID(),
		ID:                               w.ID.UUID(),
		CustomerID:                       w.CustomerID.UUID(),
		Code:                             w.Code,
		Name:                             w.Name,
		Status:                           string(w.Status),
		Currency:                         w.Currency.Code(),
		RateAmount:                       w.RateAmount,
		Priority:                         int32(w.Priority), // validated 1–50
		ExpirationAt:                     optUTC(w.ExpirationAt),
		BalanceAmount:                    w.BalanceAmount,
		CreditsBalance:                   w.CreditsBalance,
		OngoingBalanceAmount:             w.OngoingBalanceAmount,
		CreditsOngoingBalance:            w.CreditsOngoingBalance,
		OngoingUsageBalanceAmount:        w.OngoingUsageBalanceAmount,
		CreditsOngoingUsageBalance:       w.CreditsOngoingUsageBalance,
		ConsumedAmount:                   w.ConsumedAmount,
		ConsumedCredits:                  w.ConsumedCredits,
		PaidTopUpMinAmount:               w.PaidTopUpMinAmount,
		PaidTopUpMaxAmount:               w.PaidTopUpMaxAmount,
		InvoiceRequiresSuccessfulPayment: w.InvoiceRequiresSuccessfulPayment,
		AllowedFeeTypes:                  enumStrings(w.AllowedFeeTypes),
		Traceable:                        w.Traceable,
		PurchaseOrderNumber:              w.PurchaseOrderNumber,
		X402Enabled:                      w.X402Enabled,
		BillingEntityID:                  optUUID(w.BillingEntityID),
		PaymentMethodType:                walletEnum(w.PaymentMethodType),
		PaymentMethodID:                  optUUID(w.PaymentMethodID),
		LastBalanceSyncAt:                optUTC(w.LastBalanceSyncAt),
		LastConsumedCreditAt:             optUTC(w.LastConsumedCreditAt),
		DepletedOngoingBalance:           w.DepletedOngoingBalance,
		Metadata:                         encodeItemMetadata(w.Metadata),
		SkipInvoiceCustomSections:        w.InvoiceCustomSections.Skip,
		InvoiceCustomSectionIds:          uuids(w.InvoiceCustomSections.IDs),
		TerminatedAt:                     optUTC(w.TerminatedAt),
		CreatedAt:                        utc(w.CreatedAt),
		UpdatedAt:                        utc(w.UpdatedAt),
	}
}

// walletRow is the common shape of the wallet reads; the row types sqlc
// generates for each query convert to it.
type walletRow struct {
	Wallet            db.Wallet
	BillableMetricIds []uuid.UUID
}

func walletOf(row walletRow) (*wallet.Wallet, error) {
	r := row.Wallet
	cur, err := mustCurrency(r.Currency)
	if err != nil {
		return nil, fmt.Errorf("wallet %s: %w", r.ID, err)
	}
	md, err := decodeItemMetadata(r.Metadata)
	if err != nil {
		return nil, fmt.Errorf("wallet %s: %w", r.ID, err)
	}
	return &wallet.Wallet{
		ID:                               idOf[core.WalletID](r.ID),
		OrganizationID:                   idOf[core.OrganizationID](r.OrganizationID),
		CustomerID:                       idOf[core.CustomerID](r.CustomerID),
		Code:                             r.Code,
		Name:                             r.Name,
		Status:                           wallet.Status(r.Status),
		Currency:                         cur,
		RateAmount:                       r.RateAmount,
		Priority:                         int(r.Priority),
		ExpirationAt:                     optUTC(r.ExpirationAt),
		BalanceAmount:                    r.BalanceAmount,
		CreditsBalance:                   r.CreditsBalance,
		OngoingBalanceAmount:             r.OngoingBalanceAmount,
		CreditsOngoingBalance:            r.CreditsOngoingBalance,
		OngoingUsageBalanceAmount:        r.OngoingUsageBalanceAmount,
		CreditsOngoingUsageBalance:       r.CreditsOngoingUsageBalance,
		ConsumedAmount:                   r.ConsumedAmount,
		ConsumedCredits:                  r.ConsumedCredits,
		PaidTopUpMinAmount:               r.PaidTopUpMinAmount,
		PaidTopUpMaxAmount:               r.PaidTopUpMaxAmount,
		InvoiceRequiresSuccessfulPayment: r.InvoiceRequiresSuccessfulPayment,
		AllowedFeeTypes:                  enumsOf[core.FeeType](r.AllowedFeeTypes),
		BillableMetricIDs:                idsOf[core.BillableMetricID](row.BillableMetricIds),
		Traceable:                        r.Traceable,
		PurchaseOrderNumber:              r.PurchaseOrderNumber,
		X402Enabled:                      r.X402Enabled,
		BillingEntityID:                  optIDOf[core.BillingEntityID](r.BillingEntityID),
		PaymentMethodType:                core.PaymentMethodType(deref(r.PaymentMethodType)),
		PaymentMethodID:                  optIDOf[core.PaymentMethodID](r.PaymentMethodID),
		LastBalanceSyncAt:                optUTC(r.LastBalanceSyncAt),
		LastConsumedCreditAt:             optUTC(r.LastConsumedCreditAt),
		DepletedOngoingBalance:           r.DepletedOngoingBalance,
		Metadata:                         md,
		InvoiceCustomSections:            sectionsOf(r.SkipInvoiceCustomSections, r.InvoiceCustomSectionIds),
		TerminatedAt:                     optUTC(r.TerminatedAt),
		CreatedAt:                        utc(r.CreatedAt),
		UpdatedAt:                        utc(r.UpdatedAt),
	}, nil
}

// sectionsOf returns the invoice custom section selection of a row.
func sectionsOf(skip bool, ids []uuid.UUID) wallet.CustomSections {
	return wallet.CustomSections{Skip: skip, IDs: idsOf[core.InvoiceCustomSectionID](ids)}
}

// writeMetrics writes the targeted billable metrics of w, replacing the
// stored ones when replace is set.
func (r walletRepo) writeMetrics(ctx context.Context, q *db.Queries, w *wallet.Wallet, replace bool) error {
	org, id := w.OrganizationID.UUID(), w.ID.UUID()
	if replace {
		if err := q.DeleteWalletBillableMetrics(ctx, db.DeleteWalletBillableMetricsParams{OrganizationID: org, WalletID: id}); err != nil {
			return err
		}
	}
	if len(w.BillableMetricIDs) == 0 {
		return nil
	}
	return q.InsertWalletBillableMetrics(ctx, db.InsertWalletBillableMetricsParams{
		OrganizationID: org, WalletID: id, BillableMetricIds: uuids(w.BillableMetricIDs),
	})
}

func (r walletRepo) Create(ctx context.Context, w *wallet.Wallet) error {
	q, err := r.t.queries(ctx, w.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateWallet(ctx, walletParams(w))
	if err := insertErr("wallet", n, err); err != nil {
		return err
	}
	if err := r.writeMetrics(ctx, q, w, false); err != nil {
		return fmt.Errorf("create wallet %s: %w", w.ID, mapErr(err))
	}
	return nil
}

func (r walletRepo) Update(ctx context.Context, w *wallet.Wallet) error {
	q, err := r.t.queries(ctx, w.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() error {
		var err error
		n, err = q.UpdateWallet(ctx, db.UpdateWalletParams(walletParams(w)))
		if err != nil || n == 0 {
			return err
		}
		return r.writeMetrics(ctx, q, w, true)
	})
	return updateErr("wallet", w.ID, n, err)
}

func (r walletRepo) Get(ctx context.Context, org core.OrganizationID, id core.WalletID) (*wallet.Wallet, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetWallet(ctx, db.GetWalletParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("wallet", id, err)
	}
	return walletOf(walletRow(row))
}

func (r walletRepo) GetByCode(ctx context.Context, org core.OrganizationID, customer core.CustomerID, code string) (*wallet.Wallet, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetWalletByCode(ctx, db.GetWalletByCodeParams{OrganizationID: org.UUID(), CustomerID: customer.UUID(), Code: &code})
	if err != nil {
		return nil, getErr("wallet", code, err)
	}
	return walletOf(walletRow(row))
}

func (r walletRepo) List(ctx context.Context, org core.OrganizationID, f wallet.Filter, page core.PageRequest) (core.Page[*wallet.Wallet], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*wallet.Wallet]{}, err
	}
	if f.Recent {
		after, afterAt, lim, err := timeCursorArgs(page)
		if err != nil {
			return core.Page[*wallet.Wallet]{}, err
		}
		rows, err := q.ListWalletsRecent(ctx, db.ListWalletsRecentParams{
			OrganizationID: org.UUID(), CustomerID: optUUID(f.CustomerID), Status: walletEnum(f.Status), After: after, AfterAt: afterAt, Lim: lim,
		})
		if err != nil {
			return core.Page[*wallet.Wallet]{}, fmt.Errorf("list wallets: %w", mapErr(err))
		}
		return pageOf(rows, page,
			func(r db.ListWalletsRecentRow) (*wallet.Wallet, error) { return walletOf(walletRow(r)) },
			func(w *wallet.Wallet) string { return recentCursor(w.CreatedAt, w.ID.UUID()) })
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*wallet.Wallet]{}, err
	}
	rows, err := q.ListWallets(ctx, db.ListWalletsParams{
		OrganizationID: org.UUID(), CustomerID: optUUID(f.CustomerID), Status: walletEnum(f.Status), After: after, Lim: lim,
	})
	if err != nil {
		return core.Page[*wallet.Wallet]{}, fmt.Errorf("list wallets: %w", mapErr(err))
	}
	return pageOf(rows, page,
		func(r db.ListWalletsRow) (*wallet.Wallet, error) { return walletOf(walletRow(r)) },
		func(w *wallet.Wallet) string { return w.ID.String() })
}

func (r walletRepo) ListActiveForApplication(ctx context.Context, org core.OrganizationID, customer core.CustomerID) ([]*wallet.Wallet, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListWalletsActiveForApplication(ctx, db.ListWalletsActiveForApplicationParams{OrganizationID: org.UUID(), CustomerID: customer.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list wallets for application: %w", mapErr(err))
	}
	return convAll(rows, func(r db.ListWalletsActiveForApplicationRow) (*wallet.Wallet, error) { return walletOf(walletRow(r)) })
}

func (r walletRepo) CountActive(ctx context.Context, org core.OrganizationID, customer core.CustomerID) (int, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	n, err := q.CountActiveWallets(ctx, db.CountActiveWalletsParams{OrganizationID: org.UUID(), CustomerID: customer.UUID()})
	if err != nil {
		return 0, fmt.Errorf("count active wallets: %w", mapErr(err))
	}
	return int(n), nil
}

func (r walletRepo) ListExpired(ctx context.Context, org core.OrganizationID, now time.Time) ([]*wallet.Wallet, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListExpiredWallets(ctx, db.ListExpiredWalletsParams{OrganizationID: org.UUID(), Now: walletPtrUTC(now)})
	if err != nil {
		return nil, fmt.Errorf("list expired wallets: %w", mapErr(err))
	}
	return convAll(rows, func(r db.ListExpiredWalletsRow) (*wallet.Wallet, error) { return walletOf(walletRow(r)) })
}

// walletPtrUTC returns a pointer to t in UTC, for comparisons with nullable
// timestamp columns.
func walletPtrUTC(t time.Time) *time.Time {
	u := t.UTC()
	return &u
}

// walletTransactionRepo implements wallet.TransactionRepository.
type walletTransactionRepo struct{ t *txState }

var _ wallet.TransactionRepository = walletTransactionRepo{}

func walletTransactionParams(t *wallet.Transaction) db.CreateWalletTransactionParams {
	return db.CreateWalletTransactionParams{
		OrganizationID:                   t.OrganizationID.UUID(),
		ID:                               t.ID.UUID(),
		WalletID:                         t.WalletID.UUID(),
		TransactionType:                  string(t.TransactionType),
		TransactionStatus:                string(t.TransactionStatus),
		Status:                           string(t.Status),
		Source:                           string(t.Source),
		Currency:                         t.Currency.Code(),
		Amount:                           t.Amount,
		CreditAmount:                     t.CreditAmount,
		RemainingAmount:                  t.RemainingAmount,
		Priority:                         int32(t.Priority), // validated 1–50
		Name:                             t.Name,
		InvoiceID:                        optUUID(t.InvoiceID),
		CreditNoteID:                     optUUID(t.CreditNoteID),
		RecurringTransactionRuleID:       optUUID(t.RecurringTransactionRuleID),
		Metadata:                         encodeMetadata(t.Metadata),
		PaymentMethodType:                walletEnum(t.PaymentMethodType),
		PaymentMethodID:                  optUUID(t.PaymentMethodID),
		InvoiceRequiresSuccessfulPayment: t.InvoiceRequiresSuccessfulPayment,
		PurchaseOrderNumber:              t.PurchaseOrderNumber,
		VoidedInvoiceID:                  optUUID(t.VoidedInvoiceID),
		BillingEntityID:                  optUUID(t.BillingEntityID),
		SkipInvoiceCustomSections:        t.InvoiceCustomSections.Skip,
		InvoiceCustomSectionIds:          uuids(t.InvoiceCustomSections.IDs),
		SettledAt:                        optUTC(t.SettledAt),
		FailedAt:                         optUTC(t.FailedAt),
		CreatedAt:                        utc(t.CreatedAt),
		UpdatedAt:                        utc(t.UpdatedAt),
	}
}

func walletTransactionOf(r db.WalletTransaction) (*wallet.Transaction, error) {
	cur, err := mustCurrency(r.Currency)
	if err != nil {
		return nil, fmt.Errorf("wallet transaction %s: %w", r.ID, err)
	}
	md, err := decodeMetadata(r.Metadata)
	if err != nil {
		return nil, fmt.Errorf("wallet transaction %s: %w", r.ID, err)
	}
	return &wallet.Transaction{
		ID:                               idOf[core.WalletTransactionID](r.ID),
		OrganizationID:                   idOf[core.OrganizationID](r.OrganizationID),
		WalletID:                         idOf[core.WalletID](r.WalletID),
		TransactionType:                  wallet.TransactionType(r.TransactionType),
		TransactionStatus:                wallet.TransactionStatus(r.TransactionStatus),
		Status:                           wallet.SettlementStatus(r.Status),
		Source:                           wallet.TransactionSource(r.Source),
		Currency:                         cur,
		Amount:                           r.Amount,
		CreditAmount:                     r.CreditAmount,
		RemainingAmount:                  r.RemainingAmount,
		Priority:                         int(r.Priority),
		Name:                             r.Name,
		InvoiceID:                        optIDOf[core.InvoiceID](r.InvoiceID),
		CreditNoteID:                     optIDOf[core.CreditNoteID](r.CreditNoteID),
		RecurringTransactionRuleID:       optIDOf[core.RecurringTransactionRuleID](r.RecurringTransactionRuleID),
		Metadata:                         md,
		PaymentMethodType:                core.PaymentMethodType(deref(r.PaymentMethodType)),
		PaymentMethodID:                  optIDOf[core.PaymentMethodID](r.PaymentMethodID),
		InvoiceRequiresSuccessfulPayment: r.InvoiceRequiresSuccessfulPayment,
		PurchaseOrderNumber:              r.PurchaseOrderNumber,
		VoidedInvoiceID:                  optIDOf[core.InvoiceID](r.VoidedInvoiceID),
		BillingEntityID:                  optIDOf[core.BillingEntityID](r.BillingEntityID),
		InvoiceCustomSections:            sectionsOf(r.SkipInvoiceCustomSections, r.InvoiceCustomSectionIds),
		SettledAt:                        optUTC(r.SettledAt),
		FailedAt:                         optUTC(r.FailedAt),
		CreatedAt:                        utc(r.CreatedAt),
		UpdatedAt:                        utc(r.UpdatedAt),
	}, nil
}

func walletConsumptionOf(r db.WalletTransactionConsumption) (*wallet.Consumption, error) {
	return &wallet.Consumption{
		ID:                    idOf[core.WalletTransactionConsumptionID](r.ID),
		OrganizationID:        idOf[core.OrganizationID](r.OrganizationID),
		InboundTransactionID:  idOf[core.WalletTransactionID](r.InboundTransactionID),
		OutboundTransactionID: idOf[core.WalletTransactionID](r.OutboundTransactionID),
		ConsumedAmount:        r.ConsumedAmount,
		CreatedAt:             utc(r.CreatedAt),
		UpdatedAt:             utc(r.UpdatedAt),
	}, nil
}

func (r walletTransactionRepo) Create(ctx context.Context, t *wallet.Transaction) error {
	q, err := r.t.queries(ctx, t.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateWalletTransaction(ctx, walletTransactionParams(t))
	return insertErr("wallet_transaction", n, err)
}

func (r walletTransactionRepo) Update(ctx context.Context, t *wallet.Transaction) error {
	q, err := r.t.queries(ctx, t.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateWalletTransaction(ctx, db.UpdateWalletTransactionParams(walletTransactionParams(t)))
		return err
	})
	return updateErr("wallet_transaction", t.ID, n, err)
}

func (r walletTransactionRepo) Get(ctx context.Context, org core.OrganizationID, id core.WalletTransactionID) (*wallet.Transaction, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetWalletTransaction(ctx, db.GetWalletTransactionParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("wallet_transaction", id, err)
	}
	return walletTransactionOf(row)
}

func (r walletTransactionRepo) List(ctx context.Context, org core.OrganizationID, f wallet.TransactionFilter, page core.PageRequest) (core.Page[*wallet.Transaction], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*wallet.Transaction]{}, err
	}
	if f.Recent {
		after, afterAt, lim, err := timeCursorArgs(page)
		if err != nil {
			return core.Page[*wallet.Transaction]{}, err
		}
		rows, err := q.ListWalletTransactionsRecent(ctx, db.ListWalletTransactionsRecentParams{
			OrganizationID:    org.UUID(),
			WalletID:          optUUID(f.WalletID),
			InvoiceID:         optUUID(f.InvoiceID),
			TransactionType:   walletEnum(f.TransactionType),
			TransactionStatus: walletEnum(f.TransactionStatus),
			Status:            walletEnum(f.Status),
			After:             after,
			AfterAt:           afterAt,
			Lim:               lim,
		})
		if err != nil {
			return core.Page[*wallet.Transaction]{}, fmt.Errorf("list wallet transactions: %w", mapErr(err))
		}
		return pageOf(rows, page, walletTransactionOf, func(t *wallet.Transaction) string { return recentCursor(t.CreatedAt, t.ID.UUID()) })
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*wallet.Transaction]{}, err
	}
	rows, err := q.ListWalletTransactions(ctx, db.ListWalletTransactionsParams{
		OrganizationID:    org.UUID(),
		WalletID:          optUUID(f.WalletID),
		InvoiceID:         optUUID(f.InvoiceID),
		TransactionType:   walletEnum(f.TransactionType),
		TransactionStatus: walletEnum(f.TransactionStatus),
		Status:            walletEnum(f.Status),
		After:             after,
		Lim:               lim,
	})
	if err != nil {
		return core.Page[*wallet.Transaction]{}, fmt.Errorf("list wallet transactions: %w", mapErr(err))
	}
	return pageOf(rows, page, walletTransactionOf, func(t *wallet.Transaction) string { return t.ID.String() })
}

func (r walletTransactionRepo) ListByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) ([]*wallet.Transaction, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	id := invoice.UUID()
	rows, err := q.ListWalletTransactionsByInvoice(ctx, db.ListWalletTransactionsByInvoiceParams{OrganizationID: org.UUID(), InvoiceID: &id})
	if err != nil {
		return nil, fmt.Errorf("list wallet transactions of invoice %s: %w", invoice, mapErr(err))
	}
	return convAll(rows, walletTransactionOf)
}

func (r walletTransactionRepo) GetPendingPurchase(ctx context.Context, org core.OrganizationID, rule core.RecurringTransactionRuleID) (*wallet.Transaction, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	id := rule.UUID()
	row, err := q.GetPendingPurchaseWalletTransaction(ctx, db.GetPendingPurchaseWalletTransactionParams{OrganizationID: org.UUID(), RecurringTransactionRuleID: &id})
	if err != nil {
		return nil, getErr("wallet_transaction", "pending purchase of "+rule.String(), err)
	}
	return walletTransactionOf(row)
}

func (r walletTransactionRepo) ListAvailableInbound(ctx context.Context, org core.OrganizationID, w core.WalletID) ([]*wallet.Transaction, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListAvailableInboundWalletTransactions(ctx, db.ListAvailableInboundWalletTransactionsParams{OrganizationID: org.UUID(), WalletID: w.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list available inbound transactions of wallet %s: %w", w, mapErr(err))
	}
	return convAll(rows, walletTransactionOf)
}

func (r walletTransactionRepo) CreateConsumption(ctx context.Context, c *wallet.Consumption) error {
	q, err := r.t.queries(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateWalletTransactionConsumption(ctx, db.CreateWalletTransactionConsumptionParams{
		OrganizationID:        c.OrganizationID.UUID(),
		ID:                    c.ID.UUID(),
		InboundTransactionID:  c.InboundTransactionID.UUID(),
		OutboundTransactionID: c.OutboundTransactionID.UUID(),
		ConsumedAmount:        c.ConsumedAmount,
		CreatedAt:             utc(c.CreatedAt),
		UpdatedAt:             utc(c.UpdatedAt),
	})
	return insertErr("wallet_transaction_consumption", n, err)
}

func (r walletTransactionRepo) ListConsumptionsByOutbound(ctx context.Context, org core.OrganizationID, outbound core.WalletTransactionID) ([]*wallet.Consumption, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListWalletTransactionConsumptionsByOutbound(ctx, db.ListWalletTransactionConsumptionsByOutboundParams{OrganizationID: org.UUID(), OutboundTransactionID: outbound.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list consumptions of %s: %w", outbound, mapErr(err))
	}
	return convAll(rows, walletConsumptionOf)
}

func (r walletTransactionRepo) ListConsumptionsByInbound(ctx context.Context, org core.OrganizationID, inbound core.WalletTransactionID) ([]*wallet.Consumption, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListWalletTransactionConsumptionsByInbound(ctx, db.ListWalletTransactionConsumptionsByInboundParams{OrganizationID: org.UUID(), InboundTransactionID: inbound.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list consumptions of %s: %w", inbound, mapErr(err))
	}
	return convAll(rows, walletConsumptionOf)
}

// recurringTransactionRuleRepo implements
// wallet.RecurringTransactionRuleRepository.
type recurringTransactionRuleRepo struct{ t *txState }

var _ wallet.RecurringTransactionRuleRepository = recurringTransactionRuleRepo{}

func recurringTransactionRuleParams(x *wallet.RecurringTransactionRule) db.CreateRecurringTransactionRuleParams {
	var interval *string
	if x.Interval.Valid() {
		interval = walletPtrString(x.Interval.String())
	}
	return db.CreateRecurringTransactionRuleParams{
		OrganizationID:                   x.OrganizationID.UUID(),
		ID:                               x.ID.UUID(),
		WalletID:                         x.WalletID.UUID(),
		Trigger:                          string(x.Trigger),
		Interval:                         interval,
		ThresholdCredits:                 x.ThresholdCredits,
		Method:                           string(x.Method),
		PaidCredits:                      x.PaidCredits,
		GrantedCredits:                   x.GrantedCredits,
		TargetOngoingBalance:             x.TargetOngoingBalance,
		GrantsTargetTopUp:                x.GrantsTargetTopUp,
		StartedAt:                        optUTC(x.StartedAt),
		ExpirationAt:                     optUTC(x.ExpirationAt),
		Status:                           string(x.Status),
		InvoiceRequiresSuccessfulPayment: x.InvoiceRequiresSuccessfulPayment,
		TransactionMetadata:              encodeMetadata(x.TransactionMetadata),
		TransactionName:                  x.TransactionName,
		IgnorePaidTopUpLimits:            x.IgnorePaidTopUpLimits,
		PurchaseOrderNumber:              x.PurchaseOrderNumber,
		PaymentMethodType:                walletEnum(x.PaymentMethodType),
		PaymentMethodID:                  optUUID(x.PaymentMethodID),
		SkipInvoiceCustomSections:        x.InvoiceCustomSections.Skip,
		InvoiceCustomSectionIds:          uuids(x.InvoiceCustomSections.IDs),
		TerminatedAt:                     optUTC(x.TerminatedAt),
		CreatedAt:                        utc(x.CreatedAt),
		UpdatedAt:                        utc(x.UpdatedAt),
	}
}

// walletPtrString returns &s.
func walletPtrString(s string) *string { return &s }

func recurringTransactionRuleOf(r db.RecurringTransactionRule) (*wallet.RecurringTransactionRule, error) {
	var interval period.Interval
	if r.Interval != nil {
		var err error
		if interval, err = period.ParseInterval(*r.Interval); err != nil {
			return nil, fmt.Errorf("recurring transaction rule %s: %w", r.ID, err)
		}
	}
	md, err := decodeMetadata(r.TransactionMetadata)
	if err != nil {
		return nil, fmt.Errorf("recurring transaction rule %s: %w", r.ID, err)
	}
	return &wallet.RecurringTransactionRule{
		ID:                               idOf[core.RecurringTransactionRuleID](r.ID),
		OrganizationID:                   idOf[core.OrganizationID](r.OrganizationID),
		WalletID:                         idOf[core.WalletID](r.WalletID),
		Trigger:                          wallet.RecurringTrigger(r.Trigger),
		Interval:                         interval,
		ThresholdCredits:                 r.ThresholdCredits,
		Method:                           wallet.RecurringMethod(r.Method),
		PaidCredits:                      r.PaidCredits,
		GrantedCredits:                   r.GrantedCredits,
		TargetOngoingBalance:             r.TargetOngoingBalance,
		GrantsTargetTopUp:                r.GrantsTargetTopUp,
		StartedAt:                        optUTC(r.StartedAt),
		ExpirationAt:                     optUTC(r.ExpirationAt),
		Status:                           wallet.RuleStatus(r.Status),
		InvoiceRequiresSuccessfulPayment: r.InvoiceRequiresSuccessfulPayment,
		TransactionMetadata:              md,
		TransactionName:                  r.TransactionName,
		IgnorePaidTopUpLimits:            r.IgnorePaidTopUpLimits,
		PurchaseOrderNumber:              r.PurchaseOrderNumber,
		PaymentMethodType:                core.PaymentMethodType(deref(r.PaymentMethodType)),
		PaymentMethodID:                  optIDOf[core.PaymentMethodID](r.PaymentMethodID),
		InvoiceCustomSections:            sectionsOf(r.SkipInvoiceCustomSections, r.InvoiceCustomSectionIds),
		TerminatedAt:                     optUTC(r.TerminatedAt),
		CreatedAt:                        utc(r.CreatedAt),
		UpdatedAt:                        utc(r.UpdatedAt),
	}, nil
}

func (r recurringTransactionRuleRepo) Create(ctx context.Context, x *wallet.RecurringTransactionRule) error {
	q, err := r.t.queries(ctx, x.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateRecurringTransactionRule(ctx, recurringTransactionRuleParams(x))
	return insertErr("recurring_transaction_rule", n, err)
}

func (r recurringTransactionRuleRepo) Update(ctx context.Context, x *wallet.RecurringTransactionRule) error {
	q, err := r.t.queries(ctx, x.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateRecurringTransactionRule(ctx, db.UpdateRecurringTransactionRuleParams(recurringTransactionRuleParams(x)))
		return err
	})
	return updateErr("recurring_transaction_rule", x.ID, n, err)
}

func (r recurringTransactionRuleRepo) Get(ctx context.Context, org core.OrganizationID, id core.RecurringTransactionRuleID) (*wallet.RecurringTransactionRule, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetRecurringTransactionRule(ctx, db.GetRecurringTransactionRuleParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("recurring_transaction_rule", id, err)
	}
	return recurringTransactionRuleOf(row)
}

func (r recurringTransactionRuleRepo) Delete(ctx context.Context, org core.OrganizationID, id core.RecurringTransactionRuleID) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	n, err := q.DeleteRecurringTransactionRule(ctx, db.DeleteRecurringTransactionRuleParams{OrganizationID: org.UUID(), ID: id.UUID()})
	return updateErr("recurring_transaction_rule", id, n, err)
}

func (r recurringTransactionRuleRepo) ListByWallet(ctx context.Context, org core.OrganizationID, w core.WalletID) ([]*wallet.RecurringTransactionRule, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListRecurringTransactionRulesByWallet(ctx, db.ListRecurringTransactionRulesByWalletParams{OrganizationID: org.UUID(), WalletID: w.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list recurring transaction rules of wallet %s: %w", w, mapErr(err))
	}
	return convAll(rows, recurringTransactionRuleOf)
}

func (r recurringTransactionRuleRepo) ListActiveInterval(ctx context.Context, org core.OrganizationID, now time.Time) ([]*wallet.RecurringTransactionRule, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListActiveIntervalRecurringTransactionRules(ctx, db.ListActiveIntervalRecurringTransactionRulesParams{OrganizationID: org.UUID(), Now: walletPtrUTC(now)})
	if err != nil {
		return nil, fmt.Errorf("list active interval rules: %w", mapErr(err))
	}
	return convAll(rows, recurringTransactionRuleOf)
}

func (r recurringTransactionRuleRepo) ListActiveThreshold(ctx context.Context, org core.OrganizationID, w core.WalletID, now time.Time) ([]*wallet.RecurringTransactionRule, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListActiveThresholdRecurringTransactionRules(ctx, db.ListActiveThresholdRecurringTransactionRulesParams{OrganizationID: org.UUID(), WalletID: w.UUID(), Now: walletPtrUTC(now)})
	if err != nil {
		return nil, fmt.Errorf("list active threshold rules of wallet %s: %w", w, mapErr(err))
	}
	return convAll(rows, recurringTransactionRuleOf)
}

func (r recurringTransactionRuleRepo) ListEligibleForTermination(ctx context.Context, org core.OrganizationID, now time.Time) ([]*wallet.RecurringTransactionRule, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListRecurringTransactionRulesEligibleForTermination(ctx, db.ListRecurringTransactionRulesEligibleForTerminationParams{OrganizationID: org.UUID(), Now: walletPtrUTC(now)})
	if err != nil {
		return nil, fmt.Errorf("list rules eligible for termination: %w", mapErr(err))
	}
	return convAll(rows, recurringTransactionRuleOf)
}
