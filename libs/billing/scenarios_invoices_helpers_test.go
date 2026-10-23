package billing_test

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// invBETax creates a tax of rate applied to the default billing entity
// (lago :applied_to_billing_entity).
func (s *scenario) invBETax(code, rate string) *catalog.Tax {
	s.T.Helper()
	t := s.Tax(code, rate)
	s.UpdateBillingEntity(func(be *organization.BillingEntity) { be.TaxIDs = append(be.TaxIDs, t.ID) })
	return t
}

// invGrace is a customer option overriding the invoice grace period.
func invGrace(days int) func(*customer.Customer) {
	return func(c *customer.Customer) { c.InvoiceGracePeriodDays = &days }
}

// invTZ is a customer option setting its time zone.
func invTZ(name string) func(*customer.Customer) {
	return func(c *customer.Customer) {
		loc, err := time.LoadLocation(name)
		if err != nil {
			panic(err)
		}
		c.Timezone = loc
	}
}

// invAdvance is a plan option billing it in advance.
func invAdvance(p *catalog.Plan) { p.PayInAdvance = true }

// invCreateSub subscribes customer c to plan p now through the lifecycle
// service, with the customer's external ID unless an option sets one
// (lago create_subscription: an existing external ID changes the plan),
// then drains the jobs. It returns the resulting subscription.
func (s *scenario) invCreateSub(c *customer.Customer, p *catalog.Plan, opts ...func(*subscription.Subscription)) *subscription.Subscription {
	s.T.Helper()
	return s.invCreateSubAt(c, p, s.Now(), opts...)
}

// invCreateSubAt is [scenario.invCreateSub] with subscription_at at.
func (s *scenario) invCreateSubAt(c *customer.Customer, p *catalog.Plan, at time.Time, opts ...func(*subscription.Subscription)) *subscription.Subscription {
	s.T.Helper()
	sub := subscription.New(s.OrgID(), core.NewID[core.SubscriptionID](s.IDs), c.ID, p.ID, c.ExternalID, at,
		subscription.PlanFacts{PayInAdvance: p.PayInAdvance}, s.Now())
	for _, o := range opts {
		o(sub)
	}
	out, err := s.E.Lifecycle.Create(s.Ctx, sub)
	s.must(err)
	s.drain()
	return out
}

// invExternalID is a subscription option setting its external ID.
func invExternalID(id string) func(*subscription.Subscription) {
	return func(s *subscription.Subscription) { s.ExternalID = id }
}

// invTerminate terminates sub now, then drains the jobs.
func (s *scenario) invTerminate(sub *subscription.Subscription, opts ...func(*lifecycle.TerminateInput)) {
	s.T.Helper()
	in := lifecycle.TerminateInput{OrganizationID: s.OrgID(), SubscriptionID: sub.ID}
	for _, o := range opts {
		o(&in)
	}
	_, err := s.E.Lifecycle.Terminate(s.Ctx, in)
	s.must(err)
	s.drain()
}

// invSub reloads a subscription.
func (s *scenario) invSub(id core.SubscriptionID) *subscription.Subscription {
	s.T.Helper()
	return billingtest.Read(s.Harness, func(ctx context.Context, r port.Repos) (*subscription.Subscription, error) {
		return r.Subscriptions.Get(ctx, s.OrgID(), id)
	})
}

