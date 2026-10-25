package dailyusage

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// feePage is the page size of the fee lists.
const feePage = 500

// FillFromInvoice stores, for each subscription of subs the invoice id
// bills, the usage of the last day of the billed charges period, from the
// invoice's charge fees and the subscription's pay-in-advance fees of
// that period (lago DailyUsages::FillFromInvoiceService,
// app/services/daily_usages/fill_from_invoice_service.rb). A row whose
// charges period is unset or reversed, or whose snapshot is already
// stored, is skipped. It returns the stored snapshots.
//
// The amounts are those of the subscription's fees; lago adds the charge
// fees of every subscription of the invoice
// (fill_from_invoice_service.rb:61-62) to each snapshot, whose charges
// list only the subscription's own (:59), which overstates the usage of
// an invoice billing several subscriptions.
func (s *Service) FillFromInvoice(ctx context.Context, org core.OrganizationID, id core.InvoiceID, subs []core.SubscriptionID) ([]*subscription.DailyUsage, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) ([]*subscription.DailyUsage, error) {
		inv, err := r.Invoices.Get(ctx, org, id)
		if err != nil {
			return nil, fmt.Errorf("load invoice %s: %w", id, err)
		}
		rows, err := r.InvoiceSubscriptions.ListByInvoice(ctx, org, id)
		if err != nil {
			return nil, fmt.Errorf("load subscriptions of invoice %s: %w", id, err)
		}
		cenv, err := kernel.LoadCustomerEnv(ctx, r, org, inv.CustomerID, core.BillingEntityID{})
		if err != nil {
			return nil, err
		}
		invFees, err := r.Fees.ListByInvoice(ctx, org, id)
		if err != nil {
			return nil, fmt.Errorf("load fees of invoice %s: %w", id, err)
		}
		cc := newCatalogCache(r, org)
		var out []*subscription.DailyUsage
		for _, row := range rows {
			if !slices.Contains(subs, row.SubscriptionID) {
				continue
			}
			cp := row.ChargesPeriod
			if cp.From.IsZero() || cp.To.IsZero() || cp.From.After(cp.To) {
				continue
			}
			from, to := cp.From.Truncate(time.Second), cp.To.Truncate(time.Second)
			// The period ends before To; lago's inclusive end is the last
			// instant of that day (fill_from_invoice_service.rb:111-113).
			day := core.DateOf(cp.To.Add(-time.Nanosecond), cenv.Location)
			_, err := r.DailyUsages.GetByPeriodAndDate(ctx, org, row.SubscriptionID, from, to, day)
			switch {
			case err == nil:
				continue
			case !errors.Is(err, core.ErrNotFound):
				return nil, fmt.Errorf("load daily usage of subscription %s for %s: %w", row.SubscriptionID, day, err)
			}

			inAdvance, err := inAdvanceFees(ctx, r, org, row.SubscriptionID, cp)
			if err != nil {
				return nil, err
			}
			h := head{from: from, to: to, issuingDate: inv.IssuingDate, currency: inv.Currency}
			fees := slices.Clone(inAdvance)
			for _, f := range inAdvance {
				h.amount += f.Amount
				h.taxes += f.TaxesAmount
			}
			for _, f := range invFees {
				if f.Type != core.FeeTypeCharge || f.SubscriptionID != row.SubscriptionID {
					continue
				}
				h.amount += f.Amount
				h.taxes += f.TaxesAmount
				if f.NonZero() {
					fees = append(fees, f)
				}
			}
			h.total = h.amount + h.taxes
			if len(fees) == 0 {
				continue
			}
			sub, err := r.Subscriptions.Get(ctx, org, row.SubscriptionID)
			if err != nil {
				return nil, fmt.Errorf("load subscription %s: %w", row.SubscriptionID, err)
			}
			snap, err := snapshot(ctx, cc, h, fees)
			if err != nil {
				return nil, err
			}
			prev, err := previous(ctx, r, org, sub.ID, from, to, day)
			if err != nil {
				return nil, err
			}
			du, err := s.store(ctx, r, sub, snap, day, row.Timestamp, diffOf(snap, prev))
			if err != nil {
				return nil, err
			}
			out = append(out, du)
		}
		return out, nil
	})
}

