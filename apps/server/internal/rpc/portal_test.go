package rpc_test

import (
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"

	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
	customerv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/customer/v1"
	invoicev1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/invoice/v1"
	portalv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/portal/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/portal/v1/portalv1connect"
	walletv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/wallet/v1"
	"github.com/tanvoxyz/tanvo/libs/tax"
)

// portalFixture is a server with a customer and a portal client of it.
type portalFixture struct {
	s    *rpctest.Server
	h    *billingtest.Harness
	cust *customer.Customer
	c    portalv1connect.CustomerPortalServiceClient
}

func newPortalFixture(t *testing.T, opts ...func(*customer.Customer)) *portalFixture {
	t.Helper()
	s := rpctest.New(t)
	cust := s.Fixtures.Customer(opts...)
	return &portalFixture{
		s: s, h: s.Fixtures, cust: cust,
		c: portalv1connect.NewCustomerPortalServiceClient(s.Connect(s.Portal(t, cust.ID))),
	}
}

func TestPortalOrganization(t *testing.T) {
	t.Parallel()
	t.Log("source: spec/graphql/resolvers/customer_portal/organization_resolver_spec.rb:28-41")
	f := newPortalFixture(t)
	f.h.UpdateBillingEntity(func(be *organization.BillingEntity) {
		be.DefaultCurrency = money.MustCurrency("EUR")
		be.InvoiceFooter = new("Thanks")
		be.InvoiceGracePeriodDays = 3
	})
	res, err := f.c.GetPortalOrganization(t.Context(), portalv1.GetPortalOrganizationRequest_builder{}.Build())
	if err != nil {
		t.Fatalf("GetPortalOrganization() error got = %v, want = nil", err)
	}
	o := res.GetOrganization()
	if o.GetId() != f.s.Org.ID.String() || o.GetName() != f.s.Org.Name || o.GetDocumentLocale() != "en" {
		t.Errorf("got = %v, want = organization %s %q with document locale en", o, f.s.Org.ID, f.s.Org.Name)
	}
	if o.GetDefaultCurrency() != "EUR" || o.GetTimezone() != "UTC" || o.GetInvoiceFooter() != "Thanks" || o.GetInvoiceGracePeriodDays() != 3 {
		t.Errorf("got = %v, want = the default billing entity's EUR, UTC, footer and grace period", o)
	}
}

func TestPortalCustomer(t *testing.T) {
	t.Parallel()
	f := newPortalFixture(t, func(c *customer.Customer) {
		c.Name, c.Currency, c.DocumentLocale = new("Acme"), money.MustCurrency("EUR"), new("fr")
		c.Firstname, c.Lastname = new("Ada"), new("Lovelace")
	})
	t.Run("customer", func(t *testing.T) {
		t.Log("source: spec/graphql/resolvers/customer_portal/customer_resolver_spec.rb:29-41")
		res, err := f.c.GetPortalCustomer(t.Context(), portalv1.GetPortalCustomerRequest_builder{}.Build())
		if err != nil {
			t.Fatalf("GetPortalCustomer() error got = %v, want = nil", err)
		}
		c := res.GetCustomer()
		if c.GetId() != f.cust.ID.String() || c.GetName() != "Acme" || c.GetCurrency() != "EUR" {
			t.Errorf("got = %v, want = customer %s Acme in EUR", c, f.cust.ID)
		}
		if c.GetDocumentLocale() != "fr" || c.GetBillingEntityDocumentLocale() != "en" {
			t.Errorf("got locales = %q, %q, want = fr, en", c.GetDocumentLocale(), c.GetBillingEntityDocumentLocale())
		}
		if c.GetDisplayName() != "Acme - Ada Lovelace" || c.GetApplicableTimezone() != "UTC" ||
			c.GetAccountType() != customerv1.AccountType_ACCOUNT_TYPE_CUSTOMER {
			t.Errorf("got = %v, want = display name Acme - Ada Lovelace, UTC, a customer account", c)
		}
	})
	t.Run("unauthorized", func(t *testing.T) {
		t.Log("source: spec/graphql/resolvers/customer_portal/customer_resolver_spec.rb:43-50")
		cl := portalv1connect.NewCustomerPortalServiceClient(f.s.Connect(rpctest.Credential{}))
		_, err := cl.GetPortalCustomer(t.Context(), portalv1.GetPortalCustomerRequest_builder{}.Build())
		if connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Errorf("got err = %v, want = Unauthenticated", err)
		}
		// A portal RPC verifies the token as a portal token only.
		cl = portalv1connect.NewCustomerPortalServiceClient(f.s.Connect(f.s.APIKey))
		_, err = cl.GetPortalCustomer(t.Context(), portalv1.GetPortalCustomerRequest_builder{}.Build())
		if connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Errorf("API key: got err = %v, want = Unauthenticated", err)
		}
	})
}

