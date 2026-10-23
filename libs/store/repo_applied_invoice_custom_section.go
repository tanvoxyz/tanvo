package store

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// appliedCustomSectionRepo implements invoice.AppliedCustomSectionRepository.
type appliedCustomSectionRepo struct{ t *txState }

var _ invoice.AppliedCustomSectionRepository = appliedCustomSectionRepo{}

func appliedCustomSectionOf(r db.AppliedInvoiceCustomSection) (*invoice.AppliedCustomSection, error) {
	return &invoice.AppliedCustomSection{
		ID:             idOf[core.AppliedInvoiceCustomSectionID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		InvoiceID:      idOf[core.InvoiceID](r.InvoiceID),
		Code:           r.Code,
		Name:           r.Name,
		DisplayName:    r.DisplayName,
		Details:        r.Details,
		CreatedAt:      utc(r.CreatedAt),
	}, nil
}

func (r appliedCustomSectionRepo) Create(ctx context.Context, a *invoice.AppliedCustomSection) error {
	q, err := r.t.queries(ctx, a.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateAppliedInvoiceCustomSection(ctx, db.CreateAppliedInvoiceCustomSectionParams{
		OrganizationID: a.OrganizationID.UUID(),
		ID:             a.ID.UUID(),
		InvoiceID:      a.InvoiceID.UUID(),
		Code:           a.Code,
		Name:           a.Name,
		DisplayName:    a.DisplayName,
		Details:        a.Details,
		CreatedAt:      utc(a.CreatedAt),
	})
	return insertErr("applied_invoice_custom_section", n, err)
}

func (r appliedCustomSectionRepo) ListByInvoice(ctx context.Context, org core.OrganizationID, inv core.InvoiceID) ([]*invoice.AppliedCustomSection, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListAppliedInvoiceCustomSectionsByInvoice(ctx, db.ListAppliedInvoiceCustomSectionsByInvoiceParams{
		OrganizationID: org.UUID(), InvoiceID: inv.UUID(),
	})
	if err != nil {
		return nil, fmt.Errorf("list applied invoice custom sections: %w", mapErr(err))
	}
	return convAll(rows, appliedCustomSectionOf)
}

func (r appliedCustomSectionRepo) DeleteByInvoice(ctx context.Context, org core.OrganizationID, inv core.InvoiceID) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	if err := q.DeleteAppliedInvoiceCustomSectionsByInvoice(ctx, db.DeleteAppliedInvoiceCustomSectionsByInvoiceParams{
		OrganizationID: org.UUID(), InvoiceID: inv.UUID(),
	}); err != nil {
		return fmt.Errorf("delete applied invoice custom sections: %w", mapErr(err))
	}
	return nil
}
