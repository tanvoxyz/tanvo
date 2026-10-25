package rpc

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/billing/analytics"
	"github.com/tanvoxyz/tanvo/libs/billing/billingentities"
	"github.com/tanvoxyz/tanvo/libs/billing/customers"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/billing/subscriptions"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	analyticsv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/analytics/v1"
	customerv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/customer/v1"
	invoicev1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/invoice/v1"
	portalv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/portal/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/portal/v1/portalv1connect"
	subscriptionv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/subscription/v1"
	walletv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/wallet/v1"
)

// PortalDeps are the services behind the customer portal.
type PortalDeps struct {
	// Organizations reads the organization's name and logo.
	Organizations *auth.OrganizationSettings
	// BillingEntities reads the default billing entity's settings.
	BillingEntities *billingentities.Service
	// Customers reads and updates the customer.
	Customers *customers.Service
	// Invoices lists and downloads the customer's invoices.
	Invoices *invoicing.Service
	// Subscriptions lists the customer's subscriptions and their usage.
	Subscriptions *subscriptions.Service
	// Wallets lists the customer's wallets and tops them up.
	Wallets *wallets.Service
	// Analytics computes the overdue balance and the invoice collection.
	Analytics *analytics.Service
	// Files links the invoice documents; nil leaves the links empty.
	Files DocumentFiles
}

// Portal serves tanvo.portal.v1.CustomerPortalService (lago's
// customer_portal GraphQL resolvers and mutations): every RPC acts for the
// customer of the portal token, and another customer's object is not
// found.
type Portal struct {
	portalv1connect.UnimplementedCustomerPortalServiceHandler

	d PortalDeps
}

var _ portalv1connect.CustomerPortalServiceHandler = (*Portal)(nil)

// NewPortal returns the service over d.
func NewPortal(d PortalDeps) *Portal { return &Portal{d: d} }

// portalProfileFields are the update_mask paths of UpdatePortalCustomer;
// "billing_address" and "shipping_address" stand for each of their
// fields.
var portalProfileFields = slices.Concat([]string{
	"customer_type", "name", "firstname", "lastname", "legal_name", "tax_identification_number", "email",
	"document_locale", "billing_address", "shipping_address",
}, customerAddressPaths("billing_address"), customerAddressPaths("shipping_address"))

// portalFilterFields are the filter fields of the portal's lists.
var (
	portalInvoiceFilterFields      = []string{"status"}
	portalSubscriptionFilterFields = []string{"status", "plan_code", "currency"}
	portalWalletFilterFields       = []string{"status"}
)

// GetPortalOrganization implements
// CustomerPortalService.GetPortalOrganization (lago
// CustomerPortal::OrganizationResolver). The currency, time zone and
// document settings are the default billing entity's, which hold them in
// Tanvo.
func (s *Portal) GetPortalOrganization(ctx context.Context, _ *portalv1.GetPortalOrganizationRequest) (*portalv1.GetPortalOrganizationResponse, error) {
	p, err := portalPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	o, err := s.d.Organizations.Get(ctx, p.OrganizationID)
	if err != nil {
		return nil, Error(ctx, err)
	}
	logo, err := s.d.Organizations.LogoURL(ctx, o)
	if err != nil {
		return nil, Error(ctx, err)
	}
	b := portalv1.PortalOrganization_builder{
		Id:   new(o.ID.String()),
		Name: new(o.Name),
	}
	if logo != "" {
		b.LogoUrl = new(logo)
	}
	page, err := s.d.BillingEntities.List(ctx, p.OrganizationID, billingentities.Filter{IsDefault: new(true)}, core.PageRequest{Size: 1})
	if err != nil {
		return nil, Error(ctx, err)
	}
	if len(page.Items) > 0 {
		be := page.Items[0]
		b.DefaultCurrency = new(be.DefaultCurrency.Code())
		if be.Timezone != nil {
			b.Timezone = new(be.Timezone.String())
		}
		b.DocumentLocale = new(be.DocumentLocale)
		b.InvoiceFooter = be.InvoiceFooter
		b.InvoiceGracePeriodDays = new(int32(be.InvoiceGracePeriodDays))
	}
	return portalv1.GetPortalOrganizationResponse_builder{Organization: b.Build()}.Build(), nil
}