// inAdvanceFees returns the pay-in-advance charge fees of sub, from an
// event, for the charges period cp, matched to the millisecond
// (fill_from_invoice_service.rb:77-89).
func inAdvanceFees(ctx context.Context, r port.Repos, org core.OrganizationID, sub core.SubscriptionID, cp period.Period) ([]*invoice.Fee, error) {
	var out []*invoice.Fee
	page := core.PageRequest{Size: feePage}
	for {
		p, err := r.Fees.List(ctx, org, invoice.FeeFilter{SubscriptionID: sub, Types: []core.FeeType{core.FeeTypeCharge}}, page)
		if err != nil {
			return nil, fmt.Errorf("list fees of subscription %s: %w", sub, err)
		}
		for _, f := range p.Items {
			if f.PayInAdvance && f.PayInAdvanceEventTransactionID != "" &&
				sameMillisecond(f.Properties.ChargesFrom, cp.From) && sameMillisecond(f.Properties.ChargesTo, cp.To) {
				out = append(out, f)
			}
		}
		if p.NextToken == "" {
			return out, nil
		}
		page.Token = p.NextToken
	}
}

func sameMillisecond(a, b time.Time) bool {
	return a.Truncate(time.Millisecond).Equal(b.Truncate(time.Millisecond))
}

// FillHistory stores the missing snapshots of a subscription for the days
// a.From to a.To in its customer's time zone (lago
// DailyUsages::FillHistoryService,
// app/services/daily_usages/fill_history_service.rb). The range starts no
// earlier than the subscription's start and ends on its termination day,
// else on a.To, else on the previous UTC day. A day already stored is
// kept and becomes the base of the next day's diff. A day without events
// is computed only when the plan's usage changes without events, or on
// the first day of a period when the plan has a recurring metric. Each
// day's usage is the one at the end of the day, from the events up to it;
// the first day of the range has an empty diff. A Sandbox fill computes
// the usages without storing them. Once done, a terminated subscription's
// termination invoice fills its last day ([Service.FillFromInvoice]).
//
// Each day runs in its own transaction, as lago saves each row on its own
// (fill_history_service.rb:81).
func (s *Service) FillHistory(ctx context.Context, org core.OrganizationID, a kernel.FillDailyUsageHistoryArgs) error {
	h, err := read(ctx, s.d, func(ctx context.Context, r port.Repos) (*history, error) {
		return s.loadHistory(ctx, r, org, a)
	})
	if err != nil || h.last.Before(h.first) {
		return err
	}
	sub, loc := h.env.Subscription, h.env.Location()

	// The days that received events, in one query
	// (fill_history_service.rb:124-152).
	days, err := s.d.Events.SumByDay(ctx, metering.Query{
		OrganizationID: org.String(), SubscriptionID: sub.ExternalID,
		From: h.first.Midnight(loc), To: endOfDay(h.last, loc), Location: loc,
	})
	if err != nil {
		return fmt.Errorf("list event days of subscription %s: %w", sub.ID, err)
	}
	eventDays := make(map[core.Date]bool, len(days))
	for _, d := range days {
		eventDays[core.NewDate(d.Date.Year, d.Date.Month, d.Date.Day)] = true
	}

	var prev *subscription.DailyUsage
	for day := h.first; !day.After(h.last); day = day.AddDays(1) {
		err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			if !a.Sandbox {
				du, err := r.DailyUsages.GetByDate(ctx, org, sub.ID, day)
				switch {
				case err == nil:
					prev = du
					return nil
				case !errors.Is(err, core.ErrNotFound):
					return fmt.Errorf("load daily usage of subscription %s for %s: %w", sub.ID, day, err)
				}
			}
			if !eventDays[day] && !h.usage.timeDependent {
				// Recurring metrics carry their usage into a new period
				// (fill_history_service.rb:35-40).
				first, err := h.periodFirstDay(day)
				if err != nil || !h.usage.recurring || !first {
					return err
				}
			}
			du, err := s.fillDay(ctx, r, h, day, a.Sandbox, prev)
			if err == nil && du != nil {
				prev = du
			}
			return err
		})
		if err != nil {
			return err
		}
	}

	if !h.terminated {
		return nil
	}
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		rows, err := r.InvoiceSubscriptions.ListBySubscription(ctx, org, sub.ID)
		if err != nil {
			return fmt.Errorf("load invoices of subscription %s: %w", sub.ID, err)
		}
		for _, row := range rows {
			if row.Reason == invoice.InvoicingReasonSubscriptionTerminating {
				args := kernel.FillDailyUsageFromInvoiceArgs{InvoiceID: row.InvoiceID, SubscriptionIDs: []core.SubscriptionID{sub.ID}}
				return kernel.Enqueue(ctx, r, org, kernel.JobFillDailyUsageFromInvoice, args, "", time.Time{})
			}
		}
		return nil
	})
}

