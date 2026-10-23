package auditlog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// ActivityType is the kind of a resource change (lago
// Clickhouse::ActivityLog::ACTIVITY_TYPES,
// app/models/clickhouse/activity_log.rb).
type ActivityType string

// ActivityType values.
const (
	ActivityBillableMetricCreated          ActivityType = "billable_metric.created"
	ActivityBillableMetricUpdated          ActivityType = "billable_metric.updated"
	ActivityBillableMetricDeleted          ActivityType = "billable_metric.deleted"
	ActivityPlanCreated                    ActivityType = "plan.created"
	ActivityPlanUpdated                    ActivityType = "plan.updated"
	ActivityPlanDeleted                    ActivityType = "plan.deleted"
	ActivityCustomerCreated                ActivityType = "customer.created"
	ActivityCustomerUpdated                ActivityType = "customer.updated"
	ActivityCustomerDeleted                ActivityType = "customer.deleted"
	ActivityInvoiceDrafted                 ActivityType = "invoice.drafted"
	ActivityInvoiceReadyToFinalize         ActivityType = "invoice.ready_to_finalize"
	ActivityInvoiceFailed                  ActivityType = "invoice.failed"
	ActivityInvoiceCreated                 ActivityType = "invoice.created"
	ActivityInvoiceOneOffCreated           ActivityType = "invoice.one_off_created"
	ActivityInvoicePaidCreditAdded         ActivityType = "invoice.paid_credit_added"
	ActivityInvoiceGenerated               ActivityType = "invoice.generated"
	ActivityInvoicePaymentStatusUpdated    ActivityType = "invoice.payment_status_updated"
	ActivityInvoicePaymentOverdue          ActivityType = "invoice.payment_overdue"
	ActivityInvoiceVoided                  ActivityType = "invoice.voided"
	ActivityInvoiceDeleted                 ActivityType = "invoice.deleted"
	ActivityInvoiceRegenerated             ActivityType = "invoice.regenerated"
	ActivityInvoicePaymentFailure          ActivityType = "invoice.payment_failure"
	ActivityPaymentReceiptCreated          ActivityType = "payment_receipt.created"
	ActivityPaymentReceiptGenerated        ActivityType = "payment_receipt.generated"
	ActivityCreditNoteCreated              ActivityType = "credit_note.created"
	ActivityCreditNoteGenerated            ActivityType = "credit_note.generated"
	ActivityCreditNoteRefundFailure        ActivityType = "credit_note.refund_failure"
	ActivityBillingEntityCreated           ActivityType = "billing_entities.created"
	ActivityBillingEntityUpdated           ActivityType = "billing_entities.updated"
	ActivityBillingEntityDeleted           ActivityType = "billing_entities.deleted"
	ActivitySubscriptionCanceled           ActivityType = "subscription.canceled"
	ActivitySubscriptionIncomplete         ActivityType = "subscription.incomplete"
	ActivitySubscriptionStarted            ActivityType = "subscription.started"
	ActivitySubscriptionTerminated         ActivityType = "subscription.terminated"
	ActivitySubscriptionUpdated            ActivityType = "subscription.updated"
	ActivityWalletCreated                  ActivityType = "wallet.created"
	ActivityWalletUpdated                  ActivityType = "wallet.updated"
	ActivityWalletTransactionPaymentFailed ActivityType = "wallet_transaction.payment_failure"
	ActivityWalletTransactionCreated       ActivityType = "wallet_transaction.created"
	ActivityWalletTransactionUpdated       ActivityType = "wallet_transaction.updated"
	ActivityPaymentRecorded                ActivityType = "payment.recorded"
	ActivityCouponCreated                  ActivityType = "coupon.created"
	ActivityCouponUpdated                  ActivityType = "coupon.updated"
	ActivityCouponDeleted                  ActivityType = "coupon.deleted"
	ActivityAppliedCouponCreated           ActivityType = "applied_coupon.created"
	ActivityAppliedCouponDeleted           ActivityType = "applied_coupon.deleted"
	ActivityPaymentRequestCreated          ActivityType = "payment_request.created"
	ActivityEmailSent                      ActivityType = "email.sent"
	ActivityFeatureCreated                 ActivityType = "feature.created"
	ActivityFeatureDeleted                 ActivityType = "feature.deleted"
	ActivityFeatureUpdated                 ActivityType = "feature.updated"
	ActivityProductCategoryCreated         ActivityType = "product_category.created"
	ActivityProductCategoryUpdated         ActivityType = "product_category.updated"
	ActivityProductCategoryDeleted         ActivityType = "product_category.deleted"
	ActivityProductCreated                 ActivityType = "product.created"
	ActivityProductUpdated                 ActivityType = "product.updated"
	ActivityProductDeleted                 ActivityType = "product.deleted"
	ActivityProductFilterCreated           ActivityType = "product_filter.created"
	ActivityProductFilterUpdated           ActivityType = "product_filter.updated"
	ActivityProductFilterDeleted           ActivityType = "product_filter.deleted"
	ActivityRateCardCreated                ActivityType = "rate_card.created"
	ActivityRateCardUpdated                ActivityType = "rate_card.updated"
	ActivityRateCardDeleted                ActivityType = "rate_card.deleted"
	ActivityQuoteCreated                   ActivityType = "quote.created"
	ActivityQuoteUpdated                   ActivityType = "quote.updated"
	ActivityQuoteApproved                  ActivityType = "quote.approved"
	ActivityQuoteVoided                    ActivityType = "quote.voided"
	ActivityQuoteVersionCreated            ActivityType = "quote.version_created"
	ActivityOrderFormCreated               ActivityType = "order_form.created"
	ActivityOrderFormSigned                ActivityType = "order_form.signed"
	ActivityOrderFormFileUploaded          ActivityType = "order_form.file_uploaded"
	ActivityOrderFormExpired               ActivityType = "order_form.expired"
	ActivityOrderFormVoided                ActivityType = "order_form.voided"
	ActivityOrderCreated                   ActivityType = "order.created"
	ActivityOrderExecuted                  ActivityType = "order.executed"
)