// GetPortalCustomer implements CustomerPortalService.GetPortalCustomer
// (lago CustomerPortal::CustomerResolver).
func (s *Portal) GetPortalCustomer(ctx context.Context, _ *portalv1.GetPortalCustomerRequest) (*portalv1.GetPortalCustomerResponse, error) {
	p, err := portalPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.d.Customers.Get(ctx, p.OrganizationID, p.CustomerID)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out, err := s.customer(ctx, c)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return portalv1.GetPortalCustomerResponse_builder{Customer: out}.Build(), nil
}

// UpdatePortalCustomer implements CustomerPortalService.UpdatePortalCustomer
// (lago CustomerPortal::CustomerUpdateService): only the fields set, or
// named by the update mask, change; an unset field the mask names is
// cleared.
func (s *Portal) UpdatePortalCustomer(ctx context.Context, req *portalv1.UpdatePortalCustomerRequest) (*portalv1.UpdatePortalCustomerResponse, error) {
	p, err := portalPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	b := portalProfileCustomer(req.GetProfile())
	paths, err := UpdatePaths(req.GetUpdateMask(), portalProfileFields, customerHas(b))
	if err != nil {
		return nil, err
	}
	in, err := customerInput(b, paths)
	if err != nil {
		return nil, err
	}
	c, err := s.d.Customers.UpdateFromPortal(ctx, p.OrganizationID, p.CustomerID, in)
	if err != nil {
		if v, ok := validationViolations("", err); ok {
			return nil, withReason(InvalidArgument("invalid profile", renameFields(v, "profile.", nil)), ReasonValidationErrors)
		}
		return nil, Error(ctx, err)
	}
	out, err := s.customer(ctx, c)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return portalv1.UpdatePortalCustomerResponse_builder{Customer: out}.Build(), nil
}

