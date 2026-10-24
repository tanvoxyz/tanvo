package quotes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// ExecutionRecord is what an order execution wrote: the records it
// created, or the errors of a failed attempt. Every key is always present
// (lago Order::EXECUTION_RECORD_DEFAULTS, app/models/order.rb:20-29).
type ExecutionRecord struct {
	// ExecutedAt is the execution instant; nil for a failed attempt.
	ExecutedAt *time.Time `json:"executed_at"`
	// ExecutionMode is the mode of the attempt.
	ExecutionMode *quote.ExecutionMode `json:"execution_mode"`
	// InvoiceID is the one-off invoice issued.
	InvoiceID *core.InvoiceID `json:"invoice_id"`
	// SubscriptionIDs are the subscriptions created (or the subscription
	// on the amended plan).
	SubscriptionIDs []core.SubscriptionID `json:"subscription_ids"`
	// TerminatedSubscriptionIDs are the subscriptions the execution
	// terminated (an amendment taking effect now).
	TerminatedSubscriptionIDs []core.SubscriptionID `json:"terminated_subscription_ids"`
	// AppliedCouponIDs are the coupons applied.
	AppliedCouponIDs []core.AppliedCouponID `json:"applied_coupon_ids"`
	// WalletIDs are the wallets created.
	WalletIDs []core.WalletID `json:"wallet_ids"`
	// Errors are the error codes of a failed attempt.
	Errors []string `json:"errors"`
}

func newRecord(mode quote.ExecutionMode) ExecutionRecord {
	r := ExecutionRecord{
		SubscriptionIDs:           []core.SubscriptionID{},
		TerminatedSubscriptionIDs: []core.SubscriptionID{},
		AppliedCouponIDs:          []core.AppliedCouponID{},
		WalletIDs:                 []core.WalletID{},
		Errors:                    []string{},
	}
	if mode != "" {
		r.ExecutionMode = &mode
	}
	return r
}

// parseRecord reads a stored execution record onto the defaults (lago
// V1::OrderSerializer merges older records the same way).
func parseRecord(raw json.RawMessage) *ExecutionRecord {
	if len(raw) == 0 {
		return nil
	}
	r := newRecord("")
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil
	}
	return &r
}

// OrderView is an order with what its API representation shows (lago
// V1::OrderSerializer): the order type of its quote, and the currency and
// billing items of its quote version (app/models/order.rb:51-57).
type OrderView struct {
	// Order is the order.
	Order *quote.Order
	// OrderType is the order type of the quote.
	OrderType quote.OrderType
	// Currency is the deal currency.
	Currency money.Currency
	// BillingSnapshot are the billing items of the quote version.
	BillingSnapshot json.RawMessage
	// Record is the execution record; nil before any attempt.
	Record *ExecutionRecord
}

func orderView(ctx context.Context, r port.Repos, o *quote.Order) (*OrderView, error) {
	f, err := r.OrderForms.Get(ctx, o.OrganizationID, o.OrderFormID)
	if err != nil {
		return nil, fmt.Errorf("load order form %s: %w", o.OrderFormID, err)
	}
	v, err := r.QuoteVersions.Get(ctx, o.OrganizationID, f.QuoteVersionID)
	if err != nil {
		return nil, fmt.Errorf("load quote version %s: %w", f.QuoteVersionID, err)
	}
	q, err := r.Quotes.Get(ctx, o.OrganizationID, v.QuoteID)
	if err != nil {
		return nil, fmt.Errorf("load quote %s: %w", v.QuoteID, err)
	}
	return &OrderView{Order: o, OrderType: q.OrderType, Currency: v.Currency, BillingSnapshot: v.BillingItems, Record: parseRecord(o.ExecutionRecord)}, nil
}

// orderDeal loads the order form, quote version and quote of order o.
func orderDeal(ctx context.Context, r port.Repos, o *quote.Order) (*quote.Quote, *quote.Version, error) {
	f, err := r.OrderForms.Get(ctx, o.OrganizationID, o.OrderFormID)
	if err != nil {
		return nil, nil, fmt.Errorf("load order form %s: %w", o.OrderFormID, err)
	}
	v, err := r.QuoteVersions.Get(ctx, o.OrganizationID, f.QuoteVersionID)
	if err != nil {
		return nil, nil, fmt.Errorf("load quote version %s: %w", f.QuoteVersionID, err)
	}
	q, err := r.Quotes.Get(ctx, o.OrganizationID, v.QuoteID)
	if err != nil {
		return nil, nil, fmt.Errorf("load quote %s: %w", v.QuoteID, err)
	}
	return q, v, nil
}

