package alerts

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/alert"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Field error codes of the service, named after lago's.
const (
	// CodeAlertAlreadyExists: the subscription or wallet already has an
	// alert of the type (for the billable metric) (field: base).
	CodeAlertAlreadyExists = "alert_already_exists"
	// CodeNoAlerts: a batch holds no alert (field: alerts).
	CodeNoAlerts = "no_alerts"
)

// Resource is the resource name of the not-found errors of alerts.
const Resource = "alert"

// Service manages usage alerts. It uses d.Tx, d.Clock and d.IDs only.
type Service struct {
	d *kernel.Deps
}

// New returns the service over d. It fails when a dependency the service
// uses is nil.
func New(d *kernel.Deps) (*Service, error) {
	switch {
	case d == nil, d.Tx == nil:
		return nil, fmt.Errorf("%w: Tx", kernel.ErrMissingDependency)
	case d.Clock == nil:
		return nil, fmt.Errorf("%w: Clock", kernel.ErrMissingDependency)
	case d.IDs == nil:
		return nil, fmt.Errorf("%w: IDs", kernel.ErrMissingDependency)
	}
	return &Service{d: d}, nil
}

// Owner selects the subscription or the wallet whose alerts an operation
// works on. Exactly one of the fields is set: a subscription by ID (in any
// status), an active subscription by external ID (lago's default
// subscription_status, subscriptions/base_controller.rb), or a wallet.
type Owner struct {
	// SubscriptionID is the subscription's ID.
	SubscriptionID core.SubscriptionID
	// SubscriptionExternalID is the external ID of an active subscription.
	SubscriptionExternalID string
	// WalletID is the wallet's ID.
	WalletID core.WalletID
}

// Ref selects an alert of an owner by ID or, when ID is zero, by code.
type Ref struct {
	// ID is the alert's ID.
	ID core.AlertID
	// Code is the alert's code.
	Code string
}

// MetricRef selects a billable metric by ID or, when ID is zero, by code;
// the zero value selects none.
type MetricRef struct {
	// ID is the metric's ID.
	ID core.BillableMetricID
	// Code is the metric's code.
	Code string
}

// IsZero reports whether m selects no metric.
func (m MetricRef) IsZero() bool { return m.ID.IsZero() && m.Code == "" }

// View is an alert with the subscription it was reached through.
type View struct {
	*alert.Alert
	// SubscriptionID is the subscription of the owner; zero for the alerts
	// of a wallet.
	SubscriptionID core.SubscriptionID
}

// CreateInput is an alert to create.
type CreateInput struct {
	// Type is the alert type; it decides the direction.
	Type alert.Type
	// Code is unique among the alerts of the owner.
	Code string
	// Name is the display name.
	Name *string
	// BillableMetric is the metric of the billable_metric_* types.
	BillableMetric MetricRef
	// Thresholds are the alert's thresholds; their IDs are assigned.
	Thresholds []alert.Threshold
}

// UpdateInput is a change of an alert; unset fields keep their value.
type UpdateInput struct {
	// Code is the new code.
	Code core.Optional[string]
	// Name is the new name.
	Name core.Optional[*string]
	// BillableMetric is the new metric; a zero ref keeps the metric.
	BillableMetric core.Optional[MetricRef]
	// Thresholds replace the thresholds when they are not empty.
	Thresholds core.Optional[[]alert.Threshold]
}

// target is a resolved owner.
type target struct {
	sub    *subscription.Subscription
	wallet *wallet.Wallet
}

func (t target) view(a *alert.Alert) *View {
	v := &View{Alert: a}
	if t.sub != nil {
		v.SubscriptionID = t.sub.ID
	}
	return v
}

