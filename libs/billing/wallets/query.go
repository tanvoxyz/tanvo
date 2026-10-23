package wallets

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Field error codes of the queries, named after lago's.
const (
	// CodeNotTraceable: the consumptions of a wallet that does not trace
	// them were asked (field: wallet).
	CodeNotTraceable = "not_traceable"
	// CodeTransactionNotPurchased: a payment URL was asked for a
	// transaction that is not a purchase (field: base).
	CodeTransactionNotPurchased = "wallet_transaction_not_purchased"
	// CodeTransactionAlreadySettled: a payment URL was asked for a settled
	// transaction (field: base).
	CodeTransactionAlreadySettled = "wallet_transaction_already_settled"
	// CodeTransactionHasNoInvoice: a payment URL was asked for a
	// transaction without invoice (field: base).
	CodeTransactionHasNoInvoice = "wallet_transaction_has_no_attached_invoice"
)

// Resource names of the not-found errors of the queries.
const (
	// ResourceWallet names a missing wallet.
	ResourceWallet = "wallet"
	// ResourceTransaction names a missing wallet transaction.
	ResourceTransaction = "wallet_transaction"
)

// ListFilter narrows [Service.List]; zero fields match everything.
type ListFilter struct {
	// CustomerID keeps the wallets of one customer, which must exist.
	CustomerID core.CustomerID
	// Currency keeps the wallets in one currency.
	Currency money.Currency
	// BillingEntityIDs keeps the wallets whose top-up invoices one of the
	// billing entities issues: the wallet's own, or else its customer's.
	BillingEntityIDs []core.BillingEntityID
}

// TransactionListFilter narrows [Service.ListTransactions]; zero fields
// match everything.
type TransactionListFilter struct {
	// TransactionType keeps one direction.
	TransactionType wallet.TransactionType
	// TransactionStatus keeps one kind of movement.
	TransactionStatus wallet.TransactionStatus
	// Status keeps one settlement status.
	Status wallet.SettlementStatus
	// Metadata keeps the transactions carrying every pair; a blank value
	// is ignored.
	Metadata map[string]string
}

// Get returns the wallet id, or core.NotFound("wallet").
func (s *Service) Get(ctx context.Context, org core.OrganizationID, id core.WalletID) (*wallet.Wallet, error) {
	return read(ctx, s, func(ctx context.Context, r port.Repos) (*wallet.Wallet, error) {
		w, err := r.Wallets.Get(ctx, org, id)
		return w, notFound(err, ResourceWallet)
	})
}

// List returns a page of the organization's wallets matching f, oldest
// first. A customer that does not exist is core.NotFound("customer").
//
// source: app/queries/wallets_query.rb:7-53
func (s *Service) List(ctx context.Context, org core.OrganizationID, f ListFilter, page core.PageRequest) (core.Page[*wallet.Wallet], error) {
	return read(ctx, s, func(ctx context.Context, r port.Repos) (core.Page[*wallet.Wallet], error) {
		if !f.CustomerID.IsZero() {
			if _, err := r.Customers.Get(ctx, org, f.CustomerID); err != nil {
				return core.Page[*wallet.Wallet]{}, notFound(err, "customer")
			}
		}
		entities := map[core.CustomerID]core.BillingEntityID{}
		keep := func(w *wallet.Wallet) (bool, error) {
			if !f.Currency.IsZero() && w.Currency != f.Currency {
				return false, nil
			}
			if len(f.BillingEntityIDs) == 0 {
				return true, nil
			}
			be := w.BillingEntityID
			if be.IsZero() {
				var ok bool
				if be, ok = entities[w.CustomerID]; !ok {
					c, err := r.Customers.Get(ctx, org, w.CustomerID)
					if err != nil {
						return false, fmt.Errorf("load customer %s: %w", w.CustomerID, err)
					}
					be, entities[w.CustomerID] = c.BillingEntityID, c.BillingEntityID
				}
			}
			return slices.Contains(f.BillingEntityIDs, be), nil
		}
		return filterPage(page, func(p core.PageRequest) (core.Page[*wallet.Wallet], error) {
			return r.Wallets.List(ctx, org, wallet.Filter{CustomerID: f.CustomerID}, p)
		}, keep, func(w *wallet.Wallet) string { return w.ID.String() })
	})
}

