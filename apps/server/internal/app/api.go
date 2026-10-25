package app

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"buf.build/go/protovalidate"
	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/connect/v2/connectproto"
	"connectrpc.com/vanguard"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/tanvoxyz/tanvo/apps/server/internal/buildinfo"
	"github.com/tanvoxyz/tanvo/apps/server/internal/clientip"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/billing"
	"github.com/tanvoxyz/tanvo/libs/billing/addons"
	"github.com/tanvoxyz/tanvo/libs/billing/alerts"
	billinganalytics "github.com/tanvoxyz/tanvo/libs/billing/analytics"
	"github.com/tanvoxyz/tanvo/libs/billing/billingentities"
	"github.com/tanvoxyz/tanvo/libs/billing/customers"
	"github.com/tanvoxyz/tanvo/libs/billing/customsections"
	"github.com/tanvoxyz/tanvo/libs/billing/deduct"
	"github.com/tanvoxyz/tanvo/libs/billing/dunning"
	"github.com/tanvoxyz/tanvo/libs/billing/entitlements"
	"github.com/tanvoxyz/tanvo/libs/billing/exports"
	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/metrics"
	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/billing/pricingunits"
	"github.com/tanvoxyz/tanvo/libs/billing/products"
	"github.com/tanvoxyz/tanvo/libs/billing/subscriptions"
	"github.com/tanvoxyz/tanvo/libs/billing/taxes"
	"github.com/tanvoxyz/tanvo/libs/billing/usageattribution"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/billing/x402pay"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/x402"
	"github.com/tanvoxyz/tanvo/libs/documents"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/access/v1/accessv1connect"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/alert/v1/alertv1connect"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/analytics/v1/analyticsv1connect"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/attribution/v1/attributionv1connect"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1/catalogv1connect"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/coupon/v1/couponv1connect"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/creditnote/v1/creditnotev1connect"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/customer/v1/customerv1connect"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/dunning/v1/dunningv1connect"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/event/v1/eventv1connect"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/export/v1/exportv1connect"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/integration/v1/integrationv1connect"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/invoice/v1/invoicev1connect"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/log/v1/logv1connect"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/organization/v1/organizationv1connect"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/payment/v1/paymentv1connect"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/portal/v1/portalv1connect"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/quote/v1/quotev1connect"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/subscription/v1/subscriptionv1connect"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/tax/v1/taxv1connect"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/usage/v1/usagev1connect"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/wallet/v1/walletv1connect"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/webhook/v1/webhookv1connect"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/x402/v1/x402v1connect"
	"github.com/tanvoxyz/tanvo/libs/webhooks"
	x402prov "github.com/tanvoxyz/tanvo/providers/x402"
)

// usageFlushInterval is how often the API role records the uses of API
// keys it collected in memory (lago-api's per-request cache writes); the
// hourly clock job (jobs.KindTrackAPIKeyUsage) moves them to the keys'
// last_used_at.
const usageFlushInterval = time.Minute

// finalFlushTimeout bounds the flush of API key uses at shutdown.
const finalFlushTimeout = 10 * time.Second

// restPrefix is where the REST transcoding of the public RPCs is served
// (google.api.http annotations).
const restPrefix = "/v1/"

// api is the API role: the Connect handlers (Connect, gRPC and gRPC-Web
// at /<package>.<Service>/<Method>) and their REST transcoding by
// Vanguard under /v1/.
type api struct {
	usage   *auth.UsageTracker
	tx      port.Transactor
	log     *slog.Logger
	apiLogs *rpc.APILogWriter
}

