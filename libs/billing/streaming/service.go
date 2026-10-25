package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	sd "github.com/tanvoxyz/tanvo/libs/core/streaming"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
)

// JobDeliverEvent delivers one event type for one customer (lago
// DeliverEventJob); its args are [DeliverEventArgs]. It runs on
// [port.QueueStreaming], once: a failed delivery is not retried
// (app/jobs/application_job.rb retry: 0).
const JobDeliverEvent = "streaming.deliver_event"

// JobKinds returns the job kinds [Service.HandleJob] runs.
func JobKinds() []string { return []string{JobDeliverEvent} }

// DeliverEventArgs are the args of [JobDeliverEvent].
type DeliverEventArgs struct {
	// EventType is the delivered event type.
	EventType sd.EventType `json:"event_type"`
	// CustomerID is the customer.
	CustomerID core.CustomerID `json:"customer_id"`
}

// versionLayout is Ruby's Time#iso8601(6) of a UTC instant.
const versionLayout = "2006-01-02T15:04:05.000000Z07:00"

// Service delivers the usage-refreshed events. It implements
// [wallets.Streamer].
type Service struct {
	d        *kernel.Deps
	usage    *usage.Service
	producer port.StreamProducer
	log      sd.Logger
}

var _ wallets.Streamer = (*Service)(nil)

// New returns the service over the checked dependencies d, computing the
// usage with u and writing the records with p.
func New(d *kernel.Deps, u *usage.Service, p port.StreamProducer) *Service {
	return &Service{d: d, usage: u, producer: p, log: sd.NewLogger(d.Logger)}
}

// WalletsRefreshed implements [wallets.Streamer] (lago
// Customers::RefreshWalletsService#deliver_streaming_events,
// app/services/customers/refresh_wallets_service.rb:46-54): every event
// type an active destination of the organization claims is delivered for
// customer c. The current usage is delivered from the usage the refresh
// computed once the transaction commits when the producer holds
// credentials for its destination; the full usage, and the current usage
// otherwise, are enqueued as [JobDeliverEvent] in the transaction.
func (s *Service) WalletsRefreshed(ctx context.Context, r port.Repos, org core.OrganizationID, c core.CustomerID, computed wallets.ComputedUsage) (func(context.Context), error) {
	ds, err := r.StreamingDestinations.ListActive(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("list streaming destinations: %w", err)
	}
	// Read once, as lago's destinations_by_event_type: one destination
	// claims each type.
	byType := map[sd.EventType]*sd.Destination{}
	for _, d := range ds {
		for _, t := range d.EventTypes {
			byType[t] = d
		}
	}
	var (
		inline     *sd.Destination
		superseded []sd.EventType
	)
	for _, t := range sd.EventTypes() {
		d, ok := byType[t]
		if !ok {
			continue
		}
		if t == sd.EventCustomerUsageRefreshed && s.producer.CredentialsAvailable(d) {
			inline = d
			continue
		}
		taken, err := s.enqueue(ctx, r, org, c, t)
		if err != nil {
			return nil, err
		}
		if !taken {
			superseded = append(superseded, t)
		}
	}
	if inline == nil && len(superseded) == 0 {
		return nil, nil
	}
	usages, _ := computed.([]*usage.Usage)
	return func(ctx context.Context) {
		for _, t := range superseded {
			s.superseded(ctx, t, c)
		}
		if inline != nil {
			s.produceCurrentUsage(ctx, org, c, inline, usages)
		}
	}, nil
}

// produceCurrentUsage delivers the current usage of customer c from the
// usages a refresh computed; it hands the delivery to a job when it
// failed, or when the producer lost the credentials of d meanwhile.
//
// source: app/services/customers/refresh_wallets_service.rb:76-94
func (s *Service) produceCurrentUsage(ctx context.Context, org core.OrganizationID, c core.CustomerID, d *sd.Destination, usages []*usage.Usage) {
	t := sd.EventCustomerUsageRefreshed
	err := s.Deliver(ctx, org, c, t, usages)
	if err == nil && s.producer.CredentialsAvailable(d) {
		return
	}
	if err != nil {
		s.log.Emit(ctx, sd.OutcomeFailed, nil, slog.String("event_type", t.String()), slog.String("customer_id", c.String()),
			slog.String("error", errorKind(err)), slog.String("message", err.Error()))
	}
	if err := s.EnqueueDelivery(ctx, org, c, t); err != nil {
		s.d.Logger.ErrorContext(ctx, "streaming: enqueue delivery", "event_type", t.String(), "customer_id", c.String(), "error", err)
	}
}