// GetTransaction returns the transaction id, or
// core.NotFound("wallet_transaction").
func (s *Service) GetTransaction(ctx context.Context, org core.OrganizationID, id core.WalletTransactionID) (*wallet.Transaction, error) {
	return read(ctx, s, func(ctx context.Context, r port.Repos) (*wallet.Transaction, error) {
		t, err := r.WalletTransactions.Get(ctx, org, id)
		return t, notFound(err, ResourceTransaction)
	})
}

// ListTransactions returns a page of the transactions of the wallet id
// matching f, oldest first. A wallet that does not exist is
// core.NotFound("wallet").
//
// source: app/queries/wallet_transactions_query.rb:12-61
func (s *Service) ListTransactions(ctx context.Context, org core.OrganizationID, id core.WalletID, f TransactionListFilter, page core.PageRequest) (core.Page[*wallet.Transaction], error) {
	return read(ctx, s, func(ctx context.Context, r port.Repos) (core.Page[*wallet.Transaction], error) {
		if _, err := r.Wallets.Get(ctx, org, id); err != nil {
			return core.Page[*wallet.Transaction]{}, notFound(err, ResourceWallet)
		}
		tf := wallet.TransactionFilter{WalletID: id, TransactionType: f.TransactionType, TransactionStatus: f.TransactionStatus, Status: f.Status}
		keep := func(t *wallet.Transaction) (bool, error) {
			for k, v := range f.Metadata {
				if v == "" {
					continue
				}
				if got, ok := t.Metadata.Get(k); !ok || got != v {
					return false, nil
				}
			}
			return true, nil
		}
		return filterPage(page, func(p core.PageRequest) (core.Page[*wallet.Transaction], error) {
			return r.WalletTransactions.List(ctx, org, tf, p)
		}, keep, func(t *wallet.Transaction) string { return t.ID.String() })
	})
}

// ListConsumptions returns a page of the consumption links of the
// transaction id, most recent first: the outbound transactions that
// consumed an inbound one (lago's consumptions), or the inbound
// transactions an outbound one consumed (lago's fundings). The wallet must
// be traceable (wallet not_traceable).
//
// source: app/queries/wallet_transaction_consumptions_query.rb:7-42
func (s *Service) ListConsumptions(ctx context.Context, org core.OrganizationID, id core.WalletTransactionID, page core.PageRequest) (core.Page[*wallet.Consumption], error) {
	return read(ctx, s, func(ctx context.Context, r port.Repos) (core.Page[*wallet.Consumption], error) {
		t, err := r.WalletTransactions.Get(ctx, org, id)
		if err != nil {
			return core.Page[*wallet.Consumption]{}, notFound(err, ResourceTransaction)
		}
		w, err := r.Wallets.Get(ctx, org, t.WalletID)
		if err != nil {
			return core.Page[*wallet.Consumption]{}, fmt.Errorf("load wallet %s: %w", t.WalletID, err)
		}
		if !w.Traceable {
			return core.Page[*wallet.Consumption]{}, fieldError("wallet", CodeNotTraceable)
		}
		var cs []*wallet.Consumption
		if t.Inbound() {
			cs, err = r.WalletTransactions.ListConsumptionsByInbound(ctx, org, id)
		} else {
			cs, err = r.WalletTransactions.ListConsumptionsByOutbound(ctx, org, id)
		}
		if err != nil {
			return core.Page[*wallet.Consumption]{}, fmt.Errorf("list consumptions: %w", err)
		}
		return recentPage(cs, page)
	})
}