// invSubs returns the subscriptions of c with the statuses, oldest first.
func (s *scenario) invSubs(c core.CustomerID, st ...subscription.Status) []*subscription.Subscription {
	s.T.Helper()
	if len(st) == 0 {
		st = []subscription.Status{
			subscription.StatusPending, subscription.StatusIncomplete, subscription.StatusActive,
			subscription.StatusTerminated, subscription.StatusCanceled,
		}
	}
	out := billingtest.Read(s.Harness, func(ctx context.Context, r port.Repos) ([]*subscription.Subscription, error) {
		return r.Subscriptions.ListByCustomer(ctx, s.OrgID(), c, st...)
	})
	slices.SortStableFunc(out, func(a, b *subscription.Subscription) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out
}

// invActive returns the active subscription of c, failing when there is
// not exactly one.
func (s *scenario) invActive(c core.CustomerID) *subscription.Subscription {
	s.T.Helper()
	subs := s.invSubs(c, subscription.StatusActive)
	if len(subs) != 1 {
		s.T.Fatalf("active subscriptions = %d, want = 1", len(subs))
	}
	return subs[0]
}

// invSubInvoices returns the invoices billing subscription id, oldest
// first (lago subscription.invoices).
func (s *scenario) invSubInvoices(id core.SubscriptionID) []*invoice.Invoice {
	s.T.Helper()
	return billingtest.Read(s.Harness, func(ctx context.Context, r port.Repos) ([]*invoice.Invoice, error) {
		iss, err := r.InvoiceSubscriptions.ListBySubscription(ctx, s.OrgID(), id)
		if err != nil {
			return nil, err
		}
		var ids []core.InvoiceID
		for _, is := range iss {
			if !slices.Contains(ids, is.InvoiceID) {
				ids = append(ids, is.InvoiceID)
			}
		}
		var out []*invoice.Invoice
		for _, iid := range ids {
			inv, err := r.Invoices.Get(ctx, s.OrgID(), iid)
			if err != nil {
				return nil, err
			}
			out = append(out, inv)
		}
		slices.SortStableFunc(out, func(a, b *invoice.Invoice) int { return a.CreatedAt.Compare(b.CreatedAt) })
		return out, nil
	})
}

// invLast returns the latest invoice of subscription id.
func (s *scenario) invLast(id core.SubscriptionID) *invoice.Invoice {
	s.T.Helper()
	invs := s.invSubInvoices(id)
	if len(invs) == 0 {
		s.T.Fatalf("subscription %s has no invoice", id)
	}
	return invs[len(invs)-1]
}

// invISubs returns the invoice subscriptions of invoice id.
func (s *scenario) invISubs(id core.InvoiceID) []*invoice.Subscription {
	s.T.Helper()
	return billingtest.Read(s.Harness, func(ctx context.Context, r port.Repos) ([]*invoice.Subscription, error) {
		return r.InvoiceSubscriptions.ListByInvoice(ctx, s.OrgID(), id)
	})
}

// invFeesOf returns the fees of invoice id of type t.
func (s *scenario) invFeesOf(id core.InvoiceID, t core.FeeType) []*invoice.Fee {
	s.T.Helper()
	var out []*invoice.Fee
	for _, f := range s.Fees(id) {
		if f.Type == t {
			out = append(out, f)
		}
	}
	return out
}

// invCreditNote creates a credit note through the credit notes service and
// drains the jobs.
func (s *scenario) invCreditNote(in creditnotes.CreateInput) *creditnote.CreditNote {
	s.T.Helper()
	if in.OrganizationID.IsZero() {
		in.OrganizationID = s.OrgID()
	}
	cn, err := s.E.CreditNotes.Create(s.Ctx, in)
	s.must(err)
	s.drain()
	return cn
}

// invRefresh refreshes draft invoice id and drains.
func (s *scenario) invRefresh(id core.InvoiceID) *invoice.Invoice {
	s.T.Helper()
	inv, err := s.E.Invoices.RefreshDraft(s.Ctx, s.OrgID(), id)
	s.must(err)
	s.drain()
	return inv
}

// invFinalize finalizes draft invoice id and drains.
func (s *scenario) invFinalize(id core.InvoiceID) *invoice.Invoice {
	s.T.Helper()
	inv, err := s.E.Invoices.FinalizeDraft(s.Ctx, s.OrgID(), id)
	s.must(err)
	s.drain()
	return inv
}

// invAt returns the UTC instant of an RFC 3339 text.
func invAt(v string) time.Time {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		t, err = time.Parse("2006-01-02T15:04:05", v)
		if err != nil {
			panic(err)
		}
	}
	return t.UTC()
}

