package usage

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
	"github.com/tanvoxyz/tanvo/libs/pricing"
)

// ErrNoActiveSubscription is returned when the usage of a subscription that
// is not active is requested (lago no_active_subscription).
var ErrNoActiveSubscription = fmt.Errorf("usage: no active subscription: %w", core.ErrNotAllowed)

// errNotStarted is returned when the subscription has no period at the
// requested instant.
var errNotStarted = errors.New("usage: subscription not started")

// ErrFullUsageNotAllowed is returned when [Options.FullUsage] is requested
// without [Options.ChargeIDs] or for prorated charges (lago
// full_usage_not_allowed,
// app/services/invoices/customer_usage_service.rb:55,366-375).
var ErrFullUsageNotAllowed = fmt.Errorf("usage: full_usage_not_allowed: %w", core.ErrNotAllowed)

// Options tunes the computation of a current usage.
type Options struct {
	// SkipTaxes computes the usage without taxes (lago apply_taxes: false).
	SkipTaxes bool
	// Projection also projects every fee to the end of its charges period
	// (domain §10.3).
	Projection bool
	// SkipZeroUnitsFilters drops the fees of charge filters without units
	// (lago with_zero_units_filters: false).
	SkipZeroUnitsFilters bool
	// ChargeIDs keeps the fees of these charges only; nil keeps every
	// charge (lago filter_by_charge_id). A filter matching no charge of
	// the plan fails with core.ErrNotFound.
	ChargeIDs []core.ChargeID
	// FullUsage computes the usage since the subscription started instead
	// of since the start of the current period (lago UsageFilters
	// full_usage, app/services/invoices/customer_usage_service.rb:196-197);
	// it needs ChargeIDs or a group filter, and charges that are not
	// prorated, or fails with [ErrFullUsageNotAllowed].
	FullUsage bool
	// Filters narrow the aggregation: grouping skipped, a group filter
	// and a presentation filter (lago UsageFilters skip_grouping,
	// filter_by_group and filter_by_presentation).
	Filters fees.UsageFilters
	// Cache returns the cached aggregations of a scope; nil reads the
	// stored ones (domain §10.3).
	Cache func(sc event.CachedAggregationScope) metering.CachedAggregations
	// Timestamp is the instant of the usage; zero means the clock's.
	Timestamp time.Time
	// MaxTimestamp, when set, caps the events read at that instant
	// (inclusive) (lago max_timestamp,
	// app/services/invoices/customer_usage_service.rb:141-148).
	MaxTimestamp time.Time
	// SkipCache computes every charge, ignoring and not filling the
	// current-usage cache of the engine (lago with_cache: false). The
	// cache is never used with MaxTimestamp.
	SkipCache bool
	// AnyStatus computes the usage of a subscription that is not active,
	// as lago does when given the subscription itself
	// (app/services/invoices/customer_usage_service.rb:54 checks presence
	// only; the daily usages pass terminated subscriptions).
	AnyStatus bool
	// UsageBuckets reads the usage buckets for the charges they serve
	// (lago use_usage_buckets, see [fees.Options.UsageBuckets]).
	UsageBuckets bool
}

// Usage is the current usage of a subscription: its charge fees for the
// charges period so far, on an invoice that is never stored.
type Usage struct {
	// Subscription is the subscription.
	Subscription *subscription.Subscription
	// Invoice is the in-memory invoice holding the fees and totals.
	Invoice *invoice.Invoice
	// From is the start of the charges period.
	From time.Time
	// To is the end (exclusive) of the charges period.
	To time.Time
	// IssuingDate is the date the period will be invoiced (the last day
	// of the subscription period).
	IssuingDate core.Date
	// Currency is the plan currency.
	Currency money.Currency
	// Amount is the sum of the fees, in minor units.
	Amount int64
	// TaxesAmount is the taxes, in minor units; 0 with [Options.SkipTaxes].
	TaxesAmount int64
	// TaxesRate is the effective tax rate in percent.
	TaxesRate money.Decimal
	// TotalAmount is Amount + TaxesAmount.
	TotalAmount int64
	// Fees are the charge fees, sorted by metric name.
	Fees []*invoice.Fee
	// AppliedTaxes are the invoice-level taxes.
	AppliedTaxes []*invoice.AppliedTax
	// Projections are the projections of Fees, index for index, with
	// [Options.Projection]; nil otherwise.
	Projections []Projection
}