// PaymentURLInvoice checks that a payment URL can be made for the
// transaction id and returns the invoice to pay: the transaction must be a
// pending purchase with an invoice.
//
// source: app/services/wallet_transactions/payments/generate_payment_url_service.rb:13-29
func (s *Service) PaymentURLInvoice(ctx context.Context, org core.OrganizationID, id core.WalletTransactionID) (core.InvoiceID, error) {
	return read(ctx, s, func(ctx context.Context, r port.Repos) (core.InvoiceID, error) {
		t, err := r.WalletTransactions.Get(ctx, org, id)
		switch {
		case err != nil:
			return core.InvoiceID{}, notFound(err, ResourceTransaction)
		case t.TransactionStatus != wallet.TransactionStatusPurchased:
			return core.InvoiceID{}, fieldError("base", CodeTransactionNotPurchased)
		case t.Status == wallet.SettlementStatusSettled:
			return core.InvoiceID{}, fieldError("base", CodeTransactionAlreadySettled)
		case t.InvoiceID.IsZero():
			return core.InvoiceID{}, fieldError("base", CodeTransactionHasNoInvoice)
		}
		return t.InvoiceID, nil
	})
}

// ListRules returns the currently active recurring transaction rules of
// the wallet id, oldest first: active and not expired.
//
// source: app/serializers/v1/wallet_serializer.rb:55-57
// source: app/models/recurring_transaction_rule.rb:63-65
func (s *Service) ListRules(ctx context.Context, org core.OrganizationID, id core.WalletID) ([]*wallet.RecurringTransactionRule, error) {
	now := s.d.Now()
	return read(ctx, s, func(ctx context.Context, r port.Repos) ([]*wallet.RecurringTransactionRule, error) {
		if _, err := r.Wallets.Get(ctx, org, id); err != nil {
			return nil, notFound(err, ResourceWallet)
		}
		rules, err := r.RecurringTransactionRules.ListByWallet(ctx, org, id)
		if err != nil {
			return nil, fmt.Errorf("list recurring transaction rules: %w", err)
		}
		return slices.DeleteFunc(rules, func(rule *wallet.RecurringTransactionRule) bool {
			return rule.Status != wallet.RuleStatusActive || rule.ExpirationAt != nil && !rule.ExpirationAt.After(now)
		}), nil
	})
}

// CustomerID returns the ID of the customer id or, when id is zero, of
// the customer with externalID; a missing one is core.NotFound("customer").
func (s *Service) CustomerID(ctx context.Context, org core.OrganizationID, id core.CustomerID, externalID string) (core.CustomerID, error) {
	return read(ctx, s, func(ctx context.Context, r port.Repos) (core.CustomerID, error) {
		var err error
		if id.IsZero() {
			cust, e := r.Customers.GetByExternalID(ctx, org, externalID)
			if e == nil {
				id = cust.ID
			}
			err = e
		} else {
			_, err = r.Customers.Get(ctx, org, id)
		}
		return id, notFound(err, "customer")
	})
}

// BillingEntityIDs resolves the billing entities of rs (IDs or codes); a
// missing one is core.NotFound("billing_entity") (lago wallet_index and
// Wallets::CreateService billing_entity).
//
// source: app/controllers/concerns/wallet_actions.rb:50-53
func (s *Service) BillingEntityIDs(ctx context.Context, org core.OrganizationID, rs []refs.Ref) ([]core.BillingEntityID, error) {
	return read(ctx, s, func(ctx context.Context, r port.Repos) ([]core.BillingEntityID, error) {
		f := refs.Finder[core.BillingEntityID, *organization.BillingEntity]{
			Resource: "billing_entity",
			Get: func(ctx context.Context, id core.BillingEntityID) (*organization.BillingEntity, error) {
				return r.BillingEntities.Get(ctx, org, id)
			},
			GetByCode: func(ctx context.Context, code string) (*organization.BillingEntity, error) {
				return r.BillingEntities.GetByCode(ctx, org, code)
			},
			ID: func(b *organization.BillingEntity) core.BillingEntityID { return b.ID },
		}
		return f.IDs(ctx, rs)
	})
}

