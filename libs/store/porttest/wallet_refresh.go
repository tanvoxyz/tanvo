package porttest

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// testWalletRefresh checks the wallet refresh flag of customers
// (customer.Repository.FlagWalletRefresh and its companions, and
// organization.WorkAwaitingWalletRefresh).
func testWalletRefresh(t *testing.T, h Harness) {
	t.Run("wallet refresh flag", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org := a.OrganizationID
		newCustomer := func(o core.OrganizationID, entity core.BillingEntityID, ext string) *customer.Customer {
			return customer.New(o, core.NewID[core.CustomerID](e.ids), ext, entity, now)
		}
		withWallet := newCustomer(org, a.ID, "wr-wallet")
		second := newCustomer(org, a.ID, "wr-second")
		noWallet := newCustomer(org, a.ID, "wr-none")
		other := newCustomer(b.OrganizationID, b.ID, "wr-other")
		newWallet := func(c *customer.Customer) *wallet.Wallet {
			return wallet.New(c.OrganizationID, core.NewID[core.WalletID](e.ids), c.ID, money.MustCurrency("EUR"), money.MustParse("1"), now)
		}
		w1, w2, wo := newWallet(withWallet), newWallet(second), newWallet(other)
		awaiting := func(ctx context.Context, r port.Repos, o core.OrganizationID) []core.CustomerID {
			return listAll(t, 1, func(p core.PageRequest) (core.Page[core.CustomerID], error) {
				return r.Customers.ListAwaitingWalletRefresh(ctx, o, p)
			})
		}
		sorted := func(ids ...core.CustomerID) []core.CustomerID {
			slices.SortFunc(ids, func(x, y core.CustomerID) int { return x.Compare(y) })
			return ids
		}
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, c := range []*customer.Customer{withWallet, second, noWallet, other} {
				noErr(t, "Customers.Create()", r.Customers.Create(ctx, c))
			}
			for _, w := range []*wallet.Wallet{w1, w2, wo} {
				noErr(t, "Wallets.Create()", r.Wallets.Create(ctx, w))
			}
			equal(t, "ListAwaitingWalletRefresh() none flagged", len(awaiting(ctx, r, org)), 0)

			for _, c := range []*customer.Customer{withWallet, second, noWallet} {
				noErr(t, "FlagWalletRefresh()", r.Customers.FlagWalletRefresh(ctx, org, c.ID))
			}
			// Flagging through another organization does nothing.
			noErr(t, "FlagWalletRefresh() through another organization", r.Customers.FlagWalletRefresh(ctx, b.OrganizationID, other.ID))
			noErr(t, "FlagWalletRefresh() of a foreign customer", r.Customers.FlagWalletRefresh(ctx, org, other.ID))
			equal(t, "ListAwaitingWalletRefresh()", awaiting(ctx, r, org), sorted(withWallet.ID, second.ID))
			equal(t, "ListAwaitingWalletRefresh() of another organization", awaiting(ctx, r, b.OrganizationID), sorted(other.ID))

			// A flag raised again before a refresh claims it writes nothing.
			noErr(t, "FlagWalletRefresh() while flagged", r.Customers.FlagWalletRefresh(ctx, org, withWallet.ID))
			gen, err := r.Customers.ClaimWalletRefresh(ctx, org, withWallet.ID)
			noErr(t, "ClaimWalletRefresh()", err)
			equal(t, "ClaimWalletRefresh()", gen, int64(1))
			gen, err = r.Customers.ClaimWalletRefresh(ctx, org, noWallet.ID)
			noErr(t, "ClaimWalletRefresh() without a wallet", err)
			equal(t, "ClaimWalletRefresh() without a wallet", gen, int64(0))
			_, err = r.Customers.ClaimWalletRefresh(ctx, org, other.ID)
			wantErr(t, "ClaimWalletRefresh() of a foreign customer", err, core.ErrNotFound)

			// A flag raised after the refresh claimed its generation bumps
			// it, once, and survives that refresh.
			noErr(t, "FlagWalletRefresh() after the claim", r.Customers.FlagWalletRefresh(ctx, org, withWallet.ID))
			noErr(t, "FlagWalletRefresh() after the claim again", r.Customers.FlagWalletRefresh(ctx, org, withWallet.ID))
			noErr(t, "ClearWalletRefresh() stale", r.Customers.ClearWalletRefresh(ctx, org, withWallet.ID, 1))
			equal(t, "ListAwaitingWalletRefresh() after a stale clear", awaiting(ctx, r, org), sorted(withWallet.ID, second.ID))
			gen, err = r.Customers.ClaimWalletRefresh(ctx, org, withWallet.ID)
			noErr(t, "ClaimWalletRefresh() after the new flag", err)
			equal(t, "ClaimWalletRefresh() after the new flag", gen, int64(2))
			noErr(t, "ClearWalletRefresh()", r.Customers.ClearWalletRefresh(ctx, org, withWallet.ID, 2))
			equal(t, "ListAwaitingWalletRefresh() after a clear", awaiting(ctx, r, org), sorted(second.ID))
			// A flag after a clear is written again.
			noErr(t, "FlagWalletRefresh() after a clear", r.Customers.FlagWalletRefresh(ctx, org, withWallet.ID))
			equal(t, "ListAwaitingWalletRefresh() flagged after a clear", awaiting(ctx, r, org), sorted(withWallet.ID, second.ID))
			gen, err = r.Customers.ClaimWalletRefresh(ctx, org, withWallet.ID)
			noErr(t, "ClaimWalletRefresh() after a clear", err)
			equal(t, "ClaimWalletRefresh() after a clear", gen, int64(3))
			noErr(t, "ClearWalletRefresh() after the last flag", r.Customers.ClearWalletRefresh(ctx, org, withWallet.ID, 3))

			// A flagged customer whose wallets are all terminated is not listed.
			noErr(t, "Terminate()", w2.Terminate(at(time.Minute)))
			noErr(t, "Wallets.Update()", r.Wallets.Update(ctx, w2))
			equal(t, "ListAwaitingWalletRefresh() after termination", len(awaiting(ctx, r, org)), 0)
			return nil
		})
		e.tx(func(ctx context.Context, r port.Repos) error {
			got, err := r.Organizations.WithWork(ctx, organization.WorkAwaitingWalletRefresh, now)
			noErr(t, "WithWork(awaiting_wallet_refresh)", err)
			got = slices.DeleteFunc(got, func(o core.OrganizationID) bool { return o != org && o != b.OrganizationID })
			equal(t, "WithWork(awaiting_wallet_refresh)", got, []core.OrganizationID{b.OrganizationID})
			return nil
		})
	})
}
