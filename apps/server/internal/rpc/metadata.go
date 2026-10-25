package rpc

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	catalogv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1"
	creditnotev1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/creditnote/v1"
	walletv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/wallet/v1"
)

// The metadata RPCs of plans, credit notes and wallets follow lago's
// metadata controllers (app/controllers/api/v1/plans/metadata_controller.rb,
// credit_notes/metadata_controller.rb and
// app/controllers/concerns/wallet_metadata_actions.rb): Replace is create
// (the metadata replaced), Merge is update (partial_metadata), Delete
// removes all the metadata and DeleteKey one key. Each returns the
// metadata after the change.

// metadataError maps the service errors, whose metadata paths are the
// request's, onto the request.
func metadataError(ctx context.Context, what string, err error) error {
	if v, ok := validationViolations("", err); ok {
		return withReason(InvalidArgument("invalid "+what, v), ReasonValidationErrors)
	}
	return Error(ctx, err)
}

// planMetadata sets the metadata of the plan ref (an ID or code:<code>).
func (s *Plans) planMetadata(ctx context.Context, ref string, md core.Metadata, merge bool) (core.Metadata, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	plan, err := s.lookup(p.OrganizationID).Find(ctx, "plan_id", ref)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out, err := s.svc.UpdateMetadata(ctx, p.OrganizationID, plan.ID, md, merge)
	if err != nil {
		return nil, metadataError(ctx, "plan", err)
	}
	return out, nil
}

// ReplacePlanMetadata implements PlanService.ReplacePlanMetadata.
func (s *Plans) ReplacePlanMetadata(ctx context.Context, req *catalogv1.ReplacePlanMetadataRequest) (*catalogv1.ReplacePlanMetadataResponse, error) {
	md, err := s.planMetadata(ctx, req.GetPlanId(), FromMetadata(req.GetMetadata()), false)
	if err != nil {
		return nil, err
	}
	return catalogv1.ReplacePlanMetadataResponse_builder{Metadata: Metadata(md)}.Build(), nil
}

// MergePlanMetadata implements PlanService.MergePlanMetadata.
func (s *Plans) MergePlanMetadata(ctx context.Context, req *catalogv1.MergePlanMetadataRequest) (*catalogv1.MergePlanMetadataResponse, error) {
	md, err := s.planMetadata(ctx, req.GetPlanId(), FromMetadata(req.GetMetadata()), true)
	if err != nil {
		return nil, err
	}
	return catalogv1.MergePlanMetadataResponse_builder{Metadata: Metadata(md)}.Build(), nil
}

// DeletePlanMetadata implements PlanService.DeletePlanMetadata.
func (s *Plans) DeletePlanMetadata(ctx context.Context, req *catalogv1.DeletePlanMetadataRequest) (*catalogv1.DeletePlanMetadataResponse, error) {
	md, err := s.planMetadata(ctx, req.GetPlanId(), nil, false)
	if err != nil {
		return nil, err
	}
	return catalogv1.DeletePlanMetadataResponse_builder{Metadata: Metadata(md)}.Build(), nil
}

