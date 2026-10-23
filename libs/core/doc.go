// Package core is the domain model of Tanvo and the ports the rest of the
// system implements and consumes (architecture §1.3, §3.2). It depends only
// on libs/money and libs/period.
//
// This root package holds what every domain package shares: typed
// identifiers ([ID] and the aliases such as [CustomerID]), value objects
// ([Address], [Metadata], [Date]), field validation ([Validator],
// [ValidationError] with stable codes), sentinel errors ([ErrNotFound],
// [ErrConflict], [ErrInvalidTransition], …), pagination ([PageRequest],
// [Page]) and the [Clock] and [IDGenerator] ports.
//
// # Layout
//
// Each aggregate of docs/domain.md lives in its own package, which holds
// its entities, enumerations, invariants (Validate methods), lifecycle
// transitions and repository interfaces:
//
//	organization  Organization, BillingEntity (§4)
//	access        User, Membership, Role, Invite, APIKey, PasswordReset (§5)
//	permission    the permission catalog and built-in roles (§5.3)
//	customer      Customer (§6)
//	catalog       BillableMetric, Plan, Charge, ChargeFilter, FixedCharge,
//	              AddOn, PricingUnit, Tax, Commitment, UsageThreshold,
//	              Feature, Entitlement (§7)
//	coupon        Coupon, AppliedCoupon (§8)
//	subscription  Subscription, ActivationRule, FixedChargeEvent,
//	              AppliedUsageThreshold, LifetimeUsage (§9, §10.3)
//	event         Event, the raw ingested event (§10.1)
//	invoice       Invoice, InvoiceSubscription, Fee, applied taxes,
//	              settlements, custom sections (§11, §12)
//	creditnote    CreditNote and its items and taxes (§13)
//	wallet        Wallet, WalletTransaction, RecurringTransactionRule (§14)
//	payment       Payment, PaymentReceipt, Refund, PaymentRequest,
//	              PaymentMethod, PaymentProviderCustomer, InboundWebhook (§15)
//	dunning       DunningCampaign (§16)
//	integration   Integration, customers, mappings, items, resources (§17)
//	webhook       WebhookEndpoint, Webhook deliveries, event types and the
//	              domain events recorded with every state change (§2.3, §18)
//	alert         Alert, TriggeredAlert (§19)
//	export        DataExport (§20)
//	quote         Quote, QuoteVersion, OrderForm, Order (§21)
//	port          the transaction boundary and the infrastructure ports
//	memstore      an in-memory implementation of every repository
//
// Domain packages import only this root package (and money and period), so
// they never import each other: an entity refers to another aggregate by
// its typed ID only. The one exception is the leaf package permission,
// which access imports for the permission types of roles and API keys. That keeps the graph acyclic whatever references the
// domain adds. Package port imports every domain package to aggregate
// their repositories; package memstore implements port.
package core
