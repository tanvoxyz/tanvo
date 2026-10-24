package rpc

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connectproto"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/tanvoxyz/tanvo/libs/billing/x402pay"
	"github.com/tanvoxyz/tanvo/libs/core/x402"
	x402v1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/x402/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/x402/v1/x402v1connect"
)

// ReasonThirdPartyError is the reason of a failure of a third party the
// RPC relies on, such as an x402 facilitator (lago "third_party_error").
const ReasonThirdPartyError = "third_party_error"

// X402 serves tanvo.x402.v1.X402Service over the x402 payment service
// (lago Api::V1::X402::GateChecksController).
type X402 struct {
	x402v1connect.UnimplementedX402ServiceHandler

	svc *x402pay.Service
}

var _ x402v1connect.X402ServiceHandler = (*X402)(nil)

// NewX402 returns the service over svc.
func NewX402(svc *x402pay.Service) *X402 { return &X402{svc: svc} }

// CheckX402Gate implements X402Service.CheckX402Gate.
//
// source: app/controllers/api/v1/x402/gate_checks_controller.rb
func (s *X402) CheckX402Gate(ctx context.Context, req *x402v1.CheckX402GateRequest) (*x402v1.CheckX402GateResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	in := x402pay.GateCheckInput{
		ConnectionCode:     req.GetConnectionCode(),
		AgentAddress:       Opt(req.HasAgentAddress(), req.GetAgentAddress()),
		Resource:           req.GetResource(),
		PlanCode:           req.GetPlanCode(),
		WalletCode:         req.GetWalletCode(),
		BillableMetricCode: req.GetBillableMetricCode(),
		AmountCents:        Opt(req.HasAmountCents(), req.GetAmountCents()),

		EstimatedCallCostCents: Opt(req.HasEstimatedCallCostCents(), req.GetEstimatedCallCostCents()),
	}
	res, err := s.svc.GateCheck(ctx, p.OrganizationID, in)
	if err != nil {
		return nil, x402Error(ctx, err)
	}
	requirements := make([]*x402v1.X402PaymentRequirement, len(res.Requirements))
	for i, r := range res.Requirements {
		extra, err := structpb.NewStruct(r.Extra)
		if err != nil {
			return nil, Error(ctx, fmt.Errorf("x402 requirement extra: %w", err))
		}
		requirements[i] = x402v1.X402PaymentRequirement_builder{
			Scheme: new(r.Scheme), Network: new(string(r.Network)), Asset: new(string(r.Asset)),
			AssetAddress: new(r.AssetAddress), Extra: extra, PayTo: new(r.PayTo),
			AmountAtomic: new(r.AmountAtomic), MaxTimeoutSeconds: new(int32(r.MaxTimeoutSeconds)),
		}.Build()
	}
	return x402v1.CheckX402GateResponse_builder{
		BalanceCredits:         new(Decimal(res.BalanceCredits)),
		Requirements:           requirements,
		ExternalSubscriptionId: Opt(res.ExternalSubscriptionID != "", res.ExternalSubscriptionID),
	}.Build(), nil
}

// x402Error maps an x402 service error: a facilitator failure is
// Unavailable with reason third_party_error and an ErrorInfo naming the
// facilitator and its failure (lago's 422 third_party_error, whose
// error_details carry the same); anything else is [Error].
func x402Error(ctx context.Context, err error) error {
	tp, ok := errors.AsType[*x402pay.ThirdPartyError](err)
	if !ok {
		return Error(ctx, err)
	}
	detail := tp.Message
	if fe, ok := errors.AsType[*x402.FacilitatorError](tp.Err); ok {
		detail = fe.Message
	}
	e := connect.NewError(connect.CodeUnavailable, tp.Message)
	info := &errdetails.ErrorInfo{
		Reason: ReasonThirdPartyError, Domain: ErrorDomain,
		Metadata: map[string]string{"third_party": string(tp.ThirdParty), "thirdparty_error": detail},
	}
	if d, derr := connectproto.NewErrorDetail(info); derr == nil {
		e = e.WithDetail(d)
	}
	return e
}