// Create creates an alert of the owner and schedules its evaluation. The
// checks follow lago's order: the type (alert_type value_is_mandatory, or
// invalid_type when the type does not suit the owner), the thresholds, the
// billable metric (core.NotFound("billable_metric")), an alert of the same
// type (base alert_already_exists) and the code (value_already_exist). A
// wallet alert, which is decreasing, starts from the wallet's current
// value, read under the customer's prepaid_credits lock.
//
// source: app/services/usage_monitoring/create_alert_service.rb:16-118
func (s *Service) Create(ctx context.Context, org core.OrganizationID, owner Owner, in CreateInput) (*View, error) {
	var out *View
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		t, err := s.resolve(ctx, r, org, owner)
		if err != nil {
			return err
		}
		a, err := s.create(ctx, r, org, t, in)
		if err != nil {
			return err
		}
		if err := s.schedule(ctx, r, org, a, t.sub); err != nil {
			return err
		}
		out = t.view(a)
		return nil
	})
	return out, err
}

// BatchCreate creates the alerts of the owner, all or none. Each alert is
// checked as by [Service.Create] against the alerts before it; the errors
// of every failing alert are returned together, as field errors prefixed
// with alerts[i]. (a missing metric is alerts[i].billable_metric_id
// billable_metric_not_found). An empty batch is alerts no_alerts.
//
// source: app/services/usage_monitoring/alerts/create_batch_service.rb:15-56
func (s *Service) BatchCreate(ctx context.Context, org core.OrganizationID, owner Owner, ins []CreateInput) ([]*View, error) {
	if len(ins) == 0 {
		return nil, fieldError("alerts", CodeNoAlerts)
	}
	var out []*View
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		t, err := s.resolve(ctx, r, org, owner)
		if err != nil {
			return err
		}
		var errs []core.FieldError
		created := make([]*alert.Alert, 0, len(ins))
		for i, in := range ins {
			a, err := s.create(ctx, r, org, t, in)
			prefix := fmt.Sprintf("alerts[%d]", i)
			var ve *core.ValidationError
			var nf *core.NotFoundError
			switch {
			case err == nil:
				created = append(created, a)
			case errors.As(err, &ve):
				for _, fe := range ve.Errors {
					errs = append(errs, core.FieldError{Field: prefix + "." + fe.Field, Code: fe.Code})
				}
			case errors.As(err, &nf):
				errs = append(errs, core.FieldError{Field: prefix + ".billable_metric_id", Code: nf.Error()})
			default:
				return err
			}
		}
		if len(errs) > 0 {
			return &core.ValidationError{Errors: errs}
		}
		for _, a := range created {
			if err := s.schedule(ctx, r, org, a, t.sub); err != nil {
				return err
			}
			out = append(out, t.view(a))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// create checks and inserts one alert of t.
func (s *Service) create(ctx context.Context, r port.Repos, org core.OrganizationID, t target, in CreateInput) (*alert.Alert, error) {
	if in.Type == "" {
		return nil, &core.ValidationError{Errors: []core.FieldError{
			{Field: "alert_type", Code: core.CodeRequired},
			{Field: "alert_type", Code: core.CodeInvalid},
		}}
	}
	if !in.Type.Valid() || in.Type.IsWalletType() != (t.wallet != nil) {
		return nil, fieldError("alert_type", alert.CodeInvalidType)
	}
	now := s.d.Now()
	a := &alert.Alert{
		ID:             core.NewID[core.AlertID](s.d.IDs),
		OrganizationID: org,
		Code:           in.Code,
		Name:           in.Name,
		Type:           in.Type,
		Direction:      in.Type.DefaultDirection(),
		Thresholds:     s.thresholds(in.Thresholds),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if t.wallet != nil {
		a.WalletID = t.wallet.ID
	} else {
		a.SubscriptionExternalID = t.sub.ExternalID
	}
	if err := s.setMetric(ctx, r, a, in.BillableMetric); err != nil {
		return nil, err
	}
	siblings, err := s.siblings(ctx, r, org, a)
	if err != nil {
		return nil, err
	}
	for _, x := range siblings {
		if x.Type == a.Type && x.BillableMetricID == a.BillableMetricID {
			return nil, fieldError("base", CodeAlertAlreadyExists)
		}
	}
	for _, x := range siblings {
		if x.Code == a.Code {
			return nil, fieldError("code", core.CodeAlreadyExists)
		}
	}
	if a.Direction == alert.DirectionDecreasing && t.wallet != nil {
		w, err := lockWallet(ctx, r, org, t.wallet.ID)
		if err != nil {
			return nil, err
		}
		a.PreviousValue, _ = walletValue(a, w)
	}
	if err := r.Alerts.Create(ctx, a); err != nil {
		return nil, codeConflict(err)
	}
	return a, nil
}

// Get returns the alert ref of the owner.
func (s *Service) Get(ctx context.Context, org core.OrganizationID, owner Owner, ref Ref) (*View, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*View, error) {
		t, err := s.resolve(ctx, r, org, owner)
		if err != nil {
			return nil, err
		}
		a, err := find(ctx, r, org, t, ref)
		if err != nil {
			return nil, err
		}
		return t.view(a), nil
	})
}

// List returns a page of the alerts of the owner, oldest first.
//
// source: app/queries/usage_monitoring/alerts_query.rb:8-17
func (s *Service) List(ctx context.Context, org core.OrganizationID, owner Owner, page core.PageRequest) (core.Page[*View], error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (core.Page[*View], error) {
		t, err := s.resolve(ctx, r, org, owner)
		if err != nil {
			return core.Page[*View]{}, err
		}
		var f alert.Filter
		if t.wallet != nil {
			f.WalletID = t.wallet.ID
		} else {
			f.SubscriptionExternalID = t.sub.ExternalID
		}
		p, err := r.Alerts.List(ctx, org, f, page)
		if err != nil {
			return core.Page[*View]{}, fmt.Errorf("list alerts: %w", err)
		}
		out := core.Page[*View]{Items: make([]*View, len(p.Items)), NextToken: p.NextToken}
		for i, a := range p.Items {
			out.Items[i] = t.view(a)
		}
		return out, nil
	})
}

