package docgen

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
)

// maxLogoBytes bounds the logo read into a document.
const maxLogoBytes = 8 << 20

// loader loads the data of documents in one transaction, caching the
// catalog objects.
type loader struct {
	s   *Service
	r   port.Repos
	org core.OrganizationID
}

// optional turns core.ErrNotFound into a nil error and reports whether
// the object was found.
func optional(err error) (bool, error) {
	if errors.Is(err, core.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// party loads the billing entity, the customer and the logo.
func (l *loader) party(ctx context.Context, be core.BillingEntityID, cust core.CustomerID) (Party, error) {
	var p Party
	env, err := kernel.LoadCustomerEnv(ctx, l.r, l.org, cust, be)
	if err != nil {
		return p, err
	}
	p.BillingEntity, p.Customer = *env.BillingEntity, *env.Customer
	p.Locale = env.Customer.ApplicableDocumentLocale(env.Defaults)
	if p.Logo, err = l.s.logo(ctx, l.org, p.BillingEntity.Logo); err != nil {
		return p, err
	}
	return p, nil
}

// logo reads the logo ref of org, nil when unset or no blob store is
// configured.
func (s *Service) logo(ctx context.Context, org core.OrganizationID, ref core.BlobRef) (*Image, error) {
	if ref.IsZero() || s.blobs == nil {
		return nil, nil
	}
	rc, err := s.blobs.Get(ctx, org, ref)
	if errors.Is(err, core.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read logo: %w", err)
	}
	defer func() { _ = rc.Close() }()
	ct := ""
	if t, ok := rc.(interface{ ContentType() string }); ok {
		ct = t.ContentType()
	}
	b, err := io.ReadAll(io.LimitReader(rc, maxLogoBytes))
	if err != nil {
		return nil, fmt.Errorf("read logo: %w", err)
	}
	if ct == "" {
		ct = sniffImage(b)
	}
	return &Image{ContentType: ct, Data: b}, nil
}

// sniffImage returns the media type of an image from its content.
func sniffImage(b []byte) string {
	switch {
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(b) >= 3 && string(b[:3]) == "\xff\xd8\xff":
		return "image/jpeg"
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return "image/gif"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	}
	return "image/svg+xml"
}

// newCatalog returns an empty catalog.
func newCatalog() Catalog {
	return Catalog{
		Plans:              map[core.PlanID]catalog.Plan{},
		Subscriptions:      map[core.SubscriptionID]subscription.Subscription{},
		Charges:            map[core.ChargeID]catalog.Charge{},
		Metrics:            map[core.BillableMetricID]catalog.BillableMetric{},
		ChargeFilters:      map[core.ChargeFilterID]catalog.ChargeFilter{},
		FixedCharges:       map[core.FixedChargeID]catalog.FixedCharge{},
		AddOns:             map[core.AddOnID]catalog.AddOn{},
		PricingUnits:       map[core.PricingUnitID]catalog.PricingUnit{},
		WalletTransactions: map[core.WalletTransactionID]wallet.Transaction{},
		Wallets:            map[core.WalletID]wallet.Wallet{},
	}
}

// fill loads into c the objects the fees and links refer to.
func (l *loader) fill(ctx context.Context, c *Catalog, fs []invoice.Fee, links []invoice.Subscription) error {
	subs := map[core.SubscriptionID]bool{}
	for _, ln := range links {
		subs[ln.SubscriptionID] = true
	}
	for i := range fs {
		f := &fs[i]
		if !f.SubscriptionID.IsZero() {
			subs[f.SubscriptionID] = true
		}
		if err := l.feeObjects(ctx, c, f); err != nil {
			return err
		}
	}
	for id := range subs {
		if _, ok := c.Subscriptions[id]; ok {
			continue
		}
		s, err := l.r.Subscriptions.Get(ctx, l.org, id)
		if ok, err := optional(err); err != nil {
			return fmt.Errorf("load subscription %s: %w", id, err)
		} else if !ok {
			continue
		}
		c.Subscriptions[id] = *s
		if _, ok := c.Plans[s.PlanID]; ok {
			continue
		}
		p, err := l.r.Plans.Get(ctx, l.org, s.PlanID)
		if ok, err := optional(err); err != nil {
			return fmt.Errorf("load plan %s: %w", s.PlanID, err)
		} else if ok {
			c.Plans[p.ID] = *p
		}
	}
	return nil
}

// feeObjects loads the catalog objects of the fee f.
func (l *loader) feeObjects(ctx context.Context, c *Catalog, f *invoice.Fee) error {
	if id := f.ChargeID; !id.IsZero() {
		if _, ok := c.Charges[id]; !ok {
			ch, err := l.r.Charges.Get(ctx, l.org, id)
			if ok, err := optional(err); err != nil {
				return fmt.Errorf("load charge %s: %w", id, err)
			} else if ok {
				c.Charges[id] = *ch
				if _, ok := c.Metrics[ch.BillableMetricID]; !ok {
					m, err := l.r.Metrics.GetWithDiscarded(ctx, l.org, ch.BillableMetricID)
					if ok, err := optional(err); err != nil {
						return fmt.Errorf("load metric %s: %w", ch.BillableMetricID, err)
					} else if ok {
						c.Metrics[m.ID] = *m
					}
				}
				if pu := ch.AppliedPricingUnit; pu != nil {
					if _, ok := c.PricingUnits[pu.PricingUnitID]; !ok {
						u, err := l.r.PricingUnits.Get(ctx, l.org, pu.PricingUnitID)
						if ok, err := optional(err); err != nil {
							return fmt.Errorf("load pricing unit %s: %w", pu.PricingUnitID, err)
						} else if ok {
							c.PricingUnits[u.ID] = *u
						}
					}
				}
			}
		}
	}
	if id := f.ChargeFilterID; !id.IsZero() {
		if _, ok := c.ChargeFilters[id]; !ok {
			cf, err := l.r.Charges.GetFilter(ctx, l.org, id)
			if ok, err := optional(err); err != nil {
				return fmt.Errorf("load charge filter %s: %w", id, err)
			} else if ok {
				c.ChargeFilters[id] = *cf
			}
		}
	}
	if id := f.FixedChargeID; !id.IsZero() {
		if _, ok := c.FixedCharges[id]; !ok {
			fc, err := l.r.FixedCharges.Get(ctx, l.org, id)
			if ok, err := optional(err); err != nil {
				return fmt.Errorf("load fixed charge %s: %w", id, err)
			} else if ok {
				c.FixedCharges[id] = *fc
				if err := l.addOn(ctx, c, fc.AddOnID); err != nil {
					return err
				}
			}
		}
	}
	if err := l.addOn(ctx, c, f.AddOnID); err != nil {
		return err
	}
	if id := f.WalletTransactionID; !id.IsZero() {
		if _, ok := c.WalletTransactions[id]; !ok {
			t, err := l.r.WalletTransactions.Get(ctx, l.org, id)
			if ok, err := optional(err); err != nil {
				return fmt.Errorf("load wallet transaction %s: %w", id, err)
			} else if ok {
				c.WalletTransactions[id] = *t
				if _, ok := c.Wallets[t.WalletID]; !ok {
					w, err := l.r.Wallets.Get(ctx, l.org, t.WalletID)
					if ok, err := optional(err); err != nil {
						return fmt.Errorf("load wallet %s: %w", t.WalletID, err)
					} else if ok {
						c.Wallets[w.ID] = *w
					}
				}
			}
		}
	}
	return nil
}

// addOn loads the add-on id into c.
func (l *loader) addOn(ctx context.Context, c *Catalog, id core.AddOnID) error {
	if id.IsZero() {
		return nil
	}
	if _, ok := c.AddOns[id]; ok {
		return nil
	}
	a, err := l.r.AddOns.Get(ctx, l.org, id)
	if ok, err := optional(err); err != nil {
		return fmt.Errorf("load add-on %s: %w", id, err)
	} else if ok {
		c.AddOns[id] = *a
	}
	return nil
}

// links returns the subscription links of the invoice id in invoice
// order (lago sorted_invoice_subscriptions: by the subscription's
// creation, then ID).
func (l *loader) links(ctx context.Context, id core.InvoiceID) ([]invoice.Subscription, error) {
	ls, err := l.r.InvoiceSubscriptions.ListByInvoice(ctx, l.org, id)
	if err != nil {
		return nil, fmt.Errorf("load subscriptions of invoice %s: %w", id, err)
	}
	return values(ls), nil
}

// values dereferences a list.
func values[T any](in []*T) []T {
	out := make([]T, 0, len(in))
	for _, v := range in {
		out = append(out, *v)
	}
	return out
}

// invoiceData loads everything the document of inv prints.
func (l *loader) invoiceData(ctx context.Context, inv *invoice.Invoice) (*Invoice, error) {
	d := &Invoice{Invoice: *inv, Catalog: newCatalog(), Extras: map[core.FeeID]FeeExtra{}}
	var err error
	if d.Party, err = l.party(ctx, inv.BillingEntityID, inv.CustomerID); err != nil {
		return nil, err
	}
	if d.Links, err = l.links(ctx, inv.ID); err != nil {
		return nil, err
	}
	fs, err := l.r.Fees.ListByInvoice(ctx, l.org, inv.ID)
	if err != nil {
		return nil, fmt.Errorf("load fees of invoice %s: %w", inv.ID, err)
	}
	d.Fees = values(fs)
	if err := l.fill(ctx, &d.Catalog, d.Fees, d.Links); err != nil {
		return nil, err
	}
	if err := l.extras(ctx, d); err != nil {
		return nil, err
	}
	taxes, err := l.r.InvoiceAppliedTaxes.ListByInvoice(ctx, l.org, inv.ID)
	if err != nil {
		return nil, fmt.Errorf("load taxes of invoice %s: %w", inv.ID, err)
	}
	d.AppliedTaxes = values(taxes)
	if err := l.credits(ctx, d); err != nil {
		return nil, err
	}
	if err := l.threshold(ctx, d); err != nil {
		return nil, err
	}
	sections, err := l.r.AppliedInvoiceCustomSections.ListByInvoice(ctx, l.org, inv.ID)
	if err != nil {
		return nil, fmt.Errorf("load custom sections of invoice %s: %w", inv.ID, err)
	}
	for _, s := range sections {
		d.CustomSections = append(d.CustomSections, invoice.CustomSection{
			OrganizationID: s.OrganizationID, Code: s.Code, Name: s.Name, DisplayName: s.DisplayName, Details: s.Details, CreatedAt: s.CreatedAt,
		})
	}
	txs, err := l.r.WalletTransactions.ListByInvoice(ctx, l.org, inv.ID)
	if err != nil {
		return nil, fmt.Errorf("load wallet transactions of invoice %s: %w", inv.ID, err)
	}
	d.HasWalletTransactions = len(txs) > 0
	return d, nil
}

// extras resolves the breakdowns, prorations and pay-in-advance periods
// of the fees of d.
func (l *loader) extras(ctx context.Context, d *Invoice) error {
	links := map[core.SubscriptionID]*invoice.Subscription{}
	for i := range d.Links {
		links[d.Links[i].SubscriptionID] = &d.Links[i]
	}
	for i := range d.Fees {
		f := &d.Fees[i]
		c, ok := d.Catalog.Charges[f.ChargeID]
		if f.ChargeID.IsZero() || !ok {
			continue
		}
		var x FeeExtra
		is := links[f.SubscriptionID]
		if m, ok := d.Catalog.Metrics[c.BillableMetricID]; ok && fees.HasRecurringBreakdown(f, &c, &m) {
			items, err := l.s.fees.RecurringBreakdown(ctx, l.r, f, &c, &m)
			if err != nil {
				return err
			}
			x.RecurringBreakdown = items
		}
		days, total, ok, err := kernel.PayInAdvanceProration(ctx, l.r, f, &c, is)
		if err != nil {
			return err
		}
		x.ProrationDays, x.ProrationTotal, x.HasProration = days, total, ok
		p, ok, err := kernel.PayInAdvanceInterval(ctx, l.r, f, &c, is)
		if err != nil {
			return err
		}
		if ok {
			x.PayInAdvanceInterval = &p
		}
		if x.RecurringBreakdown != nil || x.HasProration || x.PayInAdvanceInterval != nil {
			d.Extras[f.ID] = x
		}
	}
	return nil
}

// credits loads the coupon credits and the progressive billing credits
// by subscription (lago credits.coupon_kind ordered by creation and
// Invoice#progressive_billing_credits_for_subscription).
func (l *loader) credits(ctx context.Context, d *Invoice) error {
	cs, err := l.r.Credits.ListByInvoice(ctx, l.org, d.Invoice.ID)
	if err != nil {
		return fmt.Errorf("load credits of invoice %s: %w", d.Invoice.ID, err)
	}
	slices.SortStableFunc(cs, func(a, b *invoice.Credit) int { return a.CreatedAt.Compare(b.CreatedAt) })
	for _, c := range cs {
		switch c.Kind() {
		case invoice.CreditKindCoupon:
			cc, err := l.couponCredit(ctx, c)
			if err != nil {
				return err
			}
			d.Coupons = append(d.Coupons, cc)
		case invoice.CreditKindProgressiveBilling:
			ls, err := l.r.InvoiceSubscriptions.ListByInvoice(ctx, l.org, c.ProgressiveBillingInvoiceID)
			if err != nil {
				return fmt.Errorf("load subscriptions of invoice %s: %w", c.ProgressiveBillingInvoiceID, err)
			}
			for _, ln := range ls {
				if d.ProgressiveBillingCredits == nil {
					d.ProgressiveBillingCredits = map[core.SubscriptionID]int64{}
				}
				d.ProgressiveBillingCredits[ln.SubscriptionID] += c.Amount
			}
		}
	}
	return nil
}

// couponCredit returns the coupon line of the credit c
// (Credit#invoice_coupon_display_name, app/models/credit.rb:62-75).
func (l *loader) couponCredit(ctx context.Context, c *invoice.Credit) (CouponCredit, error) {
	out := CouponCredit{Amount: c.Amount}
	ac, err := l.r.AppliedCoupons.Get(ctx, l.org, c.AppliedCouponID)
	if err != nil {
		return out, fmt.Errorf("load applied coupon %s: %w", c.AppliedCouponID, err)
	}
	cp, err := l.r.Coupons.Get(ctx, l.org, ac.CouponID)
	if err != nil {
		return out, fmt.Errorf("load coupon %s: %w", ac.CouponID, err)
	}
	out.CouponName, out.Currency = cp.Name, ac.Currency
	if cp.Type == coupon.TypePercentage {
		out.PercentageRate = ac.PercentageRate
	} else if ac.Amount != nil {
		out.FixedAmount = *ac.Amount
	}
	return out, nil
}

// threshold loads the last usage threshold a progressive billing invoice
// passed (Invoice#progressive_billing_last_applied_usage_threshold).
func (l *loader) threshold(ctx context.Context, d *Invoice) error {
	if d.Invoice.Type != invoice.TypeProgressiveBilling {
		return nil
	}
	as, err := l.r.AppliedUsageThresholds.ListByInvoice(ctx, l.org, d.Invoice.ID)
	if err != nil {
		return fmt.Errorf("load usage thresholds of invoice %s: %w", d.Invoice.ID, err)
	}
	if len(as) == 0 {
		return nil
	}
	last := slices.MaxFunc(as, func(a, b *subscription.AppliedUsageThreshold) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID.String(), b.ID.String()))
	})
	t, err := l.r.UsageThresholds.Get(ctx, l.org, last.UsageThresholdID)
	if err != nil {
		return fmt.Errorf("load usage threshold %s: %w", last.UsageThresholdID, err)
	}
	d.UsageThreshold = &UsageThreshold{LifetimeUsageAmount: last.LifetimeUsageAmount, PassedThresholdAmount: t.Amount}
	return nil
}