// DeletePlanMetadataKey implements PlanService.DeletePlanMetadataKey.
func (s *Plans) DeletePlanMetadataKey(ctx context.Context, req *catalogv1.DeletePlanMetadataKeyRequest) (*catalogv1.DeletePlanMetadataKeyResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	plan, err := s.lookup(p.OrganizationID).Find(ctx, "plan_id", req.GetPlanId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	md, err := s.svc.DeleteMetadataKey(ctx, p.OrganizationID, plan.ID, req.GetKey())
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.DeletePlanMetadataKeyResponse_builder{Metadata: Metadata(md)}.Build(), nil
}

// creditNoteMetadata sets the metadata of the credit note id.
func (s *CreditNotes) creditNoteMetadata(ctx context.Context, id string, md core.Metadata, merge bool) (core.Metadata, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cid, err := ParseID[core.CreditNoteID]("credit_note_id", id)
	if err != nil {
		return nil, err
	}
	cn, err := s.svc.Update(ctx, p.OrganizationID, cid, creditnotes.UpdateInput{Metadata: &md, MergeMetadata: merge})
	if err != nil {
		return nil, metadataError(ctx, "credit note", err)
	}
	return cn.Metadata, nil
}

// ReplaceCreditNoteMetadata implements
// CreditNoteService.ReplaceCreditNoteMetadata.
func (s *CreditNotes) ReplaceCreditNoteMetadata(ctx context.Context, req *creditnotev1.ReplaceCreditNoteMetadataRequest) (*creditnotev1.ReplaceCreditNoteMetadataResponse, error) {
	md, err := s.creditNoteMetadata(ctx, req.GetCreditNoteId(), FromMetadata(req.GetMetadata()), false)
	if err != nil {
		return nil, err
	}
	return creditnotev1.ReplaceCreditNoteMetadataResponse_builder{Metadata: Metadata(md)}.Build(), nil
}

// MergeCreditNoteMetadata implements
// CreditNoteService.MergeCreditNoteMetadata.
func (s *CreditNotes) MergeCreditNoteMetadata(ctx context.Context, req *creditnotev1.MergeCreditNoteMetadataRequest) (*creditnotev1.MergeCreditNoteMetadataResponse, error) {
	md, err := s.creditNoteMetadata(ctx, req.GetCreditNoteId(), FromMetadata(req.GetMetadata()), true)
	if err != nil {
		return nil, err
	}
	return creditnotev1.MergeCreditNoteMetadataResponse_builder{Metadata: Metadata(md)}.Build(), nil
}

// DeleteCreditNoteMetadata implements
// CreditNoteService.DeleteCreditNoteMetadata.
func (s *CreditNotes) DeleteCreditNoteMetadata(ctx context.Context, req *creditnotev1.DeleteCreditNoteMetadataRequest) (*creditnotev1.DeleteCreditNoteMetadataResponse, error) {
	md, err := s.creditNoteMetadata(ctx, req.GetCreditNoteId(), nil, false)
	if err != nil {
		return nil, err
	}
	return creditnotev1.DeleteCreditNoteMetadataResponse_builder{Metadata: Metadata(md)}.Build(), nil
}

// DeleteCreditNoteMetadataKey implements
// CreditNoteService.DeleteCreditNoteMetadataKey.
func (s *CreditNotes) DeleteCreditNoteMetadataKey(ctx context.Context, req *creditnotev1.DeleteCreditNoteMetadataKeyRequest) (*creditnotev1.DeleteCreditNoteMetadataKeyResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cid, err := ParseID[core.CreditNoteID]("credit_note_id", req.GetCreditNoteId())
	if err != nil {
		return nil, err
	}
	md, err := s.svc.DeleteMetadataKey(ctx, p.OrganizationID, cid, req.GetKey())
	if err != nil {
		return nil, Error(ctx, err)
	}
	return creditnotev1.DeleteCreditNoteMetadataKeyResponse_builder{Metadata: Metadata(md)}.Build(), nil
}

// walletMetadata sets the metadata of the wallet id through
// Wallets::UpdateService, as lago does.
func (s *Wallets) walletMetadata(ctx context.Context, id string, md core.Metadata, merge bool) (core.Metadata, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := s.walletGet(ctx, p.OrganizationID, "wallet_id", id)
	if err != nil {
		return nil, err
	}
	w, err := s.svc.Update(ctx, p.OrganizationID, cur.ID, wallets.UpdateInput{Metadata: wallets.To(md), MergeMetadata: merge})
	if err != nil {
		return nil, metadataError(ctx, "wallet", err)
	}
	return w.Metadata, nil
}

// ReplaceWalletMetadata implements WalletService.ReplaceWalletMetadata.
func (s *Wallets) ReplaceWalletMetadata(ctx context.Context, req *walletv1.ReplaceWalletMetadataRequest) (*walletv1.ReplaceWalletMetadataResponse, error) {
	md, err := s.walletMetadata(ctx, req.GetWalletId(), FromMetadata(req.GetMetadata()), false)
	if err != nil {
		return nil, err
	}
	return walletv1.ReplaceWalletMetadataResponse_builder{Metadata: Metadata(md)}.Build(), nil
}

// MergeWalletMetadata implements WalletService.MergeWalletMetadata.
func (s *Wallets) MergeWalletMetadata(ctx context.Context, req *walletv1.MergeWalletMetadataRequest) (*walletv1.MergeWalletMetadataResponse, error) {
	md, err := s.walletMetadata(ctx, req.GetWalletId(), FromMetadata(req.GetMetadata()), true)
	if err != nil {
		return nil, err
	}
	return walletv1.MergeWalletMetadataResponse_builder{Metadata: Metadata(md)}.Build(), nil
}

// DeleteWalletMetadata implements WalletService.DeleteWalletMetadata.
func (s *Wallets) DeleteWalletMetadata(ctx context.Context, req *walletv1.DeleteWalletMetadataRequest) (*walletv1.DeleteWalletMetadataResponse, error) {
	md, err := s.walletMetadata(ctx, req.GetWalletId(), nil, false)
	if err != nil {
		return nil, err
	}
	return walletv1.DeleteWalletMetadataResponse_builder{Metadata: Metadata(md)}.Build(), nil
}

// DeleteWalletMetadataKey implements WalletService.DeleteWalletMetadataKey.
func (s *Wallets) DeleteWalletMetadataKey(ctx context.Context, req *walletv1.DeleteWalletMetadataKeyRequest) (*walletv1.DeleteWalletMetadataKeyResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	w, err := s.walletGet(ctx, p.OrganizationID, "wallet_id", req.GetWalletId())
	if err != nil {
		return nil, err
	}
	md, err := s.svc.DeleteMetadataKey(ctx, p.OrganizationID, w.ID, req.GetKey())
	if err != nil {
		return nil, Error(ctx, err)
	}
	return walletv1.DeleteWalletMetadataKeyResponse_builder{Metadata: Metadata(md)}.Build(), nil
}