// CurrentUsage computes the current usage of the subscription id (lago
// Invoices::CustomerUsageService). Nothing is stored.
func (s *Service) CurrentUsage(ctx context.Context, r port.Repos, org core.OrganizationID, id core.SubscriptionID, opts Options) (*Usage, error) {
	env, err := kernel.LoadSubscriptionEnv(ctx, r, org, id)
	if err != nil {
		return nil, err
	}
	return s.EnvUsage(ctx, r, env, opts)
}

// CustomerUsage computes the current usage of every active subscription of
// the customer, in creation order.
func (s *Service) CustomerUsage(ctx context.Context, r port.Repos, org core.OrganizationID, c core.CustomerID, opts Options) ([]*Usage, error) {
	subs, err := r.Subscriptions.ListByCustomer(ctx, org, c, subscription.StatusActive)
	if err != nil {
		return nil, fmt.Errorf("list active subscriptions of customer %s: %w", c, err)
	}
	out := make([]*Usage, 0, len(subs))
	for _, sub := range subs {
		env, err := kernel.LoadSubscriptionEnvOf(ctx, r, sub)
		if err != nil {
			return nil, err
		}
		u, err := s.EnvUsage(ctx, r, env, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

// EnvUsage computes the current usage of the loaded subscription env.
func (s *Service) EnvUsage(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv, opts Options) (*Usage, error) {
	sub := env.Subscription
	if sub.Status != subscription.StatusActive && !opts.AnyStatus {
		return nil, ErrNoActiveSubscription
	}
	now := opts.Timestamp
	if now.IsZero() {
		now = s.d.Now()
	}
	dates, err := env.Dates(now, period.CurrentUsage)
	if err != nil {
		return nil, err
	}
	b, ok := dates.Boundaries()
	if !ok {
		return nil, fmt.Errorf("usage of subscription %s at %s: %w", sub.ID, now, errNotStarted)
	}
	inv := kernel.NewInvoice(s.d, env.Customer, kernel.InvoiceKind{Type: invoice.TypeSubscription, Recurring: true}, env.Plan.Currency, now)
	inv.IssuingDate = core.DateOf(dates.CurrentPeriodEnd().Add(-time.Nanosecond), env.Location())

	charges, err := env.Charges(ctx, r)
	if err != nil {
		return nil, err
	}
	if opts.FullUsage {
		// lago app/services/invoices/customer_usage_service.rb:366-375:
		// a charge or group filter, and no prorated charge computed.
		if opts.ChargeIDs == nil && len(opts.Filters.Group) == 0 || slices.ContainsFunc(charges, func(c *catalog.Charge) bool {
			return c.Prorated && (opts.ChargeIDs == nil || slices.Contains(opts.ChargeIDs, c.ID))
		}) {
			return nil, fmt.Errorf("usage of subscription %s: %w", sub.ID, ErrFullUsageNotAllowed)
		}
		b.Subscription.From, b.Charges.From = sub.StartedAt.UTC(), sub.StartedAt.UTC()
	}
	if opts.ChargeIDs != nil {
		charges = slices.DeleteFunc(charges, func(c *catalog.Charge) bool { return !slices.Contains(opts.ChargeIDs, c.ID) })
		if len(charges) == 0 {
			// lago app/services/invoices/customer_usage_service.rb:56.
			return nil, fmt.Errorf("usage of subscription %s: charge: %w", sub.ID, core.ErrNotFound)
		}
	}
	sc := fees.Scope{Env: env, Boundaries: b, Timestamp: now}
	cache := opts.Cache
	if cache == nil {
		cache = kernel.AggregationCache(r, env.OrganizationID())
	}
	fo := fees.Options{
		CurrentUsage: true, SkipZeroUnitsFilters: opts.SkipZeroUnitsFilters, SkipAdjustedFees: true, Cache: cache,
		MaxTimestamp: opts.MaxTimestamp, FullUsage: opts.FullUsage, UsageFilters: opts.Filters,
		UsageBuckets: opts.UsageBuckets,
	}
	if s.d.UsageCache != nil && !opts.SkipCache && opts.MaxTimestamp.IsZero() && cacheable(opts.Filters) {
		// lago charge_cache_enabled?, customer_usage_service.rb:346-364;
		// full usage has no premium gate.
		fo.UsageCache = s.d.UsageCache
	}
	type metricFee struct {
		name string
		fee  *invoice.Fee
		c    *catalog.Charge
		m    *catalog.BillableMetric
	}
	var all []metricFee
	fs := fees.New(s.d)
	for _, c := range charges {
		res, err := fs.ChargeFees(ctx, r, sc, c, fo)
		if err != nil {
			return nil, err
		}
		m, err := r.Metrics.Get(ctx, env.OrganizationID(), c.BillableMetricID)
		if err != nil {
			return nil, fmt.Errorf("load metric of charge %s: %w", c.ID, err)
		}
		for _, f := range res.Fees {
			all = append(all, metricFee{name: strings.ToLower(m.Name), fee: f, c: c, m: m})
		}
	}
	if opts.ChargeIDs == nil {
		slices.SortStableFunc(all, func(a, b metricFee) int { return cmp.Compare(a.name, b.name) })
	}

	u := &Usage{
		Subscription: sub, Invoice: inv, From: b.Charges.From, To: b.Charges.To,
		IssuingDate: inv.IssuingDate, Currency: inv.Currency, TaxesRate: money.Zero,
		Fees: make([]*invoice.Fee, len(all)),
	}
	for i, mf := range all {
		u.Fees[i] = mf.fee
		u.Amount += mf.fee.Amount
	}
	if !opts.SkipTaxes && len(u.Fees) > 0 {
		tr := kernel.NewTaxResolver(r, env.Customer)
		tr.SetPlan(sub.ID, env.Plan)
		out, err := tr.Compute(ctx, s.d, kernel.TaxInput{Invoice: inv, Fees: u.Fees, Draft: true, Inline: true})
		if err != nil {
			return nil, err
		}
		u.TaxesAmount, u.TaxesRate, u.AppliedTaxes = out.Totals.TaxesAmount, out.TaxesRate, out.AppliedTaxes
	}
	u.TotalAmount = u.Amount + u.TaxesAmount
	inv.FeesAmount, inv.SubTotalExcludingTaxesAmount = u.Amount, u.Amount
	inv.TaxesAmount, inv.TaxesRate = u.TaxesAmount, u.TaxesRate
	inv.SubTotalIncludingTaxesAmount, inv.TotalAmount = u.TotalAmount, u.TotalAmount

	if opts.Projection {
		u.Projections = make([]Projection, len(all))
		units := map[core.ChargeID]*pricing.AppliedPricingUnit{}
		for i, mf := range all {
			pu, ok := units[mf.c.ID]
			if !ok {
				if pu, err = pricingUnitOf(ctx, r, env.OrganizationID(), mf.c); err != nil {
					return nil, err
				}
				units[mf.c.ID] = pu
			}
			p, err := project(mf.fee, mf.c, mf.m, pu, now, env.Location())
			if err != nil {
				return nil, err
			}
			u.Projections[i] = p
		}
	}
	return u, nil
}

// cacheable reports whether the fees of a usage narrowed by f can go
// through the charge usage cache: a group filter computes a subset of the
// fees (lago charge_cache_enabled?, customer_usage_service.rb:353-356).
// Skipped grouping and a presentation filter, which shape the fees, have
// their own cache keys, where lago shares the entries of the unfiltered
// usage (full_usage_cache_enabled?, :360-364, §4).
func cacheable(f fees.UsageFilters) bool {
	return len(f.Group) == 0
}
