package integrations

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// CreditNoteSync is the state of a credit note at its customer's
// accounting and tax integrations (lago Types::CreditNotes::Object).
type CreditNoteSync struct {
	// Syncable reports a finalized credit note that the customer's
	// accounting integration syncs and that it does not have yet: the one
	// RetrySync pushes.
	//
	// source: app/graphql/types/credit_notes/object.rb:67-73
	Syncable bool
	// ExternalID is the credit note's ID at the accounting integration.
	//
	// source: app/graphql/types/credit_notes/object.rb:82-93
	ExternalID string
	// TaxProviderID is the credit note's ID at the tax integration.
	//
	// source: app/graphql/types/credit_notes/object.rb:95-100
	TaxProviderID string
}

// CreditNoteSyncs returns the sync state of each of cns, in order. The
// accounting integration is the customer's default accounting link, the
// one credit notes are synced to.
func (s *Service) CreditNoteSyncs(ctx context.Context, org core.OrganizationID, cns ...*creditnote.CreditNote) ([]CreditNoteSync, error) {
	out := make([]CreditNoteSync, len(cns))
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		type links struct{ accounting, tax *integration.Integration }
		byCustomer := map[core.CustomerID]links{}
		for i, cn := range cns {
			l, ok := byCustomer[cn.CustomerID]
			if !ok {
				var err error
				if l.accounting, l.tax, err = s.creditNoteLinks(ctx, r, org, cn.CustomerID); err != nil {
					return err
				}
				byCustomer[cn.CustomerID] = l
			}
			if l.accounting != nil {
				ext, err := resourceID(ctx, r, l.accounting, cn)
				if err != nil {
					return err
				}
				out[i].ExternalID = ext
				out[i].Syncable = cn.Status == creditnote.StatusFinalized && l.accounting.SyncCreditNotes && ext == ""
			}
			if l.tax != nil {
				ext, err := resourceID(ctx, r, l.tax, cn)
				if err != nil {
					return err
				}
				out[i].TaxProviderID = ext
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// creditNoteLinks loads the accounting and tax integrations of the
// customer id; each is nil when the customer has none.
func (s *Service) creditNoteLinks(ctx context.Context, r port.Repos, org core.OrganizationID, id core.CustomerID) (accounting, tax *integration.Integration, err error) {
	c := &customer.Customer{ID: id, OrganizationID: org}
	ic, err := defaultLink(ctx, r, c, integration.CategoryAccounting)
	if err != nil {
		return nil, nil, err
	}
	if ic != nil {
		if accounting, err = r.Integrations.Get(ctx, org, ic.IntegrationID); err != nil {
			return nil, nil, fmt.Errorf("load integration %s: %w", ic.IntegrationID, err)
		}
	}
	if ic, err = taxCustomer(ctx, r, c); err != nil {
		return nil, nil, err
	}
	if ic != nil {
		if tax, err = r.Integrations.Get(ctx, org, ic.IntegrationID); err != nil {
			return nil, nil, fmt.Errorf("load integration %s: %w", ic.IntegrationID, err)
		}
	}
	return accounting, tax, nil
}

// resourceID returns the ID of cn at the integration i; empty when it has
// none.
func resourceID(ctx context.Context, r port.Repos, i *integration.Integration, cn *creditnote.CreditNote) (string, error) {
	res, err := r.IntegrationResources.Find(ctx, i.OrganizationID, i.ID, integration.ResourceTypeCreditNote, cn.ID.UUID())
	switch {
	case errors.Is(err, core.ErrNotFound):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("find credit note %s at integration %s: %w", cn.ID, i.ID, err)
	}
	return res.ExternalID, nil
}