// GetOrder returns order id of org.
func (s *Service) GetOrder(ctx context.Context, org core.OrganizationID, id core.OrderID) (*OrderView, error) {
	var out *OrderView
	err := s.read(ctx, func(ctx context.Context, r port.Repos) error {
		o, err := r.Orders.Get(ctx, org, id)
		if err != nil {
			return notFound(err, "order")
		}
		out, err = orderView(ctx, r, o)
		return err
	})
	return out, err
}

// ListOrders returns the orders of org matching f, newest first (lago
// OrdersQuery).
func (s *Service) ListOrders(ctx context.Context, org core.OrganizationID, f quote.OrderFilter, page core.PageRequest) (core.Page[*OrderView], error) {
	var out core.Page[*OrderView]
	err := s.read(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := r.Orders.List(ctx, org, f, page)
		if err != nil {
			return fmt.Errorf("list orders: %w", err)
		}
		out.NextToken = p.NextToken
		for _, x := range p.Items {
			v, err := orderView(ctx, r, x)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, v)
		}
		return nil
	})
	return out, err
}

// UpdateOrderInput changes the execution settings of an order; the fields
// set are the ones changed.
type UpdateOrderInput struct {
	// ExecutionMode is the execution mode; empty clears it.
	ExecutionMode core.Optional[quote.ExecutionMode]
	// ExecuteAt schedules the execution; nil clears it.
	ExecuteAt core.Optional[*time.Time]
}

// UpdateOrder changes the execution settings of created order id (lago
// Orders::UpdateService, app/services/orders/update_service.rb:17-68): the
// settings are validated with the effective values (the request's, else
// the order's) and the requested execute_at, then, under the lock of the
// quote, an order no longer created is refused with status not_editable.
func (s *Service) UpdateOrder(ctx context.Context, org core.OrganizationID, id core.OrderID, in UpdateOrderInput) (*OrderView, error) {
	var out *OrderView
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		o, err := r.Orders.Get(ctx, org, id)
		if err != nil {
			return notFound(err, "order")
		}
		q, v, err := orderDeal(ctx, r, o)
		if err != nil {
			return err
		}
		mode, executeAt := o.ExecutionMode, o.ExecuteAt
		in.ExecutionMode.Apply(&mode)
		var requested *time.Time
		if in.ExecuteAt.Set {
			executeAt, requested = in.ExecuteAt.Value, in.ExecuteAt.Value
		}
		if err := checkExecutionSettings(mode, executeAt, requested, v, s.d.Now()); err != nil {
			return err
		}
		if err := lockQuote(ctx, r, org, q.ID); err != nil {
			return err
		}
		if o, err = r.Orders.Get(ctx, org, id); err != nil {
			return fmt.Errorf("reload order %s: %w", id, err)
		}
		if o.Status != quote.OrderStatusCreated {
			return fieldError("status", CodeNotEditable)
		}
		o.ExecutionMode = mode
		if in.ExecuteAt.Set {
			o.ExecuteAt = nil
			if executeAt != nil {
				t := executeAt.UTC()
				o.ExecuteAt = &t
			}
		}
		o.UpdatedAt = s.d.Now()
		if err := o.Validate(); err != nil {
			return err
		}
		if err := r.Orders.Update(ctx, o); err != nil {
			return fmt.Errorf("update order %s: %w", id, err)
		}
		out, err = orderView(ctx, r, o)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("update order %s: %w", id, err)
	}
	return out, nil
}

// ExecuteOrder executes order id (lago Api::V1::OrdersController#execute,
// app/controllers/api/v1/orders_controller.rb:41-57,80-88): a mode that
// differs from the order's is first stored through [Service.UpdateOrder]
// (an unchanged one is not, so a failed order can be retried), then the
// order is executed ([Service.Execute]).
func (s *Service) ExecuteOrder(ctx context.Context, org core.OrganizationID, id core.OrderID, mode quote.ExecutionMode) (*OrderView, error) {
	o, err := s.GetOrder(ctx, org, id)
	if err != nil {
		return nil, err
	}
	if mode != "" && mode != o.Order.ExecutionMode {
		if _, err := s.UpdateOrder(ctx, org, id, UpdateOrderInput{ExecutionMode: core.Some(mode)}); err != nil {
			return nil, err
		}
	}
	return s.Execute(ctx, org, id)
}

