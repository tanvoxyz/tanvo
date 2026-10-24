package kernel

import (
	"context"
	"fmt"
	"reflect"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
)

// LogActivity records the activity log of a in r, the repositories of the
// service's transaction, so that the log commits with the change and an
// error rolls the change back (lago Utils::ActivityLog.produce and the
// activity_loggable declarations of the services,
// app/services/base_service.rb:151). It records nothing without
// [Deps.ActivityLogs].
func (d *Deps) LogActivity(ctx context.Context, r port.Repos, a auditlog.Activity) error {
	if d.ActivityLogs == nil {
		return nil
	}
	return d.ActivityLogs.Produce(ctx, r.ActivityLogs, a)
}

// Change is a resource change to log with [Deps.LogChange]: the domain
// values before and after it, which [auditlog.ObjectOf] turns into the
// log's object.
type Change struct {
	// Type is the kind of change.
	Type auditlog.ActivityType
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// ResourceType and ResourceID identify the resource the log is
	// anchored on (lago Utils::ActivityLog#resource: a payment's payable,
	// an applied coupon's coupon, a quote version's quote).
	ResourceType auditlog.ResourceType
	// ResourceID is the anchor's ID.
	ResourceID uuid.UUID
	// Object is the logged record after the change (for a deletion, the
	// record deleted).
	Object any
	// Before is the logged record before an update; nil otherwise, or for
	// an update logged without its changes.
	Before any
	// ExternalCustomerID is the external ID of the record's customer (lago
	// external_customer_id: empty for metrics, coupons, plans, billing
	// entities and features).
	ExternalCustomerID string
	// CustomerID is the record's customer, whose external ID is loaded
	// when ExternalCustomerID is empty.
	CustomerID core.CustomerID
	// ExternalSubscriptionID is the external ID of a logged subscription.
	ExternalSubscriptionID string
}

// LogChange records the activity log of c in r, like [Deps.LogActivity].
//
// source: app/services/utils/activity_log.rb:52-73
func (d *Deps) LogChange(ctx context.Context, r port.Repos, c Change) error {
	if d.ActivityLogs == nil {
		return nil
	}
	a := auditlog.Activity{
		Type: c.Type, OrganizationID: c.OrganizationID, ResourceType: c.ResourceType, ResourceID: c.ResourceID,
		ExternalCustomerID: c.ExternalCustomerID, ExternalSubscriptionID: c.ExternalSubscriptionID,
	}
	if a.ExternalCustomerID == "" && !c.CustomerID.IsZero() {
		cust, err := r.Customers.Get(ctx, c.OrganizationID, c.CustomerID)
		if err != nil {
			return fmt.Errorf("log %s: load customer %s: %w", c.Type, c.CustomerID, err)
		}
		a.ExternalCustomerID = cust.ExternalID
	}
	var err error
	if a.Object, err = auditlog.ObjectOf(c.Object); err != nil {
		return fmt.Errorf("log %s: %w", c.Type, err)
	}
	switch {
	case !isNil(c.Before):
		if a.Before, err = auditlog.ObjectOf(c.Before); err != nil {
			return fmt.Errorf("log %s: %w", c.Type, err)
		}
	case c.Type.IsUpdate():
		// An update logged without its prior state has no changes (lago
		// produce without a block, activity_log.rb:52-56).
		a.Before = a.Object
	}
	return d.LogActivity(ctx, r, a)
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
		return rv.IsNil()
	}
	return false
}

// LogInvoice records the activity log t of inv, anchored on inv (lago
// Utils::ActivityLog.produce(invoice, t)).
func (d *Deps) LogInvoice(ctx context.Context, r port.Repos, t auditlog.ActivityType, inv *invoice.Invoice) error {
	return d.LogChange(ctx, r, Change{
		Type: t, OrganizationID: inv.OrganizationID, ResourceType: auditlog.ResourceInvoice, ResourceID: inv.ID.UUID(),
		Object: inv, CustomerID: inv.CustomerID,
	})
}

// LogWalletTransaction records the activity log t of wallet transaction
// tx, whose attributes before an update are before. As in lago
// (app/services/utils/activity_log.rb:185-186), a transaction's log is on
// its wallet, w when given, and names the wallet's customer.
func (d *Deps) LogWalletTransaction(ctx context.Context, r port.Repos, t auditlog.ActivityType, before map[string]any, tx *wallet.Transaction, w *wallet.Wallet) error {
	if d.ActivityLogs == nil {
		return nil
	}
	if w == nil {
		var err error
		if w, err = r.Wallets.Get(ctx, tx.OrganizationID, tx.WalletID); err != nil {
			return fmt.Errorf("load wallet %s: %w", tx.WalletID, err)
		}
	}
	return d.LogChange(ctx, r, Change{
		Type: t, OrganizationID: tx.OrganizationID, ResourceType: auditlog.ResourceWallet, ResourceID: w.ID.UUID(),
		Object: tx, Before: before, CustomerID: w.CustomerID,
	})
}

// LogWallet records the activity log t of wallet w, whose attributes
// before an update are before.
func (d *Deps) LogWallet(ctx context.Context, r port.Repos, t auditlog.ActivityType, before map[string]any, w *wallet.Wallet) error {
	return d.LogChange(ctx, r, Change{
		Type: t, OrganizationID: w.OrganizationID, ResourceType: auditlog.ResourceWallet, ResourceID: w.ID.UUID(),
		Object: w, Before: before, CustomerID: w.CustomerID,
	})
}

// LogSubscription records the activity log t of subscription sub, whose
// attributes before an update are before; the log names the
// subscription's customer and external ID.
func (d *Deps) LogSubscription(ctx context.Context, r port.Repos, t auditlog.ActivityType, before map[string]any, sub *subscription.Subscription) error {
	return d.LogChange(ctx, r, Change{
		Type: t, OrganizationID: sub.OrganizationID, ResourceType: auditlog.ResourceSubscription, ResourceID: sub.ID.UUID(),
		Object: sub, Before: before, CustomerID: sub.CustomerID, ExternalSubscriptionID: sub.ExternalID,
	})
}
