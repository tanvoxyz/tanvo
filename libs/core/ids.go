package core

// Identifier kinds. Each marker type below is the type argument of one
// exported ID alias; the markers are never instantiated.
type (
	organizationKind                 struct{}
	billingEntityKind                struct{}
	userKind                         struct{}
	membershipKind                   struct{}
	roleKind                         struct{}
	inviteKind                       struct{}
	passwordResetKind                struct{}
	apiKeyKind                       struct{}
	sessionKind                      struct{}
	userDeviceKind                   struct{}
	customerKind                     struct{}
	billableMetricKind               struct{}
	billableMetricFilterKind         struct{}
	planKind                         struct{}
	chargeKind                       struct{}
	chargeFilterKind                 struct{}
	addOnKind                        struct{}
	fixedChargeKind                  struct{}
	fixedChargeEventKind             struct{}
	pricingUnitKind                  struct{}
	taxKind                          struct{}
	commitmentKind                   struct{}
	usageThresholdKind               struct{}
	appliedUsageThresholdKind        struct{}
	featureKind                      struct{}
	privilegeKind                    struct{}
	entitlementKind                  struct{}
	entitlementRemovalKind           struct{}
	couponKind                       struct{}
	appliedCouponKind                struct{}
	subscriptionKind                 struct{}
	activationRuleKind               struct{}
	lifetimeUsageKind                struct{}
	eventKind                        struct{}
	cachedAggregationKind            struct{}
	invoiceKind                      struct{}
	invoiceSubscriptionKind          struct{}
	invoiceAppliedTaxKind            struct{}
	invoiceSettlementKind            struct{}
	invoiceCustomSectionKind         struct{}
	errorDetailKind                  struct{}
	feeKind                          struct{}
	feeAppliedTaxKind                struct{}
	adjustedFeeKind                  struct{}
	invoiceCreditKind                struct{}
	paymentIntentKind                struct{}
	creditNoteKind                   struct{}
	creditNoteItemKind               struct{}
	creditNoteAppliedTaxKind         struct{}
	walletKind                       struct{}
	walletTransactionKind            struct{}
	walletTransactionConsumptionKind struct{}
	recurringTransactionRuleKind     struct{}
	paymentKind                      struct{}
	paymentReceiptKind               struct{}
	refundKind                       struct{}
	paymentRequestKind               struct{}
	paymentMethodKind                struct{}
	paymentProviderCustomerKind      struct{}
	inboundWebhookKind               struct{}
	x402ConnectionKind               struct{}
	dunningCampaignKind              struct{}
	dunningCampaignThresholdKind     struct{}
	integrationKind                  struct{}
	integrationCustomerKind          struct{}
	integrationMappingKind           struct{}
	integrationCollectionMappingKind struct{}
	integrationItemKind              struct{}
	integrationResourceKind          struct{}
	webhookEndpointKind              struct{}
	webhookKind                      struct{}
	alertKind                        struct{}
	alertThresholdKind               struct{}
	triggeredAlertKind               struct{}
	dataExportKind                   struct{}
	dataExportPartKind               struct{}
	quoteKind                        struct{}
	quoteVersionKind                 struct{}
	orderFormKind                    struct{}
	orderKind                        struct{}
	auditEventKind                   struct{}
)