// Update changes the alert ref of the owner under its lock and schedules
// its evaluation. Thresholds beyond the limit are too_many_thresholds;
// thresholds replace the alert's when the list is not empty. A metric that
// does not exist is core.NotFound("billable_metric"); a code another
// alert of the owner uses is code value_already_exist. The type and the
// direction never change.
//
// source: app/services/usage_monitoring/update_alert_service.rb:15-77
func (s *Service) Update(ctx context.Context, org core.OrganizationID, owner Owner, ref Ref, in UpdateInput) (*View, error) {
	var out *View
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		t, err := s.resolve(ctx, r, org, owner)
		if err != nil {
			return err
		}
		a, err := find(ctx, r, org, t, ref)
		if err != nil {
			return err
		}
		if err := kernel.LockAlert(ctx, r, org, a.ID); err != nil {
			return err
		}
		if a, err = r.Alerts.Get(ctx, org, a.ID); err != nil {
			return notFound(err)
		}
		if in.Thresholds.Set && len(in.Thresholds.Value) > alert.MaxThresholds {
			return fieldError("thresholds", alert.CodeTooManyThresholds)
		}
		if in.Thresholds.Set && len(in.Thresholds.Value) > 0 {
			a.Thresholds = s.thresholds(in.Thresholds.Value)
		}
		in.Name.Apply(&a.Name)
		in.Code.Apply(&a.Code)
		if in.BillableMetric.Set && !in.BillableMetric.Value.IsZero() {
			if err := s.setMetric(ctx, r, a, in.BillableMetric.Value); err != nil {
				return err
			}
		} else if err := a.Validate(); err != nil {
			return err
		}
		if in.Code.Set {
			siblings, err := s.siblings(ctx, r, org, a)
			if err != nil {
				return err
			}
			for _, x := range siblings {
				if x.ID != a.ID && x.Code == a.Code {
					return fieldError("code", core.CodeAlreadyExists)
				}
			}
		}
		a.UpdatedAt = s.d.Now()
		if err := r.Alerts.Update(ctx, a); err != nil {
			return codeConflict(err)
		}
		if err := s.schedule(ctx, r, org, a, nil); err != nil {
			return err
		}
		out = t.view(a)
		return nil
	})
	return out, err
}