// EnqueueDelivery enqueues, in a transaction of its own, the
// [JobDeliverEvent] of t for customer c.
func (s *Service) EnqueueDelivery(ctx context.Context, org core.OrganizationID, c core.CustomerID, t sd.EventType) error {
	taken := false
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		taken, err = s.enqueue(ctx, r, org, c, t)
		return err
	})
	if err != nil {
		return err
	}
	if !taken {
		s.superseded(ctx, t, c)
	}
	return nil
}

// enqueue enqueues the [JobDeliverEvent] of t for customer c in the
// transaction of r unless one is waiting to start, and reports whether
// it did (lago unique :until_and_while_executing,
// app/jobs/deliver_event_job.rb:15).
func (s *Service) enqueue(ctx context.Context, r port.Repos, org core.OrganizationID, c core.CustomerID, t sd.EventType) (bool, error) {
	now, err := kernel.QueueNow(ctx, r, s.d.Now())
	if err != nil {
		return false, err
	}
	taken, err := r.StreamingDeliveryLocks.ClaimEnqueue(ctx, org, c, t, now, now.Add(sd.LockTTL))
	if err != nil {
		return false, fmt.Errorf("claim the delivery of %s for customer %s: %w", t, c, err)
	}
	if !taken {
		return false, nil
	}
	raw, err := json.Marshal(DeliverEventArgs{EventType: t, CustomerID: c})
	if err != nil {
		return false, fmt.Errorf("encode %s args: %w", JobDeliverEvent, err)
	}
	job := port.Job{Kind: JobDeliverEvent, Queue: port.QueueStreaming, OrganizationID: org, Args: raw, MaxAttempts: 1}
	if err := r.Jobs.Enqueue(ctx, job); err != nil {
		return false, fmt.Errorf("enqueue %s: %w", JobDeliverEvent, err)
	}
	return true, nil
}

// superseded logs a delivery of t for customer c dropped because another
// one is waiting or running.
//
// source: app/jobs/deliver_event_job.rb:6-13
func (s *Service) superseded(ctx context.Context, t sd.EventType, c core.CustomerID) {
	s.log.Emit(ctx, sd.OutcomeSuperseded, nil, slog.String("event_type", t.String()), slog.String("customer_id", c.String()),
		slog.String("lock_key", sd.LockKey(t, c)))
}

// HandleJob runs [JobDeliverEvent] and reports whether it knew j's kind.
// A delivery starting while another of the same type and customer runs is
// dropped and logged as superseded; an unknown event type fails (lago
// EVENT_SERVICES.fetch).
func (s *Service) HandleJob(ctx context.Context, j port.Job) (bool, error) {
	if j.Kind != JobDeliverEvent {
		return false, nil
	}
	var a DeliverEventArgs
	if err := json.Unmarshal(j.Args, &a); err != nil {
		return true, fmt.Errorf("%s: decode args: %w", j.Kind, err)
	}
	if !a.EventType.Valid() {
		return true, fmt.Errorf("%s: event type %q: %w", j.Kind, a.EventType, core.ErrInvalidValue)
	}
	org := j.OrganizationID
	taken := false
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		now, err := kernel.QueueNow(ctx, r, s.d.Now())
		if err != nil {
			return err
		}
		if taken, err = r.StreamingDeliveryLocks.ClaimRun(ctx, org, a.CustomerID, a.EventType, now, now.Add(sd.LockTTL)); err != nil {
			return fmt.Errorf("claim the delivery of %s for customer %s: %w", a.EventType, a.CustomerID, err)
		}
		return nil
	})
	if err != nil {
		return true, err
	}
	if !taken {
		s.superseded(ctx, a.EventType, a.CustomerID)
		return true, nil
	}
	defer func() {
		// A fresh context: the lock is released even when ctx ended.
		rctx := context.WithoutCancel(ctx)
		if err := s.d.Tx.InTx(rctx, func(ctx context.Context, r port.Repos) error {
			return r.StreamingDeliveryLocks.ReleaseRun(ctx, org, a.CustomerID, a.EventType)
		}); err != nil {
			s.d.Logger.ErrorContext(rctx, "streaming: release delivery lock", "lock_key", sd.LockKey(a.EventType, a.CustomerID), "error", err)
		}
	}()
	return true, s.Deliver(ctx, org, a.CustomerID, a.EventType, nil)
}