// ActivityTypes returns every [ActivityType], in declaration order.
func ActivityTypes() []ActivityType {
	return []ActivityType{
		ActivityBillableMetricCreated, ActivityBillableMetricUpdated, ActivityBillableMetricDeleted,
		ActivityPlanCreated, ActivityPlanUpdated, ActivityPlanDeleted,
		ActivityCustomerCreated, ActivityCustomerUpdated, ActivityCustomerDeleted,
		ActivityInvoiceDrafted, ActivityInvoiceReadyToFinalize, ActivityInvoiceFailed, ActivityInvoiceCreated,
		ActivityInvoiceOneOffCreated, ActivityInvoicePaidCreditAdded, ActivityInvoiceGenerated,
		ActivityInvoicePaymentStatusUpdated, ActivityInvoicePaymentOverdue, ActivityInvoiceVoided,
		ActivityInvoiceDeleted, ActivityInvoiceRegenerated, ActivityInvoicePaymentFailure,
		ActivityPaymentReceiptCreated, ActivityPaymentReceiptGenerated,
		ActivityCreditNoteCreated, ActivityCreditNoteGenerated, ActivityCreditNoteRefundFailure,
		ActivityBillingEntityCreated, ActivityBillingEntityUpdated, ActivityBillingEntityDeleted,
		ActivitySubscriptionCanceled, ActivitySubscriptionIncomplete, ActivitySubscriptionStarted,
		ActivitySubscriptionTerminated, ActivitySubscriptionUpdated,
		ActivityWalletCreated, ActivityWalletUpdated,
		ActivityWalletTransactionPaymentFailed, ActivityWalletTransactionCreated, ActivityWalletTransactionUpdated,
		ActivityPaymentRecorded,
		ActivityCouponCreated, ActivityCouponUpdated, ActivityCouponDeleted,
		ActivityAppliedCouponCreated, ActivityAppliedCouponDeleted,
		ActivityPaymentRequestCreated, ActivityEmailSent,
		ActivityFeatureCreated, ActivityFeatureDeleted, ActivityFeatureUpdated,
		ActivityProductCategoryCreated, ActivityProductCategoryUpdated, ActivityProductCategoryDeleted,
		ActivityProductCreated, ActivityProductUpdated, ActivityProductDeleted,
		ActivityProductFilterCreated, ActivityProductFilterUpdated, ActivityProductFilterDeleted,
		ActivityRateCardCreated, ActivityRateCardUpdated, ActivityRateCardDeleted,
		ActivityQuoteCreated, ActivityQuoteUpdated, ActivityQuoteApproved, ActivityQuoteVoided, ActivityQuoteVersionCreated,
		ActivityOrderFormCreated, ActivityOrderFormSigned, ActivityOrderFormFileUploaded,
		ActivityOrderFormExpired, ActivityOrderFormVoided,
		ActivityOrderCreated, ActivityOrderExecuted,
	}
}

