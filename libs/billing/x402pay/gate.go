package x402pay

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/x402"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// The refusals of a gate check, on the base field.
const (
	CodeBuyerNotRecognized            = "buyer_not_recognized"
	CodeWalletNotX402Enabled          = "wallet_not_x402_enabled"
	CodeCustomerNotRefreshable        = "customer_not_refreshable"
	CodeSubscriptionNotActive         = "subscription_not_active"
	CodeCreditPurchasePending         = "credit_purchase_pending"
	CodeAgentAddressFamilyUnsupported = "agent_address_family_unsupported"
	CodePlanCurrencyNotSupported      = "plan_currency_not_supported"
	CodePlanNotUsageOnly              = "plan_not_usage_only"
	CodeChargeNotTargeted             = "charge_not_targeted"
	CodeChargeNotInvoiceable          = "charge_not_invoiceable"
)

const (
	// degradedCallHeadroom is the number of estimated calls a wallet must
	// cover to pass.
	degradedCallHeadroom = 1_000
	// maxAgentAddressLength is the longest address of any family: longer
	// agent addresses are never decoded.
	maxAgentAddressLength = 44
	// routeCurrency is the currency of an x402 plan.
	routeCurrency = "USD"
)

// GateCheckInput is a gate check: the agent's call of a resource.
type GateCheckInput struct {
	// ConnectionCode is the connection's code.
	ConnectionCode string
	// AgentAddress is the agent's wallet address; nil for a stranger.
	AgentAddress *string
	// Resource is the resource called; informative.
	Resource string
	// PlanCode is the usage-only USD plan of the agent's subscription.
	PlanCode string
	// WalletCode is the x402-enabled wallet the calls draw on.
	WalletCode string
	// BillableMetricCode is the metric metering the call.
	BillableMetricCode string
	// AmountCents is the top-up the requirements ask for; positive.
	AmountCents *int64
	// EstimatedCallCostCents is the estimated cost of the call; positive.
	EstimatedCallCostCents *int64
}

// GateCheckResult is the answer of a gate check: the agent passes when
// Requirements is nil.
type GateCheckResult struct {
	// BalanceCredits is the wallet's balance net of its ongoing usage;
	// zero without a wallet.
	BalanceCredits money.Decimal
	// Requirements are the requirements of the top-up to ask for with
	// HTTP 402; nil when the agent passes.
	Requirements []Requirement
	// ExternalSubscriptionID is the agent's active subscription; "" when
	// none.
	ExternalSubscriptionID string
}

// gate is what a gate check reads.
type gate struct {
	in             GateCheckInput
	agent          string
	family         x402.Family
	connection     *x402.Connection
	customer       *customer.Customer
	wallet         *wallet.Wallet
	subscriptions  []*subscription.Subscription
	active         *subscription.Subscription
	pendingPayment bool
	taxError       bool
}

// GateCheck checks whether an agent may call a resource of org: its
// subscription to the plan is active and its wallet covers a thousand
// calls of the estimated cost. Otherwise the result carries the
// requirements of a top-up of the amount. A missing connection is
// connection_not_found; a refusal is a validation failure of base (the
// Code constants), of amount_cents (amount_above_maximum) or of an input
// field; a facilitator failure is a *ThirdPartyError. It creates nothing.
//
// source: app/services/x402/gate_checks/check_service.rb
func (s *Service) GateCheck(ctx context.Context, org core.OrganizationID, in GateCheckInput) (*GateCheckResult, error) {
	g := &gate{in: in}
	if err := g.inputErrors(); err != nil {
		return nil, err
	}
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		return s.readGate(ctx, r, org, g)
	})
	if err != nil {
		return nil, err
	}
	if g.customer == nil && !g.connection.AutoCreateCustomers {
		return nil, refusal(CodeBuyerNotRecognized)
	}
	if g.wallet != nil && !g.wallet.X402Enabled {
		return nil, refusal(CodeWalletNotX402Enabled)
	}
	if g.taxError {
		return nil, refusal(CodeCustomerNotRefreshable)
	}
	if g.active == nil && len(g.subscriptions) > 0 {
		return nil, refusal(CodeSubscriptionNotActive)
	}
	res := &GateCheckResult{BalanceCredits: money.Zero}
	if g.active != nil {
		res.ExternalSubscriptionID = g.active.ExternalID
	}
	if g.wallet != nil {
		res.BalanceCredits = g.wallet.CreditsBalance.Sub(g.wallet.CreditsOngoingUsageBalance)
	}
	if g.active != nil && g.wallet != nil && g.covered() {
		return res, nil
	}
	if m := g.wallet; m != nil && m.PaidTopUpMaxAmount != nil && *in.AmountCents > *m.PaidTopUpMaxAmount {
		return nil, fieldError("amount_cents", wallet.CodeAboveMaximum)
	}
	if g.pendingPayment {
		return nil, refusal(CodeCreditPurchasePending)
	}
	if g.agent != "" && !slices.Contains(g.connection.Families(), g.family) {
		return nil, refusal(CodeAgentAddressFamilyUnsupported)
	}
	reqs, err := s.PaymentRequirements(ctx, g.connection, *in.AmountCents)
	if err != nil {
		return nil, err
	}
	res.Requirements = reqs
	return res, nil
}

