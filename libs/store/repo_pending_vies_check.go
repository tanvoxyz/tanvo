package store

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// pendingVIESCheckRepo implements customer.PendingVIESCheckRepository.
type pendingVIESCheckRepo struct{ t *txState }

var _ customer.PendingVIESCheckRepository = pendingVIESCheckRepo{}

func pendingVIESCheckOf(r db.PendingViesCheck) *customer.PendingVIESCheck {
	return &customer.PendingVIESCheck{
		OrganizationID:          idOf[core.OrganizationID](r.OrganizationID),
		CustomerID:              idOf[core.CustomerID](r.CustomerID),
		BillingEntityID:         idOf[core.BillingEntityID](r.BillingEntityID),
		TaxIdentificationNumber: r.TaxIdentificationNumber,
		AttemptsCount:           int(r.AttemptsCount),
		LastAttemptAt:           optUTC(r.LastAttemptAt),
		LastErrorType:           r.LastErrorType,
		LastErrorMessage:        r.LastErrorMessage,
		CreatedAt:               utc(r.CreatedAt),
		UpdatedAt:               utc(r.UpdatedAt),
	}
}

func (r pendingVIESCheckRepo) Get(ctx context.Context, org core.OrganizationID, c core.CustomerID) (*customer.PendingVIESCheck, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetPendingViesCheck(ctx, db.GetPendingViesCheckParams{OrganizationID: org.UUID(), CustomerID: c.UUID()})
	if err != nil {
		return nil, getErr("pending_vies_check", c, err)
	}
	return pendingVIESCheckOf(row), nil
}

func (r pendingVIESCheckRepo) Save(ctx context.Context, p *customer.PendingVIESCheck) error {
	q, err := r.t.queries(ctx, p.OrganizationID)
	if err != nil {
		return err
	}
	if p.AttemptsCount < 0 || p.AttemptsCount > 1<<30 {
		return fmt.Errorf("pending_vies_check %s: attempts_count %d: %w", p.CustomerID, p.AttemptsCount, core.ErrInvalidValue)
	}
	err = r.t.guard(ctx, func() error {
		return q.UpsertPendingViesCheck(ctx, db.UpsertPendingViesCheckParams{
			OrganizationID:          p.OrganizationID.UUID(),
			CustomerID:              p.CustomerID.UUID(),
			BillingEntityID:         p.BillingEntityID.UUID(),
			TaxIdentificationNumber: p.TaxIdentificationNumber,
			AttemptsCount:           int32(p.AttemptsCount), //nolint:gosec // bounded above
			LastAttemptAt:           optUTC(p.LastAttemptAt),
			LastErrorType:           p.LastErrorType,
			LastErrorMessage:        p.LastErrorMessage,
			CreatedAt:               utc(p.CreatedAt),
			UpdatedAt:               utc(p.UpdatedAt),
		})
	})
	if err != nil {
		return fmt.Errorf("save pending_vies_check %s: %w", p.CustomerID, err)
	}
	return nil
}

func (r pendingVIESCheckRepo) Delete(ctx context.Context, org core.OrganizationID, c core.CustomerID) (bool, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return false, err
	}
	n, err := q.DeletePendingViesCheck(ctx, db.DeletePendingViesCheckParams{OrganizationID: org.UUID(), CustomerID: c.UUID()})
	if err != nil {
		return false, fmt.Errorf("delete pending_vies_check %s: %w", c, mapErr(err))
	}
	return n > 0, nil
}