// Delete deletes the alert ref of the owner under its lock and returns
// it; its triggered alerts are kept.
//
// source: app/services/usage_monitoring/destroy_alert_service.rb:12-22
func (s *Service) Delete(ctx context.Context, org core.OrganizationID, owner Owner, ref Ref) (*View, error) {
	var out *View
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		t, err := s.resolve(ctx, r, org, owner)
		if err != nil {
			return err
		}
		a, err := find(ctx, r, org, t, ref)
		if err != nil {
			return err
		}
		if err := kernel.LockAlert(ctx, r, org, a.ID); err != nil {
			return err
		}
		// The thresholds are deleted and the alert discarded; the
		// response shows it without them (destroy_alert_service.rb:15-20).
		now := s.d.Now()
		if err := r.Alerts.Discard(ctx, org, a.ID, now); err != nil {
			return notFound(err)
		}
		a.Thresholds, a.DeletedAt, a.UpdatedAt = nil, &now, now
		out = t.view(a)
		return nil
	})
	return out, err
}

// DeleteAll deletes every alert of the owner: it takes their locks in ID
// order, then deletes their thresholds and discards them. Triggered alerts
// are kept and no webhook is sent.
//
// source: app/services/usage_monitoring/alerts/destroy_all_service.rb:13-27
func (s *Service) DeleteAll(ctx context.Context, org core.OrganizationID, owner Owner) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		t, err := s.resolve(ctx, r, org, owner)
		if err != nil {
			return err
		}
		var alerts []*alert.Alert
		if t.wallet != nil {
			alerts, err = r.Alerts.ListByWallet(ctx, org, t.wallet.ID)
		} else {
			alerts, err = r.Alerts.ListBySubscription(ctx, org, t.sub.ExternalID)
		}
		if err != nil {
			return fmt.Errorf("load alerts: %w", err)
		}
		// Every lock first, in ID order, or this deadlocks against
		// evaluation and edits (destroy_all_service.rb:19-20).
		slices.SortFunc(alerts, func(a, b *alert.Alert) int { return a.ID.Compare(b.ID) })
		for _, a := range alerts {
			if err := kernel.LockAlert(ctx, r, org, a.ID); err != nil {
				return err
			}
		}
		now := s.d.Now()
		for _, a := range alerts {
			if err := r.Alerts.Discard(ctx, org, a.ID, now); err != nil {
				return fmt.Errorf("discard alert %s: %w", a.ID, err)
			}
		}
		return nil
	})
}

// resolve loads the owner.
func (s *Service) resolve(ctx context.Context, r port.Repos, org core.OrganizationID, o Owner) (target, error) {
	switch {
	case !o.WalletID.IsZero():
		w, err := r.Wallets.Get(ctx, org, o.WalletID)
		if errors.Is(err, core.ErrNotFound) {
			return target{}, core.NotFound("wallet")
		}
		if err != nil {
			return target{}, fmt.Errorf("load wallet %s: %w", o.WalletID, err)
		}
		return target{wallet: w}, nil
	case !o.SubscriptionID.IsZero():
		sub, err := r.Subscriptions.Get(ctx, org, o.SubscriptionID)
		if errors.Is(err, core.ErrNotFound) {
			return target{}, core.NotFound("subscription")
		}
		if err != nil {
			return target{}, fmt.Errorf("load subscription %s: %w", o.SubscriptionID, err)
		}
		return target{sub: sub}, nil
	case o.SubscriptionExternalID != "":
		sub, err := r.Subscriptions.GetByExternalID(ctx, org, o.SubscriptionExternalID)
		if errors.Is(err, core.ErrNotFound) || err == nil && sub.Status != subscription.StatusActive {
			return target{}, core.NotFound("subscription")
		}
		if err != nil {
			return target{}, fmt.Errorf("load subscription %q: %w", o.SubscriptionExternalID, err)
		}
		return target{sub: sub}, nil
	default:
		return target{}, core.NotFound("subscription")
	}
}