// inputErrors checks the input fields and decodes the agent address.
func (g *gate) inputErrors() error {
	var v core.Validator
	v.Required("plan_code", g.in.PlanCode)
	v.Required("billable_metric_code", g.in.BillableMetricCode)
	v.Required("wallet_code", g.in.WalletCode)
	positive := func(field string, n *int64) {
		switch {
		case n == nil:
			v.Add(field, core.CodeRequired)
		case *n <= 0:
			v.Add(field, core.CodeInvalid)
		}
	}
	positive("amount_cents", g.in.AmountCents)
	positive("estimated_call_cost_cents", g.in.EstimatedCallCostCents)
	if a := g.in.AgentAddress; a != nil {
		g.agent = *a
		if g.agent != "" && len(g.agent) <= maxAgentAddressLength {
			g.family = x402.FamilyOfAddress(g.agent)
		}
		v.Check(g.family != "" && x402.ValidAddress(g.agent, g.family), "agent_address", core.CodeInvalidFormat)
	}
	return v.Err()
}

// readGate reads the connection, the route, and the agent's customer,
// wallet, subscriptions and pending credit purchase.
func (s *Service) readGate(ctx context.Context, r port.Repos, org core.OrganizationID, g *gate) error {
	c, err := r.X402Connections.GetByCode(ctx, org, g.in.ConnectionCode)
	if errors.Is(err, core.ErrNotFound) {
		return core.NotFound("connection")
	}
	if err != nil {
		return fmt.Errorf("get x402 connection: %w", err)
	}
	g.connection = c
	if _, err := routeCheck(ctx, r, org, g.in.PlanCode, g.in.BillableMetricCode); err != nil {
		return err
	}
	if g.agent == "" {
		return nil
	}
	normalized := x402.NormalizeAddress(g.agent, g.family)
	if g.pendingPayment, err = r.X402Settlements.PendingCreditPurchaseExists(ctx, org, normalized); err != nil {
		return fmt.Errorf("x402 pending credit purchase: %w", err)
	}
	cust, err := r.Customers.GetByX402AgentAddress(ctx, org, x402.NormalizeAgentAddress(g.agent))
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get customer by x402 agent address: %w", err)
	}
	g.customer = cust
	ds, err := r.CustomerErrorDetails.ListByCustomers(ctx, org, []core.CustomerID{cust.ID})
	if err != nil {
		return fmt.Errorf("list error details of customer %s: %w", cust.ID, err)
	}
	g.taxError = slices.ContainsFunc(ds, func(d *customer.ErrorDetail) bool { return d.Code == customer.ErrorCodeTaxError })
	w, err := r.Wallets.GetByCode(ctx, org, cust.ID, g.in.WalletCode)
	switch {
	case err == nil:
		g.wallet = w
	case !errors.Is(err, core.ErrNotFound):
		return fmt.Errorf("get wallet %q: %w", g.in.WalletCode, err)
	}
	externalID, _ := x402.SubscriptionExternalID(g.agent, g.in.PlanCode, g.family)
	subs, err := r.Subscriptions.ListByCustomer(ctx, org, cust.ID,
		subscription.StatusActive, subscription.StatusPending, subscription.StatusIncomplete)
	if err != nil {
		return fmt.Errorf("list subscriptions: %w", err)
	}
	for _, sub := range subs {
		if sub.ExternalID != externalID {
			continue
		}
		g.subscriptions = append(g.subscriptions, sub)
		if g.active == nil && sub.Status == subscription.StatusActive {
			g.active = sub
		}
	}
	return nil
}

