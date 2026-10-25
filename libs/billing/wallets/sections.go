package wallets

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
)

// CustomSections selects the invoice custom sections of a wallet, a
// recurring rule or a transaction (lago invoice_custom_section, applied by
// InvoiceCustomSections::AttachToResourceService).
type CustomSections struct {
	// Skip, when set, turns the custom sections off (true) or on (false).
	Skip *bool
	// Sections, when not nil, are the selected sections (IDs or codes);
	// unknown and deleted ones are ignored, an empty list clears them.
	Sections []refs.Ref
}

// attachSections returns cur with the selection cs applied, as lago
// InvoiceCustomSections::AttachToResourceService does
// (app/services/invoice_custom_sections/attach_to_resource_service.rb):
// an explicit skip turns the sections off and clears them; otherwise,
// unless cur skips them, the selection replaces them. A nil cs keeps cur.
func attachSections(ctx context.Context, r port.Repos, org core.OrganizationID, cur wallet.CustomSections, cs *CustomSections) (wallet.CustomSections, error) {
	if cs == nil {
		return cur, nil
	}
	out := wallet.CustomSections{Skip: cur.Skip, IDs: slices.Clone(cur.IDs)}
	if cs.Skip != nil {
		out.Skip = *cs.Skip
		if out.Skip {
			out.IDs = nil
			return out, nil
		}
	} else if cur.Skip {
		return out, nil
	}
	if cs.Sections == nil {
		return out, nil
	}
	ids := []core.InvoiceCustomSectionID{}
	for _, ref := range cs.Sections {
		sec, err := findSection(ctx, r, org, ref)
		if err != nil {
			return wallet.CustomSections{}, err
		}
		if sec != nil && !slices.Contains(ids, sec.ID) {
			ids = append(ids, sec.ID)
		}
	}
	out.IDs = ids
	return out, nil
}

// findSection returns the live section ref, or nil when there is none.
func findSection(ctx context.Context, r port.Repos, org core.OrganizationID, ref refs.Ref) (*invoice.CustomSection, error) {
	var (
		sec *invoice.CustomSection
		err error
	)
	if ref.Code != "" {
		sec, err = r.InvoiceCustomSections.GetByCode(ctx, org, ref.Code)
	} else {
		var id core.InvoiceCustomSectionID
		if id, err = core.ParseID[core.InvoiceCustomSectionID](ref.ID.String()); err == nil {
			sec, err = r.InvoiceCustomSections.Get(ctx, org, id)
		}
	}
	if errors.Is(err, core.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load invoice custom section %s: %w", ref, err)
	}
	return sec, nil
}

// liveSections returns the IDs of cs that name sections not deleted, in
// their order (lago selected_invoice_custom_sections, whose sections are
// kept ones only).
func liveSections(ctx context.Context, r port.Repos, org core.OrganizationID, cs wallet.CustomSections) ([]core.InvoiceCustomSectionID, error) {
	if len(cs.IDs) == 0 {
		return nil, nil
	}
	secs, err := r.InvoiceCustomSections.ListByIDs(ctx, org, cs.IDs)
	if err != nil {
		return nil, fmt.Errorf("list invoice custom sections: %w", err)
	}
	return slices.DeleteFunc(slices.Clone(cs.IDs), func(id core.InvoiceCustomSectionID) bool {
		return !slices.ContainsFunc(secs, func(s *invoice.CustomSection) bool { return s.ID == id })
	}), nil
}

// ruleSections returns the selection a top-up of rule takes (lago
// RecurringTransactionRule#invoice_custom_section_params,
// app/models/recurring_transaction_rule.rb:88-93): none when the rule
// neither skips nor selects sections, else its skip and live sections.
func ruleSections(ctx context.Context, r port.Repos, rule *wallet.RecurringTransactionRule) (wallet.CustomSections, error) {
	if !rule.InvoiceCustomSections.Set() {
		return wallet.CustomSections{}, nil
	}
	if rule.InvoiceCustomSections.Skip {
		return wallet.CustomSections{Skip: true}, nil
	}
	ids, err := liveSections(ctx, r, rule.OrganizationID, rule.InvoiceCustomSections)
	return wallet.CustomSections{IDs: ids}, err
}

// sectionResource returns the custom section resource of the credit
// invoice of t (lago WalletTransaction#invoice_custom_section_resource,
// app/models/wallet_transaction.rb:113-118): the transaction when it skips
// or selects live sections, else the wallet when it does, else the
// transaction.
func sectionResource(ctx context.Context, r port.Repos, w *wallet.Wallet, t *wallet.Transaction) (kernel.CustomSectionResource, error) {
	for _, cs := range []wallet.CustomSections{t.InvoiceCustomSections, w.InvoiceCustomSections} {
		ids, err := liveSections(ctx, r, t.OrganizationID, cs)
		if err != nil {
			return kernel.CustomSectionResource{}, err
		}
		if cs.Skip || len(ids) > 0 {
			return kernel.CustomSectionResource{Skip: cs.Skip, Selected: ids}, nil
		}
	}
	return kernel.CustomSectionResource{}, nil
}