// Deliver writes a record of event type t for every active subscription
// of customer c whose plan the destination claiming t streams it for (lago
// EventDestinations::CustomerUsage::RefreshedService and
// CustomerFullUsage::RefreshedService). The current usage of a
// subscription is taken from given when there (the usage a refresh
// computed), else computed without taxes; the full usage is computed
// since the subscription started over the charges of its plan. Without a
// destination nothing is delivered.
//
// A subscription whose usage cannot be computed (no longer active, full
// usage not allowed, no charge) is logged as skipped; any other failure
// of a subscription is logged as failed and the others are delivered.
func (s *Service) Deliver(ctx context.Context, org core.OrganizationID, c core.CustomerID, t sd.EventType, given []*usage.Usage) error {
	if !t.Valid() {
		return fmt.Errorf("streaming: event type %q: %w", t, core.ErrInvalidValue)
	}
	var (
		d    *sd.Destination
		cust *customer.Customer
		subs []*subscription.Subscription
		ws   []*wallet.Wallet
	)
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		ds, err := r.StreamingDestinations.ForEvent(ctx, org, t)
		if err != nil {
			return fmt.Errorf("list destinations of %s: %w", t, err)
		}
		if len(ds) == 0 {
			return nil
		}
		d = ds[0]
		if cust, err = r.Customers.Get(ctx, org, c); err != nil {
			return fmt.Errorf("load customer %s: %w", c, err)
		}
		if subs, err = r.Subscriptions.ListByCustomer(ctx, org, c, subscription.StatusActive); err != nil {
			return fmt.Errorf("list active subscriptions of customer %s: %w", c, err)
		}
		if ws, err = r.Wallets.ListActiveForApplication(ctx, org, c); err != nil {
			return fmt.Errorf("list wallets of customer %s: %w", c, err)
		}
		return nil
	})
	if err != nil || d == nil {
		return err
	}
	version := s.d.Now().Format(versionLayout)
	for _, sub := range subs {
		log := func(o sd.Outcome, attrs ...slog.Attr) {
			attrs = append([]slog.Attr{
				slog.String("event_type", t.String()), slog.String("customer_id", c.String()),
				slog.String("subscription_id", sub.ID.String()),
			}, attrs...)
			s.log.Emit(ctx, o, d, attrs...)
		}
		data, err := s.record(ctx, d, cust, sub, t, given, ws, version)
		if err == nil && data != nil {
			err = s.produce(ctx, d, cust, data)
		}
		switch {
		case err == nil:
		case errors.Is(err, errSkipped):
			log(sd.OutcomeSkipped, slog.String("error", errorKind(err)), slog.String("message", err.Error()))
		default:
			log(sd.OutcomeFailed, slog.String("error", errorKind(err)), slog.String("message", err.Error()))
		}
	}
	return nil
}

// errSkipped marks a usage that could not be computed (lago's failed
// usage result).
var errSkipped = errors.New("usage not computed")

