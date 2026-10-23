package customers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/dunning"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/tax"
)

// billingEntity returns the active billing entity ref names, or the
// organization's default one for a zero ref (lago
// BillingEntities::ResolveService).
func (s *Service) billingEntity(ctx context.Context, r port.Repos, org core.OrganizationID, ref refs.Ref) (*organization.BillingEntity, error) {
	var (
		be  *organization.BillingEntity
		err error
	)
	switch {
	case ref.IsZero():
		be, err = r.BillingEntities.GetDefault(ctx, org)
	case ref.Code != "":
		be, err = r.BillingEntities.GetByCode(ctx, org, ref.Code)
	default:
		var id core.BillingEntityID
		if id, err = core.ParseID[core.BillingEntityID](ref.ID.String()); err == nil {
			be, err = r.BillingEntities.Get(ctx, org, id)
		}
	}
	if errors.Is(err, core.ErrNotFound) || (err == nil && be.Archived()) {
		return nil, core.NotFound(ResourceBillingEntity)
	}
	if err != nil {
		return nil, fmt.Errorf("find billing entity %s: %w", ref, err)
	}
	return be, nil
}

func dunningCampaigns(r port.Repos, org core.OrganizationID) refs.Finder[core.DunningCampaignID, *dunning.Campaign] {
	return refs.Finder[core.DunningCampaignID, *dunning.Campaign]{
		Resource: "dunning_campaign",
		Get: func(ctx context.Context, id core.DunningCampaignID) (*dunning.Campaign, error) {
			return r.DunningCampaigns.Get(ctx, org, id)
		},
		GetByCode: func(ctx context.Context, code string) (*dunning.Campaign, error) {
			return r.DunningCampaigns.GetByCode(ctx, org, code)
		},
		ID: func(c *dunning.Campaign) core.DunningCampaignID { return c.ID },
	}
}

func sectionsFinder(r port.Repos, org core.OrganizationID) refs.Finder[core.InvoiceCustomSectionID, *invoice.CustomSection] {
	return refs.Finder[core.InvoiceCustomSectionID, *invoice.CustomSection]{
		Resource: "invoice_custom_section",
		Get: func(ctx context.Context, id core.InvoiceCustomSectionID) (*invoice.CustomSection, error) {
			return r.InvoiceCustomSections.Get(ctx, org, id)
		},
		GetByCode: func(ctx context.Context, code string) (*invoice.CustomSection, error) {
			return r.InvoiceCustomSections.GetByCode(ctx, org, code)
		},
		ID: func(c *invoice.CustomSection) core.InvoiceCustomSectionID { return c.ID },
	}
}

// taxCodes returns the codes of c's taxes, in order.
func taxCodes(ctx context.Context, r port.Repos, c *customer.Customer) ([]string, error) {
	out := make([]string, 0, len(c.TaxIDs))
	for _, id := range c.TaxIDs {
		t, err := r.Taxes.Get(ctx, c.OrganizationID, id)
		if err != nil {
			return nil, fmt.Errorf("load tax %s: %w", id, err)
		}
		out = append(out, t.Code)
	}
	return out, nil
}