// history is the range and plan of a [Service.FillHistory].
type history struct {
	env        *kernel.SubscriptionEnv
	usage      planUsage
	first      core.Date
	last       core.Date
	terminated bool
}

// loadHistory resolves the range of days of a fill
// (fill_history_service.rb:105-118).
func (s *Service) loadHistory(ctx context.Context, r port.Repos, org core.OrganizationID, a kernel.FillDailyUsageHistoryArgs) (*history, error) {
	env, err := kernel.LoadSubscriptionEnv(ctx, r, org, a.SubscriptionID)
	if err != nil {
		return nil, err
	}
	sub, loc := env.Subscription, env.Location()
	if sub.StartedAt == nil {
		return nil, fmt.Errorf("fill daily usages of subscription %s: not started: %w", sub.ID, core.ErrNotAllowed)
	}
	h := &history{env: env, terminated: sub.Status == subscription.StatusTerminated && sub.TerminatedAt != nil}
	h.first = core.DateOf(*sub.StartedAt, loc)
	if a.From.After(h.first) {
		h.first = a.From
	}
	switch {
	case h.terminated:
		h.last = core.DateOf(*sub.TerminatedAt, loc)
	case a.To != nil:
		h.last = *a.To
	default:
		h.last = core.DateOf(s.d.Now(), time.UTC).AddDays(-1)
	}
	if h.usage, err = planUsageOf(ctx, r, newCatalogCache(r, org), sub.PlanID); err != nil {
		return nil, err
	}
	return h, nil
}

// periodFirstDay reports whether a subscription period starts on day
// (fill_history_service.rb:176-187: the start of the period holding the
// end of each day of the range).
func (h *history) periodFirstDay(day core.Date) (bool, error) {
	loc := h.env.Location()
	dates, err := h.env.Dates(endOfDay(day, loc), period.CurrentUsage)
	if err != nil {
		return false, err
	}
	b, ok := dates.Boundaries()
	return ok && core.DateOf(b.Subscription.From, loc) == day, nil
}

// fillDay computes the usage of the subscription at the end of day and,
// unless sandbox, stores it when its fees are not all zero, with its diff
// against prev, the previous snapshot of the same charges period
// (fill_history_service.rb:28-85).
func (s *Service) fillDay(ctx context.Context, r port.Repos, h *history, day core.Date, sandbox bool, prev *subscription.DailyUsage) (*subscription.DailyUsage, error) {
	env, loc := h.env, h.env.Location()
	org, sub := env.OrganizationID(), env.Subscription
	// The local midnight, or the UTC one when later
	// (fill_history_service.rb:28-29).
	refreshedAt := day.Midnight(loc)
	if utc := day.Midnight(time.UTC); refreshedAt.Before(utc) {
		refreshedAt = utc
	}
	end := endOfDay(day, loc)
	u, err := s.usage.EnvUsage(ctx, r, env, usage.Options{
		SkipTaxes: true, AnyStatus: true, SkipZeroUnitsFilters: true, Timestamp: end, MaxTimestamp: end, SkipCache: true,
	})
	if err != nil || sandbox {
		return nil, err
	}
	if prev != nil && !prev.From.Equal(u.From) {
		// A new period started: the diff is its whole usage.
		prev = nil
	}
	fees := nonZero(u.Fees)
	if len(fees) == 0 {
		return nil, nil
	}
	snap, err := snapshot(ctx, newCatalogCache(r, org), headOf(u), fees)
	if err != nil {
		return nil, err
	}
	var diff *subscription.UsageSnapshot
	if day != h.first {
		if prev == nil {
			if prev, err = previous(ctx, r, org, sub.ID, snap.From, snap.To, day); err != nil {
				return nil, err
			}
		}
		diff = diffOf(snap, prev)
	}
	return s.store(ctx, r, sub, snap, day, refreshedAt, diff)
}