// Typed identifiers of every entity of the domain.
type (
	// OrganizationID identifies an organization (domain §4.1).
	OrganizationID = ID[organizationKind]
	// BillingEntityID identifies a billing entity (domain §4.2).
	BillingEntityID = ID[billingEntityKind]
	// UserID identifies a user (domain §5.1).
	UserID = ID[userKind]
	// MembershipID identifies a membership (domain §5.2).
	MembershipID = ID[membershipKind]
	// RoleID identifies a role (domain §5.3).
	RoleID = ID[roleKind]
	// InviteID identifies an invite (domain §5.4).
	InviteID = ID[inviteKind]
	// PasswordResetID identifies a password reset token (domain §5.4).
	PasswordResetID = ID[passwordResetKind]
	// APIKeyID identifies an API key (domain §5.5).
	APIKeyID = ID[apiKeyKind]
	// SessionID identifies a user session (domain §5.7).
	SessionID = ID[sessionKind]
	// UserDeviceID identifies a known device of a user (domain §5.8).
	UserDeviceID = ID[userDeviceKind]
	// CustomerID identifies a customer (domain §6).
	CustomerID = ID[customerKind]
	// BillableMetricID identifies a billable metric (domain §7.1).
	BillableMetricID = ID[billableMetricKind]
	// BillableMetricFilterID identifies a billable metric filter (domain §7.1).
	BillableMetricFilterID = ID[billableMetricFilterKind]
	// PlanID identifies a plan (domain §7.2).
	PlanID = ID[planKind]
	// ChargeID identifies a charge (domain §7.3).
	ChargeID = ID[chargeKind]
	// ChargeFilterID identifies a charge filter (domain §7.4).
	ChargeFilterID = ID[chargeFilterKind]
	// AddOnID identifies an add-on (domain §7.5).
	AddOnID = ID[addOnKind]
	// FixedChargeID identifies a fixed charge (domain §7.5).
	FixedChargeID = ID[fixedChargeKind]
	// FixedChargeEventID identifies a fixed charge units event (domain §7.5).
	FixedChargeEventID = ID[fixedChargeEventKind]
	// PricingUnitID identifies a pricing unit (domain §7.6).
	PricingUnitID = ID[pricingUnitKind]
	// TaxID identifies a tax (domain §7.7).
	TaxID = ID[taxKind]
	// CommitmentID identifies a commitment (domain §7.8).
	CommitmentID = ID[commitmentKind]
	// UsageThresholdID identifies a usage threshold (domain §7.9).
	UsageThresholdID = ID[usageThresholdKind]
	// AppliedUsageThresholdID identifies an applied usage threshold (domain §7.9).
	AppliedUsageThresholdID = ID[appliedUsageThresholdKind]
	// FeatureID identifies a feature (domain §7.10).
	FeatureID = ID[featureKind]
	// PrivilegeID identifies a privilege of a feature (domain §7.10).
	PrivilegeID = ID[privilegeKind]
	// EntitlementID identifies an entitlement (domain §7.10).
	EntitlementID = ID[entitlementKind]
	// EntitlementRemovalID identifies a subscription's removal of a plan entitlement (domain §7.10).
	EntitlementRemovalID = ID[entitlementRemovalKind]
	// CouponID identifies a coupon (domain §8.1).
	CouponID = ID[couponKind]
	// AppliedCouponID identifies an applied coupon (domain §8.2).
	AppliedCouponID = ID[appliedCouponKind]
	// SubscriptionID identifies a subscription (domain §9).
	SubscriptionID = ID[subscriptionKind]
	// ActivationRuleID identifies a subscription activation rule (domain §9.3).
	ActivationRuleID = ID[activationRuleKind]
	// LifetimeUsageID identifies the lifetime usage record of a subscription chain (domain §10.3).
	LifetimeUsageID = ID[lifetimeUsageKind]
	// EventID identifies an ingested event (domain §10.1).
	EventID = ID[eventKind]
	// CachedAggregationID identifies a cached aggregation state of a charge (domain §10.3).
	CachedAggregationID = ID[cachedAggregationKind]
	// InvoiceID identifies an invoice (domain §11).
	InvoiceID = ID[invoiceKind]
	// InvoiceSubscriptionID identifies an invoice subscription (domain §11.1).
	InvoiceSubscriptionID = ID[invoiceSubscriptionKind]
	// InvoiceAppliedTaxID identifies a tax applied to an invoice (domain §22.10).
	InvoiceAppliedTaxID = ID[invoiceAppliedTaxKind]
	// InvoiceSettlementID identifies an invoice settlement (domain §11.5).
	InvoiceSettlementID = ID[invoiceSettlementKind]
	// InvoiceCustomSectionID identifies an invoice custom section (domain §11.9).
	InvoiceCustomSectionID = ID[invoiceCustomSectionKind]
	// ErrorDetailID identifies an error detail recorded on a document (parity §1.49).
	ErrorDetailID = ID[errorDetailKind]
	// FeeID identifies a fee (domain §12).
	FeeID = ID[feeKind]
	// FeeAppliedTaxID identifies a tax applied to a fee (domain §12.1).
	FeeAppliedTaxID = ID[feeAppliedTaxKind]
	// AdjustedFeeID identifies a fee adjustment of a draft invoice (domain §12.3).
	AdjustedFeeID = ID[adjustedFeeKind]
	// InvoiceCreditID identifies a credit applied to an invoice (domain §11.11).
	InvoiceCreditID = ID[invoiceCreditKind]
	// PaymentIntentID identifies a hosted payment link (domain §15.1).
	PaymentIntentID = ID[paymentIntentKind]
	// CreditNoteID identifies a credit note (domain §13).
	CreditNoteID = ID[creditNoteKind]
	// CreditNoteItemID identifies a credit note item (domain §13.1).
	CreditNoteItemID = ID[creditNoteItemKind]
	// CreditNoteAppliedTaxID identifies a tax applied to a credit note (domain §13.1).
	CreditNoteAppliedTaxID = ID[creditNoteAppliedTaxKind]
	// WalletID identifies a wallet (domain §14.1).
	WalletID = ID[walletKind]
	// WalletTransactionID identifies a wallet transaction (domain §14.2).
	WalletTransactionID = ID[walletTransactionKind]
	// WalletTransactionConsumptionID identifies a traceable wallet consumption record (domain §14.2).
	WalletTransactionConsumptionID = ID[walletTransactionConsumptionKind]
	// RecurringTransactionRuleID identifies a recurring transaction rule (domain §14.3).
	RecurringTransactionRuleID = ID[recurringTransactionRuleKind]
	// PaymentID identifies a payment (domain §15.1).
	PaymentID = ID[paymentKind]
	// PaymentReceiptID identifies a payment receipt (domain §15.2).
	PaymentReceiptID = ID[paymentReceiptKind]
	// RefundID identifies a refund (domain §15.3).
	RefundID = ID[refundKind]
	// PaymentRequestID identifies a payment request (domain §15.4).
	PaymentRequestID = ID[paymentRequestKind]
	// PaymentMethodID identifies a payment method (domain §15.5).
	PaymentMethodID = ID[paymentMethodKind]
	// PaymentProviderCustomerID identifies a customer's connection to a payment provider (domain §15.5).
	PaymentProviderCustomerID = ID[paymentProviderCustomerKind]
	// InboundWebhookID identifies an inbound provider webhook (domain §15.6).
	InboundWebhookID = ID[inboundWebhookKind]
	// X402ConnectionID identifies an x402 connection (parity §1.36).
	X402ConnectionID = ID[x402ConnectionKind]
	// DunningCampaignID identifies a dunning campaign (domain §16).
	DunningCampaignID = ID[dunningCampaignKind]
	// DunningCampaignThresholdID identifies a dunning campaign threshold (domain §16).
	DunningCampaignThresholdID = ID[dunningCampaignThresholdKind]
	// IntegrationID identifies an integration connection (domain §17.1).
	IntegrationID = ID[integrationKind]
	// IntegrationCustomerID identifies an integration customer (domain §17.2).
	IntegrationCustomerID = ID[integrationCustomerKind]
	// IntegrationMappingID identifies an integration mapping (domain §17.3).
	IntegrationMappingID = ID[integrationMappingKind]
	// IntegrationCollectionMappingID identifies an integration collection mapping (domain §17.3).
	IntegrationCollectionMappingID = ID[integrationCollectionMappingKind]
	// IntegrationItemID identifies a cached integration item (domain §17.3).
	IntegrationItemID = ID[integrationItemKind]
	// IntegrationResourceID identifies a synced integration resource (domain §17.3).
	IntegrationResourceID = ID[integrationResourceKind]
	// WebhookEndpointID identifies a webhook endpoint (domain §18.1).
	WebhookEndpointID = ID[webhookEndpointKind]
	// WebhookID identifies a webhook delivery (domain §18.2).
	WebhookID = ID[webhookKind]
	// AlertID identifies an alert (domain §19).
	AlertID = ID[alertKind]
	// AlertThresholdID identifies an alert threshold (domain §19).
	AlertThresholdID = ID[alertThresholdKind]
	// TriggeredAlertID identifies a triggered alert (domain §19.2).
	TriggeredAlertID = ID[triggeredAlertKind]
	// DataExportID identifies a data export (domain §20).
	DataExportID = ID[dataExportKind]
	// DataExportPartID identifies a part of a data export (domain §20).
	DataExportPartID = ID[dataExportPartKind]
	// QuoteID identifies a quote (domain §21.1).
	QuoteID = ID[quoteKind]
	// QuoteVersionID identifies a quote version (domain §21.1).
	QuoteVersionID = ID[quoteVersionKind]
	// OrderFormID identifies an order form (domain §21.2).
	OrderFormID = ID[orderFormKind]
	// OrderID identifies an order (domain §21.3).
	OrderID = ID[orderKind]
	// AuditEventID identifies a recorded domain event (domain §2.3).
	AuditEventID = ID[auditEventKind]
)
