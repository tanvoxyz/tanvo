package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/libs/billing"
	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/jobs"
	"github.com/tanvoxyz/tanvo/libs/webhooks"
	"github.com/tanvoxyz/tanvo/providers/anrok"
	"github.com/tanvoxyz/tanvo/providers/avalara"
	"github.com/tanvoxyz/tanvo/providers/hubspot"
	"github.com/tanvoxyz/tanvo/providers/netsuite"
	"github.com/tanvoxyz/tanvo/providers/salesforce"
	"github.com/tanvoxyz/tanvo/providers/xero"
)

// integrationsWiring is the integrations service over the tax,
// accounting and CRM adapters, and the tax provider of the billing
// engine.
type integrationsWiring struct {
	svc   *integrations.Service
	taxes *integrations.Taxes
	// docs renders the documents sent to NetSuite once [integrationsWiring.bind]
	// gives it the billing engine's services.
	docs *rpc.Documents
	// slots bounds the concurrent sync jobs across the deployment; nil
	// leaves them unbounded.
	slots slotAcquirer
}

// slotAcquirer takes one of a bounded number of named slots shared by
// every server ([store.Store.AcquireSlot]).
type slotAcquirer interface {
	AcquireSlot(ctx context.Context, name string, limit int) (release func(), ok bool, err error)
}

// throttledSnooze is how long a sync job waits for a free slot, as
// sidekiq-throttled 1.4.0 pushes a throttled job back to its queue,
// fetched again after its cooldown period of one second.
const throttledSnooze = time.Second

// newIntegrations returns the integrations service over the adapters of
// Anrok, Avalara, NetSuite, Xero, HubSpot and Salesforce, and GoCardless'
// OAuth connection (gocardlessOAuth), with the credentials sealed with
// the installation secret. Its sync triggers are bound to triggers, the
// webhook emitter of the billing and payments services.
func newIntegrations(cfg config.Config, tx port.Transactor, clock core.Clock, log *slog.Logger, tp trace.TracerProvider, mp metric.MeterProvider,
	gocardlessOAuth integration.OAuthGateway, triggers *syncTriggers, blobs port.BlobStore,
) (*integrationsWiring, error) {
	// The OAuth adapters store their refreshed grants through the
	// service, made after them.
	store := &connectionStore{}
	set, err := port.NewIntegrationProviderSet(
		anrok.New(anrok.WithTracerProvider(tp), anrok.WithMeterProvider(mp), anrok.WithClock(clock.Now)),
		avalara.New(avalara.WithTracerProvider(tp), avalara.WithMeterProvider(mp)),
		netsuite.New(netsuite.WithTracerProvider(tp), netsuite.WithMeterProvider(mp)),
		xero.New(xero.WithTracerProvider(tp), xero.WithMeterProvider(mp), xero.WithClock(clock.Now), xero.WithConnectionStore(store),
			xero.WithOAuthClient(xero.OAuthClient{ClientID: cfg.XeroClientID, ClientSecret: cfg.XeroClientSecret})),
		hubspot.New(hubspot.WithTracerProvider(tp), hubspot.WithMeterProvider(mp), hubspot.WithClock(clock.Now), hubspot.WithConnectionStore(store),
			hubspot.WithOAuthClient(hubspot.OAuthClient{ClientID: cfg.HubSpotClientID, ClientSecret: cfg.HubSpotClientSecret})),
		salesforce.New(),
		gocardlessOAuth,
	)
	if err != nil {
		return nil, fmt.Errorf("integrations: %w", err)
	}
	var box integrations.Sealer
	secret, err := cfg.SecretKeyBytes()
	if err != nil {
		return nil, fmt.Errorf("integrations: %w", err)
	}
	if secret != nil {
		b, err := NewSecretBox(secret)
		if err != nil {
			return nil, fmt.Errorf("integrations: %w", err)
		}
		box = b
	}
	deps := &kernel.Deps{
		Tx: tx, Clock: clock, IDs: core.UUIDv7Generator{}, Webhooks: &webhooks.Emitter{Tx: tx}, Logger: log,
		ActivityLogs: auditlog.NewActivityProducer(clock, core.UUIDv7Generator{}, rpc.ActivityActor),
		Transient:    retriedByBillingRetry,
	}
	docs := &rpc.Documents{}
	svc := integrations.New(deps, box, integrations.WithProviders(set), integrations.WithAppURL(cfg.AppURL), integrations.WithDocuments(docs),
		integrations.WithBlobs(blobs))
	store.svc = svc
	triggers.svc = svc
	slots, _ := tx.(slotAcquirer)
	return &integrationsWiring{
		svc:   svc,
		docs:  docs,
		slots: slots,
		taxes: integrations.NewTaxes(svc, integrations.NewDraftTaxCache(cfg.AnrokDraftTaxesCacheTTL, clock.Now)),
	}, nil
}