// find returns the alert ref of t.
//
// source: app/controllers/api/v1/subscriptions/alerts_controller.rb:122-129
func find(ctx context.Context, r port.Repos, org core.OrganizationID, t target, ref Ref) (*alert.Alert, error) {
	var (
		a   *alert.Alert
		err error
	)
	switch {
	case !ref.ID.IsZero():
		a, err = r.Alerts.Get(ctx, org, ref.ID)
	case t.wallet != nil:
		a, err = r.Alerts.GetByWalletCode(ctx, org, t.wallet.ID, ref.Code)
	default:
		a, err = r.Alerts.GetBySubscriptionCode(ctx, org, t.sub.ExternalID, ref.Code)
	}
	if err != nil {
		return nil, notFound(err)
	}
	if t.wallet != nil && a.WalletID != t.wallet.ID || t.sub != nil && (a.SubscriptionExternalID != t.sub.ExternalID || !a.WalletID.IsZero()) {
		return nil, core.NotFound(Resource)
	}
	return a, nil
}

// setMetric validates a and sets its billable metric from ref, so that
// the alert's errors come before a missing metric, as in lago.
//
// source: app/services/usage_monitoring/concerns/create_or_update_concern.rb:17-27
func (s *Service) setMetric(ctx context.Context, r port.Repos, a *alert.Alert, ref MetricRef) error {
	if !ref.IsZero() {
		// A placeholder stands for the metric while a is validated.
		a.BillableMetricID = ref.ID
		if a.BillableMetricID.IsZero() {
			a.BillableMetricID = core.NewID[core.BillableMetricID](s.d.IDs)
		}
	}
	if err := a.Validate(); err != nil {
		return err
	}
	if ref.IsZero() {
		return nil
	}
	var (
		m   *catalog.BillableMetric
		err error
	)
	if !ref.ID.IsZero() {
		m, err = r.Metrics.Get(ctx, a.OrganizationID, ref.ID)
	} else {
		m, err = r.Metrics.GetByCode(ctx, a.OrganizationID, ref.Code)
	}
	if errors.Is(err, core.ErrNotFound) {
		return core.NotFound("billable_metric")
	}
	if err != nil {
		return fmt.Errorf("load billable metric: %w", err)
	}
	a.BillableMetricID = m.ID
	return nil
}

// siblings returns the alerts of the owner of a.
func (s *Service) siblings(ctx context.Context, r port.Repos, org core.OrganizationID, a *alert.Alert) ([]*alert.Alert, error) {
	var (
		xs  []*alert.Alert
		err error
	)
	if !a.WalletID.IsZero() {
		xs, err = r.Alerts.ListByWallet(ctx, org, a.WalletID)
	} else {
		xs, err = r.Alerts.ListBySubscription(ctx, org, a.SubscriptionExternalID)
	}
	if err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	return xs, nil
}

// thresholds copies in with new IDs.
//
// source: app/services/usage_monitoring/base_service.rb:7-16
func (s *Service) thresholds(in []alert.Threshold) []alert.Threshold {
	out := make([]alert.Threshold, len(in))
	for i, t := range in {
		t.ID = core.NewID[core.AlertThresholdID](s.d.IDs)
		out[i] = t
	}
	return out
}

