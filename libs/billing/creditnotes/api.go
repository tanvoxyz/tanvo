package creditnotes

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Get returns the finalized credit note id; a draft is not found (lago
// Api::V1::CreditNotesController#show).
func (s *Service) Get(ctx context.Context, org core.OrganizationID, id core.CreditNoteID) (*creditnote.CreditNote, error) {
	return inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*creditnote.CreditNote, error) {
		cn, err := get(ctx, r, org, id, false)
		if err != nil {
			return nil, err
		}
		return cn, withErrors(ctx, r, org, cn)
	})
}

// List returns the finalized credit notes matching f, newest first (lago
// CreditNotesQuery over finalized notes). A filter on an invoice that
// does not exist is invoice not found.
//
// source: app/graphql/resolvers/invoice_credit_notes_resolver.rb:18-28
func (s *Service) List(ctx context.Context, org core.OrganizationID, f creditnote.Filter, page core.PageRequest) (core.Page[*creditnote.CreditNote], error) {
	f.Statuses = []creditnote.Status{creditnote.StatusFinalized}
	f.Order = core.OrderRecent
	return inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (core.Page[*creditnote.CreditNote], error) {
		if !f.InvoiceID.IsZero() {
			if _, err := r.Invoices.Get(ctx, org, f.InvoiceID); errors.Is(err, core.ErrNotFound) {
				return core.Page[*creditnote.CreditNote]{}, notFound("invoice", f.InvoiceID)
			} else if err != nil {
				return core.Page[*creditnote.CreditNote]{}, fmt.Errorf("load invoice %s: %w", f.InvoiceID, err)
			}
		}
		p, err := r.CreditNotes.List(ctx, org, f, page)
		if err != nil {
			return core.Page[*creditnote.CreditNote]{}, fmt.Errorf("list credit notes: %w", err)
		}
		return p, withErrors(ctx, r, org, p.Items...)
	})
}

// InvoiceCurrency returns the currency of the visible invoice id, the
// currency of the amounts of a credit note on it; an invoice still
// generating is not found (lago invoices.visible).
func (s *Service) InvoiceCurrency(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (money.Currency, error) {
	return inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (money.Currency, error) {
		inv, err := visibleInvoice(ctx, r, org, id)
		if err != nil {
			return money.Currency{}, err
		}
		return inv.Currency, nil
	})
}

// Estimation is an estimated credit note with the most of it that may be
// credited, refunded and offset.
type Estimation struct {
	// CreditNote is the estimated note, not stored.
	CreditNote *creditnote.CreditNote
	// MaxCreditable is the most that may be credited, in minor units.
	MaxCreditable int64
	// MaxRefundable is the most that may be refunded, in minor units.
	MaxRefundable int64
	// MaxOffsettable is the most that may be offset against the invoice's
	// due amount, in minor units.
	MaxOffsettable int64
}

// EstimateMaximums estimates the credit note the items would make on the
// visible invoice id with its maximum amounts (lago
// CreditNotes::EstimateService and Types::CreditNotes::Estimate: the
// offsettable amount is the invoice's due amount clamped to the creditable
// one).
func (s *Service) EstimateMaximums(ctx context.Context, org core.OrganizationID, id core.InvoiceID, items []ItemInput) (*Estimation, error) {
	return inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*Estimation, error) {
		if _, err := visibleInvoice(ctx, r, org, id); err != nil {
			return nil, err
		}
		st, err := loadInvoice(ctx, r, org, id)
		if err != nil {
			return nil, err
		}
		cn, err := s.estimate(st, items)
		if err != nil {
			return nil, err
		}
		return &Estimation{
			CreditNote:     cn,
			MaxCreditable:  cn.CreditAmount,
			MaxRefundable:  cn.RefundAmount,
			MaxOffsettable: min(max(st.inv.TotalDueAmount(), 0), cn.CreditAmount),
		}, nil
	})
}

// Download returns the finalized credit note id and whether its document
// is ready; when it has none yet it enqueues the document job, which
// renders it, and returns false (lago
// Api::V1::CreditNotesController#download_pdf).
func (s *Service) Download(ctx context.Context, org core.OrganizationID, id core.CreditNoteID) (*creditnote.CreditNote, bool, error) {
	var ready bool
	cn, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*creditnote.CreditNote, error) {
		cn, err := get(ctx, r, org, id, false)
		if err != nil {
			return nil, err
		}
		if !cn.File.IsZero() {
			ready = true
			return cn, nil
		}
		args := kernel.CreditNoteArgs{CreditNoteID: cn.ID}
		if err := kernel.Enqueue(ctx, r, org, kernel.JobGenerateCreditNoteDocument, args, "", s.d.Now()); err != nil {
			return nil, err
		}
		return cn, nil
	})
	if err != nil {
		return nil, false, err
	}
	return cn, ready, nil
}

// visibleInvoice loads the invoice id unless it is still generating.
func visibleInvoice(ctx context.Context, r port.Repos, org core.OrganizationID, id core.InvoiceID) (*invoice.Invoice, error) {
	inv, err := r.Invoices.Get(ctx, org, id)
	if errors.Is(err, core.ErrNotFound) || (err == nil && !inv.Visible()) {
		return nil, notFound("invoice", id)
	}
	if err != nil {
		return nil, fmt.Errorf("load invoice %s: %w", id, err)
	}
	return inv, nil
}