// Execute executes order id (lago Orders::ExecuteService and
// Orders::BaseExecuteService, app/services/orders/execute_service.rb:15-29,
// app/services/orders/base_execute_service.rb:16-107). An executed order
// is returned unchanged; an order without execution mode is refused with
// execution_mode value_is_mandatory. Under the lock of the quote, mode
// execute creates what the deal states (by order type: subscriptions,
// coupons and wallets; the amended subscription; or a one-off invoice)
// and mode order_only nothing; the order is then executed with its
// execution record and order.executed is emitted. When creating the
// records fails with a validation, rule or not-found error, the attempt is
// rolled back and the order is marked failed with the error codes in a
// transaction of its own, and the error is returned.
func (s *Service) Execute(ctx context.Context, org core.OrganizationID, id core.OrderID) (*OrderView, error) {
	var out *OrderView
	var mode quote.ExecutionMode
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		o, err := r.Orders.Get(ctx, org, id)
		if err != nil {
			return notFound(err, "order")
		}
		if o.Status == quote.OrderStatusExecuted {
			out, err = orderView(ctx, r, o)
			return err
		}
		if o.ExecutionMode == "" {
			return fieldError("execution_mode", core.CodeRequired)
		}
		q, v, err := orderDeal(ctx, r, o)
		if err != nil {
			return err
		}
		if err := lockQuote(ctx, r, org, q.ID); err != nil {
			return err
		}
		if o, err = r.Orders.Get(ctx, org, id); err != nil {
			return fmt.Errorf("reload order %s: %w", id, err)
		}
		if o.Status != quote.OrderStatusExecuted {
			mode = o.ExecutionMode
			rec := newRecord(mode)
			if mode == quote.ExecutionModeExecute {
				if err := s.createRecords(ctx, r, o, q, v, &rec); err != nil {
					return err
				}
			}
			now := s.d.Now()
			rec.ExecutedAt = &now
			raw, err := json.Marshal(rec)
			if err != nil {
				return fmt.Errorf("encode execution record: %w", err)
			}
			if err := o.Execute(mode, raw, now); err != nil {
				return err
			}
			if err := r.Orders.Update(ctx, o); err != nil {
				return fmt.Errorf("update order %s: %w", id, err)
			}
			if err := kernel.Emit(ctx, s.d, org, webhook.EventTypeOrderExecuted, "order", o.ID.UUID(), o); err != nil {
				return err
			}
			// source: app/services/orders/base_execute_service.rb:68
			if err := s.logOrder(ctx, r, auditlog.ActivityOrderExecuted, o); err != nil {
				return err
			}
		}
		out, err = orderView(ctx, r, o)
		return err
	})
	if err != nil && mode != "" {
		if codes, ok := executionErrors(err); ok {
			if ferr := s.recordFailure(ctx, org, id, mode, codes); ferr != nil {
				return nil, errors.Join(err, ferr)
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("execute order %s: %w", id, err)
	}
	return out, nil
}

// createRecords dispatches on the order type of q (lago
// Orders::ExecuteService, app/services/orders/execute_service.rb:19-28).
func (s *Service) createRecords(ctx context.Context, r port.Repos, o *quote.Order, q *quote.Quote, v *quote.Version, rec *ExecutionRecord) error {
	x := &execution{s: s, r: r, order: o, quote: q, version: v, items: decodeItems(v.BillingItems)}
	switch q.OrderType {
	case quote.OrderTypeOneOff:
		return x.oneOff(ctx, rec)
	case quote.OrderTypeSubscriptionCreation:
		return x.subscriptionCreation(ctx, rec)
	case quote.OrderTypeSubscriptionAmendment:
		return x.subscriptionAmendment(ctx, rec)
	default:
		return fieldError("order_type", CodeUnsupportedOrderType)
	}
}

// executionErrors returns the codes a failed attempt records (lago
// BaseExecuteService#execution_errors, base_execute_service.rb:99-107):
// the codes of a validation error, the code of a rule error and
// <resource>_not_found for a not-found error. Other errors (store, lock)
// record nothing.
func executionErrors(err error) ([]string, bool) {
	var ve *core.ValidationError
	var re *core.RuleError
	var nf *core.NotFoundError
	switch {
	case errors.As(err, &ve):
		codes := make([]string, len(ve.Errors))
		for i, fe := range ve.Errors {
			codes[i] = fe.Code
		}
		return codes, true
	case errors.As(err, &re):
		return []string{re.Code}, true
	case errors.As(err, &nf):
		return []string{nf.Resource + "_not_found"}, true
	}
	return nil, false
}

// recordFailure marks order id failed with the error codes once the
// attempt rolled back (lago record_execution_failure!,
// base_execute_service.rb:71-82): it leaves the executable orders, and a
// retry is a deliberate manual action.
func (s *Service) recordFailure(ctx context.Context, org core.OrganizationID, id core.OrderID, mode quote.ExecutionMode, codes []string) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		o, err := r.Orders.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("reload order %s: %w", id, err)
		}
		rec := newRecord(mode)
		rec.Errors = codes
		raw, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("encode execution record: %w", err)
		}
		if err := o.Fail(raw, s.d.Now()); err != nil {
			return err
		}
		if err := r.Orders.Update(ctx, o); err != nil {
			return fmt.Errorf("update order %s: %w", id, err)
		}
		return nil
	})
}