// endOfDay returns the last instant of day in loc (Rails end_of_day).
func endOfDay(day core.Date, loc *time.Location) time.Time {
	return day.AddDays(1).Midnight(loc).Add(-time.Nanosecond)
}

// historyDeletionBatch is how many daily usages one transaction of
// [Service.RebuildHistory] deletes (lago lib/tasks/daily_usages.rake:11).
const historyDeletionBatch = 10_000

// RebuildHistory rebuilds the daily usage history of org (lago rake
// daily_usages:fill_history, lib/tasks/daily_usages.rake): with
// a.DeleteExisting, it first deletes the stored daily usages from a.From
// on of the subscriptions in scope ([Service.EnqueueFillHistory]), in
// batches of [historyDeletionBatch] each committed on its own
// (daily_usages.rake:63-71), then enqueues their fills. It returns how
// many daily usages were deleted and how many fills were enqueued.
func (s *Service) RebuildHistory(ctx context.Context, org core.OrganizationID, a kernel.RebuildDailyUsageHistoryArgs) (deleted, enqueued int, err error) {
	from := a.From
	if from.IsZero() {
		from = core.DateOf(s.d.Now(), time.UTC).AddDays(-subscription.DefaultDailyUsageHistoryDays)
	}
	if a.DeleteExisting {
		scope, err := read(ctx, s.d, func(ctx context.Context, r port.Repos) ([]core.SubscriptionID, error) {
			subs, err := historyScope(ctx, r, org, from, a.SubscriptionIDs)
			ids := make([]core.SubscriptionID, len(subs))
			for i, sub := range subs {
				ids[i] = sub.ID
			}
			return ids, err
		})
		if err != nil {
			return 0, 0, err
		}
		for len(scope) > 0 {
			var n int
			err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
				var err error
				if n, err = r.DailyUsages.DeleteFrom(ctx, org, scope, from, historyDeletionBatch); err != nil {
					return fmt.Errorf("delete daily usages of organization %s: %w", org, err)
				}
				return nil
			})
			if err != nil {
				return deleted, 0, err
			}
			deleted += n
			if n < historyDeletionBatch {
				break
			}
		}
	}
	enqueued, err = s.EnqueueFillHistory(ctx, org, from, a.SubscriptionIDs)
	return deleted, enqueued, err
}

// EnqueueFillHistory enqueues the [kernel.JobFillDailyUsageHistory] from
// day from of every started active or terminated subscription of org not
// terminated before from, or of those of ids when ids is not empty (lago
// rake daily_usages:fill_history, lib/tasks/daily_usages.rake:20-25 and
// 79-82). It returns how many were enqueued.
func (s *Service) EnqueueFillHistory(ctx context.Context, org core.OrganizationID, from core.Date, ids []core.SubscriptionID) (int, error) {
	n := 0
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		subs, err := historyScope(ctx, r, org, from, ids)
		if err != nil {
			return err
		}
		for _, sub := range subs {
			args := kernel.FillDailyUsageHistoryArgs{SubscriptionID: sub.ID, From: from}
			if err := kernel.Enqueue(ctx, r, org, kernel.JobFillDailyUsageHistory, args, "", time.Time{}); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// historyScope returns the subscriptions a history rebuild from day from
// covers: the started active or terminated subscriptions of org not
// terminated before from, restricted to ids when it is not empty (lago
// lib/tasks/daily_usages.rake:20-25).
func historyScope(ctx context.Context, r port.Repos, org core.OrganizationID, from core.Date, ids []core.SubscriptionID) ([]*subscription.Subscription, error) {
	f := subscription.Filter{Statuses: []subscription.Status{subscription.StatusActive, subscription.StatusTerminated}}
	page := core.PageRequest{Size: selectionPage}
	var out []*subscription.Subscription
	for {
		subs, err := r.Subscriptions.List(ctx, org, f, page)
		if err != nil {
			return nil, fmt.Errorf("list subscriptions of organization %s: %w", org, err)
		}
		for _, sub := range subs.Items {
			if sub.StartedAt == nil || len(ids) > 0 && !slices.Contains(ids, sub.ID) ||
				sub.TerminatedAt != nil && sub.TerminatedAt.Before(from.Midnight(time.UTC)) {
				continue
			}
			out = append(out, sub)
		}
		if subs.NextToken == "" {
			return out, nil
		}
		page.Token = subs.NextToken
	}
}