// bind gives the document renderer the billing engine's invoices and
// credit notes.
func (w *integrationsWiring) bind(e *billing.Engine) {
	w.docs.Invoices, w.docs.CreditNotes = e.Invoices, e.CreditNotes
}

// connectionStore stores the grants the OAuth adapters refresh through
// the integrations service.
type connectionStore struct{ svc *integrations.Service }

// UpdateConnection implements [integration.ConnectionStore].
func (s *connectionStore) UpdateConnection(ctx context.Context, conn integration.Connection, u integration.ConnectionUpdate) error {
	if s.svc == nil {
		return fmt.Errorf("integrations: connection store: %w", core.ErrNotFound)
	}
	return s.svc.UpdateConnection(ctx, conn, u)
}

// syncTriggers is the webhook emitter of the billing and payments
// services: it emits the webhooks and, once [newIntegrations] binds the
// integrations service, queues the provider syncs they start in the same
// transaction.
type syncTriggers struct {
	next port.WebhookEmitter
	svc  *integrations.Service
}

// newSyncTriggers returns the triggers over the webhooks of tx, unbound.
func newSyncTriggers(tx port.Transactor) *syncTriggers {
	return &syncTriggers{next: &webhooks.Emitter{Tx: tx}}
}

// Emit implements [port.WebhookEmitter].
func (t *syncTriggers) Emit(ctx context.Context, e webhook.Event) error {
	if t.svc == nil {
		return t.next.Emit(ctx, e)
	}
	return t.svc.Triggers(t.next).Emit(ctx, e)
}

// register registers the integration job kinds with their retry rules
// (lago app/jobs/integrations/ retry_on declarations), then the rules of
// the transient database failures.
func (w *integrationsWiring) register(reg *jobs.Registry) error {
	for _, k := range integrations.JobKinds() {
		var rules []jobs.RetryRule
		for _, r := range integrations.RetryRules(k) {
			b := jobs.Polynomial(0.15)
			if r.Delay > 0 {
				b = constantDelay(r.Delay)
			}
			rules = append(rules, jobs.RetryRule{Match: r.Match, Attempts: r.Attempts, Backoff: b})
		}
		rules = append(rules, billingRetry()...)
		if err := jobs.Register(reg, jobs.Kind{Name: k, MaxAttempts: 1, Retry: rules}, jobs.HandlerFunc[json.RawMessage](w.run)); err != nil {
			return err
		}
	}
	return nil
}

// constantDelay waits d between attempts.
func constantDelay(d time.Duration) jobs.Backoff { return func(int) time.Duration { return d } }

// run runs an integration job with the service.
func (w *integrationsWiring) run(ctx context.Context, j *jobs.Job[json.RawMessage]) error {
	if w.slots != nil && integrations.ConcurrencyLimited(j.Kind) {
		release, ok, err := w.slots.AcquireSlot(ctx, integrations.ConcurrencySlot, integrations.ConcurrencyLimit)
		if err != nil {
			return err
		}
		if !ok {
			return jobs.Snooze(throttledSnooze)
		}
		defer release()
	}
	args := j.Args
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	ok, err := w.svc.HandleJob(ctx, port.Job{
		Kind: j.Kind, Queue: kernel.QueueOf(j.Kind), OrganizationID: j.OrganizationID, Args: args, UniqueKey: j.UniqueKey,
	}, j.Attempt)
	if !ok {
		return jobs.Permanent(fmt.Errorf("integrations: kind %s: %w", j.Kind, core.ErrNotFound))
	}
	return err
}
