package memstore

import (
	"context"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/export"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// WithWork implements organization.Repository.
func (r organizationRepo) WithWork(_ context.Context, w organization.Work, at time.Time) ([]core.OrganizationID, error) {
	if err := organization.CheckWork(w, at); err != nil {
		return nil, err
	}
	seen := map[core.OrganizationID]bool{}
	add := func(org core.OrganizationID) bool {
		seen[org] = true
		return false
	}
	before := func(t *time.Time, limit time.Time) bool { return t != nil && !t.After(limit) }
	st := r.st
	switch w {
	case organization.WorkActiveSubscriptions, organization.WorkPendingSubscriptions, organization.WorkEndingSubscriptions:
		st.subscription.subscriptions.find(func(s *subscription.Subscription) bool {
			switch w {
			case organization.WorkActiveSubscriptions:
				if s.Status == subscription.StatusActive {
					return add(s.OrganizationID)
				}
			case organization.WorkPendingSubscriptions:
				if s.Status == subscription.StatusPending && !s.SubscriptionAt.After(at.Add(organization.ScanDayMargin)) {
					return add(s.OrganizationID)
				}
			default:
				if s.Status == subscription.StatusActive && s.EndingAt != nil &&
					!s.EndingAt.Before(at.Add(-organization.EndingMargin)) && !s.EndingAt.After(at.Add(organization.EndingMargin)) {
					return add(s.OrganizationID)
				}
			}
			return false
		})
	case organization.WorkDraftInvoices, organization.WorkRefreshRequested:
		st.invoice.invoices.find(func(inv *invoice.Invoice) bool {
			if inv.Status == invoice.StatusDraft && (w == organization.WorkDraftInvoices || inv.RefreshRequested) {
				return add(inv.OrganizationID)
			}
			return false
		})
	case organization.WorkLifetimeUsages:
		st.subscription.lifetimeUsages.find(func(l *subscription.LifetimeUsage) bool {
			if l.RecalculateInvoicedUsage {
				return add(l.OrganizationID)
			}
			return false
		})
	case organization.WorkAwaitingWalletRefresh:
		repo := customerRepo{st.customer, st}
		st.customer.customers.find(func(c *customer.Customer) bool {
			if f, ok := st.customer.walletRefresh.get(c.ID); ok && f.awaiting && !c.Deleted() && repo.hasActiveWallet(c.OrganizationID, c.ID) {
				return add(c.OrganizationID)
			}
			return false
		})
	case organization.WorkExpiredWallets:
		st.wallet.wallets.find(func(x *wallet.Wallet) bool {
			if x.Status == wallet.StatusActive && before(x.ExpirationAt, at) {
				return add(x.OrganizationID)
			}
			return false
		})
		st.wallet.rules.find(func(x *wallet.RecurringTransactionRule) bool {
			if x.Status == wallet.RuleStatusActive && before(x.ExpirationAt, at) {
				return add(x.OrganizationID)
			}
			return false
		})
	case organization.WorkIntervalRules:
		st.wallet.rules.find(func(x *wallet.RecurringTransactionRule) bool {
			if x.Status == wallet.RuleStatusActive && x.Trigger == wallet.RecurringTriggerInterval {
				return add(x.OrganizationID)
			}
			return false
		})
	case organization.WorkOverdueInvoices:
		next := core.DateOf(at, time.UTC).AddDays(1)
		st.invoice.invoices.find(func(inv *invoice.Invoice) bool {
			if inv.OverdueCandidate(next) {
				return add(inv.OrganizationID)
			}
			return false
		})
	case organization.WorkEndingAtSubscriptions:
		day := core.DateOf(at, time.UTC).Midnight(time.UTC)
		st.subscription.subscriptions.find(func(s *subscription.Subscription) bool {
			if s.Status == subscription.StatusActive && s.EndingAt != nil && !s.EndingAt.Before(day) {
				return add(s.OrganizationID)
			}
			return false
		})
	case organization.WorkExpirableSubscriptions:
		st.subscription.subscriptions.find(func(s *subscription.Subscription) bool {
			if s.Status == subscription.StatusIncomplete && st.subscription.activationRules.any(func(a *subscription.ActivationRule) bool {
				return a.SubscriptionID == s.ID && a.Status == subscription.ActivationRuleStatusPending && before(a.ExpiresAt, at)
			}) {
				return add(s.OrganizationID)
			}
			return false
		})
	case organization.WorkExpiringOrderForms:
		st.quote.orderForms.find(func(f *quote.OrderForm) bool {
			if f.Status == quote.OrderFormStatusGenerated && before(f.ExpiresAt, at.Add(organization.ScanDayMargin)) {
				return add(f.OrganizationID)
			}
			return false
		})
	case organization.WorkExecutableOrders:
		st.quote.orders.find(func(o *quote.Order) bool {
			if o.Executable(at) {
				return add(o.OrganizationID)
			}
			return false
		})
	case organization.WorkDailyUsages:
		st.subscription.subscriptions.find(func(s *subscription.Subscription) bool {
			if s.Status == subscription.StatusActive && !s.SkipDailyUsage {
				return add(s.OrganizationID)
			}
			return false
		})
	case organization.WorkDunningCustomers:
		st.invoice.invoices.find(func(inv *invoice.Invoice) bool {
			if !inv.PaymentOverdue || inv.SelfBilled {
				return false
			}
			if c, ok := st.customer.customers.get(inv.CustomerID); ok && !c.Deleted() && !c.ExcludeFromDunningCampaign {
				return add(inv.OrganizationID)
			}
			return false
		})
	case organization.WorkEventsToValidate:
		from, to := event.ValidationWindow(at)
		st.event.events.find(func(e *event.Event) bool {
			if !seen[e.OrganizationID] && !e.CreatedAt.Before(from) && e.CreatedAt.Before(to) &&
				st.webhook.endpoints.any(func(x *webhook.Endpoint) bool { return x.OrganizationID == e.OrganizationID }) {
				return add(e.OrganizationID)
			}
			return false
		})
	case organization.WorkStuckGeneratingInvoices:
		failed := map[core.InvoiceID]bool{}
		st.invoice.errorDetails.find(func(e *invoice.ErrorDetail) bool {
			failed[e.InvoiceID] = failed[e.InvoiceID] || e.Code == invoice.ErrorCodeInvoiceGenerationError
			return false
		})
		st.invoice.invoices.find(func(inv *invoice.Invoice) bool {
			if inv.StuckGenerating(at) && !failed[inv.ID] {
				return add(inv.OrganizationID)
			}
			return false
		})
	case organization.WorkAwaitingBillingSegments:
		st.subscription.segments.find(func(b *subscription.BillingSegment) bool {
			if b.Status == subscription.SegmentPending || b.Status == subscription.SegmentProcessing {
				return add(b.OrganizationID)
			}
			return false
		})
	case organization.WorkTaxRetriedInvoices:
		retried := map[core.InvoiceID]bool{}
		st.invoice.errorDetails.find(func(e *invoice.ErrorDetail) bool {
			retried[e.InvoiceID] = retried[e.InvoiceID] || e.TaxRetried()
			return false
		})
		st.invoice.invoices.find(func(inv *invoice.Invoice) bool {
			if inv.Status == invoice.StatusFailed && retried[inv.ID] {
				return add(inv.OrganizationID)
			}
			return false
		})
	case organization.WorkInboundWebhooks:
		since := at.Add(-payment.WebhookRetryWindow)
		st.payment.inboundWebhooks.find(func(x *payment.InboundWebhook) bool {
			if x.Status != payment.InboundWebhookStatusSucceeded && !x.CreatedAt.Before(since) {
				return add(x.OrganizationID)
			}
			return false
		})
	case organization.WorkExpiredInboundWebhooks:
		st.payment.inboundWebhooks.find(func(x *payment.InboundWebhook) bool {
			if x.Expired(at) {
				return add(x.OrganizationID)
			}
			return false
		})
	case organization.WorkAbandonedPayments:
		from, to := payment.AbandonedRange(at)
		st.payment.payments.find(func(p *payment.Payment) bool {
			if p.Kind == payment.KindProvider && p.Payable.Type == payment.PayableTypeInvoice && p.Status == payment.StateProcessing &&
				p.ProviderStatus != nil && *p.ProviderStatus == payment.ProviderStatusRequiresAction &&
				!p.UpdatedAt.Before(from) && !p.UpdatedAt.After(to) {
				return add(p.OrganizationID)
			}
			return false
		})
	case organization.WorkExpiredRecordDeletions:
		cutoff := export.RecordDeletionCutoff(at)
		st.export.deletions.find(func(x *export.RecordDeletion) bool {
			if x.DeletedAt.Before(cutoff) {
				return add(x.OrganizationID)
			}
			return false
		})
	case organization.WorkExpiredCoupons:
		st.coupon.coupons.find(func(c *coupon.Coupon) bool {
			if c.Status == coupon.StatusActive && c.Expiration == coupon.ExpirationTimeLimit && before(c.ExpirationAt, at) {
				return add(c.OrganizationID)
			}
			return false
		})
	}
	out := make([]core.OrganizationID, 0, len(seen))
	for org := range seen {
		out = append(out, org)
	}
	slices.SortFunc(out, core.OrganizationID.Compare)
	return out, nil
}
