package estimates

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	expression "github.com/tanvoxyz/tanvo/libs/expression/go"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/pricing"
)

// Error codes of the estimates.
const (
	// CodeNoInstantCharge: the event's code matches no pay-in-advance
	// charge of the subscription's plan (on the code field).
	CodeNoInstantCharge = "does_not_match_an_instant_charge"
	// CodeNotANumber: the aggregated property of a sum, max or latest
	// metric is not a number (on the properties field).
	CodeNotANumber = "value_is_not_valid_number"
	// CodeExpressionFailed: the metric's expression cannot be evaluated
	// for the event (on the properties field).
	CodeExpressionFailed = "expression_evaluation_failed"
)

// Event is an event to estimate.
type Event struct {
	// TransactionID identifies the event in the estimate; a random one
	// when empty.
	TransactionID string
	// Code is the billable metric code.
	Code string
	// ExternalSubscriptionID is the subscription's external ID.
	ExternalSubscriptionID string
	// Properties are the event properties, numbers as decimal text.
	Properties map[string]string
	// PreciseTotalAmountCents is the amount of the event for dynamic
	// charges.
	PreciseTotalAmountCents *money.Decimal
}

// Service estimates fees. It writes nothing.
type Service struct {
	d    *kernel.Deps
	fees *fees.Service

	mu       sync.Mutex
	programs map[string]*expression.Program
}

// New returns the service over the checked dependencies d.
func New(d *kernel.Deps) *Service {
	return &Service{d: d, fees: fees.New(d), programs: map[string]*expression.Program{}}
}

