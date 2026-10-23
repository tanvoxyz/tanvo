package quote

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Order is the commitment created by signing an order form (domain
// §21.3). An order form has at most one order.
type Order struct {
	// ID identifies the order.
	ID core.OrderID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// Number is unique per organization, see [FormatNumber].
	Number string
	// SequentialID is the sequence of the number, per organization and
	// year.
	SequentialID int
	// CustomerID is the customer.
	CustomerID core.CustomerID
	// OrderFormID is the signed order form.
	OrderFormID core.OrderFormID
	// Status is the lifecycle state.
	Status OrderStatus
	// ExecutionMode says whether execution applies the quote content;
	// required once executed or scheduled.
	ExecutionMode ExecutionMode
	// ExecuteAt schedules the execution; nil waits for a manual one.
	ExecuteAt *time.Time
	// ExecutedAt is set once executed.
	ExecutedAt *time.Time
	// ExecutionRecord lists what the execution created (subscription,
	// invoice, coupon and wallet ids, errors) as a JSON object.
	ExecutionRecord json.RawMessage
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// NewOrder creates the order of a signed order form.
func NewOrder(id core.OrderID, f *OrderForm, mode ExecutionMode, executeAt *time.Time, now time.Time) (*Order, error) {
	if f.Status != OrderFormStatusSigned {
		return nil, core.NewRuleError(CodeNotSignable, "an order needs a signed order form")
	}
	return &Order{
		ID:             id,
		OrganizationID: f.OrganizationID,
		CustomerID:     f.CustomerID,
		OrderFormID:    f.ID,
		Status:         OrderStatusCreated,
		ExecutionMode:  mode,
		ExecuteAt:      executeAt,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// AssignNumber sets SequentialID and, unless a number is already set,
// Number from the creation year.
func (o *Order) AssignNumber(seq int) {
	o.SequentialID = seq
	if o.Number == "" {
		o.Number = FormatNumber(OrderPrefix, o.CreatedAt.UTC().Year(), seq)
	}
}

// Validate checks the invariants of domain §21.3.
func (o *Order) Validate() error {
	var v core.Validator
	v.Required("number", o.Number)
	v.Check(!o.CustomerID.IsZero(), "customer_id", core.CodeRequired)
	v.Check(!o.OrderFormID.IsZero(), "order_form_id", core.CodeRequired)
	v.Enum("status", o.Status)
	if o.Status == OrderStatusExecuted || o.ExecuteAt != nil {
		v.Enum("execution_mode", o.ExecutionMode)
	} else {
		v.OptionalEnum("execution_mode", o.ExecutionMode)
	}
	if o.Status == OrderStatusExecuted {
		v.Check(o.ExecutedAt != nil, "executed_at", core.CodeRequired)
	}
	if len(o.ExecutionRecord) > 0 {
		v.Check(json.Valid(o.ExecutionRecord), "execution_record", core.CodeInvalidFormat)
	}
	return v.Err()
}

var orderTransitions = map[OrderStatus][]OrderStatus{
	OrderStatusCreated: {OrderStatusExecuted, OrderStatusFailed},
}

// CanTransitionOrder reports whether an order may go from one status to
// another: created → executed | failed.
func CanTransitionOrder(from, to OrderStatus) bool {
	return slices.Contains(orderTransitions[from], to)
}

func (o *Order) to(s OrderStatus, now time.Time) error {
	if !CanTransitionOrder(o.Status, s) {
		return &core.TransitionError{Entity: "order", From: string(o.Status), To: string(s)}
	}
	o.Status = s
	o.UpdatedAt = now
	return nil
}

// Execute marks the order executed at now in mode with what the execution
// created.
func (o *Order) Execute(mode ExecutionMode, record json.RawMessage, now time.Time) error {
	if !mode.Valid() {
		return core.InvalidEnum("execution mode", string(mode))
	}
	if err := o.to(OrderStatusExecuted, now); err != nil {
		return err
	}
	o.ExecutionMode, o.ExecutedAt, o.ExecutionRecord = mode, &now, slices.Clone(record)
	return nil
}

// Fail marks the order failed at now with the execution record (its
// errors).
func (o *Order) Fail(record json.RawMessage, now time.Time) error {
	if err := o.to(OrderStatusFailed, now); err != nil {
		return err
	}
	o.ExecutionRecord = slices.Clone(record)
	return nil
}

// Executable reports whether a scheduled execution is due at now (lago
// Order.executable).
func (o *Order) Executable(now time.Time) bool {
	return o.Status == OrderStatusCreated && o.ExecuteAt != nil && !o.ExecuteAt.After(now)
}