// APIOptions are what the API role serves. Tests build the same handler
// stack with them (apps/server/internal/rpctest).
type APIOptions struct {
	// Tx is the store.
	Tx port.Transactor
	// Ingest is the event intake; nil leaves the event RPCs Unimplemented.
	Ingest *ingest.Service
	// EventReader reads the events of GetEvent and ListEvents; nil leaves
	// them Unimplemented.
	EventReader *ingest.Reader
	// AttributedUsage answers GetAttributedUsage; nil makes it available
	// to no organization.
	AttributedUsage metering.AttributedUsageReader
	// Billing is the billing engine; nil leaves the RPCs served by its
	// services Unimplemented.
	Billing *billing.Engine
	// MaxBytes bounds a request body.
	MaxBytes int64
	// Log is the logger.
	Log *slog.Logger
	// TracerProvider and MeterProvider receive the RPC telemetry; nil is
	// the no-op providers.
	TracerProvider trace.TracerProvider
	// MeterProvider receives the RPC metrics.
	MeterProvider metric.MeterProvider
	// TrustedProxies report client addresses besides the default ones.
	TrustedProxies clientip.Proxies
	// Clock is the services' clock; nil is the system clock.
	Clock core.Clock
	// AppURL is the web console URL: invite links and the OpenID Connect
	// callbacks (AppURL + "/auth/<provider>/callback").
	AppURL string
	// DisableSignup turns off self sign-up.
	DisableSignup bool
	// AnalyticsCacheTTL is how long a computed analytics report is reused;
	// 0 computes every report.
	AnalyticsCacheTTL time.Duration
	// GoogleClientID and GoogleClientSecret enable Google sign-in.
	GoogleClientID string
	// GoogleClientSecret is the OAuth client secret of Google sign-in.
	GoogleClientSecret string
	// PasswordParams are the password hashing costs; zero is
	// auth.DefaultArgon2Params (tests lower them).
	PasswordParams auth.Argon2Params
	// WebhooksAllowPrivate lets webhook endpoints use plain HTTP and
	// private addresses (TANVO_WEBHOOKS_ALLOW_PRIVATE).
	WebhooksAllowPrivate bool
	// OIDCAllowPrivate lets OpenID Connect providers use plain HTTP and
	// private addresses (TANVO_OIDC_ALLOW_PRIVATE), for development.
	OIDCAllowPrivate bool
	// APILogs, when set, queues the API logs instead of a writer of the
	// API's own; tests flush it, since [NewAPIHandler] runs no writer.
	APILogs *rpc.APILogWriter
	// SecretKey is the decoded installation secret (TANVO_SECRET_KEY),
	// from which the portal token and secret box keys are derived; nil
	// refuses the customer portal tokens and leaves the SSO connection
	// RPCs Unimplemented.
	SecretKey []byte
	// X402Facilitators reaches the x402 facilitators; nil is the HTTP
	// clients of providers/x402. Without SecretKey, every facilitator
	// call fails.
	X402Facilitators x402.Facilitators
	// Payments is the payments service wired to the payment providers
	// and the billing engine; nil serves the payment RPCs with a service
	// without providers.
	Payments *payments.Service
	// InboundWebhooks stores the payment provider webhooks; nil leaves
	// [rpc.InboundWebhookPattern] unmounted.
	InboundWebhooks *webhooks.Inbound
	// Integrations is the integrations service wired to the tax,
	// accounting and CRM providers; its sync triggers wrap the webhooks
	// of the API's services. Nil serves the integration RPCs with a
	// service without providers.
	Integrations *integrations.Service
	// Files links the stored documents, renders receipts and resends the
	// document emails; nil serves the downloads without links and the
	// resend RPCs as unimplemented.
	Files rpc.DocumentFiles
	// Blobs stores the logos, quote files and data exports; nil keeps
	// none.
	Blobs port.BlobStore
}

// oidcCallbacks are the callback paths of the OpenID Connect methods
// under the web console URL (lago google_service.rb:26,
// okta/authorize_service.rb:27, entra_id/authorize_service.rb:27).
var oidcCallbacks = map[organization.AuthenticationMethod]string{
	organization.AuthenticationMethodGoogleOAuth: "/auth/google/callback",
	organization.AuthenticationMethodOkta:        "/auth/okta/callback",
	organization.AuthenticationMethodEntraID:     "/auth/entra/callback",
	organization.AuthenticationMethodOIDC:        "/auth/oidc/callback",
}

