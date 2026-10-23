package invoicing

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// applyCustomSections records on inv copies of the custom sections it
// prints ([kernel.ApplyCustomSections]): the sections ids when given,
// else those the resources select, else the customer's.
func (s *Service) applyCustomSections(ctx context.Context, r port.Repos, cenv *kernel.CustomerEnv, inv *invoice.Invoice, ids []core.InvoiceCustomSectionID, resources ...kernel.CustomSectionResource) error {
	return kernel.ApplyCustomSections(ctx, s.d, r, cenv, inv, ids, resources)
}
