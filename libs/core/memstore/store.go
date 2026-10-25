package memstore

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// JobDeliverWebhookEvent is the kind of the job [Store.Emit] enqueues for a
// recorded domain event.
const JobDeliverWebhookEvent = "webhook.deliver_event"

// Store is an in-memory [port.Transactor]. The zero value is not usable;
// call [New].
type Store struct {
	mu   sync.Mutex
	st   *state
	jobs []port.Job
}

// state is the whole database.
type state struct {
	organization *organizationData
	access       *accessData
	customer     *customerData
	catalog      *catalogData
	catalogPart2 *catalogPart2Data
	coupon       *couponData
	subscription *subscriptionData
	event        *eventData
	invoice      *invoiceData
	creditNote   *creditNoteData
	wallet       *walletData
	payment      *paymentData
	dunning      *dunningData
	integration  *integrationData
	webhook      *webhookData
	alert        *alertData
	export       *exportData
	quote        *quoteData
	idempotency  *idempotencyData
	auditLog     *auditLogData
	attribution  *attributionData
	sequences    map[storeSequenceKey]int64

	productCatalog *productCatalogData
	x402           *x402Data
}

type storeSequenceKey struct {
	org core.OrganizationID
	key port.SequenceKey
}

func newState() *state {
	return &state{
		organization: newOrganizationData(),
		access:       newAccessData(),
		customer:     newCustomerData(),
		catalog:      newCatalogData(),
		catalogPart2: newCatalogPart2Data(),
		coupon:       newCouponData(),
		subscription: newSubscriptionData(),
		event:        newEventData(),
		invoice:      newInvoiceData(),
		creditNote:   newCreditNoteData(),
		wallet:       newWalletData(),
		payment:      newPaymentData(),
		dunning:      newDunningData(),
		integration:  newIntegrationData(),
		webhook:      newWebhookData(),
		alert:        newAlertData(),
		export:       newExportData(),
		quote:        newQuoteData(),
		idempotency:  newIdempotencyData(),
		auditLog:     newAuditLogData(),
		attribution:  newAttributionData(),
		sequences:    map[storeSequenceKey]int64{},

		productCatalog: newProductCatalogData(),
		x402:           newX402Data(),
	}
}

func (s *state) snapshot() *state {
	return &state{
		organization: s.organization.snapshot(),
		access:       s.access.snapshot(),
		customer:     s.customer.snapshot(),
		catalog:      s.catalog.snapshot(),
		catalogPart2: s.catalogPart2.snapshot(),
		coupon:       s.coupon.snapshot(),
		subscription: s.subscription.snapshot(),
		event:        s.event.snapshot(),
		invoice:      s.invoice.snapshot(),
		creditNote:   s.creditNote.snapshot(),
		wallet:       s.wallet.snapshot(),
		payment:      s.payment.snapshot(),
		dunning:      s.dunning.snapshot(),
		integration:  s.integration.snapshot(),
		webhook:      s.webhook.snapshot(),
		alert:        s.alert.snapshot(),
		export:       s.export.snapshot(),
		quote:        s.quote.snapshot(),
		idempotency:  s.idempotency.snapshot(),
		auditLog:     s.auditLog.snapshot(),
		attribution:  s.attribution.snapshot(),
		sequences:    maps.Clone(s.sequences),

		productCatalog: s.productCatalog.snapshot(),
		x402:           s.x402.snapshot(),
	}
}

// New returns an empty store.
func New() *Store { return &Store{st: newState()} }

var _ port.Transactor = (*Store)(nil)

// storeTx is one running transaction.
type storeTx struct {
	st        *state
	committed []port.Job
	jobs      []port.Job
	repos     port.Repos
}

type storeTxKey struct{}

// InTx implements [port.Transactor]. Transactions are serialized; a nested
// call joins the transaction carried by ctx.
func (s *Store) InTx(ctx context.Context, fn func(ctx context.Context, r port.Repos) error) error {
	if tx, ok := ctx.Value(storeTxKey{}).(*storeTx); ok {
		return fn(ctx, tx.repos)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx := &storeTx{st: s.st.snapshot(), committed: s.jobs}
	tx.repos = tx.bind()
	if err := fn(context.WithValue(ctx, storeTxKey{}, tx), tx.repos); err != nil {
		return err
	}
	recordDeletions(s.st, tx.st)
	s.st = tx.st
	s.jobs = append(s.jobs, tx.jobs...)
	return nil
}

// Jobs returns the jobs enqueued by committed transactions, oldest first.
func (s *Store) Jobs() []port.Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.jobs)
}