func TestUpdatePortalCustomer(t *testing.T) {
	t.Parallel()
	address := func(prefix, country string) *commonv1.Address {
		return commonv1.Address_builder{
			Line1: new(prefix + " line1"), Line2: new(prefix + " line2"), Zipcode: new(prefix + " zipcode"),
			City: new(prefix + " city"), State: new(prefix + " state"), Country: new(country),
		}.Build()
	}
	update := func(f *portalFixture, p *portalv1.PortalProfile, paths ...string) (*portalv1.PortalCustomer, error) {
		req := portalv1.UpdatePortalCustomerRequest_builder{Profile: p}
		if len(paths) > 0 {
			req.UpdateMask = &fieldmaskpb.FieldMask{Paths: paths}
		}
		res, err := f.c.UpdatePortalCustomer(t.Context(), req.Build())
		return res.GetCustomer(), err
	}

	t.Run("every field", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/graphql/mutations/customer_portal/update_customer_spec.rb:83-107")
		t.Log("source: spec/services/customer_portal/customer_update_service_spec.rb:37-65")
		f := newPortalFixture(t)
		c, err := update(f, portalv1.PortalProfile_builder{
			CustomerType: new(customerv1.CustomerType_CUSTOMER_TYPE_COMPANY), Name: new("Updated customer name"),
			Firstname: new("Updated customer firstname"), Lastname: new("Updated customer lastname"),
			LegalName: new("Updated customer legalName"), TaxIdentificationNumber: new("2246"),
			Email: new("customer@email.test"), DocumentLocale: new("fr"),
			BillingAddress: address("Updated customer", "pt"), ShippingAddress: address("Updated customer shipping", "es"),
		}.Build())
		if err != nil {
			t.Fatalf("UpdatePortalCustomer() error got = %v, want = nil", err)
		}
		if c.GetCustomerType() != customerv1.CustomerType_CUSTOMER_TYPE_COMPANY || c.GetName() != "Updated customer name" ||
			c.GetFirstname() != "Updated customer firstname" || c.GetLastname() != "Updated customer lastname" ||
			c.GetLegalName() != "Updated customer legalName" || c.GetTaxIdentificationNumber() != "2246" ||
			c.GetEmail() != "customer@email.test" || c.GetDocumentLocale() != "fr" || c.GetBillingEntityDocumentLocale() != "en" {
			t.Errorf("got = %v, want = every profile field updated", c)
		}
		if b := c.GetBillingAddress(); b.GetLine1() != "Updated customer line1" || b.GetLine2() != "Updated customer line2" ||
			b.GetZipcode() != "Updated customer zipcode" || b.GetCity() != "Updated customer city" ||
			b.GetState() != "Updated customer state" || b.GetCountry() != "PT" {
			t.Errorf("got billing address = %v, want = the updated one", b)
		}
		// customer_update_service.rb:44: the country is upper-cased.
		if a := c.GetShippingAddress(); a.GetLine1() != "Updated customer shipping line1" || a.GetCountry() != "ES" {
			t.Errorf("got shipping address = %v, want = the updated one in ES", a)
		}
		// customer_update_service.rb sends no webhook.
		if hooks := f.s.Webhooks(t); slices.Contains(hooks, "customer.updated") {
			t.Errorf("got webhooks = %v, want = no customer.updated", hooks)
		}
	})

	t.Run("some fields", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/graphql/mutations/customer_portal/update_customer_spec.rb:109-120")
		t.Log("source: spec/services/customer_portal/customer_update_service_spec.rb:80-97")
		f := newPortalFixture(t, func(c *customer.Customer) {
			c.Firstname, c.Email = new("Old"), new("old@email.test")
			c.ShippingAddress = core.Address{Line2: "Old line2"}
		})
		c, err := update(f, portalv1.PortalProfile_builder{
			Name:            new("Updated customer name"),
			ShippingAddress: commonv1.Address_builder{Line1: new("Updated customer shipping address line1")}.Build(),
		}.Build())
		if err != nil {
			t.Fatalf("UpdatePortalCustomer() error got = %v, want = nil", err)
		}
		if c.GetName() != "Updated customer name" || c.GetFirstname() != "Old" || c.GetEmail() != "old@email.test" {
			t.Errorf("got = %v, want = the name only changed", c)
		}
		if a := c.GetShippingAddress(); a.GetLine1() != "Updated customer shipping address line1" || a.GetLine2() != "Old line2" {
			t.Errorf("got shipping address = %v, want = line1 changed and line2 kept", a)
		}

		// A field the mask names but the profile leaves unset is cleared.
		c, err = update(f, portalv1.PortalProfile_builder{}.Build(), "firstname")
		if err != nil || c.HasFirstname() || c.GetName() != "Updated customer name" {
			t.Errorf("got = %v, %v, want = the first name cleared", c, err)
		}
		_, err = update(f, portalv1.PortalProfile_builder{}.Build(), "currency")
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("update_mask currency: got err = %v, want = InvalidArgument", err)
		}
	})

	t.Run("email sanitized", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/customer_portal/customer_update_service_spec.rb:99-109")
		f := newPortalFixture(t)
		c, err := update(f, portalv1.PortalProfile_builder{Email: new("hello@something–other.com")}.Build())
		if err != nil || c.GetEmail() != "hello@something-other.com" {
			t.Errorf("got = %v, %v, want = hello@something-other.com", c.GetEmail(), err)
		}
	})

	t.Run("eu taxes", func(t *testing.T) {
		t.Parallel()
		f := newPortalFixture(t)
		other := f.h.Customer()
		fr := f.h.Tax(tax.EUStandardTaxCode("FR"), "20")
		t.Log("source: spec/services/customer_portal/customer_update_service_spec.rb:131-139")
		if _, err := update(f, portalv1.PortalProfile_builder{Name: new("Without EU tax management")}.Build()); err != nil {
			t.Fatalf("UpdatePortalCustomer() error got = %v, want = nil", err)
		}
		if got := f.h.ReloadCustomer(f.cust.ID).TaxIDs; len(got) != 0 {
			t.Errorf("without EU tax management: got taxes = %v, want = none", got)
		}
		t.Log("source: spec/services/customer_portal/customer_update_service_spec.rb:111-129")
		t.Log("source: app/services/customers/eu_auto_taxes_service.rb:74-81")
		f.h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.EUTaxManagement = true; be.Address.Country = "FR" })
		if _, err := update(f, portalv1.PortalProfile_builder{
			BillingAddress: commonv1.Address_builder{Country: new("FR")}.Build(),
		}.Build()); err != nil {
			t.Fatalf("UpdatePortalCustomer() error got = %v, want = nil", err)
		}
		if got := f.h.ReloadCustomer(f.cust.ID).TaxIDs; !slices.Equal(got, []core.TaxID{fr.ID}) {
			t.Errorf("got taxes = %v, want = [%s]", got, fr.ID)
		}
		if got := f.h.ReloadCustomer(other.ID).TaxIDs; len(got) != 0 {
			t.Errorf("another customer: got taxes = %v, want = none", got)
		}
	})

	t.Run("validation error", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/customer_portal/customer_update_service_spec.rb:166-174")
		f := newPortalFixture(t)
		for _, tt := range []struct{ country, reason string }{
			{"invalid country code", "string.pattern"},
			{"ZZ", "country_code_invalid"},
		} {
			_, err := update(f, portalv1.PortalProfile_builder{
				BillingAddress: commonv1.Address_builder{Country: new(tt.country)}.Build(),
			}.Build())
			portalWantViolation(t, err, "profile.billing_address.country", tt.reason)
		}
	})
}