// MetricIDs resolves the billable metrics of rs (IDs or codes) for the
// limitations of a wallet. IDs are kept as they are and a code no metric
// has becomes an ID no metric has, so that the wallet service reports
// either as billable_metrics invalid_identifier, as lago does for codes
// (Wallets::ValidateLimitationsService).
func (s *Service) MetricIDs(ctx context.Context, org core.OrganizationID, rs []refs.Ref) ([]core.BillableMetricID, error) {
	return read(ctx, s, func(ctx context.Context, r port.Repos) ([]core.BillableMetricID, error) {
		if rs == nil {
			return nil, nil
		}
		out := make([]core.BillableMetricID, 0, len(rs))
		for _, ref := range rs {
			if ref.Code == "" {
				id, err := core.ParseID[core.BillableMetricID](ref.ID.String())
				if err != nil {
					return nil, err
				}
				out = append(out, id)
				continue
			}
			m, err := r.Metrics.GetByCode(ctx, org, ref.Code)
			switch {
			case errors.Is(err, core.ErrNotFound):
				out = append(out, core.NewID[core.BillableMetricID](s.d.IDs))
			case err != nil:
				return nil, fmt.Errorf("load billable metric %q: %w", ref.Code, err)
			default:
				out = append(out, m.ID)
			}
		}
		return out, nil
	})
}

// filterPage reads the pages of fetch from req.Token and keeps the items
// keep accepts until the page of req is full. The token of the next page
// is the key of the last item kept, as the store's.
func filterPage[T any](req core.PageRequest, fetch func(core.PageRequest) (core.Page[T], error), keep func(T) (bool, error), key func(T) string) (core.Page[T], error) {
	limit := req.Limit()
	out := core.Page[T]{Items: make([]T, 0, limit)}
	token := req.Token
	for {
		p, err := fetch(core.PageRequest{Size: limit, Token: token})
		if err != nil {
			return core.Page[T]{}, err
		}
		for i, x := range p.Items {
			ok, err := keep(x)
			if err != nil {
				return core.Page[T]{}, err
			}
			if !ok {
				continue
			}
			out.Items = append(out.Items, x)
			if len(out.Items) == limit {
				if i < len(p.Items)-1 || p.NextToken != "" {
					out.NextToken = key(x)
				}
				return out, nil
			}
		}
		if p.NextToken == "" {
			return out, nil
		}
		token = p.NextToken
	}
}

// recentPage cuts cs into the page of req, by creation instant newest
// first, then by ID (lago apply_consistent_ordering); the token is a
// [core.Cursor] over both.
func recentPage(cs []*wallet.Consumption, req core.PageRequest) (core.Page[*wallet.Consumption], error) {
	slices.SortFunc(cs, func(a, b *wallet.Consumption) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), strings.Compare(a.ID.String(), b.ID.String()))
	})
	start := 0
	if req.Token != "" {
		c, err := core.DecodeCursor(req.Token)
		if err != nil {
			return core.Page[*wallet.Consumption]{}, err
		}
		start = len(cs)
		for i, x := range cs {
			if x.ID.UUID() == c.ID {
				start = i + 1
				break
			}
		}
	}
	end := min(start+req.Limit(), len(cs))
	p := core.Page[*wallet.Consumption]{Items: cs[start:end]}
	if end < len(cs) {
		last := cs[end-1]
		p.NextToken = core.Cursor{Key: last.CreatedAt.UTC().Format(time.RFC3339Nano), ID: last.ID.UUID()}.Encode()
	}
	return p, nil
}

func read[T any](ctx context.Context, s *Service, fn func(context.Context, port.Repos) (T, error)) (T, error) {
	var out T
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		v, err := fn(ctx, r)
		out = v
		return err
	})
	return out, err
}

// notFound turns a missing record into core.NotFound(resource).
func notFound(err error, resource string) error {
	if errors.Is(err, core.ErrNotFound) {
		return core.NotFound(resource)
	}
	return err
}