// taxes applies the taxes of in and the EU VAT tax decided for c, and
// reports whether c's taxes changed and what becomes of c's pending VIES
// check:
//
//   - for a billing entity managing EU VAT, a new customer, one without an
//     EU tax, or one whose country, zipcode or tax identification number
//     changed (or whose billing entity changed) gets the decided EU tax
//     added to the requested taxes, which are otherwise empty;
//   - a billing entity change without requested taxes keeps the non-EU
//     taxes only;
//   - an unknown tax is not found (tax).
//
// A VAT number needing a VIES check gets no EU tax yet: the check is
// scheduled ([JobVIESCheck]); an EU tax decided without one cancels a
// pending check.
//
// source: app/services/customers/eu_auto_taxes_service.rb:19-35
// source: app/services/customers/upsert_from_api_service.rb:110-137
// source: app/services/customers/apply_taxes_service.rb:14-30
func (s *Service) taxes(ctx context.Context, r port.Repos, c, prev *customer.Customer, in Input, created bool) (bool, viesAction, error) {
	var requested []refs.Ref
	set := in.Taxes.Set
	if set {
		requested = slices.Clone(in.Taxes.Value)
	}
	beChanged := !created && c.BillingEntityID != prev.BillingEntityID
	be, err := r.BillingEntities.Get(ctx, c.OrganizationID, c.BillingEntityID)
	if err != nil {
		return false, viesKeep, fmt.Errorf("load billing entity %s: %w", c.BillingEntityID, err)
	}
	codes, err := taxCodes(ctx, r, prev)
	if err != nil {
		return false, viesKeep, err
	}
	vies := viesKeep
	if be.EUTaxManagement {
		hasEU := slices.ContainsFunc(codes, isEUTax)
		changed := beChanged ||
			(in.TaxIdentificationNumber.Set && value(c.TaxIdentificationNumber) != value(prev.TaxIdentificationNumber)) ||
			(in.BillingAddress.Zipcode.Set && c.BillingAddress.Zipcode != prev.BillingAddress.Zipcode) ||
			(in.BillingAddress.Country.Set && c.BillingAddress.Country != prev.BillingAddress.Country)
		if tax.EUTaxesApplicable(created, hasEU, changed) {
			d := tax.ResolveEUTax(tax.EUTaxInput{
				BillingCountry: be.Address.Country,
				Customer: tax.EUCustomer{
					Country:                 c.BillingAddress.Country,
					Zipcode:                 c.BillingAddress.Zipcode,
					TaxIdentificationNumber: value(c.TaxIdentificationNumber),
				},
				At: s.d.Now(),
			})
			if d.TaxCode != "" {
				requested = append(requested, refs.ByCode(d.TaxCode))
				set = true
			}
			vies = viesCancel
			if d.VIESCheckRequired {
				vies = viesSchedule
			}
		}
	}
	if beChanged && !set {
		for _, code := range codes {
			if !isEUTax(code) {
				requested = append(requested, refs.ByCode(code))
			}
		}
		set = true
	}
	if !set {
		return false, vies, nil
	}
	ids, err := refs.Taxes(r, c.OrganizationID).IDs(ctx, requested)
	if err != nil {
		return false, viesKeep, err
	}
	if ids == nil {
		ids = []core.TaxID{}
	}
	c.TaxIDs = ids
	return !created, vies, nil
}

func isEUTax(code string) bool { return strings.HasPrefix(code, tax.EUTaxCodePrefix) }

