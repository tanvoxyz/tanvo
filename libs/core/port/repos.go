package port

import (
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/alert"
	"github.com/tanvoxyz/tanvo/libs/core/analytics"
	"github.com/tanvoxyz/tanvo/libs/core/attribution"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/dunning"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/export"
	"github.com/tanvoxyz/tanvo/libs/core/idempotency"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/core/x402"
)

// Repos gives every repository, the advisory locks, the sequences and the
// job queue, all bound to one transaction. It is only valid inside the
// function passed to [Transactor.InTx].
type Repos struct {
	// Organizations stores organizations (domain §4.1).
	Organizations organization.Repository
	// BillingEntities stores billing entities (domain §4.2).
	BillingEntities organization.BillingEntityRepository

	// Users stores users (domain §5).
	Users access.UserRepository
	// Memberships stores memberships.
	Memberships access.MembershipRepository
	// Roles stores custom roles and serves the built-in ones.
	Roles access.RoleRepository
	// Invites stores invites.
	Invites access.InviteRepository
	// PasswordResets stores password reset tokens.
	PasswordResets access.PasswordResetRepository
	// APIKeys stores API keys.
	APIKeys access.APIKeyRepository
	// Sessions stores user sessions (domain §5.7).
	Sessions access.SessionRepository
	// OIDCStates stores OpenID Connect sign-ins in progress.
	OIDCStates access.OIDCStateRepository
	// UserDevices stores the known devices of users (domain §5.8).
	UserDevices access.UserDeviceRepository

	// Customers stores customers (domain §6).
	Customers customer.Repository
	// PendingVIESChecks stores the VIES checks waiting to be retried
	// (domain §22.10).
	PendingVIESChecks customer.PendingVIESCheckRepository

	// Metrics stores billable metrics and their filters (domain §7.1).
	Metrics catalog.MetricRepository
	// Plans stores plans and plan overrides (domain §7.2).
	Plans catalog.PlanRepository
	// Charges stores charges and charge filters (domain §7.3–§7.4).
	Charges catalog.ChargeRepository
	// FixedCharges stores fixed charges (domain §7.5).
	FixedCharges catalog.FixedChargeRepository
	// AddOns stores add-ons (domain §7.6).
	AddOns catalog.AddOnRepository
	// PricingUnits stores pricing units.
	PricingUnits catalog.PricingUnitRepository
	// Taxes stores taxes (domain §7.7).
	Taxes catalog.TaxRepository
	// Commitments stores plan commitments (domain §7.8).
	Commitments catalog.CommitmentRepository
	// UsageThresholds stores progressive billing thresholds (domain §7.9).
	UsageThresholds catalog.UsageThresholdRepository
	// Features stores features and privileges (domain §7.10).
	Features catalog.FeatureRepository
	// Entitlements stores entitlements and their removals.
	Entitlements catalog.EntitlementRepository
	// ProductCategories stores product categories (domain §21.4).
	ProductCategories catalog.ProductCategoryRepository
	// Products stores products (domain §21.4).
	Products catalog.ProductRepository
	// ProductFilters stores product filters (domain §21.4).
	ProductFilters catalog.ProductFilterRepository
	// RateCards stores rate cards (domain §21.4).
	RateCards catalog.RateCardRepository
	// Rates stores the rates of rate cards (domain §21.4).
	Rates catalog.RateRepository
	// PlanRateCards stores the rate cards of plans and their phases
	// (domain §21.4).
	PlanRateCards catalog.PlanRateCardRepository

	// Coupons stores coupons (domain §8).
	Coupons coupon.Repository
	// AppliedCoupons stores applied coupons.
	AppliedCoupons coupon.AppliedRepository

	// Subscriptions stores subscriptions (domain §9).
	Subscriptions subscription.Repository
	// Contracts reads the subscriptions priced by rate cards (domain
	// §21.4).
	Contracts subscription.ContractRepository
	// ContractRateCards stores the rate cards of contracts and their phases
	// (domain §21.4).
	ContractRateCards subscription.RateCardRepository
	// BillingSegments stores the billing segments of contract rate cards
	// (domain §21.4).
	BillingSegments subscription.BillingSegmentRepository
	// ActivationRules stores subscription activation rules (domain §9.3).
	ActivationRules subscription.ActivationRuleRepository
	// FixedChargeEvents stores fixed charge unit changes (domain §7.5).
	FixedChargeEvents subscription.FixedChargeEventRepository
	// FixedChargeUnitsOverrides stores the fixed charge units overridden per
	// subscription (domain §7.5).
	FixedChargeUnitsOverrides subscription.FixedChargeUnitsOverrideRepository
	// AppliedUsageThresholds stores crossed usage thresholds (domain §7.9).
	AppliedUsageThresholds subscription.AppliedUsageThresholdRepository
	// LifetimeUsages stores lifetime usage (domain §10.3).
	LifetimeUsages subscription.LifetimeUsageRepository
	// DailyUsages stores the daily usage snapshots of subscriptions.
	DailyUsages subscription.DailyUsageRepository

	// Events is the ingestion log deduplicating events (domain §10.1).
	Events event.Log
	// CachedAggregations stores the running aggregation states of charges
	// (domain §10.3).
	CachedAggregations event.CachedAggregationRepository

	// Invoices stores invoices (domain §11).
	Invoices invoice.Repository
	// InvoiceSubscriptions stores the subscriptions billed by invoices.
	InvoiceSubscriptions invoice.SubscriptionRepository
	// Fees stores fees (domain §12).
	Fees invoice.FeeRepository
	// InvoiceAppliedTaxes stores invoice-level applied taxes.
	InvoiceAppliedTaxes invoice.AppliedTaxRepository
	// AdjustedFees stores fee adjustments on draft invoices (domain §12.3).
	AdjustedFees invoice.AdjustedFeeRepository
	// InvoiceSettlements stores invoice settlements.
	InvoiceSettlements invoice.SettlementRepository
	// InvoiceCustomSections stores invoice custom sections (domain §11.9).
	InvoiceCustomSections invoice.CustomSectionRepository
	// AppliedInvoiceCustomSections stores the custom sections printed on
	// invoices (domain §11.9).
	AppliedInvoiceCustomSections invoice.AppliedCustomSectionRepository
	// ErrorDetails stores invoice error details.
	ErrorDetails invoice.ErrorDetailRepository
	// Credits stores the coupon, credit-note and progressive-billing
	// credits of invoices (domain §11.11).
	Credits invoice.CreditRepository

	// CreditNotes stores credit notes with items and taxes (domain §13).
	CreditNotes creditnote.Repository
	// CreditNoteErrorDetails stores credit note error details.
	CreditNoteErrorDetails creditnote.ErrorDetailRepository

	// Wallets stores wallets (domain §14.1).
	Wallets wallet.Repository
	// WalletTransactions stores wallet transactions and consumptions.
	WalletTransactions wallet.TransactionRepository
	// RecurringTransactionRules stores wallet top-up rules (domain §14.3).
	RecurringTransactionRules wallet.RecurringTransactionRuleRepository

	// Payments stores payments (domain §15.1).
	Payments payment.Repository
	// PaymentReceipts stores payment receipts (domain §15.2).
	PaymentReceipts payment.ReceiptRepository
	// Refunds stores refunds (domain §15.3).
	Refunds payment.RefundRepository
	// PaymentRequests stores payment requests (domain §15.4).
	PaymentRequests payment.RequestRepository
	// PaymentMethods stores customer payment methods (domain §15.5).
	PaymentMethods payment.MethodRepository
	// PaymentProviderCustomers stores provider-side customers.
	PaymentProviderCustomers payment.ProviderCustomerRepository
	// PaymentIntents stores hosted payment URLs.
	PaymentIntents payment.IntentRepository
	// InboundWebhooks stores provider webhooks (domain §15.6).
	InboundWebhooks payment.InboundWebhookRepository

	// DunningCampaigns stores dunning campaigns (domain §16).
	DunningCampaigns dunning.Repository

	// Integrations stores integrations (domain §17).
	Integrations integration.Repository
	// IntegrationCustomers stores customers linked to integrations.
	IntegrationCustomers integration.CustomerRepository
	// IntegrationMappings stores per-object mappings.
	IntegrationMappings integration.MappingRepository
	// IntegrationCollectionMappings stores collection mappings.
	IntegrationCollectionMappings integration.CollectionMappingRepository
	// IntegrationItems stores items fetched from integrations.
	IntegrationItems integration.ItemRepository
	// IntegrationResources stores objects synced to integrations.
	IntegrationResources integration.ResourceRepository

	// WebhookEndpoints stores webhook endpoints (domain §18.1).
	WebhookEndpoints webhook.EndpointRepository
	// WebhookDeliveries stores webhook deliveries (domain §18.2).
	WebhookDeliveries webhook.DeliveryRepository
	// DomainEvents stores domain events (domain §2.3).
	DomainEvents webhook.EventRepository

	// Alerts stores usage alerts (domain §19).
	Alerts alert.Repository
	// TriggeredAlerts stores alert firings.
	TriggeredAlerts alert.TriggeredAlertRepository

	// DataExports stores data exports (domain §20).
	DataExports export.Repository
	// DataExportParts stores data export parts.
	DataExportParts export.PartRepository
	// RecordDeletions reads the tombstones of deleted rows (parity §1.49).
	RecordDeletions export.RecordDeletionRepository

	// Analytics computes the analytics reports (parity §1.44).
	Analytics analytics.Repository

	// Quotes stores quotes (domain §21).
	Quotes quote.Repository
	// QuoteVersions stores quote versions.
	QuoteVersions quote.VersionRepository
	// OrderForms stores order forms.
	OrderForms quote.OrderFormRepository
	// Orders stores orders.
	Orders quote.OrderRepository

	// UsageAttributionTypes stores usage attribution types (parity §1.42).
	UsageAttributionTypes attribution.TypeRepository
	// UsageAttributionValues stores usage attribution values.
	UsageAttributionValues attribution.ValueRepository

	// X402Connections stores x402 connections (parity §1.36).
	X402Connections x402.ConnectionRepository
	// X402Settlements stores x402 settlements.
	X402Settlements x402.SettlementRepository

	// IdempotencyKeys stores the responses of requests made with an
	// Idempotency-Key header (architecture §4.2).
	IdempotencyKeys idempotency.Repository

	// SecurityLogs stores security logs (parity §1.45).
	SecurityLogs auditlog.SecurityLogRepository
	// APILogs stores the logs of API-key write requests.
	APILogs auditlog.APILogRepository
	// ActivityLogs stores resource activity logs.
	ActivityLogs auditlog.ActivityLogRepository

	// Locks takes transaction-scoped advisory locks.
	Locks Locker
	// Sequences hands out document sequence numbers.
	Sequences Sequences
	// Jobs enqueues background jobs committed with the transaction.
	Jobs JobEnqueuer
}
