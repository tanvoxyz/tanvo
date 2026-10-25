package rpc_test

import (
	"context"
	"log/slog"
	"net/http"
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/apps/server/internal/app"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
	subscriptionv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/subscription/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/subscription/v1/subscriptionv1connect"
)

// authGateway is a Stripe adapter that only places pre-authorizations.
type authGateway struct {
	payment.Gateway

	res   payment.PaymentResult
	err   error
	calls []payment.AuthorizeParams
}

func (g *authGateway) Provider() integration.Provider { return integration.ProviderStripe }

func (g *authGateway) Authorize(_ context.Context, _ payment.Connection, p payment.AuthorizeParams) (payment.PaymentResult, error) {
	g.calls = append(g.calls, p)
	return g.res, g.err
}

// newAuthorizationServer is a server whose payments reach gw, with a
// customer of a Stripe connection.
func newAuthorizationServer(t *testing.T, gw *authGateway) (*rpctest.Server, *customer.Customer) {
	t.Helper()
	set, err := port.NewPaymentProviderSet(gw)
	if err != nil {
		t.Fatal(err)
	}
	s := rpctest.New(t, func(o *app.APIOptions) {
		d := &kernel.Deps{Tx: o.Tx, Clock: o.Clock, IDs: core.UUIDv7Generator{}, Logger: slog.New(slog.DiscardHandler)}
		o.Payments = payments.New(d).WithProviders(payments.Providers{Gateways: set})
	})
	cust := s.Fixtures.Customer()
	h := s.Fixtures
	integ := &integration.Integration{
		ID: core.NewID[core.IntegrationID](h.IDs), OrganizationID: h.OrgID(), Code: "stripe", Name: "Stripe",
		Category: integration.CategoryPayment, Provider: integration.ProviderStripe,
		Settings: []byte("{}"), CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	pcID := "cus_12345"
	h.Do(func(ctx context.Context, r port.Repos) error {
		if err := r.Integrations.Create(ctx, integ); err != nil {
			return err
		}
		return r.PaymentProviderCustomers.Create(ctx, &payment.ProviderCustomer{
			ID: core.NewID[core.PaymentProviderCustomerID](h.IDs), OrganizationID: h.OrgID(), CustomerID: cust.ID,
			IntegrationID: integ.ID, ProviderCustomerID: &pcID, CreatedAt: h.Now(), UpdatedAt: h.Now(),
		})
	})
	return s, cust
}

func TestCreateSubscriptionAuthorization(t *testing.T) {
	t.Parallel()
	usd := money.MustCurrency("USD")
	held := payment.PaymentResult{
		ProviderPaymentID: "pi_12345", ProviderStatus: "requires_capture", Status: payment.StateProcessing,
		Amount: 100, AmountCapturable: 100, Currency: usd,
	}
	request := func(cust, plan string) *subscriptionv1.CreateSubscriptionRequest {
		return subscriptionv1.CreateSubscriptionRequest_builder{
			Subscription: subscriptionv1.Subscription_builder{
				ExternalId: new("sub_" + cust), CustomerId: new(rpc.ExternalIDPrefix + cust), PlanId: new(rpc.CodePrefix + plan),
			}.Build(),
			Authorization: subscriptionv1.PaymentAuthorizationInput_builder{
				Amount: commonv1.Money_builder{Currency: new("USD"), Amount: new("1.00")}.Build(),
			}.Build(),
		}.Build()
	}

	t.Run("holds then subscribes", func(t *testing.T) {
		// source: spec/requests/api/v1/subscriptions_controller_spec.rb:472
		gw := &authGateway{res: held}
		s, cust := newAuthorizationServer(t, gw)
		plan := s.Fixtures.Plan(period.Monthly, 1000)
		c := subscriptionv1connect.NewSubscriptionServiceClient(s.Connect(s.APIKey))
		resp, err := c.CreateSubscription(t.Context(), request(cust.ExternalID, plan.Code))
		if err != nil {
			t.Fatalf("CreateSubscription: %v", err)
		}
		a := resp.GetAuthorization()
		if a.GetId() != "pi_12345" || a.GetStatus() != "requires_capture" || a.GetAmount().GetAmount() != "1.00" || a.GetAmountCapturable().GetAmount() != "1.00" {
			t.Errorf("authorization: got = %v, want = pi_12345 requires_capture holding 1.00", a)
		}
		if resp.GetSubscription().GetStatus() != subscriptionv1.SubscriptionStatus_SUBSCRIPTION_STATUS_ACTIVE {
			t.Errorf("subscription: got = %v, want = active", resp.GetSubscription().GetStatus())
		}
		if len(gw.calls) != 1 || gw.calls[0].Amount != 100 || gw.calls[0].ProviderCustomerID != "cus_12345" || gw.calls[0].Metadata["plan_code"] != plan.Code {
			t.Errorf("authorize calls: got = %+v, want = 100 on cus_12345 for %s", gw.calls, plan.Code)
		}
		var jobs int
		if err := s.Admin.QueryRow(t.Context(), `SELECT count(*) FROM job_outbox WHERE kind = $1 AND args->>'provider_payment_id' = 'pi_12345'`,
			kernel.JobCancelAuthorization).Scan(&jobs); err != nil || jobs != 1 {
			t.Errorf("release jobs: got = %d (%v), want = 1", jobs, err)
		}
	})

	t.Run("REST", func(t *testing.T) {
		gw := &authGateway{res: held}
		s, cust := newAuthorizationServer(t, gw)
		plan := s.Fixtures.Plan(period.Monthly, 1000)
		status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/subscriptions?authorization.amount.currency=USD&authorization.amount.amount=1",
			`{"external_id":"sub_rest","customer_id":"external_id:`+cust.ExternalID+`","plan_id":"code:`+plan.Code+`"}`)
		if status != http.StatusOK {
			t.Fatalf("POST /v1/subscriptions = %d %s, want = 200", status, body)
		}
		got := decode[struct {
			Authorization struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"authorization"`
		}](t, body).Authorization
		if got.ID != "pi_12345" || got.Status != "requires_capture" {
			t.Errorf("authorization: got = %+v, want = pi_12345 requires_capture", got)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		gw := &authGateway{}
		s, cust := newAuthorizationServer(t, gw)
		plan := s.Fixtures.Plan(period.Monthly, 1000)
		bare := s.Fixtures.Customer()
		c := subscriptionv1connect.NewSubscriptionServiceClient(s.Connect(s.APIKey))
		tests := []struct {
			name   string
			source string
			cust   string
			err    error
			code   connect.Code
			reason string
		}{
			{
				name: "customer without Stripe", source: "app/controllers/api/v1/subscriptions_controller.rb:35",
				cust: bare.ExternalID, code: connect.CodeFailedPrecondition, reason: payments.CodeStripeRequired,
			},
			{
				name: "no payment method", source: "spec/requests/api/v1/subscriptions_controller_spec.rb:499",
				cust: cust.ExternalID, err: payment.ErrNoPaymentMethod, code: connect.CodeInvalidArgument, reason: rpc.ReasonValidationErrors,
			},
			{
				name: "card declined", source: "spec/requests/api/v1/subscriptions_controller_spec.rb:520",
				cust: cust.ExternalID, code: connect.CodeFailedPrecondition, reason: rpc.ReasonProviderError,
				err: &payment.ProviderError{
					Provider: "stripe", Kind: payment.KindCardDeclined, Code: "card_declined", Message: "Your card was declined.",
					HTTPStatus: http.StatusPaymentRequired, RequestID: "req_R6dwJQCrHDQkZr",
				},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Logf("source: %s", tt.source)
				gw.err = tt.err
				_, err := c.CreateSubscription(t.Context(), request(tt.cust, plan.Code))
				subWantCode(t, err, tt.code, tt.reason)
				if tt.reason == rpc.ReasonValidationErrors {
					subWantViolation(t, err, "payment_method_id", payments.CodeCustomerHasNoPaymentMethod)
				}
				if md := rpc.ErrorMetadata(err); tt.reason == rpc.ReasonProviderError &&
					(md["code"] != "card_declined" || md["request_id"] != "req_R6dwJQCrHDQkZr" || md["http_status"] != "402") {
					t.Errorf("metadata: got = %v, want = card_declined req_R6dwJQCrHDQkZr 402", md)
				}
				list, err := c.ListSubscriptions(t.Context(), subscriptionv1.ListSubscriptionsRequest_builder{CustomerId: new(rpc.ExternalIDPrefix + tt.cust)}.Build())
				if err != nil || len(list.GetSubscriptions()) != 0 {
					t.Errorf("subscriptions: got = %d (%v), want = none created", len(list.GetSubscriptions()), err)
				}
			})
		}
	})
}