// ListPortalInvoices implements CustomerPortalService.ListPortalInvoices
// (lago CustomerPortal::InvoicesResolver over InvoicesQuery): the
// customer's visible invoices, latest issued first; the search matches the
// number, or the ID of a UUID.
func (s *Portal) ListPortalInvoices(ctx context.Context, req *portalv1.ListPortalInvoicesRequest) (*portalv1.ListPortalInvoicesResponse, error) {
	p, err := portalPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	eq, err := portalFilter(req.GetFilter(), portalInvoiceFilterFields)
	if err != nil {
		return nil, err
	}
	f := invoice.Filter{CustomerID: p.CustomerID, Order: core.OrderRecent, Search: req.GetSearch()}
	statuses, err := FilterEnum[invoicev1.InvoiceStatus]("status", eq["status"], invoicev1.InvoiceStatus(0).Descriptor())
	if err != nil {
		return nil, err
	}
	for _, v := range statuses {
		st := invStatuses.Core(v)
		if !(invoice.Filter{}).Lists(st) {
			return nil, filterError(fmt.Sprintf("field \"status\": %s invoices are not listed", st))
		}
		f.Statuses = append(f.Statuses, st)
	}
	page, err := s.d.Invoices.ListViews(ctx, p.OrganizationID, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return portalv1.ListPortalInvoicesResponse_builder{
		Invoices:      Items(page, invoiceProto),
		NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// DownloadPortalInvoice implements
// CustomerPortalService.DownloadPortalInvoice (lago
// Mutations::CustomerPortal::DownloadInvoice): the PDF of one of the
// customer's visible invoices, queued for rendering when it has none; a
// draft is is_draft.
func (s *Portal) DownloadPortalInvoice(ctx context.Context, req *portalv1.DownloadPortalInvoiceRequest) (*portalv1.DownloadPortalInvoiceResponse, error) {
	p, err := portalPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.InvoiceID]("invoice_id", req.GetInvoiceId())
	if err != nil {
		return nil, err
	}
	inv, ready, err := s.d.Invoices.DownloadForCustomer(ctx, p.OrganizationID, p.CustomerID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	v, err := s.d.Invoices.View(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := portalv1.DownloadPortalInvoiceResponse_builder{Invoice: invoiceProto(v)}
	if ready {
		if out.Pdf, out.Xml, err = documentPair(ctx, s.d.Files, p.OrganizationID, inv.File, inv.XMLFile); err != nil {
			return nil, Error(ctx, err)
		}
	}
	return out.Build(), nil
}

// ListPortalSubscriptions implements
// CustomerPortalService.ListPortalSubscriptions (lago
// CustomerPortal::SubscriptionsResolver over SubscriptionsQuery): every
// status unless the filter names some, most recent subscription_at first.
func (s *Portal) ListPortalSubscriptions(ctx context.Context, req *portalv1.ListPortalSubscriptionsRequest) (*portalv1.ListPortalSubscriptionsResponse, error) {
	p, err := portalPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	eq, err := portalFilter(req.GetFilter(), portalSubscriptionFilterFields)
	if err != nil {
		return nil, err
	}
	in := subscriptions.ListInput{
		OrganizationID: p.OrganizationID,
		Customer:       subscriptions.CustomerRef{ID: p.CustomerID},
		Page:           Page(req.GetPageSize(), req.GetPageToken()),
	}
	statuses, err := FilterEnum[subscriptionv1.SubscriptionStatus]("status", eq["status"], subscriptionv1.SubscriptionStatus(0).Descriptor())
	if err != nil {
		return nil, err
	}
	for _, st := range statuses {
		in.Statuses = append(in.Statuses, subStatuses.Core(st))
	}
	if len(in.Statuses) == 0 {
		// subscriptions_query.rb:23 filters by status only when asked.
		in.Statuses = subscription.StatusValues()
	}
	code, err := FilterSingle("plan_code", eq["plan_code"])
	if err != nil {
		return nil, err
	}
	if code != "" {
		in.Plan = refs.ByCode(code)
	}
	cur, err := FilterSingle("currency", eq["currency"])
	if err != nil {
		return nil, err
	}
	if cur != "" {
		if in.Currency, err = walletCurrency(FilterField, cur); err != nil {
			return nil, err
		}
	}
	page, err := s.d.Subscriptions.List(ctx, in)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return portalv1.ListPortalSubscriptionsResponse_builder{
		Subscriptions: Items(page, subscriptionProto),
		NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// GetPortalSubscription implements
// CustomerPortalService.GetPortalSubscription (lago
// CustomerPortal::SubscriptionResolver): one of the customer's
// subscriptions, whatever its status.
func (s *Portal) GetPortalSubscription(ctx context.Context, req *portalv1.GetPortalSubscriptionRequest) (*portalv1.GetPortalSubscriptionResponse, error) {
	p, err := portalPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.SubscriptionID]("subscription_id", req.GetSubscriptionId())
	if err != nil {
		return nil, err
	}
	v, err := s.d.Subscriptions.Get(ctx, p.OrganizationID, subscriptions.SubscriptionRef{ID: id})
	if err == nil && v.Subscription.CustomerID != p.CustomerID {
		err = core.NotFound("subscription")
	}
	if err != nil {
		return nil, Error(ctx, err)
	}
	return portalv1.GetPortalSubscriptionResponse_builder{Subscription: subscriptionProto(v)}.Build(), nil
}

// GetPortalCurrentUsage implements
// CustomerPortalService.GetPortalCurrentUsage (lago
// CustomerPortal::Customers::UsageResolver and ProjectedUsageResolver):
// the usage of one of the customer's active subscriptions, without taxes;
// any other subscription is no_active_subscription.
func (s *Portal) GetPortalCurrentUsage(ctx context.Context, req *portalv1.GetPortalCurrentUsageRequest) (*portalv1.GetPortalCurrentUsageResponse, error) {
	p, err := portalPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.SubscriptionID]("subscription_id", req.GetSubscriptionId())
	if err != nil {
		return nil, err
	}
	u, err := s.d.Subscriptions.CurrentUsage(ctx, subscriptions.CurrentUsageInput{
		OrganizationID: p.OrganizationID,
		Customer:       subscriptions.CustomerRef{ID: p.CustomerID},
		Subscription:   subscriptions.SubscriptionRef{ID: id},
		SkipTaxes:      true,
		Projection:     req.GetIncludeProjection(),
	})
	if err != nil {
		return nil, Error(ctx, err)
	}
	return portalv1.GetPortalCurrentUsageResponse_builder{Usage: usageProto(u)}.Build(), nil
}

// ListPortalWallets implements CustomerPortalService.ListPortalWallets
// (lago CustomerPortal::WalletsResolver): the customer's wallets in one
// status, the active ones by default, in application order. The page
// token is the ID of the last wallet of the previous page.
func (s *Portal) ListPortalWallets(ctx context.Context, req *portalv1.ListPortalWalletsRequest) (*portalv1.ListPortalWalletsResponse, error) {
	p, err := portalPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	eq, err := portalFilter(req.GetFilter(), portalWalletFilterFields)
	if err != nil {
		return nil, err
	}
	status := wallet.StatusActive
	raw, err := FilterSingle("status", eq["status"])
	if err != nil {
		return nil, err
	}
	if raw != "" {
		v, err := FilterEnum[walletv1.WalletStatus]("status", []string{raw}, walletv1.WalletStatus(0).Descriptor())
		if err != nil {
			return nil, err
		}
		status = walletStatuses.Core(v[0])
	}
	var cursor *core.Cursor
	if t := req.GetPageToken(); t != "" {
		c, err := core.DecodeCursor(t)
		if err != nil {
			return nil, InvalidArgument("invalid page token", []FieldViolation{{Field: "page_token", Reason: core.CodeInvalid}})
		}
		cursor = &c
	}
	var all []*wallet.Wallet
	for page := (core.PageRequest{Size: core.MaxPageSize}); ; {
		ws, err := s.d.Wallets.List(ctx, p.OrganizationID, wallets.ListFilter{CustomerID: p.CustomerID}, page)
		if err != nil {
			return nil, Error(ctx, err)
		}
		for _, w := range ws.Items {
			if w.Status == status {
				all = append(all, w)
			}
		}
		if ws.NextToken == "" {
			break
		}
		page.Token = ws.NextToken
	}
	slices.SortFunc(all, wallet.CompareForApplication)
	start := 0
	if cursor != nil {
		start = portalWalletOffset(all, *cursor)
	}
	all = all[start:]
	size := Page(req.GetPageSize(), "").Limit()
	next := ""
	if len(all) > size {
		all = all[:size]
		next = core.Cursor{Key: strconv.Itoa(start + size), ID: all[size-1].ID.UUID()}.Encode()
	}
	out := make([]*portalv1.PortalWallet, len(all))
	for i, w := range all {
		out[i] = portalWalletProto(w)
	}
	return portalv1.ListPortalWalletsResponse_builder{Wallets: out, NextPageToken: new(next)}.Build(), nil
}

// GetPortalWallet implements CustomerPortalService.GetPortalWallet (lago
// CustomerPortal::WalletResolver).
func (s *Portal) GetPortalWallet(ctx context.Context, req *portalv1.GetPortalWalletRequest) (*portalv1.GetPortalWalletResponse, error) {
	p, err := portalPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.WalletID]("wallet_id", req.GetWalletId())
	if err != nil {
		return nil, err
	}
	w, err := s.wallet(ctx, p, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return portalv1.GetPortalWalletResponse_builder{Wallet: portalWalletProto(w)}.Build(), nil
}

// TopUpPortalWallet implements CustomerPortalService.TopUpPortalWallet
// (lago Mutations::CustomerPortal::WalletTransactions::Create over
// WalletTransactions::CreateFromParamsService): a pending purchase of the
// paid credits, within the wallet's paid top-up limits. Another
// customer's wallet is wallet_id wallet_not_found.
func (s *Portal) TopUpPortalWallet(ctx context.Context, req *portalv1.TopUpPortalWalletRequest) (*portalv1.TopUpPortalWalletResponse, error) {
	p, err := portalPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.WalletID]("wallet_id", req.GetWalletId())
	if err != nil {
		return nil, err
	}
	// The validator looks the wallet up among the customer's
	// (wallet_transactions/validate_service.rb:35-42).
	if _, err := s.wallet(ctx, p, id); err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return nil, withReason(InvalidArgument("invalid wallet", []FieldViolation{{
				Field: "wallet_id", Reason: wallets.CodeWalletNotFound,
			}}), ReasonValidationErrors)
		}
		return nil, Error(ctx, err)
	}
	in := wallets.TransactionInput{WalletID: id}
	if in.PaidCredits, err = walletDecimal("paid_credits", req.HasPaidCredits(), req.GetPaidCredits()); err != nil {
		return nil, err
	}
	out := []*portalv1.PortalWalletTransaction{}
	if in.PaidCredits == nil {
		return portalv1.TopUpPortalWalletResponse_builder{WalletTransactions: out}.Build(), nil
	}
	ts, err := s.d.Wallets.CreateTransactions(ctx, p.OrganizationID, in)
	if err != nil {
		return nil, walletError(ctx, "", err)
	}
	w, err := s.d.Wallets.Get(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	pw := portalWalletProto(w)
	for _, t := range ts {
		out = append(out, portalWalletTransactionProto(pw, t))
	}
	return portalv1.TopUpPortalWalletResponse_builder{WalletTransactions: out}.Build(), nil
}

// GetPortalOverdueBalance implements
// CustomerPortalService.GetPortalOverdueBalance (lago
// CustomerPortal::Analytics::OverdueBalancesResolver): the customer's
// overdue balance in its currency, every month since the organization's
// creation.
func (s *Portal) GetPortalOverdueBalance(ctx context.Context, req *portalv1.GetPortalOverdueBalanceRequest) (*portalv1.GetPortalOverdueBalanceResponse, error) {
	p, f, err := s.analyticsFilter(ctx)
	if err != nil {
		return nil, err
	}
	f.ExpireCache = req.GetExpireCache()
	rows, err := s.d.Analytics.OverdueBalance(ctx, p.OrganizationID, f)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := make([]*analyticsv1.OverdueBalancePoint, len(rows))
	for i, r := range rows {
		ids := make([]string, len(r.InvoiceIDs))
		for j, id := range r.InvoiceIDs {
			ids[j] = id.String()
		}
		out[i] = analyticsv1.OverdueBalancePoint_builder{
			Month:           monthDate(r.Month),
			Amount:          Money(r.Amount, r.Currency),
			InvoiceIds:      ids,
			BillingEntityId: new(r.BillingEntityID.String()),
		}.Build()
	}
	return portalv1.GetPortalOverdueBalanceResponse_builder{Points: out}.Build(), nil
}

// GetPortalInvoiceCollection implements
// CustomerPortalService.GetPortalInvoiceCollection (lago
// CustomerPortal::Analytics::InvoiceCollectionsResolver): the customer's
// finalized invoices in its currency, every month since the
// organization's creation.
func (s *Portal) GetPortalInvoiceCollection(ctx context.Context, req *portalv1.GetPortalInvoiceCollectionRequest) (*portalv1.GetPortalInvoiceCollectionResponse, error) {
	p, f, err := s.analyticsFilter(ctx)
	if err != nil {
		return nil, err
	}
	f.ExpireCache = req.GetExpireCache()
	rows, err := s.d.Analytics.InvoiceCollection(ctx, p.OrganizationID, f)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := make([]*analyticsv1.InvoiceCollectionPoint, len(rows))
	for i, r := range rows {
		out[i] = analyticsv1.InvoiceCollectionPoint_builder{
			Month:         monthDate(r.Month),
			PaymentStatus: new(payStatuses.Proto(r.PaymentStatus)),
			Amount:        Money(r.Amount, r.Currency),
			InvoicesCount: new(r.InvoicesCount),
		}.Build()
	}
	return portalv1.GetPortalInvoiceCollectionResponse_builder{Points: out}.Build(), nil
}

// analyticsFilter returns the caller and the filter of its reports: the
// customer, in the customer's currency (all currencies without one).
func (s *Portal) analyticsFilter(ctx context.Context) (auth.Principal, analytics.Filter, error) {
	p, err := portalPrincipal(ctx)
	if err != nil {
		return p, analytics.Filter{}, err
	}
	c, err := s.d.Customers.Get(ctx, p.OrganizationID, p.CustomerID)
	if err != nil {
		return p, analytics.Filter{}, Error(ctx, err)
	}
	return p, analytics.Filter{Currency: c.Currency, Customer: analytics.CustomerRef{ID: c.ID}}, nil
}

// wallet returns the wallet id of the caller's customer, or
// core.NotFound("wallet").
func (s *Portal) wallet(ctx context.Context, p auth.Principal, id core.WalletID) (*wallet.Wallet, error) {
	w, err := s.d.Wallets.Get(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, err
	}
	if w.CustomerID != p.CustomerID {
		return nil, core.NotFound(wallets.ResourceWallet)
	}
	return w, nil
}

// customer converts c with its billing entity's document locale.
func (s *Portal) customer(ctx context.Context, c *customer.Customer) (*portalv1.PortalCustomer, error) {
	views, err := s.d.Customers.Views(ctx, c)
	if err != nil {
		return nil, err
	}
	be, err := s.d.BillingEntities.Get(ctx, c.OrganizationID, c.BillingEntityID)
	if err != nil {
		return nil, err
	}
	b := portalv1.PortalCustomer_builder{
		Id:                          new(c.ID.String()),
		DisplayName:                 new(c.DisplayName(true)),
		Name:                        c.Name,
		Firstname:                   c.Firstname,
		Lastname:                    c.Lastname,
		LegalName:                   c.LegalName,
		LegalNumber:                 c.LegalNumber,
		TaxIdentificationNumber:     c.TaxIdentificationNumber,
		Email:                       c.Email,
		BillingAddress:              beAddress(c.BillingAddress),
		ShippingAddress:             beAddress(c.ShippingAddress),
		DocumentLocale:              c.DocumentLocale,
		BillingEntityDocumentLocale: new(be.DocumentLocale),
	}
	if t := customerAccountTypes.Proto(c.AccountType); t != 0 {
		b.AccountType = &t
	}
	if t := customerTypes.Proto(c.CustomerType); t != 0 {
		b.CustomerType = &t
	}
	if !c.Currency.IsZero() {
		b.Currency = new(c.Currency.Code())
	}
	if tz := views[0].ApplicableTimezone; tz != nil {
		b.ApplicableTimezone = new(tz.String())
	}
	return b.Build(), nil
}

// portalWalletOffset returns where the page after c starts in ws: after
// the wallet c names, or at c's offset when that wallet left the list.
func portalWalletOffset(ws []*wallet.Wallet, c core.Cursor) int {
	if i := slices.IndexFunc(ws, func(w *wallet.Wallet) bool { return w.ID.UUID() == c.ID }); i >= 0 {
		return i + 1
	}
	n, err := strconv.Atoi(c.Key)
	if err != nil || n < 0 {
		return 0
	}
	return min(n, len(ws))
}

// portalPrincipal returns the portal principal of ctx.
func portalPrincipal(ctx context.Context) (auth.Principal, error) {
	p, err := principal(ctx)
	if err != nil {
		return p, err
	}
	if p.Kind != auth.PrincipalPortal || p.CustomerID.IsZero() {
		return p, connect.NewError(connect.CodePermissionDenied, "a customer portal token is required")
	}
	return p, nil
}

// portalFilter parses a list filter of equalities over fields.
func portalFilter(s string, fields []string) (map[string][]string, error) {
	e, err := ParseFilter(s)
	if err != nil {
		return nil, err
	}
	return Equalities(e, fields...)
}

// portalProfileCustomer copies the fields of a profile, with their
// presence, onto a customer message for customerInput.
func portalProfileCustomer(pr *portalv1.PortalProfile) *customerv1.Customer {
	b := customerv1.Customer_builder{
		BillingAddress:  pr.GetBillingAddress(),
		ShippingAddress: pr.GetShippingAddress(),
	}
	if pr.HasCustomerType() {
		b.CustomerType = new(pr.GetCustomerType())
	}
	for _, f := range []struct {
		has bool
		v   string
		dst **string
	}{
		{pr.HasName(), pr.GetName(), &b.Name},
		{pr.HasFirstname(), pr.GetFirstname(), &b.Firstname},
		{pr.HasLastname(), pr.GetLastname(), &b.Lastname},
		{pr.HasLegalName(), pr.GetLegalName(), &b.LegalName},
		{pr.HasTaxIdentificationNumber(), pr.GetTaxIdentificationNumber(), &b.TaxIdentificationNumber},
		{pr.HasEmail(), pr.GetEmail(), &b.Email},
		{pr.HasDocumentLocale(), pr.GetDocumentLocale(), &b.DocumentLocale},
	} {
		if f.has {
			*f.dst = new(f.v)
		}
	}
	return b.Build()
}

// portalWalletProto converts w (lago Types::CustomerPortal::Wallets::Object).
func portalWalletProto(w *wallet.Wallet) *portalv1.PortalWallet {
	c := w.Currency
	b := portalv1.PortalWallet_builder{
		Id:                        new(w.ID.String()),
		Code:                      w.Code,
		Name:                      w.Name,
		Currency:                  new(c.Code()),
		RateAmount:                new(Decimal(w.RateAmount)),
		Priority:                  new(int32(w.Priority)),
		ExpiresAt:                 TimestampPtr(w.ExpirationAt),
		BalanceAmount:             Money(w.BalanceAmount, c),
		CreditsBalance:            new(Decimal(w.CreditsBalance)),
		OngoingBalanceAmount:      Money(w.OngoingBalanceAmount, c),
		CreditsOngoingBalance:     new(Decimal(w.CreditsOngoingBalance)),
		OngoingUsageBalanceAmount: Money(w.OngoingUsageBalanceAmount, c),
		ConsumedAmount:            Money(w.ConsumedAmount, c),
		ConsumedCredits:           new(Decimal(w.ConsumedCredits)),
		PaidTopUpMinAmount:        walletMajor(w.PaidTopUpMinAmount, c),
		PaidTopUpMaxAmount:        walletMajor(w.PaidTopUpMaxAmount, c),
		LastBalanceSyncAt:         TimestampPtr(w.LastBalanceSyncAt),
	}
	// wallet.rb:83-93.
	if w.PaidTopUpMinAmount != nil {
		b.PaidTopUpMinCredits = new(Decimal(wallet.CreditsFromMinor(*w.PaidTopUpMinAmount, c, w.RateAmount)))
	}
	if w.PaidTopUpMaxAmount != nil {
		b.PaidTopUpMaxCredits = new(Decimal(wallet.CreditsFromMinor(*w.PaidTopUpMaxAmount, c, w.RateAmount)))
	}
	if st := walletStatuses.Proto(w.Status); st != 0 {
		b.Status = &st
	}
	return b.Build()
}

// portalWalletTransactionProto converts t of the wallet w (lago
// Types::CustomerPortal::WalletTransactions::Object).
func portalWalletTransactionProto(w *portalv1.PortalWallet, t *wallet.Transaction) *portalv1.PortalWalletTransaction {
	b := portalv1.PortalWalletTransaction_builder{
		Id:           new(t.ID.String()),
		Wallet:       w,
		Amount:       Money(t.Amount, t.Currency),
		CreditAmount: new(Decimal(t.CreditAmount)),
		SettledAt:    TimestampPtr(t.SettledAt),
		CreatedAt:    Timestamp(t.CreatedAt),
		UpdatedAt:    Timestamp(t.UpdatedAt),
	}
	if v := walletTransactionTypes.Proto(t.TransactionType); v != 0 {
		b.TransactionType = &v
	}
	if v := walletTransactionStatuses.Proto(t.TransactionStatus); v != 0 {
		b.TransactionStatus = &v
	}
	if v := walletSettlementStatuses.Proto(t.Status); v != 0 {
		b.Status = &v
	}
	return b.Build()
}