// Valid reports whether t is a declared type.
func (t ActivityType) Valid() bool { return slices.Contains(ActivityTypes(), t) }

// IsUpdate reports whether t records attribute changes: lago keeps the
// changes of the types containing "updated" only (activity_log.rb
// object_changes).
func (t ActivityType) IsUpdate() bool { return strings.Contains(string(t), "updated") }

// ResourceType is the kind of resource an activity log is anchored on
// (lago Clickhouse::ActivityLog::RESOURCE_TYPES; Tanvo uses its keys).
type ResourceType string

// ResourceType values. A wallet transaction's activity is anchored on its
// wallet, an applied coupon's on its coupon, a payment's on what it pays
// and a quote version's on its quote (lago activity_log.rb resource).
const (
	ResourceBillableMetric  ResourceType = "billable_metric"
	ResourcePlan            ResourceType = "plan"
	ResourceCatalogPlan     ResourceType = "catalog_plan"
	ResourceCustomer        ResourceType = "customer"
	ResourceInvoice         ResourceType = "invoice"
	ResourceCreditNote      ResourceType = "credit_note"
	ResourceBillingEntity   ResourceType = "billing_entity"
	ResourceSubscription    ResourceType = "subscription"
	ResourceWallet          ResourceType = "wallet"
	ResourceCoupon          ResourceType = "coupon"
	ResourcePaymentReceipt  ResourceType = "payment_receipt"
	ResourcePaymentRequest  ResourceType = "payment_request"
	ResourceFeature         ResourceType = "feature"
	ResourceProduct         ResourceType = "product"
	ResourceProductCategory ResourceType = "product_category"
	ResourceProductFilter   ResourceType = "product_filter"
	ResourceRateCard        ResourceType = "rate_card"
	ResourceQuote           ResourceType = "quote"
	ResourceOrderForm       ResourceType = "order_form"
	ResourceOrder           ResourceType = "order"
)

// ResourceTypes returns every [ResourceType], in declaration order.
func ResourceTypes() []ResourceType {
	return []ResourceType{
		ResourceBillableMetric, ResourcePlan, ResourceCatalogPlan, ResourceCustomer, ResourceInvoice,
		ResourceCreditNote, ResourceBillingEntity, ResourceSubscription, ResourceWallet, ResourceCoupon,
		ResourcePaymentReceipt, ResourcePaymentRequest, ResourceFeature, ResourceProduct, ResourceProductCategory,
		ResourceProductFilter, ResourceRateCard, ResourceQuote, ResourceOrderForm, ResourceOrder,
	}
}

// Valid reports whether r is a declared resource type.
func (r ResourceType) Valid() bool { return slices.Contains(ResourceTypes(), r) }

// Actor is who performs an activity (lago CurrentContext: the source, the
// API key and the membership's user).
type Actor struct {
	// Source is where the activity originated; empty means system.
	Source ActivitySource
	// UserID is the acting user; zero for API keys and jobs.
	UserID core.UserID
	// APIKeyID is the acting API key; zero for users and jobs.
	APIKeyID core.APIKeyID
}

// Activity is a resource change to log.
type Activity struct {
	// Type is the kind of change.
	Type ActivityType
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// ResourceType and ResourceID identify the resource the log is
	// anchored on.
	ResourceType ResourceType
	// ResourceID is the resource's ID.
	ResourceID uuid.UUID
	// Object is the resource after the change, from [ObjectOf]; for a
	// deletion, the resource deleted.
	Object map[string]any
	// Before is the resource before an update, from [ObjectOf]; the
	// changed attributes of an update type are its differences with
	// Object. It is ignored for the other types.
	Before map[string]any
	// ExternalCustomerID is the external ID of the related customer, if
	// any.
	ExternalCustomerID string
	// ExternalSubscriptionID is the external ID of the related
	// subscription, if any.
	ExternalSubscriptionID string
}

// ActivityLogger records activity logs inside the caller's transaction,
// so that a log exists exactly when its change commits (lago
// Utils::ActivityLog.produce, which produces to Kafka after commit).
type ActivityLogger interface {
	// Produce stores the log of a in repo, the repository of the caller's
	// transaction.
	Produce(ctx context.Context, repo ActivityLogRepository, a Activity) error
}