// creditNoteData loads everything the document of cn prints.
func (l *loader) creditNoteData(ctx context.Context, cn *creditnote.CreditNote) (*CreditNote, error) {
	inv, err := l.r.Invoices.Get(ctx, l.org, cn.InvoiceID)
	if err != nil {
		return nil, fmt.Errorf("load invoice %s: %w", cn.InvoiceID, err)
	}
	d := &CreditNote{CreditNote: *cn, Invoice: *inv, Catalog: newCatalog()}
	if d.Party, err = l.party(ctx, inv.BillingEntityID, cn.CustomerID); err != nil {
		return nil, err
	}
	taxes, err := l.r.InvoiceAppliedTaxes.ListByInvoice(ctx, l.org, inv.ID)
	if err != nil {
		return nil, fmt.Errorf("load taxes of invoice %s: %w", inv.ID, err)
	}
	d.InvoiceAppliedTaxes = values(taxes)
	if d.Links, err = l.links(ctx, inv.ID); err != nil {
		return nil, err
	}
	fs, err := l.r.Fees.ListByInvoice(ctx, l.org, inv.ID)
	if err != nil {
		return nil, fmt.Errorf("load fees of invoice %s: %w", inv.ID, err)
	}
	d.Fees = values(fs)
	if err := l.fill(ctx, &d.Catalog, d.Fees, d.Links); err != nil {
		return nil, err
	}
	// Invoice#associated_active_wallet (app/models/invoice.rb:483-487):
	// the wallet of the credit invoice's first credit fee, when active.
	if inv.Type == invoice.TypeCredit {
		for i := range d.Fees {
			t, ok := d.Catalog.WalletTransactions[d.Fees[i].WalletTransactionID]
			if d.Fees[i].Type != core.FeeTypeCredit || !ok {
				continue
			}
			if w, ok := d.Catalog.Wallets[t.WalletID]; ok && w.Status == wallet.StatusActive && w.Name != nil {
				d.WalletName = *w.Name
			}
			break
		}
	}
	return d, nil
}

