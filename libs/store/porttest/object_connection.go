package porttest

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func testObjectConnection(t *testing.T, h Harness) {
	t.Run("billing object connections", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		p, other := subscriptionFixtures(e)
		org := p.org
		sub := p.newSub(e, "sub_conn")
		w := wallet.New(org, core.NewID[core.WalletID](e.ids), p.customer.ID, money.MustCurrency("EUR"), money.MustParse("1"), now)
		rule := &wallet.RecurringTransactionRule{
			ID: core.NewID[core.RecurringTransactionRuleID](e.ids), OrganizationID: org, WalletID: w.ID,
			Trigger: wallet.RecurringTriggerThreshold, ThresholdCredits: ptr(money.MustParse("10")),
			Method: wallet.RecurringMethodFixed, PaidCredits: money.MustParse("20"), Status: wallet.RuleStatusActive,
			CreatedAt: now, UpdatedAt: now,
		}
		stripe := integrationFixture(e, org, "stripe", integration.ProviderStripe)
		ns := integrationFixture(e, org, "netsuite", integration.ProviderNetsuite)
		integrationFixtureCreate(e, stripe, ns)
		pc := &payment.ProviderCustomer{
			ID: core.NewID[core.PaymentProviderCustomerID](e.ids), OrganizationID: org,
			CustomerID: p.customer.ID, IntegrationID: stripe.ID, IsDefault: true, CreatedAt: now, UpdatedAt: now,
		}
		ic := &integration.IntegrationCustomer{
			ID: core.NewID[core.IntegrationCustomerID](e.ids), OrganizationID: org, IntegrationID: ns.ID,
			CustomerID: p.customer.ID, Category: integration.CategoryAccounting, ExternalCustomerID: "ns_1",
			IsDefault: true, CreatedAt: now, UpdatedAt: now,
		}
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Subscriptions.Create()", r.Subscriptions.Create(ctx, sub))
			noErr(t, "Wallets.Create()", r.Wallets.Create(ctx, w))
			noErr(t, "RecurringTransactionRules.Create()", r.RecurringTransactionRules.Create(ctx, rule))
			noErr(t, "PaymentProviderCustomers.Create()", r.PaymentProviderCustomers.Create(ctx, pc))
			noErr(t, "IntegrationCustomers.Create()", r.IntegrationCustomers.Create(ctx, ic))
			return nil
		})
		mk := func(owner integration.ObjectOwner, cat integration.Category, b integration.ConnectionBehavior) *integration.ObjectConnection {
			c := &integration.ObjectConnection{
				ID: core.NewID[core.ObjectConnectionID](e.ids), OrganizationID: org, Owner: owner,
				Category: cat, Behavior: b, CreatedAt: now, UpdatedAt: now,
			}
			if b == integration.ConnectionBehaviorSpecific && cat == integration.CategoryPayment {
				c.PaymentProviderCustomerID = pc.ID
			} else if b == integration.ConnectionBehaviorSpecific {
				c.IntegrationCustomerID = ic.ID
			}
			return c
		}
		subOwner := integration.ObjectOwner{SubscriptionID: sub.ID}
		walletOwner := integration.ObjectOwner{WalletID: w.ID}
		ruleOwner := integration.ObjectOwner{RecurringTransactionRuleID: rule.ID}
		subPay := mk(subOwner, integration.CategoryPayment, integration.ConnectionBehaviorSpecific)
		subTax := mk(subOwner, integration.CategoryTax, integration.ConnectionBehaviorSkip)
		walletAcc := mk(walletOwner, integration.CategoryAccounting, integration.ConnectionBehaviorSpecific)
		rulePay := mk(ruleOwner, integration.CategoryPayment, integration.ConnectionBehaviorSpecific)
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, c := range []*integration.ObjectConnection{subPay, subTax, walletAcc, rulePay} {
				noErr(t, "Create()", r.ObjectConnections.Create(ctx, c))
			}
			wantErr(t, "Create() same ID", r.ObjectConnections.Create(ctx, subPay), core.ErrConflict)
			dup := mk(subOwner, integration.CategoryPayment, integration.ConnectionBehaviorSkip)
			wantErr(t, "Create() second of the category", r.ObjectConnections.Create(ctx, dup), core.ErrConflict)

			list, err := r.ObjectConnections.ListByOwner(ctx, org, subOwner)
			noErr(t, "ListByOwner()", err)
			equal(t, "ListByOwner()", list, []*integration.ObjectConnection{subPay, subTax})
			list, err = r.ObjectConnections.ListByOwner(ctx, org, walletOwner)
			noErr(t, "ListByOwner() wallet", err)
			equal(t, "ListByOwner() wallet", list, []*integration.ObjectConnection{walletAcc})
			list, err = r.ObjectConnections.ListByOwner(ctx, other.org, subOwner)
			noErr(t, "ListByOwner() other org", err)
			equal(t, "ListByOwner() other org", len(list), 0)

			subPay.Behavior, subPay.PaymentProviderCustomerID, subPay.UpdatedAt = integration.ConnectionBehaviorSkip, core.PaymentProviderCustomerID{}, at(time.Minute)
			noErr(t, "Update()", r.ObjectConnections.Update(ctx, subPay))
			moved := *subTax
			moved.Category = integration.CategoryPayment
			wantErr(t, "Update() onto a taken category", r.ObjectConnections.Update(ctx, &moved), core.ErrConflict)
			otherOrg := *subTax
			otherOrg.OrganizationID = other.org
			wantErr(t, "Update() other org", r.ObjectConnections.Update(ctx, &otherOrg), core.ErrNotFound)

			noErr(t, "Delete()", r.ObjectConnections.Delete(ctx, org, subTax.ID))
			wantErr(t, "Delete() again", r.ObjectConnections.Delete(ctx, org, subTax.ID), core.ErrNotFound)
			wantErr(t, "Delete() other org", r.ObjectConnections.Delete(ctx, other.org, subPay.ID), core.ErrNotFound)
			list, err = r.ObjectConnections.ListByOwner(ctx, org, subOwner)
			noErr(t, "ListByOwner() after Update() and Delete()", err)
			equal(t, "ListByOwner() after Update() and Delete()", list, []*integration.ObjectConnection{subPay})

			// The choices of a payment connection go with it.
			noErr(t, "DeleteByPaymentProviderCustomer()", r.ObjectConnections.DeleteByPaymentProviderCustomer(ctx, org, pc.ID))
			list, err = r.ObjectConnections.ListByOwner(ctx, org, ruleOwner)
			noErr(t, "ListByOwner() rule", err)
			equal(t, "ListByOwner() rule", len(list), 0)

			// An integration customer and a rule take their choices with them.
			rulePay2 := mk(ruleOwner, integration.CategoryTax, integration.ConnectionBehaviorSkip)
			noErr(t, "Create() rule", r.ObjectConnections.Create(ctx, rulePay2))
			noErr(t, "RecurringTransactionRules.Delete()", r.RecurringTransactionRules.Delete(ctx, org, rule.ID))
			list, err = r.ObjectConnections.ListByOwner(ctx, org, ruleOwner)
			noErr(t, "ListByOwner() deleted rule", err)
			equal(t, "ListByOwner() deleted rule", len(list), 0)
			noErr(t, "IntegrationCustomers.Delete()", r.IntegrationCustomers.Delete(ctx, org, ic.ID))
			list, err = r.ObjectConnections.ListByOwner(ctx, org, walletOwner)
			noErr(t, "ListByOwner() deleted integration customer", err)
			equal(t, "ListByOwner() deleted integration customer", len(list), 0)
			return nil
		})
	})
}