// TakeJobs returns the jobs enqueued by committed transactions and forgets
// them, as a worker would.
func (s *Store) TakeJobs() []port.Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs := s.jobs
	s.jobs = nil
	return jobs
}

// Emit implements [port.WebhookEmitter]: it records e and enqueues a
// [JobDeliverWebhookEvent] job on the webhooks queue, in the transaction
// carried by ctx or in a new one.
func (s *Store) Emit(ctx context.Context, e webhook.Event) error {
	if err := e.Validate(); err != nil {
		return fmt.Errorf("emit %s: %w", e.Type, err)
	}
	return s.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := r.DomainEvents.Record(ctx, &e); err != nil {
			return fmt.Errorf("emit %s: %w", e.Type, err)
		}
		args, err := json.Marshal(map[string]string{"event_id": e.ID.String()})
		if err != nil {
			return fmt.Errorf("emit %s: %w", e.Type, err)
		}
		return r.Jobs.Enqueue(ctx, port.Job{
			Kind:           JobDeliverWebhookEvent,
			Queue:          port.QueueWebhooks,
			OrganizationID: e.OrganizationID,
			Args:           args,
			UniqueKey:      e.ID.String(),
		})
	})
}

var _ port.WebhookEmitter = (*Store)(nil)

func (tx *storeTx) bind() port.Repos {
	st := tx.st
	return port.Repos{
		Organizations:   organizationRepo{st.organization, st},
		BillingEntities: billingEntityRepo{st.organization},

		Users:          userRepo{st.access},
		Memberships:    membershipRepo{st.access},
		Roles:          roleRepo{st.access},
		Invites:        inviteRepo{st.access},
		PasswordResets: passwordResetRepo{st.access},
		APIKeys:        apiKeyRepo{st.access},
		Sessions:       sessionRepo{st.access},
		OIDCStates:     oidcStateRepo{st.access},
		UserDevices:    userDeviceRepo{st.access},

		Customers:         customerRepo{st.customer, st},
		PendingVIESChecks: pendingVIESCheckRepo{st.customer, st},

		Metrics:         metricRepo{st.catalog},
		Plans:           planRepo{st.catalog},
		Charges:         chargeRepo{st.catalog},
		FixedCharges:    fixedChargeRepo{st.catalog},
		AddOns:          addOnRepo{st.catalog},
		PricingUnits:    pricingUnitRepo{st.catalog},
		Taxes:           taxRepo{st.catalogPart2, st},
		Commitments:     commitmentRepo{st.catalogPart2},
		UsageThresholds: usageThresholdRepo{st.catalogPart2},
		Features:        featureRepo{st.catalogPart2},
		Entitlements:    entitlementRepo{st.catalogPart2},

		Coupons:        couponRepo{st.coupon},
		AppliedCoupons: appliedCouponRepo{st.coupon, st.customer},

		Subscriptions:          subscriptionRepo{st.subscription, st},
		Contracts:              contractRepo{st},
		ContractRateCards:      contractRateCardRepo{st},
		BillingSegments:        billingSegmentRepo{st},
		ActivationRules:        activationRuleRepo{st.subscription},
		FixedChargeEvents:      fixedChargeEventRepo{st.subscription},
		AppliedUsageThresholds: appliedUsageThresholdRepo{st.subscription},
		LifetimeUsages:         lifetimeUsageRepo{st.subscription},
		DailyUsages:            dailyUsageRepo{st.subscription},

		FixedChargeUnitsOverrides: unitsOverrideRepo{st.subscription},

		Events:             eventLogRepo{d: st.event, c: st.catalog},
		CachedAggregations: cachedAggregationRepo{st.event},

		Invoices:              invoiceRepo{st.invoice, st},
		InvoiceSubscriptions:  invoiceSubscriptionRepo{st.invoice},
		Fees:                  feeRepo{st.invoice, st},
		InvoiceAppliedTaxes:   invoiceAppliedTaxRepo{st.invoice},
		AdjustedFees:          adjustedFeeRepo{st.invoice},
		InvoiceSettlements:    invoiceSettlementRepo{st.invoice},
		InvoiceCustomSections: invoiceCustomSectionRepo{st.invoice},
		ErrorDetails:          errorDetailRepo{st.invoice},
		Credits:               invoiceCreditRepo{st.invoice},

		AppliedInvoiceCustomSections: appliedCustomSectionRepo{st.invoice},

		CreditNotes:            creditNoteRepo{st.creditNote, st},
		CreditNoteErrorDetails: creditNoteErrorDetailRepo{st.creditNote},

		Wallets:                   walletRepo{st.wallet},
		WalletTransactions:        walletTransactionRepo{st.wallet},
		RecurringTransactionRules: recurringRuleRepo{st.wallet, st},

		Payments:                 paymentRepo{st.payment, st},
		PaymentReceipts:          paymentReceiptRepo{st.payment, st},
		Refunds:                  refundRepo{st.payment},
		PaymentRequests:          paymentRequestRepo{st.payment, st},
		PaymentMethods:           paymentMethodRepo{st.payment},
		PaymentProviderCustomers: providerCustomerRepo{st.payment},
		PaymentIntents:           paymentIntentRepo{st.payment},
		InboundWebhooks:          inboundWebhookRepo{st.payment},

		DunningCampaigns: dunningCampaignRepo{st.dunning},

		Integrations:                  integrationRepo{st.integration, st},
		IntegrationCustomers:          integrationCustomerRepo{st.integration},
		IntegrationMappings:           integrationMappingRepo{st.integration},
		IntegrationCollectionMappings: integrationCollectionMappingRepo{st.integration},
		IntegrationItems:              integrationItemRepo{st.integration},
		IntegrationResources:          integrationResourceRepo{st.integration},
		ObjectConnections:             objectConnectionRepo{st.integration},

		WebhookEndpoints:  webhookEndpointRepo{st.webhook},
		WebhookDeliveries: webhookDeliveryRepo{st.webhook},
		DomainEvents:      domainEventRepo{st.webhook},

		Alerts:          alertRepo{st.alert},
		TriggeredAlerts: triggeredAlertRepo{st.alert},

		DataExports:     dataExportRepo{st.export},
		DataExportParts: dataExportPartRepo{st.export},
		RecordDeletions: recordDeletionRepo{st.export},
		Analytics:       analyticsRepo{},

		Quotes:        quoteRepo{st.quote, st},
		QuoteVersions: quoteVersionRepo{st.quote},
		OrderForms:    orderFormRepo{st.quote},
		Orders:        orderRepo{st.quote},

		UsageAttributionTypes:  attributionTypeRepo{st.attribution},
		UsageAttributionValues: attributionValueRepo{st.attribution},

		ProductCategories: productCategoryRepo{st.productCatalog},
		Products:          productRepo{st.productCatalog},
		ProductFilters:    productFilterRepo{st.productCatalog, st},
		RateCards:         rateCardRepo{st.productCatalog},
		Rates:             rateRepo{st.productCatalog},
		PlanRateCards:     planRateCardRepo{st.productCatalog},

		X402Connections: x402ConnectionRepo{st.x402},
		X402Settlements: x402SettlementRepo{st.x402},

		IdempotencyKeys: idempotencyRepo{st.idempotency},

		SecurityLogs: securityLogRepo{st.auditLog},
		APILogs:      apiLogRepo{st.auditLog},
		ActivityLogs: activityLogRepo{st.auditLog},

		Locks:     storeLocker{},
		Sequences: storeSequences{tx},
		Jobs:      storeJobs{tx},
	}
}

