package invoicing

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// HandleJob runs an invoicing job (billing runs, drafts, provider taxes,
// in-advance charges and advance charges) and reports whether j is one.
func (s *Service) HandleJob(ctx context.Context, j port.Job) (bool, error) {
	org := j.OrganizationID
	decode := func(v any) error {
		if err := json.Unmarshal(j.Args, v); err != nil {
			return fmt.Errorf("decode %s args: %w", j.Kind, err)
		}
		return nil
	}
	switch j.Kind {
	case kernel.JobBillSubscriptions:
		var a kernel.BillSubscriptionsArgs
		if err := decode(&a); err != nil {
			return true, err
		}
		_, err := s.BillSubscriptions(ctx, org, a.BillingAt)
		return true, err
	case kernel.JobBillSubscription:
		var a kernel.BillSubscriptionArgs
		if err := decode(&a); err != nil {
			return true, err
		}
		_, err := s.BillSubscription(ctx, org, a)
		return true, err
	case kernel.JobBillAdvanceCharges:
		var a kernel.BillSubscriptionArgs
		if err := decode(&a); err != nil {
			return true, err
		}
		return true, s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			_, err := s.AdvanceCharges(ctx, r, org, a.SubscriptionIDs, a.BillingAt)
			return err
		})
	case kernel.JobFinalizeDraft, kernel.JobRefreshDraft, kernel.JobPullProviderTaxes, kernel.JobVoidProviderTaxes:
		var a kernel.InvoiceArgs
		if err := decode(&a); err != nil {
			return true, err
		}
		var err error
		switch j.Kind {
		case kernel.JobFinalizeDraft:
			_, err = s.FinalizeDraft(kernel.WithFinalizationTime(ctx, a.At), org, a.InvoiceID)
		case kernel.JobRefreshDraft:
			_, err = s.RefreshDraft(ctx, org, a.InvoiceID)
		case kernel.JobPullProviderTaxes:
			_, err = s.Retry(ctx, org, a.InvoiceID)
		default:
			err = s.VoidProviderTaxes(ctx, org, a.InvoiceID)
		}
		return true, err
	case kernel.JobFinalizeDueDrafts:
		var a kernel.InstantArgs
		if err := decode(&a); err != nil {
			return true, err
		}
		_, err := s.FinalizeDueDrafts(ctx, org, a.At)
		return true, err
	case kernel.JobRefreshFlaggedDrafts:
		_, err := s.RefreshFlaggedDrafts(ctx, org)
		return true, err
	case kernel.JobCreateInAdvanceChargeFees:
		var a kernel.InAdvanceChargeArgs
		if err := decode(&a); err != nil {
			return true, err
		}
		_, err := s.InAdvanceCharge(ctx, org, a)
		return true, err
	default:
		return false, nil
	}
}