// invUsage returns the current usage of subscription id.
func (s *scenario) invUsage(id core.SubscriptionID) *usage.Usage {
	s.T.Helper()
	return billingtest.Read(s.Harness, func(ctx context.Context, r port.Repos) (*usage.Usage, error) {
		return s.E.Usage.CurrentUsage(ctx, r, s.OrgID(), id, usage.Options{})
	})
}

// invCustomerCreditNotes returns the credit notes of every invoice of
// customer c, oldest invoice first.
func (s *scenario) invCustomerCreditNotes(c core.CustomerID) []*creditnote.CreditNote {
	s.T.Helper()
	var out []*creditnote.CreditNote
	for _, inv := range s.Invoices(c) {
		out = append(out, s.CreditNotes(inv.ID)...)
	}
	return out
}

// invAdjustFee adjusts fee f of its draft invoice to units at unitPrice
// (currency units), or to units priced by the charge model when unitPrice
// is empty, and refreshes the draft (lago AdjustedFees::CreateService).
func (s *scenario) invAdjustFee(f *invoice.Fee, units, unitPrice string) *invoice.Invoice {
	s.T.Helper()
	mode, price := invoice.AdjustmentModeUnitsAndUnitPrice, money.Zero
	if unitPrice == "" {
		mode = invoice.AdjustmentModeUnits
	} else {
		price = money.MustParse(unitPrice)
	}
	a := &invoice.AdjustedFee{
		ID: core.NewID[core.AdjustedFeeID](s.IDs), OrganizationID: s.OrgID(), InvoiceID: f.InvoiceID, FeeID: f.ID,
		SubscriptionID: f.SubscriptionID, ChargeID: f.ChargeID, ChargeFilterID: f.ChargeFilterID,
		FixedChargeID: f.FixedChargeID, FeeType: f.Type, GroupedBy: f.GroupedBy,
		Mode: mode, Units: money.MustParse(units), UnitPreciseAmount: price, CreatedAt: s.Now(),
	}
	s.tx(func(ctx context.Context, r port.Repos) error { return r.AdjustedFees.Create(ctx, a) })
	return s.invRefresh(f.InvoiceID)
}

// invEntity creates a non-default billing entity with the prefix, UTC.
func (s *scenario) invEntity(code, prefix string) *organization.BillingEntity {
	s.T.Helper()
	be := organization.NewBillingEntity(s.OrgID(), core.NewID[core.BillingEntityID](s.IDs), code, code, s.Now())
	be.DocumentNumberPrefix = prefix
	s.tx(func(ctx context.Context, r port.Repos) error { return r.BillingEntities.Create(ctx, be) })
	return be
}

// invUpdateEntity changes a stored billing entity.
func (s *scenario) invUpdateEntity(be *organization.BillingEntity, fn func(*organization.BillingEntity)) {
	s.T.Helper()
	fn(be)
	s.tx(func(ctx context.Context, r port.Repos) error { return r.BillingEntities.Update(ctx, be) })
}

// invNumbering is a billing entity option setting its numbering and,
// unless empty, its prefix.
func invNumbering(n organization.DocumentNumbering, prefix string) func(*organization.BillingEntity) {
	return func(be *organization.BillingEntity) {
		be.DocumentNumbering = n
		if prefix != "" {
			be.DocumentNumberPrefix = prefix
		}
	}
}

// invNum is the numbering of an invoice: its sequential IDs (0 when
// unset) and number.
type invNum struct {
	Seq, BESeq int64
	Number     string
}

// invNumOf returns the numbering of inv.
func invNumOf(inv *invoice.Invoice) invNum {
	n := invNum{Number: inv.Number}
	if inv.SequentialID != nil {
		n.Seq = *inv.SequentialID
	}
	if inv.BillingEntitySequentialID != nil {
		n.BESeq = *inv.BillingEntitySequentialID
	}
	return n
}

// invLatestNums returns the numbering of the latest invoice of each
// customer, in order.
func (s *scenario) invLatestNums(cs ...*customer.Customer) []invNum {
	s.T.Helper()
	var out []invNum
	for _, c := range cs {
		invs := s.invCustomerInvoices(c.ID)
		if len(invs) == 0 {
			s.T.Fatalf("customer %s has no invoice", c.ID)
		}
		out = append(out, invNumOf(invs[len(invs)-1]))
	}
	return out
}