// storeLocker grants every lock at once: transactions are already
// serialized by the store's mutex.
type storeLocker struct{}

func (storeLocker) Lock(_ context.Context, org core.OrganizationID, k port.LockKey) error {
	if org.IsZero() || k.Scope == "" {
		return fmt.Errorf("lock %s: %w", k, core.ErrInvalidValue)
	}
	return nil
}

type storeSequences struct{ tx *storeTx }

func (s storeSequences) Next(_ context.Context, org core.OrganizationID, k port.SequenceKey) (int64, error) {
	if org.IsZero() || k.Name == "" {
		return 0, fmt.Errorf("sequence %s: %w", k, core.ErrInvalidValue)
	}
	key := storeSequenceKey{org, k}
	s.tx.st.sequences[key]++
	return s.tx.st.sequences[key], nil
}

type storeJobs struct{ tx *storeTx }

func (j storeJobs) Enqueue(_ context.Context, jobs ...port.Job) error {
	for _, job := range jobs {
		if job.Kind == "" || !job.Queue.Valid() || job.OrganizationID.IsZero() {
			return fmt.Errorf("enqueue %q: %w", job.Kind, core.ErrInvalidValue)
		}
		same := func(p port.Job) bool {
			return p.OrganizationID == job.OrganizationID && p.Kind == job.Kind && p.UniqueKey == job.UniqueKey
		}
		if job.UniqueKey != "" && (slices.ContainsFunc(j.tx.jobs, same) || slices.ContainsFunc(j.tx.committed, same)) {
			continue
		}
		job.Args = slices.Clone(job.Args)
		j.tx.jobs = append(j.tx.jobs, job)
	}
	return nil
}