// record returns the record of t for sub, or nil when the destination
// does not stream t for sub's plan.
func (s *Service) record(ctx context.Context, d *sd.Destination, cust *customer.Customer, sub *subscription.Subscription, t sd.EventType,
	given []*usage.Usage, ws []*wallet.Wallet, version string,
) ([]byte, error) {
	var data []byte
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		plan, err := r.Plans.Get(ctx, sub.OrganizationID, sub.PlanID)
		if err != nil {
			return fmt.Errorf("load plan of subscription %s: %w", sub.ID, err)
		}
		if !slices.Contains(d.EventTypesFor(plan.Code), t) {
			return nil
		}
		u, err := s.subscriptionUsage(ctx, r, sub, t, given)
		if err != nil {
			return err
		}
		charges, err := chargeInfos(ctx, r, sub.OrganizationID, chargeIDs(u))
		if err != nil {
			return err
		}
		env := Envelope{
			EventID:                s.d.IDs.NewUUID().String(),
			EventType:              t.String(),
			ObjectType:             ObjectType,
			OrganizationID:         cust.OrganizationID.String(),
			CustomerExternalID:     cust.ExternalID,
			SubscriptionExternalID: sub.ExternalID,
			Version:                version,
			CustomerUsage:          SerializeUsage(u, ws, charges),
		}
		if data, err = json.Marshal(env); err != nil {
			return fmt.Errorf("encode record: %w", err)
		}
		return nil
	})
	return data, err
}

// subscriptionUsage returns the usage of sub that t streams.
func (s *Service) subscriptionUsage(ctx context.Context, r port.Repos, sub *subscription.Subscription, t sd.EventType, given []*usage.Usage) (*usage.Usage, error) {
	opts := usage.Options{SkipTaxes: true}
	if t == sd.EventCustomerUsageRefreshed {
		if i := slices.IndexFunc(given, func(u *usage.Usage) bool { return u.Subscription.ID == sub.ID }); i >= 0 {
			return given[i], nil
		}
		// The source of the inline delivery of the wallet refresh, so that
		// the record does not depend on where it ran.
		opts = usage.WalletRefreshOptions()
		opts.SkipTaxes = true
	} else {
		cs, err := r.Charges.ListByPlan(ctx, sub.OrganizationID, sub.PlanID)
		if err != nil {
			return nil, fmt.Errorf("load charges of plan %s: %w", sub.PlanID, err)
		}
		opts.FullUsage = true
		// An empty list is no charge filter, so the full usage is not
		// allowed (lago filter_by_charge_id: [] is not present).
		for _, ch := range cs {
			opts.ChargeIDs = append(opts.ChargeIDs, ch.ID)
		}
	}
	u, err := s.usage.CurrentUsage(ctx, r, sub.OrganizationID, sub.ID, opts)
	if errors.Is(err, core.ErrNotAllowed) || errors.Is(err, core.ErrNotFound) {
		return nil, fmt.Errorf("%w: %w", errSkipped, err)
	}
	return u, err
}

// chargeInfos loads the charges ids and their metrics.
func chargeInfos(ctx context.Context, r port.Repos, org core.OrganizationID, ids []core.ChargeID) (map[core.ChargeID]ChargeInfo, error) {
	out := make(map[core.ChargeID]ChargeInfo, len(ids))
	for _, id := range ids {
		ch, err := r.Charges.Get(ctx, org, id)
		if err != nil {
			return nil, fmt.Errorf("load charge %s: %w", id, err)
		}
		m, err := r.Metrics.GetWithDiscarded(ctx, org, ch.BillableMetricID)
		if err != nil {
			return nil, fmt.Errorf("load metric %s: %w", ch.BillableMetricID, err)
		}
		out[id] = ChargeInfo{Code: ch.Code, MetricID: m.ID, MetricCode: m.Code}
	}
	return out, nil
}

// produce writes data to d under the customer's partition key.
func (s *Service) produce(ctx context.Context, d *sd.Destination, cust *customer.Customer, data []byte) error {
	key, err := d.PartitionKeyFor(cust.ExternalID)
	if err != nil {
		return err
	}
	if err := s.producer.Produce(ctx, d, data, key); err != nil {
		return fmt.Errorf("produce: %w", err)
	}
	return nil
}

// errorKind names the kind of err in the delivery logs, as lago logs the
// error class.
func errorKind(err error) string {
	for _, k := range []struct {
		err  error
		name string
	}{
		{core.ErrNotFound, "not_found"},
		{core.ErrNotAllowed, "not_allowed"},
		{core.ErrInvalidValue, "invalid_value"},
		{core.ErrConflict, "conflict"},
		{core.ErrInvalidTransition, "invalid_transition"},
		{context.Canceled, "canceled"},
		{context.DeadlineExceeded, "deadline_exceeded"},
	} {
		if errors.Is(err, k.err) {
			return k.name
		}
	}
	return "internal"
}
