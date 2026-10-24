package integrations

import (
	"context"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// Triggers returns an emitter that records events through next and, in
// the same transaction, enqueues the accounting and CRM syncs the event
// starts. Lago enqueues them next to the webhooks it sends at the same
// places: invoice.created, one_off_created and paid_credit_added
// (Invoices::CreateJob if should_sync_invoice?, Hubspot::CreateJob if
// should_sync_hubspot_invoice?), invoice.voided, payment_dispute_lost and
// payment_status_updated (Hubspot::UpdateJob if
// should_update_hubspot_invoice?), credit_note.created
// (CreditNotes::CreateJob if should_sync_credit_note?), subscription.started
// (Subscriptions::Hubspot::CreateJob), and subscription.updated,
// trial_ended and terminated (Subscriptions::Hubspot::UpdateJob).
func (s *Service) Triggers(next port.WebhookEmitter) port.WebhookEmitter {
	return &triggers{s: s, next: next}
}

type triggers struct {
	s    *Service
	next port.WebhookEmitter
}

// Emit implements [port.WebhookEmitter].
func (t *triggers) Emit(ctx context.Context, e webhook.Event) error {
	return t.s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := t.next.Emit(ctx, e); err != nil {
			return err
		}
		return t.s.trigger(ctx, r, e)
	})
}

func (s *Service) trigger(ctx context.Context, r port.Repos, e webhook.Event) error {
	org := e.OrganizationID
	switch e.Type {
	case webhook.EventTypeInvoiceCreated, webhook.EventTypeInvoiceOneOffCreated, webhook.EventTypeInvoicePaidCreditAdded:
		inv, c, err := invoiceOf(ctx, r, e)
		if err != nil || inv.SelfBilled || inv.Status != invoice.StatusFinalized {
			return err
		}
		if on, err := syncs(ctx, r, c, integration.CategoryAccounting, func(i *integration.Integration) bool { return i.SyncInvoices }); err != nil {
			return err
		} else if on {
			if err := kernel.Enqueue(ctx, r, org, kernel.JobSyncInvoice, kernel.SyncInvoiceArgs{InvoiceID: inv.ID}, "invoice:"+inv.ID.String()+":accounting", time.Time{}); err != nil {
				return err
			}
		}
		return enqueueHubspot(ctx, r, c, func(i *integration.Integration) bool { return i.SyncInvoices },
			kernel.JobCreateCRMInvoice, kernel.InvoiceArgs{InvoiceID: inv.ID}, "invoice:"+inv.ID.String()+":crm")
	case webhook.EventTypeInvoiceVoided, webhook.EventTypeInvoicePaymentDisputeLost, webhook.EventTypeInvoicePaymentStatusUpdated:
		inv, c, err := invoiceOf(ctx, r, e)
		if err != nil || inv.SelfBilled {
			return err
		}
		return enqueueHubspot(ctx, r, c, func(i *integration.Integration) bool { return i.SyncInvoices },
			kernel.JobUpdateCRMInvoice, kernel.InvoiceArgs{InvoiceID: inv.ID}, "")
	case webhook.EventTypeCreditNoteCreated:
		id, err := core.ParseID[core.CreditNoteID](e.ObjectID.String())
		if err != nil {
			return fmt.Errorf("credit note of %s: %w", e.Type, err)
		}
		cn, err := r.CreditNotes.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load credit note %s: %w", id, err)
		}
		if cn.Status != creditnote.StatusFinalized {
			return nil
		}
		c, err := r.Customers.Get(ctx, org, cn.CustomerID)
		if err != nil {
			return fmt.Errorf("load customer %s: %w", cn.CustomerID, err)
		}
		on, err := syncs(ctx, r, c, integration.CategoryAccounting, func(i *integration.Integration) bool { return i.SyncCreditNotes })
		if err != nil || !on {
			return err
		}
		return kernel.Enqueue(ctx, r, org, kernel.JobSyncCreditNote, kernel.CreditNoteArgs{CreditNoteID: id}, "credit_note:"+id.String()+":accounting", time.Time{})
	case webhook.EventTypeSubscriptionStarted, webhook.EventTypeSubscriptionUpdated, webhook.EventTypeSubscriptionTrialEnded, webhook.EventTypeSubscriptionTerminated:
		id, err := core.ParseID[core.SubscriptionID](e.ObjectID.String())
		if err != nil {
			return fmt.Errorf("subscription of %s: %w", e.Type, err)
		}
		sub, err := r.Subscriptions.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load subscription %s: %w", id, err)
		}
		c, err := r.Customers.Get(ctx, org, sub.CustomerID)
		if err != nil {
			return fmt.Errorf("load customer %s: %w", sub.CustomerID, err)
		}
		kind, key := kernel.JobUpdateCRMSubscription, ""
		if e.Type == webhook.EventTypeSubscriptionStarted {
			kind, key = kernel.JobCreateCRMSubscription, "subscription:"+id.String()+":crm"
		}
		return enqueueHubspot(ctx, r, c, func(i *integration.Integration) bool { return i.SyncSubscriptions },
			kind, kernel.SubscriptionArgs{SubscriptionID: id}, key)
	}
	return nil
}

func invoiceOf(ctx context.Context, r port.Repos, e webhook.Event) (*invoice.Invoice, *customer.Customer, error) {
	id, err := core.ParseID[core.InvoiceID](e.ObjectID.String())
	if err != nil {
		return nil, nil, fmt.Errorf("invoice of %s: %w", e.Type, err)
	}
	inv, err := r.Invoices.Get(ctx, e.OrganizationID, id)
	if err != nil {
		return nil, nil, fmt.Errorf("load invoice %s: %w", id, err)
	}
	c, err := r.Customers.Get(ctx, e.OrganizationID, inv.CustomerID)
	if err != nil {
		return nil, nil, fmt.Errorf("load customer %s: %w", inv.CustomerID, err)
	}
	return inv, c, nil
}

// enqueueHubspot enqueues kind when one of c's HubSpot links satisfies on.
func enqueueHubspot(ctx context.Context, r port.Repos, c *customer.Customer, on func(*integration.Integration) bool, kind string, args any, key string) error {
	ics, err := r.IntegrationCustomers.ListByCustomer(ctx, c.OrganizationID, c.ID, integration.CategoryCRM)
	if err != nil {
		return fmt.Errorf("load CRM links of customer %s: %w", c.ID, err)
	}
	for _, ic := range ics {
		i, err := r.Integrations.Get(ctx, c.OrganizationID, ic.IntegrationID)
		if err != nil {
			return fmt.Errorf("load integration %s: %w", ic.IntegrationID, err)
		}
		if i.Provider == integration.ProviderHubspot && on(i) {
			return kernel.Enqueue(ctx, r, c.OrganizationID, kind, args, key, time.Time{})
		}
	}
	return nil
}
