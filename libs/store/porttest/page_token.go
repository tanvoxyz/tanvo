package porttest

import (
	"context"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/alert"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/dunning"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
)

// pageTokenList lists one page of a repository, discarding the items.
type pageTokenList func(ctx context.Context, r port.Repos, org core.OrganizationID, page core.PageRequest) error

// discard drops the items of a page.
func discard[T any](_ core.Page[T], err error) error { return err }

// testPageToken checks that every list refuses a malformed page token with
// core.ErrInvalidValue, in each order it supports, while the same list
// without a token succeeds.
func testPageToken(t *testing.T, h Harness) {
	e := newEnv(t, h)
	b, _ := e.twoOrgs()
	lists := []struct {
		name string
		list pageTokenList
	}{
		{"BillingEntities", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.BillingEntities.List(ctx, org, organization.BillingEntityFilter{}, p))
		}},
		{"Memberships", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.Memberships.List(ctx, org, access.MembershipFilter{}, p))
		}},
		{"Roles", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.Roles.List(ctx, org, p))
		}},
		{"Invites", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.Invites.List(ctx, org, access.InviteFilter{}, p))
		}},
		{"APIKeys", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.APIKeys.List(ctx, org, access.APIKeyFilter{}, p))
		}},
		{"Customers", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.Customers.List(ctx, org, customer.Filter{}, p))
		}},
		{"Customers recent", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.Customers.List(ctx, org, customer.Filter{Order: core.OrderRecent}, p))
		}},
		{"Metrics", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.Metrics.List(ctx, org, catalog.MetricListFilter{}, p))
		}},
		{"Plans", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.Plans.List(ctx, org, catalog.PlanListFilter{}, p))
		}},
		{"AddOns", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.AddOns.List(ctx, org, catalog.AddOnFilter{}, p))
		}},
		{"AddOns recent", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.AddOns.List(ctx, org, catalog.AddOnFilter{Order: core.OrderRecent}, p))
		}},
		{"PricingUnits", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.PricingUnits.List(ctx, org, catalog.PricingUnitFilter{}, p))
		}},
		{"PricingUnits recent", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.PricingUnits.List(ctx, org, catalog.PricingUnitFilter{Order: core.OrderRecent}, p))
		}},
		{"Taxes", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.Taxes.List(ctx, org, catalog.TaxFilter{}, p))
		}},
		{"Taxes recent", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.Taxes.List(ctx, org, catalog.TaxFilter{Order: core.OrderRecent}, p))
		}},
		{"Features", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.Features.List(ctx, org, catalog.FeatureListFilter{}, p))
		}},
		{"Coupons", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.Coupons.List(ctx, org, coupon.Filter{}, p))
		}},
		{"AppliedCoupons", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.AppliedCoupons.List(ctx, org, coupon.AppliedFilter{}, p))
		}},
		{"DunningCampaigns", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.DunningCampaigns.List(ctx, org, dunning.Filter{}, p))
		}},
		{"Subscriptions", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.Subscriptions.List(ctx, org, subscription.Filter{}, p))
		}},
		{"Subscriptions recent", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.Subscriptions.List(ctx, org, subscription.Filter{Order: core.OrderRecent}, p))
		}},
		{"Alerts", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.Alerts.List(ctx, org, alert.Filter{}, p))
		}},
		{"Invoices", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.Invoices.List(ctx, org, invoice.Filter{}, p))
		}},
		{"Invoices recent", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.Invoices.List(ctx, org, invoice.Filter{Order: core.OrderRecent}, p))
		}},
		{"InvoiceCustomSections", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.InvoiceCustomSections.List(ctx, org, p))
		}},
		{"CreditNotes", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.CreditNotes.List(ctx, org, creditnote.Filter{}, p))
		}},
		{"CreditNotes recent", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.CreditNotes.List(ctx, org, creditnote.Filter{Order: core.OrderRecent}, p))
		}},
		{"Wallets", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.Wallets.List(ctx, org, wallet.Filter{}, p))
		}},
		{"Payments", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.Payments.List(ctx, org, payment.Filter{}, p))
		}},
		{"PaymentReceipts", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.PaymentReceipts.List(ctx, org, payment.ReceiptFilter{}, p))
		}},
		{"PaymentRequests", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.PaymentRequests.List(ctx, org, payment.RequestFilter{}, p))
		}},
		{"Integrations", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.Integrations.List(ctx, org, integration.Filter{}, p))
		}},
		{"WebhookEndpoints", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.WebhookEndpoints.List(ctx, org, p))
		}},
		{"Quotes", func(ctx context.Context, r port.Repos, org core.OrganizationID, p core.PageRequest) error {
			return discard(r.Quotes.List(ctx, org, quote.Filter{}, p))
		}},
	}
	for _, l := range lists {
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, l.name+".List() without a token", l.list(ctx, r, b.OrganizationID, core.PageRequest{Size: 2}))
			return nil
		})
		for _, token := range []string{"not-a-token", "%%%", "AAAA"} {
			err := e.txErr(func(ctx context.Context, r port.Repos) error {
				return l.list(ctx, r, b.OrganizationID, core.PageRequest{Size: 2, Token: token})
			})
			wantErr(t, l.name+".List() with the token "+token, err, core.ErrInvalidValue)
		}
	}
}
