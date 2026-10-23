package catalog

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func TestAddOnValidate(t *testing.T) {
	type tc struct {
		Name               string   `yaml:"name"`
		Source             string   `yaml:"source"`
		Code               *string  `yaml:"code"`
		AddOnName          *string  `yaml:"add_on_name"`
		Amount             int64    `yaml:"amount"`
		Currency           string   `yaml:"currency"`
		InvoiceDisplayName *string  `yaml:"invoice_display_name"`
		WantInvoiceName    *string  `yaml:"want_invoice_name"`
		Errors             []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "add_on.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			a := &AddOn{ID: catalogTestAddOn, OrganizationID: catalogTestOrg, Code: "setup", Name: "Setup", Amount: c.Amount, InvoiceDisplayName: c.InvoiceDisplayName}
			if cur, err := money.LookupCurrency(c.Currency); err == nil {
				a.Currency = cur
			}
			if c.Code != nil {
				a.Code = *c.Code
			}
			if c.AddOnName != nil {
				a.Name = *c.AddOnName
			}
			catalogCheckErrors(t, "Validate()", a.Validate(), c.Errors, c.Source)
			if c.WantInvoiceName != nil {
				if got := a.InvoiceName(); got != *c.WantInvoiceName {
					t.Errorf("InvoiceName() got = %q, want = %q (%s)", got, *c.WantInvoiceName, c.Source)
				}
			}
		})
	}
}