// ExecuteScheduledOrders enqueues the [kernel.JobExecuteOrder] of every
// created order of org whose execute_at is at or before at, and returns
// their number (lago Clock::ExecuteScheduledOrdersJob and Order.executable,
// app/jobs/clock/execute_scheduled_orders_job.rb:7-11,
// app/models/order.rb:49).
func (s *Service) ExecuteScheduledOrders(ctx context.Context, org core.OrganizationID, at time.Time) (int, error) {
	n := 0
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		page := core.PageRequest{Size: sweepPage}
		for {
			p, err := r.Orders.List(ctx, org, quote.OrderFilter{Statuses: []quote.OrderStatus{quote.OrderStatusCreated}}, page)
			if err != nil {
				return fmt.Errorf("list created orders: %w", err)
			}
			for _, o := range p.Items {
				if !o.Executable(at) {
					continue
				}
				if err := kernel.Enqueue(ctx, r, org, kernel.JobExecuteOrder, kernel.OrderArgs{OrderID: o.ID}, kernel.ExecuteOrderKey(o.ID), time.Time{}); err != nil {
					return err
				}
				n++
			}
			if p.NextToken == "" {
				return nil
			}
			page.Token = p.NextToken
		}
	})
	return n, err
}

// sweepPage is the page size of the scheduled order sweep.
const sweepPage = 500

// HandleJob runs the order jobs; it reports false for any other kind.
// A failed execution is recorded on the order and completes the job: the
// order leaves the executable orders and is retried on request only
// (base_execute_service.rb:71-73).
func (s *Service) HandleJob(ctx context.Context, j port.Job) (bool, error) {
	switch j.Kind {
	case kernel.JobExecuteScheduledOrders:
		var a kernel.InstantArgs
		if err := json.Unmarshal(j.Args, &a); err != nil {
			return true, fmt.Errorf("decode %s args: %w", j.Kind, err)
		}
		if a.At.IsZero() {
			return true, fmt.Errorf("%s: %w: no instant", j.Kind, core.ErrInvalidValue)
		}
		_, err := s.ExecuteScheduledOrders(ctx, j.OrganizationID, a.At)
		return true, err
	case kernel.JobExecuteOrder:
		var a kernel.OrderArgs
		if err := json.Unmarshal(j.Args, &a); err != nil {
			return true, fmt.Errorf("decode %s args: %w", j.Kind, err)
		}
		_, err := s.Execute(ctx, j.OrganizationID, a.OrderID)
		if _, failed := executionErrors(err); failed {
			slog.WarnContext(ctx, "order execution failed", "organization_id", j.OrganizationID.String(), "order_id", a.OrderID.String(), "error", err)
			return true, nil
		}
		return true, err
	}
	return false, nil
}
