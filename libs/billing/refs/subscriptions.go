package refs

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// SubscriptionRef references a subscription by ID or by external ID:
// exactly one is set.
type SubscriptionRef struct {
	// ID is the subscription's ID.
	ID core.SubscriptionID
	// ExternalID is the subscription's external ID.
	ExternalID string
}

// String returns the ID, or "external_id:<external ID>".
func (r SubscriptionRef) String() string {
	if r.ExternalID != "" {
		return "external_id:" + r.ExternalID
	}
	return r.ID.String()
}

// Subscription resolves r in org: by ID any subscription, by external ID
// the active one (lago Api::V1::Subscriptions controllers, find_subscription
// with the default status active). A missing subscription, or a zero r,
// fails with core.NotFound("subscription").
func Subscription(ctx context.Context, r port.Repos, org core.OrganizationID, ref SubscriptionRef) (*subscription.Subscription, error) {
	var (
		s   *subscription.Subscription
		err error
	)
	switch {
	case ref.ExternalID != "":
		s, err = r.Subscriptions.GetByExternalID(ctx, org, ref.ExternalID)
		if err == nil && s.Status != subscription.StatusActive {
			err = core.ErrNotFound
		}
	case !ref.ID.IsZero():
		s, err = r.Subscriptions.Get(ctx, org, ref.ID)
	default:
		err = core.ErrNotFound
	}
	if errors.Is(err, core.ErrNotFound) {
		return nil, core.NotFound("subscription")
	}
	if err != nil {
		return nil, fmt.Errorf("find subscription %s: %w", ref, err)
	}
	return s, nil
}