func portalWantViolation(t *testing.T, err error, field, reason string) {
	t.Helper()
	for _, v := range rpc.ErrorViolations(err) {
		if v.Field == field && v.Reason == reason {
			return
		}
	}
	t.Fatalf("got err = %v (violations %+v), want = %s %s", err, rpc.ErrorViolations(err), field, reason)
}

func TestPortalInvoices(t *testing.T) {
	t.Parallel()
	f := newPortalFixture(t)
	draft := f.h.StoredInvoice(f.cust, func(i *invoice.Invoice) { i.Status = invoice.StatusDraft; i.Number = "INV-DRAFT" })
	final := f.h.StoredInvoice(f.cust, func(i *invoice.Invoice) {
		i.Status = invoice.StatusFinalized
		i.Number = "INV-FINAL"
		i.IssuingDate = i.IssuingDate.AddDays(-1)
	})
	f.h.StoredInvoice(f.cust) // generating: not visible
	other := f.h.StoredInvoice(f.h.Customer(), func(i *invoice.Invoice) { i.Status = invoice.StatusFinalized })
	list := func(filter, search string) []string {
		t.Helper()
		res, err := f.c.ListPortalInvoices(t.Context(), portalv1.ListPortalInvoicesRequest_builder{
			Filter: new(filter), Search: new(search),
		}.Build())
		if err != nil {
			t.Fatalf("ListPortalInvoices(%q, %q) error got = %v, want = nil", filter, search, err)
		}
		var ids []string
		for _, inv := range res.GetInvoices() {
			ids = append(ids, inv.GetId())
		}
		return ids
	}
	for _, tt := range []struct {
		source, filter, search string
		want                   []string
	}{
		{"spec/graphql/resolvers/customer_portal/invoices_resolver_spec.rb:30-42", "", "", []string{draft.ID.String(), final.ID.String()}},
		{"spec/graphql/resolvers/customer_portal/invoices_resolver_spec.rb:44-69", "status = draft", "", []string{draft.ID.String()}},
		{"app/queries/invoices_query.rb:79-86", "", "final", []string{final.ID.String()}},
		{"app/queries/invoices_query.rb:79-86", "", other.ID.String(), nil},
	} {
		t.Log("source: " + tt.source)
		if got := list(tt.filter, tt.search); !slices.Equal(got, tt.want) {
			t.Errorf("ListPortalInvoices(%q, %q): got = %v, want = %v", tt.filter, tt.search, got, tt.want)
		}
	}
	_, err := f.c.ListPortalInvoices(t.Context(), portalv1.ListPortalInvoicesRequest_builder{Filter: new("currency = USD")}.Build())
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("filter currency: got err = %v, want = InvalidArgument", err)
	}

	t.Run("download", func(t *testing.T) {
		t.Log("source: spec/graphql/mutations/customer_portal/download_invoice_spec.rb:25-39")
		res, err := f.c.DownloadPortalInvoice(t.Context(), portalv1.DownloadPortalInvoiceRequest_builder{InvoiceId: new(final.ID.String())}.Build())
		if err != nil {
			t.Fatalf("DownloadPortalInvoice() error got = %v, want = nil", err)
		}
		if res.GetInvoice().GetId() != final.ID.String() || res.GetInvoice().GetStatus() != invoicev1.InvoiceStatus_INVOICE_STATUS_FINALIZED || res.HasPdf() {
			t.Errorf("got = %v, want = the finalized invoice, its PDF queued", res)
		}
		var jobs int
		if err := f.s.Admin.QueryRow(t.Context(), `SELECT count(*) FROM job_outbox WHERE kind = 'documents.generate_invoice'`).Scan(&jobs); err != nil || jobs != 1 {
			t.Errorf("got invoice document jobs = %d (%v), want = 1", jobs, err)
		}
		for _, tt := range []struct {
			source string
			id     core.InvoiceID
			code   connect.Code
			reason string
		}{
			{"app/services/invoices/generate_pdf_service.rb:16", draft.ID, connect.CodeFailedPrecondition, "is_draft"},
			{"app/graphql/mutations/customer_portal/download_invoice.rb:17", other.ID, connect.CodeNotFound, "invoice_not_found"},
		} {
			t.Log("source: " + tt.source)
			_, err := f.c.DownloadPortalInvoice(t.Context(), portalv1.DownloadPortalInvoiceRequest_builder{InvoiceId: new(tt.id.String())}.Build())
			subWantCode(t, err, tt.code, tt.reason)
		}
	})
}