func value(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// documents returns the facts c's editability depends on.
//
// source: app/models/customer.rb:277-283
func documents(ctx context.Context, r port.Repos, c *customer.Customer) (customer.Documents, error) {
	var d customer.Documents
	org := c.OrganizationID
	subs, err := r.Subscriptions.ListByCustomer(ctx, org, c.ID)
	if err != nil {
		return d, fmt.Errorf("list subscriptions: %w", err)
	}
	d.HasSubscriptions = len(subs) > 0
	invs, err := r.Invoices.List(ctx, org, invoice.Filter{CustomerID: c.ID, IncludeUnlisted: true}, core.PageRequest{Size: 1})
	if err != nil {
		return d, fmt.Errorf("list invoices: %w", err)
	}
	d.HasInvoices = len(invs.Items) > 0
	ws, err := r.Wallets.List(ctx, org, wallet.Filter{CustomerID: c.ID}, core.PageRequest{Size: 1})
	if err != nil {
		return d, fmt.Errorf("list wallets: %w", err)
	}
	d.HasWallets = len(ws.Items) > 0
	for page := (core.PageRequest{Size: core.MaxPageSize}); !d.HasFixedAmountAppliedCoupons; {
		p, err := r.AppliedCoupons.List(ctx, org, coupon.AppliedFilter{CustomerID: c.ID}, page)
		if err != nil {
			return d, fmt.Errorf("list applied coupons: %w", err)
		}
		d.HasFixedAmountAppliedCoupons = slices.ContainsFunc(p.Items, func(a *coupon.AppliedCoupon) bool { return a.Amount != nil })
		if p.NextToken == "" {
			break
		}
		page.Token = p.NextToken
	}
	return d, nil
}

// updateDrafts carries the changes from prev to c over c's draft
// invoices. The issuing date settings before and after are both resolved
// against c's billing entity, as lago does.
//
// source: app/services/customers/update_invoice_payment_due_date_service.rb:14-27
// source: app/services/customers/update_invoice_issuing_date_settings_service.rb:18-35
func (s *Service) updateDrafts(ctx context.Context, r port.Repos, prev, c *customer.Customer, in Input, refresh bool) error {
	be, err := r.BillingEntities.Get(ctx, c.OrganizationID, c.BillingEntityID)
	if err != nil {
		return fmt.Errorf("load billing entity %s: %w", c.BillingEntityID, err)
	}
	defaults := kernel.DefaultsOf(be)
	prevBE := be
	if prev.BillingEntityID != c.BillingEntityID {
		if prevBE, err = r.BillingEntities.Get(ctx, c.OrganizationID, prev.BillingEntityID); err != nil {
			return fmt.Errorf("load billing entity %s: %w", prev.BillingEntityID, err)
		}
	}
	term := c.ApplicableNetPaymentTermDays(defaults)
	termChanged := in.NetPaymentTermDays.Set && !samePtr(c.NetPaymentTermDays, prev.NetPaymentTermDays) &&
		term != prev.ApplicableNetPaymentTermDays(kernel.DefaultsOf(prevBE))
	issuingChanged := c.IssuingDateAnchor != prev.IssuingDateAnchor ||
		c.IssuingDateAdjustment != prev.IssuingDateAdjustment ||
		!samePtr(c.InvoiceGracePeriodDays, prev.InvoiceGracePeriodDays)
	if !termChanged && !issuingChanged && !refresh {
		return nil
	}
	now := s.d.Now()
	today := core.DateOf(now, time.UTC)
	f := invoice.Filter{CustomerID: c.ID, Statuses: []invoice.Status{invoice.StatusDraft}}
	for page := (core.PageRequest{Size: core.MaxPageSize}); ; {
		p, err := r.Invoices.List(ctx, c.OrganizationID, f, page)
		if err != nil {
			return fmt.Errorf("list draft invoices: %w", err)
		}
		for _, inv := range p.Items {
			if termChanged {
				inv.NetPaymentTermDays = term
				inv.PaymentDueDate = invoice.PaymentDueDate(inv.IssuingDate, term)
			}
			if issuingChanged {
				if err := reissue(ctx, r, inv, prev, c, defaults); err != nil {
					return err
				}
			}
			if refresh {
				inv.RefreshRequested = true
			}
			inv.UpdatedAt = now
			if err := r.Invoices.Update(ctx, inv); err != nil {
				return fmt.Errorf("update draft invoice %s: %w", inv.ID, err)
			}
			if issuingChanged && inv.FinalizationDue(today) {
				args := kernel.InvoiceArgs{InvoiceID: inv.ID}
				if err := kernel.Enqueue(ctx, r, c.OrganizationID, kernel.JobFinalizeDraft, args, "invoice:"+inv.ID.String(), now); err != nil {
					return err
				}
			}
		}
		if p.NextToken == "" {
			return nil
		}
		page.Token = p.NextToken
	}
}

// reissue moves the dates of the draft inv from the issuing date settings
// of before to those of after.
//
// source: app/services/customers/update_invoice_issuing_date_settings_service.rb:24-28,62-102
func reissue(ctx context.Context, r port.Repos, inv *invoice.Invoice, before, after *customer.Customer, d customer.Defaults) error {
	subs, err := r.InvoiceSubscriptions.ListByInvoice(ctx, inv.OrganizationID, inv.ID)
	if err != nil {
		return fmt.Errorf("load invoice subscriptions of %s: %w", inv.ID, err)
	}
	recurring := len(subs) > 0 && subs[0].Recurring
	facts := func(c *customer.Customer) invoice.IssuingDateFacts {
		return invoice.IssuingDateFacts{
			Recurring:       recurring,
			Anchor:          c.ApplicableIssuingDateAnchor(d),
			Adjustment:      c.ApplicableIssuingDateAdjustment(d),
			GracePeriodDays: c.ApplicableGracePeriodDays(d),
		}
	}
	oldFacts, newFacts := facts(before), facts(after)
	inv.IssuingDate = inv.IssuingDate.AddDays(invoice.IssuingDateOffset(newFacts) - invoice.IssuingDateOffset(oldFacts))
	inv.ExpectedFinalizationDate = inv.ExpectedFinalizationDate.AddDays(newFacts.GracePeriodDays - oldFacts.GracePeriodDays)
	inv.AppliedGracePeriodDays = newFacts.GracePeriodDays
	inv.PaymentDueDate = invoice.PaymentDueDate(inv.IssuingDate, after.ApplicableNetPaymentTermDays(d))
	return nil
}

func samePtr(a, b *int) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// JobTerminateRelations is the job kind terminating a deleted customer's
// relations (lago Customers::TerminateRelationsJob).
const JobTerminateRelations = "customers.terminate_relations"

// TerminateRelationsArgs are the arguments of [JobTerminateRelations].
type TerminateRelationsArgs struct {
	// CustomerID is the deleted customer.
	CustomerID core.CustomerID `json:"customer_id"`
}

// finalizeDelay is the wait before the drafts of a deleted customer are
// finalized (terminate_relations_service.rb:25).
const finalizeDelay = 5 * time.Minute

// HandleJob runs a job of this service and reports whether it handled
// its kind.
func (s *Service) HandleJob(ctx context.Context, j port.Job) (bool, error) {
	if j.Kind == JobVIESCheck {
		var a VIESCheckArgs
		if err := json.Unmarshal(j.Args, &a); err != nil {
			return true, fmt.Errorf("%s: %w", j.Kind, err)
		}
		return true, s.CheckVIES(ctx, j.OrganizationID, a.CustomerID)
	}
	if j.Kind != JobTerminateRelations {
		return false, nil
	}
	var a TerminateRelationsArgs
	if err := json.Unmarshal(j.Args, &a); err != nil {
		return true, fmt.Errorf("%s: %w", j.Kind, err)
	}
	return true, s.TerminateRelations(ctx, j.OrganizationID, a.CustomerID)
}

// TerminateRelations terminates the relations of the customer id: its
// active subscriptions are terminated, its pending ones canceled, the
// finalization of its drafts queued five minutes ahead, and its active
// applied coupons and wallets terminated.
//
// source: app/services/customers/terminate_relations_service.rb:12-36
func (s *Service) TerminateRelations(ctx context.Context, org core.OrganizationID, id core.CustomerID) error {
	switch {
	case s.rel.Lifecycle == nil:
		return fmt.Errorf("%w: Relations.Lifecycle", kernel.ErrMissingDependency)
	case s.rel.Coupons == nil:
		return fmt.Errorf("%w: Relations.Coupons", kernel.ErrMissingDependency)
	case s.rel.Wallets == nil:
		return fmt.Errorf("%w: Relations.Wallets", kernel.ErrMissingDependency)
	}
	type facts struct {
		subs    []core.SubscriptionID
		coupons []core.AppliedCouponID
		wallets []core.WalletID
	}
	f, err := read(ctx, s.d, func(ctx context.Context, r port.Repos) (facts, error) {
		var f facts
		if _, err := r.Customers.Get(ctx, org, id); err != nil {
			return f, notFound(err)
		}
		// Active first, then pending (terminating an active subscription
		// may already cancel its pending successor).
		for _, st := range []subscription.Status{subscription.StatusActive, subscription.StatusPending} {
			subs, err := r.Subscriptions.ListByCustomer(ctx, org, id, st)
			if err != nil {
				return f, fmt.Errorf("list subscriptions: %w", err)
			}
			for _, sub := range subs {
				f.subs = append(f.subs, sub.ID)
			}
		}
		acs, err := r.AppliedCoupons.ListActiveByCustomer(ctx, org, id)
		if err != nil {
			return f, fmt.Errorf("list applied coupons: %w", err)
		}
		for _, ac := range acs {
			f.coupons = append(f.coupons, ac.ID)
		}
		ws, err := kernel.All(func(page core.PageRequest) (core.Page[*wallet.Wallet], error) {
			return r.Wallets.List(ctx, org, wallet.Filter{CustomerID: id, Status: wallet.StatusActive}, page)
		})
		if err != nil {
			return f, fmt.Errorf("list wallets: %w", err)
		}
		for _, w := range ws {
			f.wallets = append(f.wallets, w.ID)
		}
		return f, nil
	})
	if err != nil {
		return fmt.Errorf("terminate relations of customer %s: %w", id, err)
	}
	for _, sub := range f.subs {
		if _, err := s.rel.Lifecycle.Terminate(ctx, lifecycle.TerminateInput{OrganizationID: org, SubscriptionID: sub}); err != nil && !errors.Is(err, core.ErrInvalidTransition) {
			return fmt.Errorf("terminate subscription %s: %w", sub, err)
		}
	}
	err = s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		now := s.d.Now()
		drafts := invoice.Filter{CustomerID: id, Statuses: []invoice.Status{invoice.StatusDraft}}
		invs, err := kernel.All(func(page core.PageRequest) (core.Page[*invoice.Invoice], error) {
			return r.Invoices.List(ctx, org, drafts, page)
		})
		if err != nil {
			return fmt.Errorf("list draft invoices: %w", err)
		}
		for _, inv := range invs {
			args := kernel.InvoiceArgs{InvoiceID: inv.ID}
			if err := kernel.Enqueue(ctx, r, org, kernel.JobFinalizeDraft, args, "invoice:"+inv.ID.String(), now.Add(finalizeDelay)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("finalize drafts of customer %s: %w", id, err)
	}
	for _, ac := range f.coupons {
		if _, err := s.rel.Coupons.TerminateAppliedCoupon(ctx, org, ac); err != nil && !errors.Is(err, core.ErrInvalidTransition) {
			return fmt.Errorf("terminate applied coupon %s: %w", ac, err)
		}
	}
	for _, w := range f.wallets {
		if _, err := s.rel.Wallets.Terminate(ctx, org, w); err != nil && !errors.Is(err, core.ErrInvalidTransition) {
			return fmt.Errorf("terminate wallet %s: %w", w, err)
		}
	}
	return nil
}
