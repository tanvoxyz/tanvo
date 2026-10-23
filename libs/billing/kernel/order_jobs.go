package kernel

import "github.com/tanvoxyz/tanvo/libs/core"

// Jobs of the order executions.
const (
	// JobExecuteScheduledOrders enqueues the [JobExecuteOrder] of every
	// order of an organization whose scheduled execution is due at the
	// clock instant (lago Clock::ExecuteScheduledOrdersJob,
	// app/jobs/clock/execute_scheduled_orders_job.rb:7-11); its args are
	// [InstantArgs].
	JobExecuteScheduledOrders = "billing.execute_scheduled_orders"
	// JobExecuteOrder executes one order (lago Orders::ExecuteOrderJob,
	// app/jobs/orders/execute_order_job.rb:11-13); its args are
	// [OrderArgs], unique per order ([ExecuteOrderKey]).
	JobExecuteOrder = "billing.execute_order"
)

// OrderArgs are the args of jobs about one order.
type OrderArgs struct {
	// OrderID is the order.
	OrderID core.OrderID `json:"order_id"`
}

// ExecuteOrderKey is the unique key of the [JobExecuteOrder] of order id
// (lago unique :until_executed, app/jobs/orders/execute_order_job.rb:7).
func ExecuteOrderKey(id core.OrderID) string { return "execute_order:" + id.String() }

// orderJobKinds returns the job kinds of the order executions.
func orderJobKinds() []string { return []string{JobExecuteScheduledOrders, JobExecuteOrder} }