// schedule enqueues the evaluation of a, as the engine's AlertSaved does:
// the activity of its subscription (sub, or the active subscription with
// its external ID when sub is nil) when it is active, the wallet alert
// processing of its wallet when the wallet is active.
//
// source: app/services/usage_monitoring/create_alert_service.rb:136-151
func (s *Service) schedule(ctx context.Context, r port.Repos, org core.OrganizationID, a *alert.Alert, sub *subscription.Subscription) error {
	if !a.WalletID.IsZero() {
		w, err := r.Wallets.Get(ctx, org, a.WalletID)
		if err != nil {
			return fmt.Errorf("load wallet %s: %w", a.WalletID, err)
		}
		if !w.Active() {
			return nil
		}
		return kernel.EnqueueWalletAlerts(ctx, r, w)
	}
	if sub == nil {
		var err error
		sub, err = r.Subscriptions.GetByExternalID(ctx, org, a.SubscriptionExternalID)
		if errors.Is(err, core.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load subscription %q: %w", a.SubscriptionExternalID, err)
		}
	}
	if sub.Status != subscription.StatusActive {
		return nil
	}
	now, err := kernel.QueueNow(ctx, r, s.d.Now())
	if err != nil {
		return err
	}
	key, at := kernel.ActivityKey(sub.ID, now)
	return kernel.Enqueue(ctx, r, org, kernel.JobProcessSubscriptionActivity, kernel.SubscriptionArgs{SubscriptionID: sub.ID}, key, at)
}

// lockWallet takes the prepaid_credits lock of the wallet's customer and
// returns the wallet read under it (lago alertable.lock!).
func lockWallet(ctx context.Context, r port.Repos, org core.OrganizationID, id core.WalletID) (*wallet.Wallet, error) {
	w, err := r.Wallets.Get(ctx, org, id)
	if err != nil {
		return nil, fmt.Errorf("load wallet %s: %w", id, err)
	}
	if err := kernel.LockCustomer(ctx, r, org, w.CustomerID, customer.LockScopePrepaidCredits); err != nil {
		return nil, err
	}
	if w, err = r.Wallets.Get(ctx, org, id); err != nil {
		return nil, fmt.Errorf("load wallet %s: %w", id, err)
	}
	return w, nil
}

// walletValue is the value of a wallet alert a for wallet w (lago find_value
// of the wallet alerts, app/models/usage_monitoring/wallet_*_alert.rb).
func walletValue(a *alert.Alert, w *wallet.Wallet) (money.Decimal, bool) {
	switch a.Type {
	case alert.TypeWalletBalanceAmount:
		return money.NewFromInt(w.BalanceAmount), true
	case alert.TypeWalletCreditsBalance:
		return w.CreditsBalance, true
	case alert.TypeWalletOngoingBalanceAmount:
		return money.NewFromInt(w.OngoingBalanceAmount), true
	case alert.TypeWalletCreditsOngoingBalance:
		return w.CreditsOngoingBalance, true
	default:
		return money.Decimal{}, false
	}
}

func read[T any](ctx context.Context, d *kernel.Deps, fn func(context.Context, port.Repos) (T, error)) (T, error) {
	var out T
	err := d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		v, err := fn(ctx, r)
		out = v
		return err
	})
	return out, err
}

// notFound turns a missing alert into core.NotFound("alert").
func notFound(err error) error {
	if errors.Is(err, core.ErrNotFound) {
		return core.NotFound(Resource)
	}
	return err
}

// codeConflict turns a uniqueness violation, which a concurrent write can
// still cause after the checks, into code value_already_exist (lago
// duplicate_code_error?); the type of a wallet alert is checked before.
func codeConflict(err error) error {
	if errors.Is(err, core.ErrConflict) {
		if strings.Contains(err.Error(), "alert_type") {
			return fieldError("base", CodeAlertAlreadyExists)
		}
		return fieldError("code", core.CodeAlreadyExists)
	}
	return err
}

func fieldError(field, code string) error {
	return &core.ValidationError{Errors: []core.FieldError{{Field: field, Code: code}}}
}