func TestPortalSubscriptions(t *testing.T) {
	t.Parallel()
	f := newPortalFixture(t)
	plan := f.h.Plan(period.Monthly, 100, func(p *catalog.Plan) { p.Code = "basic" })
	brl := f.h.Plan(period.Monthly, 100, func(p *catalog.Plan) { p.Code = "brl"; p.Currency = money.MustCurrency("BRL") })
	active := f.h.Subscription(f.cust, plan, billingtest.SubscribedAt(rpctest.Now.AddDate(0, -1, 0)))
	terminated := f.h.Subscription(f.cust, plan, billingtest.SubscribedAt(rpctest.Now.AddDate(0, -3, 0)))
	f.h.UpdateSubscription(terminated, func(s *subscription.Subscription) {
		s.Status, s.TerminatedAt = subscription.StatusTerminated, new(rpctest.Now.AddDate(0, -2, 0))
	})
	inBRL := f.h.Subscription(f.cust, brl, billingtest.SubscribedAt(rpctest.Now.AddDate(0, -2, 0)))
	other := f.h.Subscription(f.h.Customer(), plan)

	list := func(filter string) []string {
		t.Helper()
		res, err := f.c.ListPortalSubscriptions(t.Context(), portalv1.ListPortalSubscriptionsRequest_builder{Filter: new(filter)}.Build())
		if err != nil {
			t.Fatalf("ListPortalSubscriptions(%q) error got = %v, want = nil", filter, err)
		}
		var ids []string
		for _, s := range res.GetSubscriptions() {
			ids = append(ids, s.GetId())
		}
		return ids
	}
	for _, tt := range []struct {
		source, filter string
		want           []string
	}{
		{"app/queries/subscriptions_query.rb:23-29", "", []string{active.ID.String(), inBRL.ID.String(), terminated.ID.String()}},
		{"spec/graphql/resolvers/customer_portal/subscriptions_resolver_spec.rb:31-39", "plan_code = basic AND status = active", []string{active.ID.String()}},
		{"spec/graphql/resolvers/customer_portal/subscriptions_resolver_spec.rb:41-65", "status = terminated", []string{terminated.ID.String()}},
		{"spec/graphql/resolvers/customer_portal/subscriptions_resolver_spec.rb:67-90", "currency = BRL", []string{inBRL.ID.String()}},
	} {
		t.Log("source: " + tt.source)
		if got := list(tt.filter); !slices.Equal(got, tt.want) {
			t.Errorf("ListPortalSubscriptions(%q): got = %v, want = %v", tt.filter, got, tt.want)
		}
	}

	t.Run("get", func(t *testing.T) {
		t.Log("source: spec/graphql/resolvers/customer_portal/subscription_resolver_spec.rb:34-53")
		res, err := f.c.GetPortalSubscription(t.Context(), portalv1.GetPortalSubscriptionRequest_builder{SubscriptionId: new(terminated.ID.String())}.Build())
		if err != nil || res.GetSubscription().GetId() != terminated.ID.String() {
			t.Errorf("got = %v, %v, want = subscription %s", res.GetSubscription(), err, terminated.ID)
		}
		t.Log("source: spec/graphql/resolvers/customer_portal/subscription_resolver_spec.rb:127-137")
		for _, id := range []core.SubscriptionID{other.ID, core.NewID[core.SubscriptionID](core.UUIDv7Generator{})} {
			_, err := f.c.GetPortalSubscription(t.Context(), portalv1.GetPortalSubscriptionRequest_builder{SubscriptionId: new(id.String())}.Build())
			subWantCode(t, err, connect.CodeNotFound, "subscription_not_found")
		}
	})
}