// covered reports whether the wallet's balance net of its ongoing usage
// covers the headroom of estimated calls.
func (g *gate) covered() bool {
	cost := *g.in.EstimatedCallCostCents
	if cost > math.MaxInt64/degradedCallHeadroom {
		return false
	}
	return g.wallet.BalanceAmount-g.wallet.OngoingUsageBalanceAmount >= degradedCallHeadroom*cost
}

// routeCheck checks the route of a gate check: the original plan with
// planCode is a usage-only USD plan whose charges on the metric all
// target the event's wallet and are invoiceable when paid in advance.
// It returns the plan; plan_not_found or charge_not_found when either is
// missing, a base refusal otherwise.
//
// source: app/services/x402/gate_checks/route_check_service.rb
func routeCheck(ctx context.Context, r port.Repos, org core.OrganizationID, planCode, metricCode string) (*catalog.Plan, error) {
	p, err := r.Plans.GetByCode(ctx, org, planCode)
	if errors.Is(err, core.ErrNotFound) {
		return nil, core.NotFound("plan")
	}
	if err != nil {
		return nil, fmt.Errorf("get plan %q: %w", planCode, err)
	}
	all, err := r.Charges.ListByPlan(ctx, org, p.ID)
	if err != nil {
		return nil, fmt.Errorf("list charges: %w", err)
	}
	var charges []*catalog.Charge
	m, err := r.Metrics.GetByCode(ctx, org, metricCode)
	switch {
	case err == nil:
		for _, c := range all {
			if c.BillableMetricID == m.ID {
				charges = append(charges, c)
			}
		}
	case !errors.Is(err, core.ErrNotFound):
		return nil, fmt.Errorf("get billable metric %q: %w", metricCode, err)
	}
	if len(charges) == 0 {
		return nil, core.NotFound("charge")
	}
	if p.Currency.String() != routeCurrency {
		return nil, refusal(CodePlanCurrencyNotSupported)
	}
	usageOnly, err := usageOnly(ctx, r, p, all)
	if err != nil {
		return nil, err
	}
	if !usageOnly {
		return nil, refusal(CodePlanNotUsageOnly)
	}
	// Lago also requires the events targeting wallets premium integration;
	// Tanvo has no premium gates.
	if !all1(charges, func(c *catalog.Charge) bool { return c.AcceptsTargetWallet }) {
		return nil, refusal(CodeChargeNotTargeted)
	}
	if slices.ContainsFunc(charges, func(c *catalog.Charge) bool { return c.PayInAdvance && !c.Invoiceable }) {
		return nil, refusal(CodeChargeNotInvoiceable)
	}
	return p, nil
}

// usageOnly reports whether p has no base fee, no minimum commitment, no
// fixed charge and no charge minimum.
func usageOnly(ctx context.Context, r port.Repos, p *catalog.Plan, charges []*catalog.Charge) (bool, error) {
	if p.Amount != 0 || slices.ContainsFunc(charges, func(c *catalog.Charge) bool { return c.MinAmount >= 1 }) {
		return false, nil
	}
	_, err := r.Commitments.GetByPlan(ctx, p.OrganizationID, p.ID, catalog.CommitmentTypeMinimumCommitment)
	switch {
	case err == nil:
		return false, nil
	case !errors.Is(err, core.ErrNotFound):
		return false, fmt.Errorf("get minimum commitment: %w", err)
	}
	fixed, err := r.FixedCharges.ListByPlan(ctx, p.OrganizationID, p.ID)
	if err != nil {
		return false, fmt.Errorf("list fixed charges: %w", err)
	}
	return len(fixed) == 0, nil
}

// all1 reports whether pred holds for every element of s.
func all1[T any](s []T, pred func(T) bool) bool {
	for _, x := range s {
		if !pred(x) {
			return false
		}
	}
	return true
}
