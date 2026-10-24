package kernel_test

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

func TestEnqueueReportInvoiceTaxes(t *testing.T) {
	t.Log("source: app/services/subscriptions/activation_rules/payment/resolve_service.rb:46")
	for _, tt := range []struct {
		name   string
		linked bool
		want   int
	}{
		{"customer without tax provider", false, 0},
		{"tax customer", true, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := billingtest.New(t, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
			c := h.Customer()
			inv := core.NewID[core.InvoiceID](h.IDs)
			h.Do(func(ctx context.Context, r port.Repos) error {
				if tt.linked {
					integ := &integration.Integration{
						ID: core.NewID[core.IntegrationID](h.IDs), OrganizationID: h.OrgID(), Code: "anrok", Name: "anrok",
						Category: integration.CategoryTax, Provider: integration.ProviderAnrok, CreatedAt: h.Now(), UpdatedAt: h.Now(),
					}
					if err := r.Integrations.Create(ctx, integ); err != nil {
						return err
					}
					if err := r.IntegrationCustomers.Create(ctx, &integration.IntegrationCustomer{
						ID: core.NewID[core.IntegrationCustomerID](h.IDs), OrganizationID: h.OrgID(), IntegrationID: integ.ID,
						CustomerID: c.ID, Category: integration.CategoryTax, SyncWithProvider: true, CreatedAt: h.Now(), UpdatedAt: h.Now(),
					}); err != nil {
						return err
					}
				}
				return kernel.EnqueueReportInvoiceTaxes(ctx, r, h.OrgID(), c.ID, inv)
			})
			n := 0
			for _, k := range h.JobKinds() {
				if k == kernel.JobReportInvoiceTaxes {
					n++
				}
			}
			if n != tt.want {
				t.Errorf("report jobs: got = %d, want = %d", n, tt.want)
			}
		})
	}
}