func TestPortalCurrentUsage(t *testing.T) {
	t.Parallel()
	f := newPortalFixture(t)
	p := f.h.Plan(period.Monthly, 0)
	m := f.h.Metric("calls", catalog.AggregationTypeCount, "")
	f.h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
	sub := f.h.Subscription(f.cust, p, billingtest.SubscribedAt(rpctest.Now.AddDate(0, 0, -5)))
	f.h.Events.Add(f.h.Event(sub, "calls", rpctest.Now.Add(-time.Hour)))
	f.h.Events.Add(f.h.Event(sub, "calls", rpctest.Now.Add(-time.Hour)))
	usage := func(id core.SubscriptionID, projection bool) (*portalv1.GetPortalCurrentUsageResponse, error) {
		return f.c.GetPortalCurrentUsage(t.Context(), portalv1.GetPortalCurrentUsageRequest_builder{
			SubscriptionId: new(id.String()), IncludeProjection: new(projection),
		}.Build())
	}

	t.Log("source: spec/graphql/resolvers/customer_portal/customers/usage_resolver_spec.rb:130-168")
	res, err := usage(sub.ID, false)
	if err != nil {
		t.Fatalf("GetPortalCurrentUsage() error got = %v, want = nil", err)
	}
	u := res.GetUsage()
	if u.GetAmount().GetAmount() != "2.00" || len(u.GetCharges()) != 1 || u.GetCharges()[0].GetUnits() != "2" || u.GetTaxesAmount().GetAmount() != "0.00" {
		t.Errorf("got = %v, want = 2.00 over 2 calls without taxes", u)
	}

	t.Log("source: spec/graphql/resolvers/customer_portal/customers/projected_usage_resolver_spec.rb:146-196")
	res, err = usage(sub.ID, true)
	if err != nil {
		t.Fatalf("GetPortalCurrentUsage(projection) error got = %v, want = nil", err)
	}
	if c := res.GetUsage().GetCharges(); len(c) != 1 || !c[0].HasProjectedUnits() {
		t.Errorf("got charges = %v, want = one charge with projected units", c)
	}

	t.Log("source: app/services/invoices/customer_usage_service.rb:57")
	other := f.h.Subscription(f.h.Customer(), p, billingtest.SubscribedAt(rpctest.Now.AddDate(0, 0, -5)))
	_, err = usage(other.ID, false)
	subWantCode(t, err, connect.CodeFailedPrecondition, "no_active_subscription")
}

