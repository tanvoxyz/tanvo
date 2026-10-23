package billingtest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/memstore"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// Harness wires the billing dependencies over in-memory stores, with an
// organization and its default billing entity created.
type Harness struct {
	// T is the running test.
	T testing.TB
	// Ctx is the context of the test.
	Ctx context.Context
	// Store is the memstore behind the repositories.
	Store *memstore.Store
	// Clock is the fake clock.
	Clock *Clock
	// IDs generates the identifiers.
	IDs *IDs
	// Events is the event store.
	Events *Events
	// Credits stores the invoice credits.
	Credits *Credits
	// Taxes is the fake tax provider (wired, taxing nobody by default).
	Taxes *TaxProvider
	// Refunds is the fake refund gateway.
	Refunds *RefundGateway
	// Emitted records the domain events.
	Emitted *Emitter
	// Deps are the billing dependencies.
	Deps *kernel.Deps
	// Org is the organization.
	Org *organization.Organization
	// BillingEntity is the organization's default billing entity (UTC,
	// USD, per-customer numbering, prefix "ORG").
	BillingEntity *organization.BillingEntity

	events int
}

// New returns a harness over a memstore whose clock reads now.
func New(t testing.TB, now time.Time) *Harness {
	t.Helper()
	st := memstore.New()
	h := NewOn(t, now, st, st)
	h.Store = st
	return h
}

// NewOn returns a harness over the transactor tx (a PostgreSQL store in
// the store-backed tests) whose domain events go to webhooks and whose
// clock reads now. Its Store is nil.
func NewOn(t testing.TB, now time.Time, tx port.Transactor, webhooks port.WebhookEmitter) *Harness {
	t.Helper()
	credits := &Credits{}
	emitter := &Emitter{Inner: webhooks}
	h := &Harness{
		T: t, Ctx: context.Background(), Clock: NewClock(now), IDs: &IDs{},
		Events: NewEvents(), Credits: credits, Taxes: &TaxProvider{}, Refunds: &RefundGateway{}, Emitted: emitter,
	}
	h.Deps = &kernel.Deps{
		Tx:       &Tx{Inner: tx, Credits: credits, Emitter: emitter},
		Events:   h.Events,
		Clock:    h.Clock,
		IDs:      h.IDs,
		Webhooks: emitter,
		Credits:  credits,
		Taxes:    h.Taxes,
		Refunds:  h.Refunds,
	}
	if err := h.Deps.Check(); err != nil {
		t.Fatalf("deps: %v", err)
	}
	h.Org = organization.New(core.NewID[core.OrganizationID](h.IDs), "Org", now)
	h.BillingEntity = organization.NewBillingEntity(h.Org.ID, core.NewID[core.BillingEntityID](h.IDs), "org", "Org", now)
	h.BillingEntity.IsDefault = true
	h.BillingEntity.DocumentNumberPrefix = "ORG"
	h.Do(func(ctx context.Context, r port.Repos) error {
		if err := r.Organizations.Create(ctx, h.Org); err != nil {
			return err
		}
		return r.BillingEntities.Create(ctx, h.BillingEntity)
	})
	return h
}

// Now returns the clock's instant.
func (h *Harness) Now() time.Time { return h.Clock.Now() }

// OrgID returns the organization's ID.
func (h *Harness) OrgID() core.OrganizationID { return h.Org.ID }

// Do runs fn in a transaction and fails the test on error.
func (h *Harness) Do(fn func(ctx context.Context, r port.Repos) error) {
	h.T.Helper()
	if err := h.Deps.Tx.InTx(h.Ctx, fn); err != nil {
		h.T.Fatalf("transaction: %v", err)
	}
}

// Read runs fn in a transaction and returns its result, failing the test
// on error.
func Read[T any](h *Harness, fn func(ctx context.Context, r port.Repos) (T, error)) T {
	h.T.Helper()
	var out T
	h.Do(func(ctx context.Context, r port.Repos) error {
		v, err := fn(ctx, r)
		out = v
		return err
	})
	return out
}

// UpdateBillingEntity changes the default billing entity.
func (h *Harness) UpdateBillingEntity(fn func(*organization.BillingEntity)) {
	h.T.Helper()
	fn(h.BillingEntity)
	h.Do(func(ctx context.Context, r port.Repos) error { return r.BillingEntities.Update(ctx, h.BillingEntity) })
}

// Invoices returns the visible invoices of a customer, oldest first.
func (h *Harness) Invoices(c core.CustomerID) []*invoice.Invoice {
	h.T.Helper()
	return Read(h, func(ctx context.Context, r port.Repos) ([]*invoice.Invoice, error) {
		return kernel.All(func(p core.PageRequest) (core.Page[*invoice.Invoice], error) {
			return r.Invoices.List(ctx, h.OrgID(), invoice.Filter{CustomerID: c}, p)
		})
	})
}

// Invoice reloads an invoice.
func (h *Harness) Invoice(id core.InvoiceID) *invoice.Invoice {
	h.T.Helper()
	return Read(h, func(ctx context.Context, r port.Repos) (*invoice.Invoice, error) {
		return r.Invoices.Get(ctx, h.OrgID(), id)
	})
}

// Fees returns the fees of an invoice in creation order.
func (h *Harness) Fees(id core.InvoiceID) []*invoice.Fee {
	h.T.Helper()
	return Read(h, func(ctx context.Context, r port.Repos) ([]*invoice.Fee, error) {
		return r.Fees.ListByInvoice(ctx, h.OrgID(), id)
	})
}

// CreditNotes returns the credit notes of an invoice.
func (h *Harness) CreditNotes(id core.InvoiceID) []*creditnote.CreditNote {
	h.T.Helper()
	return Read(h, func(ctx context.Context, r port.Repos) ([]*creditnote.CreditNote, error) {
		return r.CreditNotes.ListByInvoice(ctx, h.OrgID(), id)
	})
}

// ReloadWallet reloads a wallet.
func (h *Harness) ReloadWallet(id core.WalletID) *wallet.Wallet {
	h.T.Helper()
	return Read(h, func(ctx context.Context, r port.Repos) (*wallet.Wallet, error) {
		return r.Wallets.Get(ctx, h.OrgID(), id)
	})
}

// ReloadCustomer reloads a customer.
func (h *Harness) ReloadCustomer(id core.CustomerID) *customer.Customer {
	h.T.Helper()
	return Read(h, func(ctx context.Context, r port.Repos) (*customer.Customer, error) {
		return r.Customers.Get(ctx, h.OrgID(), id)
	})
}

// Webhooks returns the types of the domain events emitted by committed
// transactions, oldest first.
func (h *Harness) Webhooks() []webhook.EventType {
	evs := h.Emitted.Events()
	out := make([]webhook.EventType, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

// JobKinds returns the kinds of the jobs enqueued by committed
// transactions and not yet taken.
func (h *Harness) JobKinds() []string {
	jobs := h.Store.Jobs()
	out := make([]string, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j.Kind)
	}
	return out
}

func (h *Harness) nextTransactionID() string {
	h.events++
	return fmt.Sprintf("tr_%06d", h.events)
}
