package kernel

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// CustomSectionResource is a resource whose custom section selection an
// invoice it bills follows (lago
// Invoices::ApplyInvoiceCustomSectionsService::Resource): a subscription,
// a wallet or a wallet transaction.
type CustomSectionResource struct {
	// Skip leaves the resource out of the selection.
	Skip bool
	// Selected are the sections the resource selects.
	Selected []core.InvoiceCustomSectionID
}

// SubscriptionSectionResource is the custom section resource of sub.
func SubscriptionSectionResource(sub *subscription.Subscription) CustomSectionResource {
	return CustomSectionResource{Skip: sub.SkipInvoiceCustomSections, Selected: sub.InvoiceCustomSectionIDs}
}

// ApplyCustomSections records on inv copies of the custom sections it
// prints (lago Invoices::ApplyInvoiceCustomSectionsService,
// app/services/invoices/apply_invoice_custom_sections_service.rb). The
// manual sections are, in order of precedence:
//
//   - ids, when given (a one-off invoice);
//   - with resources, the union of the sections the resources not
//     skipping them select, plus the customer's configurable ones when one
//     of those resources selects none; the customer's configurable ones
//     when none selects any;
//   - else the customer's configurable sections (none when the customer
//     skips them, else its selection, else its billing entity's, by name).
//
// The customer's system-generated sections follow. Nothing is applied
// when no participating resource selects a section and either every
// resource skips them, or, without ids, the customer skips them.
func ApplyCustomSections(ctx context.Context, d *Deps, r port.Repos, cenv *CustomerEnv, inv *invoice.Invoice, ids []core.InvoiceCustomSectionID, resources []CustomSectionResource) error {
	c := cenv.Customer
	org := inv.OrganizationID
	list := func(ids []core.InvoiceCustomSectionID) ([]*invoice.CustomSection, error) {
		if len(ids) == 0 {
			return nil, nil
		}
		out, err := r.InvoiceCustomSections.ListByIDs(ctx, org, ids)
		if err != nil {
			return nil, fmt.Errorf("list invoice custom sections: %w", err)
		}
		return out, nil
	}
	// selected_invoice_custom_sections of each participating resource.
	var with, without int
	var chosen []*invoice.CustomSection
	for _, res := range resources {
		if res.Skip {
			continue
		}
		sel, err := list(res.Selected)
		if err != nil {
			return err
		}
		if len(sel) == 0 {
			without++
			continue
		}
		with++
		chosen = append(chosen, sel...)
	}
	switch {
	case with > 0:
	case len(resources) > 0 && without == 0:
		return nil
	case len(ids) > 0:
	case c.SkipInvoiceCustomSections:
		return nil
	}
	configurable := func() ([]*invoice.CustomSection, error) {
		out, err := list(c.ConfigurableInvoiceCustomSections(cenv.Defaults))
		slices.SortStableFunc(out, func(a, b *invoice.CustomSection) int { return cmp.Compare(a.Name, b.Name) })
		return out, err
	}
	var manual []*invoice.CustomSection
	var err error
	switch {
	case len(ids) > 0:
		manual, err = list(ids)
	case with > 0:
		manual = chosen
		if without > 0 {
			var more []*invoice.CustomSection
			more, err = configurable()
			manual = append(manual, more...)
		}
	default:
		manual, err = configurable()
	}
	if err != nil {
		return err
	}
	system, err := list(c.SystemInvoiceCustomSectionIDs)
	if err != nil {
		return err
	}
	now := d.Now()
	seen := map[core.InvoiceCustomSectionID]bool{}
	for _, cs := range slices.Concat(manual, system) {
		if seen[cs.ID] {
			continue
		}
		seen[cs.ID] = true
		a := invoice.AppliedCustomSectionOf(core.NewID[core.AppliedInvoiceCustomSectionID](d.IDs), inv.ID, cs, now)
		if err := r.AppliedInvoiceCustomSections.Create(ctx, a); err != nil {
			return fmt.Errorf("apply invoice custom section %s: %w", cs.Code, err)
		}
	}
	return nil
}