func TestPortalWallets(t *testing.T) {
	t.Parallel()
	f := newPortalFixture(t)
	day := 24 * time.Hour
	older := f.h.Wallet(f.cust, "10", func(w *wallet.Wallet) {
		w.Priority = 50
		w.PaidTopUpMinAmount = new(int64(1000))
		w.CreatedAt = rpctest.Now.Add(-2 * day)
	})
	expiring := f.h.Wallet(f.cust, "10", func(w *wallet.Wallet) {
		w.Priority = 50
		w.ExpirationAt = new(rpctest.Now.Add(30 * day))
	})
	top := f.h.Wallet(f.cust, "10", func(w *wallet.Wallet) { w.Priority = 1 })
	terminated := f.h.Wallet(f.cust, "0", func(w *wallet.Wallet) {
		w.Status, w.TerminatedAt = wallet.StatusTerminated, new(rpctest.Now)
	})
	other := f.h.Wallet(f.h.Customer(), "10")

	list := func(filter string, size int32, token string) ([]string, string) {
		t.Helper()
		res, err := f.c.ListPortalWallets(t.Context(), portalv1.ListPortalWalletsRequest_builder{
			Filter: new(filter), PageSize: new(size), PageToken: new(token),
		}.Build())
		if err != nil {
			t.Fatalf("ListPortalWallets(%q) error got = %v, want = nil", filter, err)
		}
		var ids []string
		for _, w := range res.GetWallets() {
			ids = append(ids, w.GetId())
		}
		return ids, res.GetNextPageToken()
	}
	t.Run("list", func(t *testing.T) {
		t.Log("source: spec/graphql/resolvers/customer_portal/wallets_resolver_spec.rb:54-68")
		want := []string{top.ID.String(), expiring.ID.String(), older.ID.String()}
		if got, _ := list("", 0, ""); !slices.Equal(got, want) {
			t.Errorf("got = %v, want = %v", got, want)
		}
		first, token := list("", 2, "")
		rest, last := list("", 2, token)
		if got := slices.Concat(first, rest); !slices.Equal(got, want) || token == "" || last != "" {
			t.Errorf("paged: got = %v (tokens %q, %q), want = %v over two pages", got, token, last, want)
		}
		t.Log("source: app/graphql/resolvers/customer_portal/wallets_resolver.rb:18")
		if got, _ := list("status = terminated", 0, ""); !slices.Equal(got, []string{terminated.ID.String()}) {
			t.Errorf("terminated: got = %v, want = [%s]", got, terminated.ID)
		}
	})
	t.Run("paid top-up limits", func(t *testing.T) {
		t.Log("source: spec/graphql/resolvers/customer_portal/wallets_resolver_spec.rb:36-52")
		res, err := f.c.GetPortalWallet(t.Context(), portalv1.GetPortalWalletRequest_builder{WalletId: new(older.ID.String())}.Build())
		if err != nil {
			t.Fatalf("GetPortalWallet() error got = %v, want = nil", err)
		}
		w := res.GetWallet()
		if w.GetId() != older.ID.String() || w.GetPaidTopUpMinAmount() != "10.00" || w.GetPaidTopUpMinCredits() != "10.00000" ||
			w.HasPaidTopUpMaxAmount() || w.GetStatus() != walletv1.WalletStatus_WALLET_STATUS_ACTIVE || w.GetBalanceAmount().GetAmount() != "10.00" {
			t.Errorf("got = %v, want = an active wallet of 10.00 with a 10.00 (10 credits) minimum top-up", w)
		}
	})
	t.Run("get", func(t *testing.T) {
		t.Log("source: spec/graphql/resolvers/customer_portal/wallet_resolver_spec.rb:31-46")
		res, err := f.c.GetPortalWallet(t.Context(), portalv1.GetPortalWalletRequest_builder{WalletId: new(terminated.ID.String())}.Build())
		if err != nil || res.GetWallet().GetStatus() != walletv1.WalletStatus_WALLET_STATUS_TERMINATED {
			t.Errorf("got = %v, %v, want = the terminated wallet", res.GetWallet(), err)
		}
		t.Log("source: spec/graphql/resolvers/customer_portal/wallet_resolver_spec.rb:48-58")
		_, err = f.c.GetPortalWallet(t.Context(), portalv1.GetPortalWalletRequest_builder{WalletId: new(other.ID.String())}.Build())
		subWantCode(t, err, connect.CodeNotFound, "wallet_not_found")
	})
	t.Run("top up", func(t *testing.T) {
		topUp := func(id core.WalletID, credits *string) ([]*portalv1.PortalWalletTransaction, error) {
			res, err := f.c.TopUpPortalWallet(t.Context(), portalv1.TopUpPortalWalletRequest_builder{
				WalletId: new(id.String()), PaidCredits: credits,
			}.Build())
			return res.GetWalletTransactions(), err
		}
		t.Log("source: spec/graphql/mutations/customer_portal/wallet_transactions/create_spec.rb:24-41")
		ts, err := topUp(top.ID, new("5.00"))
		if err != nil || len(ts) != 1 {
			t.Fatalf("TopUpPortalWallet() got = %v, %v, want = one transaction", ts, err)
		}
		if g := ts[0]; g.GetStatus() != walletv1.SettlementStatus_SETTLEMENT_STATUS_PENDING ||
			g.GetTransactionStatus() != walletv1.TransactionStatus_TRANSACTION_STATUS_PURCHASED ||
			g.GetTransactionType() != walletv1.TransactionType_TRANSACTION_TYPE_INBOUND ||
			g.GetCreditAmount() != "5.00000" || g.GetAmount().GetAmount() != "5.00" || g.GetWallet().GetId() != top.ID.String() {
			t.Errorf("got = %v, want = a pending inbound purchase of 5 credits of the wallet", g)
		}
		t.Log("source: app/graphql/mutations/customer_portal/wallet_transactions/create.rb:12")
		if ts, err := topUp(top.ID, nil); err != nil || len(ts) != 0 {
			t.Errorf("without paid credits: got = %v, %v, want = no transaction", ts, err)
		}
		t.Log("source: spec/graphql/mutations/customer_portal/wallet_transactions/create_spec.rb:43-61")
		_, err = topUp(older.ID, new("1"))
		portalWantViolation(t, err, "paid_credits", wallet.CodeBelowMinimum)
		t.Log("source: spec/graphql/mutations/customer_portal/wallet_transactions/create_spec.rb:63-81")
		_, err = topUp(other.ID, new("5"))
		portalWantViolation(t, err, "wallet_id", "wallet_not_found")
		t.Log("source: app/services/wallet_transactions/validate_service.rb:40")
		_, err = topUp(terminated.ID, new("5"))
		portalWantViolation(t, err, "wallet_id", "wallet_is_terminated")
	})
}