// newAPI mounts the API on mux. Every RPC passes through the
// interceptors, outermost first: telemetry, panic recovery,
// authentication and authorization (the RPC's access option), the API
// logs, request validation (protovalidate) and idempotency keys.
func newAPI(mux *http.ServeMux, o APIOptions) (*api, error) {
	if o.Clock == nil {
		o.Clock = core.SystemClock{}
	}
	if o.TracerProvider == nil {
		o.TracerProvider = tracenoop.NewTracerProvider()
	}
	if o.MeterProvider == nil {
		o.MeterProvider = metricnoop.NewMeterProvider()
	}
	if o.PasswordParams == (auth.Argon2Params{}) {
		o.PasswordParams = auth.DefaultArgon2Params()
	}
	a := &api{usage: auth.NewUsageTracker(), tx: o.Tx, log: o.Log}
	ad := auth.Deps{
		Tx: o.Tx, Clock: o.Clock, IDs: core.UUIDv7Generator{}, Rand: rand.Reader, Logger: o.Log,
		SecurityLog: auth.NewRepoSecurityLogger(o.Tx, o.Clock, core.UUIDv7Generator{}, rpc.SecurityContext(clientip.New(o.TrustedProxies...))),
	}
	keys := auth.NewAPIKeys(ad, a.usage)
	sessions := auth.NewSessions(ad, auth.DefaultSessionConfig())
	// A sign-up's organization gets its default billing entity once the
	// billing entity service is built below.
	signupOrgs := &organizationCreator{base: auth.BasicOrganizationCreator{Clock: o.Clock, IDs: core.UUIDv7Generator{}}}
	users := auth.NewUsers(ad, sessions, auth.NewPasswordHasher(o.PasswordParams, rand.Reader), auth.UsersConfig{SignupDisabled: o.DisableSignup, Organizations: signupOrgs})
	invites := auth.NewInvites(ad, users, auth.InvitesConfig{FrontURL: o.AppURL})
	redirects := map[organization.AuthenticationMethod]string{}
	if o.AppURL != "" {
		for m, path := range oidcCallbacks {
			redirects[m] = strings.TrimRight(o.AppURL, "/") + path
		}
	}
	oc := auth.OIDCConfig{
		Connections: auth.NewStaticConnections(), HTTPClient: newOIDCClient(o.OIDCAllowPrivate), AllowInsecure: o.OIDCAllowPrivate,
	}
	var (
		portal *auth.PortalTokens
		sso    *auth.SSOConnections
		// secretBox seals the integrations' credentials.
		secretBox integrations.Sealer
	)
	if o.SecretKey != nil {
		var err error
		if portal, err = NewPortalTokens(o.SecretKey, o.AppURL, o.Clock); err != nil {
			return nil, fmt.Errorf("api: %w", err)
		}
		box, err := NewSecretBox(o.SecretKey)
		if err != nil {
			return nil, fmt.Errorf("api: %w", err)
		}
		sso = auth.NewSSOConnections(ad, box)
		secretBox = box
		oc.Connections = auth.IntegrationConnections{
			Tx: o.Tx, Secret: sso.Secret,
			RedirectURL: func(m organization.AuthenticationMethod) string { return redirects[m] },
		}
	}
	if o.GoogleClientID != "" {
		oc.Google = auth.GoogleConnection(o.GoogleClientID, o.GoogleClientSecret, redirects[organization.AuthenticationMethodGoogleOAuth])
	}
	oidcSvc := auth.NewOIDC(ad, users, invites, oc)
	if sso != nil {
		sso.WithIssuerChecker(oidcSvc)
	}
	authz := auth.NewAuthorizer(o.Tx)
	ips := clientip.New(o.TrustedProxies...)

	billingDeps := &kernel.Deps{
		Tx: o.Tx, Clock: o.Clock, IDs: core.UUIDv7Generator{}, Webhooks: &webhooks.Emitter{Tx: o.Tx}, Logger: o.Log,
		ActivityLogs: auditlog.NewActivityProducer(o.Clock, core.UUIDv7Generator{}, rpc.ActivityActor),
	}
	// The analytics reports of the API and of the customer portal share
	// one cache, as lago's share the Rails cache.
	analyticsSvc := billinganalytics.New(billingDeps).WithCache(billinganalytics.NewCache(o.AnalyticsCacheTTL))
	integrationSvc := o.Integrations
	if integrationSvc != nil {
		billingDeps.Webhooks = integrationSvc.Triggers(billingDeps.Webhooks)
	} else {
		integrationSvc = integrations.New(billingDeps, secretBox)
		integrations.NewTaxes(integrationSvc, nil)
	}
	integrationSvc.WithSecurityLog(rpc.IntegrationSecurityLog(ad.SecurityLog))
	metricSvc, err := metrics.New(billingDeps, nil)
	if err != nil {
		return nil, fmt.Errorf("api: metrics: %w", err)
	}
	billingEntitySvc, err := billingentities.New(billingDeps, o.Blobs)
	if err != nil {
		return nil, fmt.Errorf("api: billing entities: %w", err)
	}
	signupOrgs.entities = billingEntitySvc
	entitlementSvc, err := entitlements.New(billingDeps)
	if err != nil {
		return nil, fmt.Errorf("api: entitlements: %w", err)
	}
	taxSvc, err := taxes.New(billingDeps)
	if err != nil {
		return nil, fmt.Errorf("api: taxes: %w", err)
	}
	addOnSvc, err := addons.New(billingDeps)
	if err != nil {
		return nil, fmt.Errorf("api: add-ons: %w", err)
	}
	pricingUnitSvc, err := pricingunits.New(billingDeps)
	if err != nil {
		return nil, fmt.Errorf("api: pricing units: %w", err)
	}
	productSvc, err := products.New(billingDeps)
	if err != nil {
		return nil, fmt.Errorf("api: products: %w", err)
	}
	attributionSvc, err := usageattribution.New(billingDeps)
	if err != nil {
		return nil, fmt.Errorf("api: usage attribution: %w", err)
	}
	attributedReader := o.AttributedUsage
	if attributedReader == nil {
		attributedReader = noAttributedUsage{}
	}
	attributedSvc, err := usageattribution.NewQuery(billingDeps, attributedReader)
	if err != nil {
		return nil, fmt.Errorf("api: attributed usage: %w", err)
	}
	alertSvc, err := alerts.New(billingDeps)
	if err != nil {
		return nil, fmt.Errorf("api: alerts: %w", err)
	}
	walletSvc := wallets.New(billingDeps)
	if o.Billing != nil {
		walletSvc = o.Billing.Wallets
	}
	paymentSvc := o.Payments
	if paymentSvc == nil {
		paymentSvc = payments.New(billingDeps).WithCreditInvoices(walletSvc)
	}
	var customerSvc *customers.Service
	if o.Billing != nil {
		customerSvc = o.Billing.Customers
	} else if customerSvc, err = customers.New(billingDeps, customers.Relations{}); err != nil {
		return nil, fmt.Errorf("api: customers: %w", err)
	}

	validator, err := protovalidate.New()
	if err != nil {
		return nil, fmt.Errorf("api: validator: %w", err)
	}
	telemetry, err := rpc.Telemetry(o.TracerProvider, o.MeterProvider)
	if err != nil {
		return nil, err
	}
	if a.apiLogs = o.APILogs; a.apiLogs == nil {
		if a.apiLogs, err = rpc.NewAPILogWriter(o.Tx, o.Log, o.MeterProvider, rpc.APILogWriterConfig{}); err != nil {
			return nil, err
		}
	}
	server := connect.NewServer(telemetry, rpc.Recover(),
		rpc.Auth(authenticators(keys, sessions, authz, portal, o.Tx)),
		rpc.APILogs(a.apiLogs, o.Clock, core.UUIDv7Generator{}),
		rpc.Validate(validator),
		rpc.Idempotency(o.Tx, o.Clock))
	if o.Ingest != nil {
		events := rpc.NewEvents(o.Ingest, ips)
		if o.EventReader != nil {
			events.WithReader(o.EventReader)
		}
		if o.Billing != nil {
			events.WithEstimator(o.Billing.Estimates)
		}
		eventv1connect.RegisterEventServiceHandler(server, events)
	}
	accessv1connect.RegisterAuthServiceHandler(server, rpc.NewAuthService(rpc.AuthDeps{
		Users: users, Invites: invites, Resets: auth.NewPasswordResets(ad, users, sessions),
		OIDC: oidcSvc, Sessions: sessions, IPs: ips, RedirectURLs: redirects,
	}))
	if sso != nil {
		accessv1connect.RegisterSsoConnectionServiceHandler(server, rpc.NewSsoConnections(sso))
	}
	catalogv1connect.RegisterBillableMetricServiceHandler(server, rpc.NewBillableMetrics(metricSvc))
	catalogv1connect.RegisterFeatureServiceHandler(server, rpc.NewFeatures(entitlementSvc))
	catalogv1connect.RegisterEntitlementServiceHandler(server, rpc.NewEntitlements(entitlementSvc))
	catalogv1connect.RegisterAddOnServiceHandler(server, rpc.NewAddOns(addOnSvc))
	catalogv1connect.RegisterPricingUnitServiceHandler(server, rpc.NewPricingUnits(pricingUnitSvc))
	catalogv1connect.RegisterProductServiceHandler(server, rpc.NewProducts(productSvc))
	catalogv1connect.RegisterRateCardServiceHandler(server, rpc.NewRateCards(productSvc))
	catalogv1connect.RegisterPlanRateCardServiceHandler(server, rpc.NewPlanRateCards(productSvc))
	attributionv1connect.RegisterUsageAttributionServiceHandler(server, rpc.NewUsageAttribution(attributionSvc).WithAttributedUsage(attributedSvc))
	if o.Billing != nil {
		quoteSvc := o.Billing.Quotes.WithBlobs(o.Blobs)
		mentions, err := documents.NewMentionLocalizer()
		if err != nil {
			return nil, fmt.Errorf("api: quotes: %w", err)
		}
		quotev1connect.RegisterQuoteServiceHandler(server, rpc.NewQuotes(quoteSvc, mentions))
		quotev1connect.RegisterOrderFormServiceHandler(server, rpc.NewOrderForms(quoteSvc))
		quotev1connect.RegisterOrderServiceHandler(server, rpc.NewOrders(quoteSvc))
	}
	taxv1connect.RegisterTaxServiceHandler(server, rpc.NewTaxes(taxSvc))
	alertv1connect.RegisterAlertServiceHandler(server, rpc.NewAlerts(alertSvc))
	walletv1connect.RegisterWalletServiceHandler(server, rpc.NewWallets(walletSvc, walletPaymentURLs(o.Payments)))
	couponv1connect.RegisterCouponServiceHandler(server, rpc.NewCoupons(deduct.New(billingDeps)))
	organizationv1connect.RegisterBillingEntityServiceHandler(server, rpc.NewBillingEntities(billingEntitySvc))
	customerv1connect.RegisterCustomerServiceHandler(server, rpc.NewCustomers(customerSvc, portal, o.Tx))
	if o.Billing != nil {
		creditnotev1connect.RegisterCreditNoteServiceHandler(server, rpc.NewCreditNotes(o.Billing.CreditNotes).WithIntegrations(integrationSvc).WithFiles(o.Files))
	}
	if o.Billing != nil {
		invoicev1connect.RegisterFeeServiceHandler(server, rpc.NewFees(o.Billing.Invoices))
		invoicev1connect.RegisterInvoiceServiceHandler(server, rpc.NewInvoices(o.Billing.Invoices, paymentSvc).WithIntegrations(integrationSvc).WithFiles(o.Files))
		catalogv1connect.RegisterPlanServiceHandler(server, rpc.NewPlans(o.Billing.Plans))
		catalogv1connect.RegisterChargeServiceHandler(server, rpc.NewCharges(o.Billing.Plans))
		catalogv1connect.RegisterFixedChargeServiceHandler(server, rpc.NewFixedCharges(o.Billing.Plans))
	}
	if o.Billing != nil {
		// The subscriptions read the engine's events for the hourly usage.
		subscriptionDeps := *billingDeps
		subscriptionDeps.Events = o.Billing.Events()
		subscriptionSvc := subscriptions.New(&subscriptionDeps, o.Billing.Lifecycle, o.Billing.Usage, o.Billing.Plans)
		subscriptionv1connect.RegisterSubscriptionServiceHandler(server, rpc.NewSubscriptions(subscriptionSvc).WithPayments(paymentSvc))
		subscriptionv1connect.RegisterContractServiceHandler(server, rpc.NewContracts(o.Billing.Contracts, productSvc))
		usagev1connect.RegisterUsageServiceHandler(server, rpc.NewUsage(subscriptionSvc))
		portalv1connect.RegisterCustomerPortalServiceHandler(server, rpc.NewPortal(rpc.PortalDeps{
			Organizations: auth.NewOrganizationSettings(ad, o.Blobs), BillingEntities: billingEntitySvc,
			Customers: customerSvc, Invoices: o.Billing.Invoices, Subscriptions: subscriptionSvc,
			Wallets: walletSvc, Analytics: analyticsSvc, Files: o.Files,
		}))
	}
	paymentv1connect.RegisterPaymentServiceHandler(server, rpc.NewPayments(paymentSvc))
	paymentv1connect.RegisterPaymentRequestServiceHandler(server, rpc.NewPaymentRequests(paymentSvc))
	paymentv1connect.RegisterPaymentReceiptServiceHandler(server, rpc.NewPaymentReceipts(paymentSvc).WithFiles(o.Files))
	paymentv1connect.RegisterPaymentMethodServiceHandler(server, rpc.NewPaymentMethods(paymentSvc))
	invoicev1connect.RegisterInvoiceCustomSectionServiceHandler(server, rpc.NewInvoiceCustomSections(customsections.New(billingDeps)))
	dunningv1connect.RegisterDunningCampaignServiceHandler(server, rpc.NewDunningCampaigns(dunning.New(billingDeps)))
	webhookv1connect.RegisterWebhookServiceHandler(server, rpc.NewWebhooks(&webhooks.Endpoints{
		Tx: o.Tx, Clock: o.Clock, IDs: core.UUIDv7Generator{}, Rand: rand.Reader, AllowPrivate: o.WebhooksAllowPrivate,
		SecurityLog: rpc.WebhookSecurityLog(ad.SecurityLog),
	}))
	integrationv1connect.RegisterIntegrationServiceHandler(server, rpc.NewIntegrations(integrationSvc))
	analyticsv1connect.RegisterAnalyticsServiceHandler(server, rpc.NewAnalytics(analyticsSvc))
	exportv1connect.RegisterDataExportServiceHandler(server, rpc.NewDataExports(exports.New(billingDeps, o.Blobs, rpc.ExportFilters()), ad.SecurityLog))
	accessv1connect.RegisterMemberServiceHandler(server, rpc.NewMembers(auth.NewMemberships(ad), auth.NewRoles(ad), invites))
	accessv1connect.RegisterApiKeyServiceHandler(server, rpc.NewAPIKeys(keys, o.Clock))
	organizationv1connect.RegisterOrganizationServiceHandler(server, rpc.NewOrganizations(auth.NewOrganizationSettings(ad, o.Blobs)))
	organizationv1connect.RegisterServerServiceHandler(server, rpc.NewServer(serverInfo()))
	logv1connect.RegisterLogServiceHandler(server, rpc.NewLogs(auth.NewAuditLogs(ad)))
	if o.X402Facilitators == nil {
		o.X402Facilitators = x402prov.NewFacilitators(x402prov.WithTracerProvider(o.TracerProvider),
			x402prov.WithMeterProvider(o.MeterProvider), x402prov.WithLogger(o.Log))
	}
	var x402Box x402pay.Sealer
	if secretBox != nil {
		x402Box = secretBox
	}
	x402Svc, err := x402pay.New(billingDeps, x402Box, o.X402Facilitators)
	if err != nil {
		return nil, fmt.Errorf("api: %w", err)
	}
	x402v1connect.RegisterX402ServiceHandler(server, rpc.NewX402(x402Svc))

	// JSON uses the proto field names (snake_case) and keeps the numbers
	// of event properties exact, on both transports.
	connecthttp.Mount(mux, server,
		connecthttp.WithCodecs(connectproto.NewBinaryCodec(), rpc.NewJSONCodec()),
		connecthttp.WithReadMaxBytes(int(o.MaxBytes)),
	)
	rest := http.NewServeMux()
	if err := vanguard.Mount(rest, server,
		vanguard.WithCodec(func(res connectproto.TypeResolver) vanguard.RESTCodec {
			return rpc.NewRESTCodec(res)
		}),
		vanguard.WithMaxReadBytes(o.MaxBytes),
	); err != nil {
		return nil, fmt.Errorf("api: rest: %w", err)
	}
	// REST update masks accept the proto field names, as the JSON does.
	mux.Handle(restPrefix, rpc.RESTRequests(rpc.RESTFieldMasks(rest)))
	if o.InboundWebhooks != nil {
		mux.Handle(rpc.InboundWebhookPattern, &rpc.InboundWebhooks{Tx: o.Tx, Receiver: o.InboundWebhooks, Acks: paymentSvc, Log: o.Log})
	}
	return a, nil
}

