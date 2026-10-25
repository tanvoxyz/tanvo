package store

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// objectConnectionRepo implements integration.ObjectConnectionRepository.
type objectConnectionRepo struct{ t *txState }

var _ integration.ObjectConnectionRepository = objectConnectionRepo{}

func objectConnectionParams(c *integration.ObjectConnection) db.CreateBillingObjectConnectionParams {
	return db.CreateBillingObjectConnectionParams{
		OrganizationID:             c.OrganizationID.UUID(),
		ID:                         c.ID.UUID(),
		SubscriptionID:             optUUID(c.Owner.SubscriptionID),
		WalletID:                   optUUID(c.Owner.WalletID),
		RecurringTransactionRuleID: optUUID(c.Owner.RecurringTransactionRuleID),
		Category:                   string(c.Category),
		Behavior:                   string(c.Behavior),
		PaymentProviderCustomerID:  optUUID(c.PaymentProviderCustomerID),
		IntegrationCustomerID:      optUUID(c.IntegrationCustomerID),
		CreatedAt:                  utc(c.CreatedAt),
		UpdatedAt:                  utc(c.UpdatedAt),
	}
}

func objectConnectionOf(r db.BillingObjectConnection) *integration.ObjectConnection {
	return &integration.ObjectConnection{
		ID:             idOf[core.ObjectConnectionID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		Owner: integration.ObjectOwner{
			SubscriptionID:             optIDOf[core.SubscriptionID](r.SubscriptionID),
			WalletID:                   optIDOf[core.WalletID](r.WalletID),
			RecurringTransactionRuleID: optIDOf[core.RecurringTransactionRuleID](r.RecurringTransactionRuleID),
		},
		Category:                  integration.Category(r.Category),
		Behavior:                  integration.ConnectionBehavior(r.Behavior),
		PaymentProviderCustomerID: optIDOf[core.PaymentProviderCustomerID](r.PaymentProviderCustomerID),
		IntegrationCustomerID:     optIDOf[core.IntegrationCustomerID](r.IntegrationCustomerID),
		CreatedAt:                 utc(r.CreatedAt),
		UpdatedAt:                 utc(r.UpdatedAt),
	}
}

func (r objectConnectionRepo) Create(ctx context.Context, c *integration.ObjectConnection) error {
	q, err := r.t.queries(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateBillingObjectConnection(ctx, objectConnectionParams(c))
	return insertErr("billing_object_connection", n, err)
}

func (r objectConnectionRepo) Update(ctx context.Context, c *integration.ObjectConnection) error {
	q, err := r.t.queries(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	// One row per (owner, category).
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateBillingObjectConnection(ctx, db.UpdateBillingObjectConnectionParams(objectConnectionParams(c)))
		return err
	})
	return updateErr("billing_object_connection", c.ID, n, err)
}

func (r objectConnectionRepo) Delete(ctx context.Context, org core.OrganizationID, id core.ObjectConnectionID) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	n, err := q.DeleteBillingObjectConnection(ctx, db.DeleteBillingObjectConnectionParams{OrganizationID: org.UUID(), ID: id.UUID()})
	return updateErr("billing_object_connection", id, n, err)
}

func (r objectConnectionRepo) ListByOwner(ctx context.Context, org core.OrganizationID, owner integration.ObjectOwner) ([]*integration.ObjectConnection, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListBillingObjectConnectionsByOwner(ctx, db.ListBillingObjectConnectionsByOwnerParams{
		OrganizationID:             org.UUID(),
		SubscriptionID:             optUUID(owner.SubscriptionID),
		WalletID:                   optUUID(owner.WalletID),
		RecurringTransactionRuleID: optUUID(owner.RecurringTransactionRuleID),
	})
	if err != nil {
		return nil, fmt.Errorf("list billing object connections: %w", mapErr(err))
	}
	out := make([]*integration.ObjectConnection, len(rows))
	for i, row := range rows {
		out[i] = objectConnectionOf(row)
	}
	return out, nil
}

func (r objectConnectionRepo) DeleteByPaymentProviderCustomer(ctx context.Context, org core.OrganizationID, id core.PaymentProviderCustomerID) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	if err := q.DeleteBillingObjectConnectionsByPaymentProviderCustomer(ctx, db.DeleteBillingObjectConnectionsByPaymentProviderCustomerParams{
		OrganizationID: org.UUID(), PaymentProviderCustomerID: optUUID(id),
	}); err != nil {
		return fmt.Errorf("delete billing object connections of %s: %w", id, mapErr(err))
	}
	return nil
}