func TestPortalAnalytics(t *testing.T) {
	t.Parallel()
	f := newPortalFixture(t, func(c *customer.Customer) { c.Currency = money.MustCurrency("EUR") })
	issued := rpctest.Now.AddDate(0, 0, -5)
	analyticsInvoice(t, f.s, f.cust, issued, 1000)
	var overdue []string
	for range 2 {
		inv := analyticsInvoice(t, f.s, f.cust, issued, 2000, func(i *invoice.Invoice) {
			i.PaymentOverdue = true
			i.PaymentDueDate = core.DateOf(issued, time.UTC)
		})
		overdue = append(overdue, inv.ID.String())
	}
	// Another customer's invoices and the customer's invoices in another
	// currency are left out.
	analyticsInvoice(t, f.s, f.h.Customer(), issued, 7000, func(i *invoice.Invoice) { i.PaymentOverdue = true })
	analyticsInvoice(t, f.s, f.cust, issued, 9000, func(i *invoice.Invoice) {
		i.Currency, i.PaymentOverdue = money.MustCurrency("USD"), true
	})

	t.Run("invoice collection", func(t *testing.T) {
		t.Log("source: spec/graphql/resolvers/customer_portal/analytics/invoice_collections_resolver_spec.rb:27-42")
		res, err := f.c.GetPortalInvoiceCollection(t.Context(), portalv1.GetPortalInvoiceCollectionRequest_builder{}.Build())
		if err != nil {
			t.Fatalf("GetPortalInvoiceCollection() error got = %v, want = nil", err)
		}
		p := res.GetPoints()
		if len(p) != 1 || p[0].GetAmount().GetAmount() != "50.00" || p[0].GetAmount().GetCurrency() != "EUR" || p[0].GetInvoicesCount() != 3 ||
			p[0].GetMonth().GetMonth() != 3 {
			t.Errorf("got = %v, want = 3 EUR invoices of 50.00 in March", p)
		}
	})
	t.Run("overdue balance", func(t *testing.T) {
		t.Log("source: spec/graphql/resolvers/customer_portal/analytics/overdue_balances_resolver_spec.rb:27-43")
		res, err := f.c.GetPortalOverdueBalance(t.Context(), portalv1.GetPortalOverdueBalanceRequest_builder{}.Build())
		if err != nil {
			t.Fatalf("GetPortalOverdueBalance() error got = %v, want = nil", err)
		}
		p := res.GetPoints()
		if len(p) != 1 || p[0].GetAmount().GetAmount() != "40.00" || p[0].GetAmount().GetCurrency() != "EUR" ||
			!slices.Equal(p[0].GetInvoiceIds(), overdue) {
			t.Errorf("got = %v, want = 40.00 EUR over %v", p, overdue)
		}
	})
}
