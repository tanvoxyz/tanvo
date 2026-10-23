package subscriptions

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// LifetimeView is a lifetime usage with what its API representation
// shows.
type LifetimeView struct {
	// LifetimeUsage is the lifetime usage.
	LifetimeUsage *subscription.LifetimeUsage
	// Subscription is its subscription.
	Subscription *subscription.Subscription
	// Thresholds is the progress toward the usage thresholds; empty when
	// the subscription has no progressive billing.
	Thresholds []usage.ThresholdProgress
}

// LifetimeUsage returns the lifetime usage of a subscription; an external
// ID selects the active subscription.
//
// source: app/controllers/api/v1/subscriptions/lifetime_usages_controller.rb:7, app/serializers/v1/lifetime_usage_serializer.rb
func (s *Service) LifetimeUsage(ctx context.Context, org core.OrganizationID, ref SubscriptionRef) (*LifetimeView, error) {
	var out *LifetimeView
	err := s.read(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = s.lifetime(ctx, r, org, ref)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("get lifetime usage: %w", err)
	}
	return out, nil
}

// UpdateLifetimeUsage sets the usage of a subscription billed outside
// Tanvo, a decimal amount in the lifetime usage's currency (lago
// LifetimeUsages::UpdateService); an amount finer than the currency's
// minor unit is invalid (field external_historical_usage_amount).
//
// source: app/controllers/api/v1/subscriptions/lifetime_usages_controller.rb:15, app/services/lifetime_usages/update_service.rb
func (s *Service) UpdateLifetimeUsage(ctx context.Context, org core.OrganizationID, ref SubscriptionRef, amount money.Decimal) (*LifetimeView, error) {
	var out *LifetimeView
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		lv, err := s.lifetime(ctx, r, org, ref)
		if err != nil {
			return err
		}
		lu := lv.LifetimeUsage
		minor, err := lu.Currency.ToMinor(amount, money.HalfUp)
		var v core.Validator
		v.Check(err == nil && minor >= 0 && amount.Mul(lu.Currency.SubunitToUnit()).IsInteger(), "external_historical_usage_amount", core.CodeInvalid)
		if err := v.Err(); err != nil {
			return err
		}
		if _, err := s.usage.UpdateHistoricalUsage(ctx, r, org, lu.ExternalSubscriptionID, minor); err != nil {
			return err
		}
		out, err = s.lifetime(ctx, r, org, SubscriptionRef{ID: lv.Subscription.ID})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("update lifetime usage: %w", err)
	}
	return out, nil
}

// lifetime loads the lifetime usage view of the subscription ref
// designates (lago Api::V1::Subscriptions::BaseController#find_subscription).
func (s *Service) lifetime(ctx context.Context, r port.Repos, org core.OrganizationID, ref SubscriptionRef) (*LifetimeView, error) {
	var (
		sub *subscription.Subscription
		err error
	)
	if ref.ExternalID != "" {
		sub, err = findSubscriptionIn(ctx, r, org, ref, subscription.StatusActive)
	} else {
		sub, err = findSubscription(ctx, r, org, ref)
	}
	if err != nil {
		return nil, err
	}
	lu, err := r.LifetimeUsages.GetBySubscription(ctx, org, sub.ID)
	if errors.Is(err, core.ErrNotFound) {
		return nil, core.NotFound(ResourceLifetimeUsage)
	}
	if err != nil {
		return nil, fmt.Errorf("load lifetime usage of %s: %w", sub.ID, err)
	}
	ps, err := s.usage.ThresholdsCompletion(ctx, r, sub, lu)
	if err != nil {
		return nil, err
	}
	return &LifetimeView{LifetimeUsage: lu, Subscription: sub, Thresholds: ps}, nil
}