// walletPaymentURLs returns the payment links of the wallets' credit
// invoices, nil without a provider-wired payments service.
func walletPaymentURLs(svc *payments.Service) rpc.WalletPaymentURLs {
	if svc == nil {
		return nil
	}
	return svc
}

// authenticators are the credentials the API accepts: API keys, user
// sessions and, when portal is set, customer portal tokens.
func authenticators(keys *auth.APIKeys, sessions *auth.Sessions, authz *auth.Authorizer, portal *auth.PortalTokens, tx port.Transactor) rpc.Authenticators {
	a := rpc.Authenticators{Keys: keys, Sessions: sessions, Authz: authz}
	if portal != nil {
		a.Portal = auth.PortalAuthenticator{Tokens: portal, Tx: tx}
	}
	return a
}

// NewAPIHandler returns the API's HTTP handler (the Connect routes and
// the REST routes under /v1/), without the usage flush of API keys and
// the API log writer (set [APIOptions.APILogs] to flush the logs): the
// handler stack of the server, for tests.
func NewAPIHandler(o APIOptions) (http.Handler, error) {
	mux := http.NewServeMux()
	if _, err := newAPI(mux, o); err != nil {
		return nil, err
	}
	return mux, nil
}

// runUsageFlush writes the API key uses every [usageFlushInterval] until
// ctx ends, then once more.
func (a *api) runUsageFlush(ctx context.Context) error {
	t := time.NewTicker(usageFlushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalFlushTimeout)
			a.flushUsage(fctx)
			cancel()
			return nil
		case <-t.C:
			a.flushUsage(ctx)
		}
	}
}

func (a *api) flushUsage(ctx context.Context) {
	if a.usage.Pending() == 0 {
		return
	}
	if err := a.usage.Flush(ctx, a.tx); err != nil {
		a.log.WarnContext(ctx, "api: track api key usage", "error", err)
	}
}

// runAPILogs writes the queued API logs until ctx ends, then the rest.
func (a *api) runAPILogs(ctx context.Context) error { return a.apiLogs.Run(ctx) }

// serverInfo describes the running binary.
func serverInfo() rpc.ServerInfo {
	b := buildinfo.Read()
	return rpc.ServerInfo{Version: b.Version, Revision: b.Revision}
}