// PayInAdvance returns the fees the event would create now on the
// pay-in-advance charges of its subscription's plan for its code, priced
// with the period's usage and taxed (lago Fees::EstimatePayInAdvanceService):
// one fee per charge, attached to no event. The subscription is the one
// active now (a missing one is subscription_not_found), the code a billable
// metric's (else billable_metric_not_found).
func (s *Service) PayInAdvance(ctx context.Context, org core.OrganizationID, ev Event) ([]*invoice.Fee, error) {
	var out []*invoice.Fee
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		now := s.d.Now()
		sub, err := activeSubscription(ctx, r, org, ev.ExternalSubscriptionID, now)
		if err != nil {
			return err
		}
		m, err := r.Metrics.GetByCode(ctx, org, ev.Code)
		if errors.Is(err, core.ErrNotFound) {
			return core.NotFound("billable_metric")
		}
		if err != nil {
			return fmt.Errorf("load billable metric %q: %w", ev.Code, err)
		}
		if err := checkProperties(m, ev.Properties); err != nil {
			return err
		}
		props, err := s.evaluate(ctx, m, ev, now)
		if err != nil {
			return err
		}
		env, err := kernel.LoadSubscriptionEnvOf(ctx, r, sub)
		if err != nil {
			return err
		}
		charges, err := env.Charges(ctx, r)
		if err != nil {
			return err
		}
		var matched []*catalog.Charge
		for _, c := range charges {
			if c.PayInAdvance && c.BillableMetricID == m.ID {
				matched = append(matched, c)
			}
		}
		if len(matched) == 0 {
			return noInstantCharge()
		}
		tx := ev.TransactionID
		if tx == "" {
			tx = core.NewID[core.EventID](s.d.IDs).String()
		}
		me := metering.Event{
			OrganizationID: org.String(), SubscriptionID: sub.ExternalID, Code: ev.Code,
			TransactionID: tx, Timestamp: now, Properties: metering.Properties(props),
			PreciseTotalAmountCents: amountOrZero(ev.PreciseTotalAmountCents),
		}
		for _, c := range matched {
			res, err := s.fees.InAdvanceChargeFees(ctx, r, env, c, fees.InAdvanceEvent{Event: me, Estimate: true}, fees.Options{})
			if err != nil {
				return err
			}
			if res != nil {
				out = append(out, res.Fees...)
			}
		}
		return s.applyTaxes(ctx, r, env, out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// applyTaxes taxes the fees as standalone fees, with the provider when the
// customer has one (lago EstimatePayInAdvanceService#apply_taxes); a
// provider error fails the estimate.
func (s *Service) applyTaxes(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv, fs []*invoice.Fee) error {
	resolver := kernel.NewTaxResolver(r, env.Customer)
	resolver.SetPlan(env.Subscription.ID, env.Plan)
	for _, f := range fs {
		stub := &invoice.Invoice{OrganizationID: f.OrganizationID, CustomerID: env.Customer.Customer.ID, Currency: f.Currency, Type: invoice.TypeSubscription}
		if _, err := resolver.Compute(ctx, s.d, kernel.TaxInput{Invoice: stub, Fees: []*invoice.Fee{f}, Inline: true}); err != nil {
			return err
		}
	}
	return nil
}

// InstantFee is the instant estimate of one event on one charge.
type InstantFee struct {
	// Charge is the charge.
	Charge *catalog.Charge
	// ChargeFilterID is the filter the event falls in; zero for the
	// charge's own properties.
	ChargeFilterID core.ChargeFilterID
	// Metric is the charge's billable metric.
	Metric *catalog.BillableMetric
	// Subscription is the subscription.
	Subscription *subscription.Subscription
	// TransactionID is the event's transaction ID.
	TransactionID string
	// Currency is the plan's currency.
	Currency money.Currency
	// Units are the event's units, rounded with the metric's rounding.
	Units money.Decimal
	// PreciseAmount is the unrounded amount, in currency units.
	PreciseAmount money.Decimal
	// Amount is the amount rounded to the currency, in minor units.
	Amount int64
	// UnitAmount is the rounded amount per unit, in currency units; 0
	// when the amount is.
	UnitAmount money.Decimal
}

// Instant estimates the events on the percentage and standard
// pay-in-advance charges of their subscriptions, each priced on its own
// (lago EventsController#batch_estimate_instant_fees): the events are
// grouped by external subscription ID, in order; each group needs the
// subscription active now (else subscription_not_found) whose plan has
// such a charge (else does_not_match_an_instant_charge on code); an event
// gets one fee per charge of its code.
func (s *Service) Instant(ctx context.Context, org core.OrganizationID, events []Event) ([]InstantFee, error) {
	var order []string
	groups := map[string][]Event{}
	for _, e := range events {
		if _, ok := groups[e.ExternalSubscriptionID]; !ok {
			order = append(order, e.ExternalSubscriptionID)
		}
		groups[e.ExternalSubscriptionID] = append(groups[e.ExternalSubscriptionID], e)
	}
	var out []InstantFee
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		now := s.d.Now()
		for _, ext := range order {
			fs, err := s.instant(ctx, r, org, ext, groups[ext], now)
			if err != nil {
				return err
			}
			out = append(out, fs...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// instant is lago Fees::EstimateInstant::BatchPayInAdvanceService.
func (s *Service) instant(ctx context.Context, r port.Repos, org core.OrganizationID, ext string, events []Event, now time.Time) ([]InstantFee, error) {
	sub, err := activeSubscription(ctx, r, org, ext, now)
	if err != nil {
		return nil, err
	}
	env, err := kernel.LoadSubscriptionEnvOf(ctx, r, sub)
	if err != nil {
		return nil, err
	}
	all, err := env.Charges(ctx, r)
	if err != nil {
		return nil, err
	}
	var charges []*catalog.Charge
	metrics := map[core.BillableMetricID]*catalog.BillableMetric{}
	for _, c := range all {
		if !c.PayInAdvance || (c.ChargeModel != catalog.ChargeModelStandard && c.ChargeModel != catalog.ChargeModelPercentage) {
			continue
		}
		if _, ok := metrics[c.BillableMetricID]; !ok {
			m, err := r.Metrics.Get(ctx, org, c.BillableMetricID)
			if err != nil {
				return nil, fmt.Errorf("load billable metric %s: %w", c.BillableMetricID, err)
			}
			metrics[c.BillableMetricID] = m
		}
		charges = append(charges, c)
	}
	if len(charges) == 0 {
		return nil, noInstantCharge()
	}
	cur := env.Plan.Currency
	var out []InstantFee
	for _, ev := range events {
		tx := ev.TransactionID
		if tx == "" {
			tx = core.NewID[core.EventID](s.d.IDs).String()
		}
		for _, c := range charges {
			m := metrics[c.BillableMetricID]
			if m.Code != ev.Code {
				continue
			}
			// The expression's failure leaves the properties as sent
			// (estimate_instant/base_service.rb:18).
			props, err := s.evaluate(ctx, m, ev, now)
			if err != nil {
				if !errors.Is(err, core.ErrInvalidValue) {
					return nil, err
				}
				props = ev.Properties
			}
			f, err := estimateCharge(c, m, props, cur)
			if err != nil {
				return nil, err
			}
			f.Subscription, f.TransactionID = sub, tx
			out = append(out, f)
		}
	}
	return out, nil
}

// estimateCharge prices one event on a charge
// (estimate_instant/base_service.rb:14).
func estimateCharge(c *catalog.Charge, m *catalog.BillableMetric, props map[string]string, cur money.Currency) (InstantFee, error) {
	raw := c.Properties
	f := InstantFee{Charge: c, Metric: m, Currency: cur}
	if cf := catalog.MatchChargeFilter(c.Filters, props, m); cf != nil {
		f.ChargeFilterID = cf.ID
		if !cf.Properties.IsZero() {
			raw = cf.Properties
		}
	}
	p, err := pricing.DecodeProperties(pricing.ChargeModel(c.ChargeModel), raw.Raw())
	if err != nil {
		return InstantFee{}, fmt.Errorf("properties of charge %s: %w", c.ID, err)
	}
	// Metrics without a field name count each event as one unit.
	units := money.Zero
	if m.FieldName == nil {
		units = money.NewFromInt(1)
	}
	if m.FieldName != nil {
		if v, ok := props[*m.FieldName]; ok {
			if units, err = money.Parse(strings.TrimSpace(v)); err != nil {
				return InstantFee{}, invalid("properties", CodeNotANumber)
			}
		}
	}
	units = kernel.MeteringMetric(m).Round(units)
	est, err := pricing.EstimateInstant(p, units)
	if err != nil {
		return InstantFee{}, fmt.Errorf("estimate charge %s: %w", c.ID, err)
	}
	rounded := cur.Round(est.Amount, money.HalfUp)
	f.Units, f.PreciseAmount = units, est.Amount
	if f.Amount, err = cur.ToMinor(rounded, money.HalfUp); err != nil {
		return InstantFee{}, fmt.Errorf("amount of charge %s: %w", c.ID, err)
	}
	f.UnitAmount = money.Zero
	if !rounded.IsZero() {
		f.UnitAmount = rounded.DivOrZero(units)
	}
	return f, nil
}

// activeSubscription returns the subscription with the external ID that
// is active at t (lago Event#subscription), or subscription_not_found.
func activeSubscription(ctx context.Context, r port.Repos, org core.OrganizationID, ext string, t time.Time) (*subscription.Subscription, error) {
	if ext == "" {
		return nil, core.NotFound("subscription")
	}
	sub, err := r.Subscriptions.MatchEvent(ctx, org, ext, t, false)
	if errors.Is(err, core.ErrNotFound) {
		return nil, core.NotFound("subscription")
	}
	if err != nil {
		return nil, fmt.Errorf("match subscription %q: %w", ext, err)
	}
	return sub, nil
}

// checkProperties checks the aggregated property of sum, max and latest
// metrics: absent or a number (events/validate_creation_service.rb:56).
func checkProperties(m *catalog.BillableMetric, props map[string]string) error {
	switch m.AggregationType {
	case catalog.AggregationTypeMax, catalog.AggregationTypeSum, catalog.AggregationTypeLatest:
	default:
		return nil
	}
	if m.FieldName == nil {
		return nil
	}
	v, ok := props[*m.FieldName]
	if !ok {
		return nil
	}
	if _, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err != nil {
		return invalid("properties", CodeNotANumber)
	}
	return nil
}

// evaluate returns the event's properties with the result of the metric's
// expression under its field name (lago Events::CalculateExpressionService);
// an expression that cannot be evaluated is expression_evaluation_failed.
func (s *Service) evaluate(ctx context.Context, m *catalog.BillableMetric, ev Event, now time.Time) (map[string]string, error) {
	props := maps.Clone(ev.Properties)
	if props == nil {
		props = map[string]string{}
	}
	if m.Expression == nil || *m.Expression == "" || m.FieldName == nil {
		return props, nil
	}
	prog, err := s.program(*m.Expression)
	if err != nil {
		return nil, fmt.Errorf("compile expression of metric %q: %w", m.Code, err)
	}
	in := make(map[string]any, len(props))
	for k, v := range props {
		in[k] = v
	}
	v, err := prog.Evaluate(ctx, expression.Event{Code: ev.Code, Timestamp: now, Properties: in})
	if err != nil {
		var ee *expression.EvaluationError
		if errors.As(err, &ee) {
			return nil, invalid("properties", CodeExpressionFailed)
		}
		return nil, fmt.Errorf("evaluate expression of metric %q: %w", m.Code, err)
	}
	props[*m.FieldName] = v.String()
	return props, nil
}

// maxPrograms bounds the compiled expressions kept.
const maxPrograms = 256

func (s *Service) program(src string) (*expression.Program, error) {
	s.mu.Lock()
	p, ok := s.programs[src]
	s.mu.Unlock()
	if ok {
		return p, nil
	}
	p, err := expression.Compile(src)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if len(s.programs) >= maxPrograms {
		clear(s.programs)
	}
	s.programs[src] = p
	s.mu.Unlock()
	return p, nil
}

func noInstantCharge() error {
	return invalid("code", CodeNoInstantCharge)
}

// amountOrZero returns the event's amount, 0 when unset
// (estimate_pay_in_advance_service.rb:62).
func amountOrZero(d *money.Decimal) *money.Decimal {
	if d == nil {
		z := money.Zero
		return &z
	}
	v := *d
	return &v
}

func invalid(field, code string) error {
	return &core.ValidationError{Errors: []core.FieldError{{Field: field, Code: code}}}
}