// receiptData loads everything the document of rc prints.
func (l *loader) receiptData(ctx context.Context, rc *payment.Receipt) (*PaymentReceipt, error) {
	p, err := l.r.Payments.Get(ctx, l.org, rc.PaymentID)
	if err != nil {
		return nil, fmt.Errorf("load payment %s: %w", rc.PaymentID, err)
	}
	d := &PaymentReceipt{Receipt: *rc, Payment: *p}
	if d.Party, err = l.party(ctx, rc.BillingEntityID, p.CustomerID); err != nil {
		return nil, err
	}
	if !p.IntegrationID.IsZero() {
		in, err := l.r.Integrations.Get(ctx, l.org, p.IntegrationID)
		if ok, err := optional(err); err != nil {
			return nil, fmt.Errorf("load integration %s: %w", p.IntegrationID, err)
		} else if ok {
			d.Provider = in
		}
	}
	switch p.Payable.Type {
	case payment.PayableTypePaymentRequest:
		q, err := l.r.PaymentRequests.Get(ctx, l.org, p.Payable.PaymentRequestID)
		if err != nil {
			return nil, fmt.Errorf("load payment request %s: %w", p.Payable.PaymentRequestID, err)
		}
		pr := &PaymentRequest{Party: d.Party, Request: *q}
		for _, id := range q.InvoiceIDs {
			inv, err := l.r.Invoices.Get(ctx, l.org, id)
			if err != nil {
				return nil, fmt.Errorf("load invoice %s: %w", id, err)
			}
			pr.Invoices = append(pr.Invoices, *inv)
		}
		d.Request = pr
	default:
		inv, err := l.r.Invoices.Get(ctx, l.org, p.Payable.InvoiceID)
		if err != nil {
			return nil, fmt.Errorf("load invoice %s: %w", p.Payable.InvoiceID, err)
		}
		if d.Invoice, err = l.invoiceData(ctx, inv); err != nil {
			return nil, err
		}
	}
	return d, nil
}