// invCustomerInvoices returns the invoices of customer c, oldest first.
func (s *scenario) invCustomerInvoices(c core.CustomerID) []*invoice.Invoice {
	s.T.Helper()
	invs := s.Invoices(c)
	// Invoices of one instant keep their creation order: IDs are time ordered.
	slices.SortFunc(invs, func(a, b *invoice.Invoice) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.ID.String(), b.ID.String()))
	})
	return invs
}

// invAllNumbers returns the sorted numbers of every invoice of the
// customers.
func (s *scenario) invAllNumbers(cs ...*customer.Customer) []string {
	s.T.Helper()
	var out []string
	for _, c := range cs {
		for _, inv := range s.Invoices(c.ID) {
			out = append(out, inv.Number)
		}
	}
	slices.Sort(out)
	return out
}

// invDrafts returns the draft invoices of customer c, oldest first.
func (s *scenario) invDrafts(c core.CustomerID) []*invoice.Invoice {
	s.T.Helper()
	var out []*invoice.Invoice
	for _, inv := range s.invCustomerInvoices(c) {
		if inv.Status == invoice.StatusDraft {
			out = append(out, inv)
		}
	}
	return out
}

// invSubFees returns the fees of subscription id of type t, oldest first.
func (s *scenario) invSubFees(id core.SubscriptionID, t core.FeeType) []*invoice.Fee {
	s.T.Helper()
	out := billingtest.Read(s.Harness, func(ctx context.Context, r port.Repos) ([]*invoice.Fee, error) {
		return kernel.All(func(p core.PageRequest) (core.Page[*invoice.Fee], error) {
			return r.Fees.List(ctx, s.OrgID(), invoice.FeeFilter{SubscriptionID: id, Types: []core.FeeType{t}}, p)
		})
	})
	slices.SortStableFunc(out, func(a, b *invoice.Fee) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out
}

// invUnbilled returns the fees of fs without invoice.
func invUnbilled(fs []*invoice.Fee) []*invoice.Fee {
	var out []*invoice.Fee
	for _, f := range fs {
		if f.InvoiceID.IsZero() {
			out = append(out, f)
		}
	}
	return out
}

// invMarkPaid marks the fees' payments succeeded at (lago
// fee.update!(payment_status: :succeeded)).
func (s *scenario) invMarkPaid(at time.Time, fs ...*invoice.Fee) {
	s.T.Helper()
	s.tx(func(ctx context.Context, r port.Repos) error {
		for _, f := range fs {
			f.PaymentStatus = invoice.FeePaymentStatusSucceeded
			f.SucceededAt = &at
			if err := r.Fees.Update(ctx, f); err != nil {
				return err
			}
		}
		return nil
	})
}

// invOfType returns the invoices of customer c of type t, oldest first.
func (s *scenario) invOfType(c core.CustomerID, t invoice.Type) []*invoice.Invoice {
	s.T.Helper()
	var out []*invoice.Invoice
	for _, inv := range s.invCustomerInvoices(c) {
		if inv.Type == t {
			out = append(out, inv)
		}
	}
	return out
}

// invAppliedTaxes returns the applied taxes of invoice id.
func (s *scenario) invAppliedTaxes(id core.InvoiceID) []*invoice.AppliedTax {
	s.T.Helper()
	return billingtest.Read(s.Harness, func(ctx context.Context, r port.Repos) ([]*invoice.AppliedTax, error) {
		return r.InvoiceAppliedTaxes.ListByInvoice(ctx, s.OrgID(), id)
	})
}

// invReloadFee reloads fee f.
func (s *scenario) invReloadFee(f *invoice.Fee) *invoice.Fee {
	s.T.Helper()
	return billingtest.Read(s.Harness, func(ctx context.Context, r port.Repos) (*invoice.Fee, error) {
		return r.Fees.Get(ctx, s.OrgID(), f.ID)
	})
}