// ActivityProducer is the [ActivityLogger] of the services. The actor of
// each log comes from the request context.
type ActivityProducer struct {
	clock core.Clock
	ids   core.IDGenerator
	actor func(context.Context) Actor
}

var _ ActivityLogger = (*ActivityProducer)(nil)

// NewActivityProducer returns a producer stamping logs with clock and ids.
// actor gives the actor of a context; nil, or a zero source, is the
// system (background jobs).
func NewActivityProducer(clock core.Clock, ids core.IDGenerator, actor func(context.Context) Actor) *ActivityProducer {
	return &ActivityProducer{clock: clock, ids: ids, actor: actor}
}

// ignoredChanges are the attributes left out of the changes (lago
// activity_log.rb IGNORED_FIELDS).
var ignoredChanges = []string{"updated_at"}

// Produce stores the log of a in repo.
func (p *ActivityProducer) Produce(ctx context.Context, repo ActivityLogRepository, a Activity) error {
	if !a.Type.Valid() {
		return fmt.Errorf("auditlog: activity type %q: %w", a.Type, core.ErrInvalidValue)
	}
	if !a.ResourceType.Valid() {
		return fmt.Errorf("auditlog: resource type %q: %w", a.ResourceType, core.ErrInvalidValue)
	}
	var who Actor
	if p.actor != nil {
		who = p.actor(ctx)
	}
	if who.Source == "" {
		who = Actor{Source: ActivitySourceSystem}
	}
	changes := map[string]any{}
	if a.Type.IsUpdate() {
		changes = Changes(a.Before, a.Object)
	}
	now := p.clock.Now().UTC()
	l := &ActivityLog{
		ID: p.ids.NewUUID(), OrganizationID: a.OrganizationID, UserID: who.UserID, APIKeyID: who.APIKeyID,
		Type: string(a.Type), Source: who.Source, Object: a.Object, ObjectChanges: changes,
		ResourceID: a.ResourceID, ResourceType: string(a.ResourceType),
		ExternalCustomerID: a.ExternalCustomerID, ExternalSubscriptionID: a.ExternalSubscriptionID,
		LoggedAt: now, CreatedAt: now,
	}
	if err := repo.Create(ctx, l); err != nil {
		return fmt.Errorf("auditlog: produce %s: %w", a.Type, err)
	}
	return nil
}

// Changes maps each top-level attribute whose value differs between
// before and after to its [before, after] pair, except updated_at (lago
// activity_log.rb produce and object_changes). An attribute only after
// has is new, with a nil before value.
func Changes(before, after map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range slices.Sorted(maps.Keys(before)) {
		if slices.Contains(ignoredChanges, k) {
			continue
		}
		if b, a := before[k], after[k]; !reflect.DeepEqual(b, a) {
			out[k] = []any{b, a}
		}
	}
	for k, a := range after {
		if _, ok := before[k]; !ok && !slices.Contains(ignoredChanges, k) {
			out[k] = []any{nil, a}
		}
	}
	return out
}

// ObjectOf returns the attributes of v, a domain value, as the JSON object
// of an activity log: its JSON encoding with the field names in
// snake_case (CreatedAt becomes created_at, APIKeyID api_key_id) and the
// numbers kept exact.
func ObjectOf(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("auditlog: object: %w", err)
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var raw any
	if err := d.Decode(&raw); err != nil {
		return nil, fmt.Errorf("auditlog: object: %w", err)
	}
	m, ok := snakeKeys(raw).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("auditlog: object of %T is not a JSON object: %w", v, core.ErrInvalidValue)
	}
	return m, nil
}

// snakeKeys renames the object keys of v to snake_case, recursively.
func snakeKeys(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[snake(k)] = snakeKeys(e)
		}
		return out
	case []any:
		for i, e := range x {
			x[i] = snakeKeys(e)
		}
		return x
	default:
		return v
	}
}

// snake converts a Go field name to snake_case, keeping initialisms
// together (APIKeyID is api_key_id, URL is url).
func snake(s string) string {
	r := []rune(s)
	var b strings.Builder
	for i, c := range r {
		if unicode.IsUpper(c) {
			prevLower := i > 0 && (unicode.IsLower(r[i-1]) || unicode.IsDigit(r[i-1]))
			nextLower := i > 0 && i+1 < len(r) && unicode.IsUpper(r[i-1]) && unicode.IsLower(r[i+1])
			if prevLower || nextLower {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(c))
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}
