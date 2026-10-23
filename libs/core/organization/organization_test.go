package organization

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
)

var testNow = time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)

func fieldErrors(err error) []string {
	out := []string{}
	for _, fe := range core.FieldErrors(err) {
		out = append(out, fe.String())
	}
	return out
}

func TestBillingEntityValidate(t *testing.T) {
	type tc struct {
		Name                  string   `yaml:"name"`
		Source                string   `yaml:"source"`
		EntityName            *string  `yaml:"entity_name"`
		DocumentNumberPrefix  *string  `yaml:"document_number_prefix"`
		Email                 *string  `yaml:"email"`
		Country               *string  `yaml:"country"`
		DocumentLocale        *string  `yaml:"document_locale"`
		FooterLength          int      `yaml:"footer_length"`
		EmailSettings         []string `yaml:"email_settings"`
		IssuingDateAnchor     *string  `yaml:"issuing_date_anchor"`
		IssuingDateAdjustment *string  `yaml:"issuing_date_adjustment"`
		EInvoicing            bool     `yaml:"einvoicing"`
		EUTaxManagement       bool     `yaml:"eu_tax_management"`
		GracePeriodDays       int      `yaml:"grace_period_days"`
		Errors                []string `yaml:"errors"`
	}
	id := core.MustParseID[core.BillingEntityID]("0190a0a0-0000-7000-8000-00000000abcd")
	for _, c := range spectest.Load[tc](t, "billing_entity.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			b := NewBillingEntity(core.OrganizationID{}, id, "acme", "Acme Corp", testNow)
			b.Address.Country = "FR"
			if c.EntityName != nil {
				b.Name = *c.EntityName
			}
			if c.DocumentNumberPrefix != nil {
				b.DocumentNumberPrefix = NormalizeDocumentNumberPrefix(*c.DocumentNumberPrefix)
			}
			b.Email = c.Email
			if c.Country != nil {
				b.Address.Country = *c.Country
			}
			b.Address = b.Address.Normalize()
			if c.DocumentLocale != nil {
				b.DocumentLocale = *c.DocumentLocale
			}
			if c.FooterLength > 0 {
				f := strings.Repeat("a", c.FooterLength)
				b.InvoiceFooter = &f
			}
			for _, s := range c.EmailSettings {
				b.EmailSettings = append(b.EmailSettings, EmailSetting(s))
			}
			if c.IssuingDateAnchor != nil {
				b.IssuingDateAnchor = core.IssuingDateAnchor(*c.IssuingDateAnchor)
			}
			if c.IssuingDateAdjustment != nil {
				b.IssuingDateAdjustment = core.IssuingDateAdjustment(*c.IssuingDateAdjustment)
			}
			b.EInvoicing = c.EInvoicing
			b.EUTaxManagement = c.EUTaxManagement
			b.InvoiceGracePeriodDays = c.GracePeriodDays
			if got := fieldErrors(b.Validate()); !slices.Equal(got, c.Errors) {
				t.Errorf("Validate() errors got = %v, want = %v (%s)", got, c.Errors, c.Source)
			}
		})
	}
}

func TestDefaultDocumentNumberPrefix(t *testing.T) {
	id := core.MustParseID[core.BillingEntityID]("0190a0a0-0000-7000-8000-00000000abcd")
	tests := []struct {
		name, entity, want string
	}{
		{"first three letters and last four id characters", "Acme Corp", "ACM-ABCD"}, // spec/models/billing_entity_spec.rb:265
		{"non-letters are skipped", "1-a b&c", "ABC-ABCD"},
		{"short names", "Zé", "Z-ABCD"},
	}
	for _, tt := range tests {
		if got := DefaultDocumentNumberPrefix(tt.entity, id); got != tt.want {
			t.Errorf("%s: DefaultDocumentNumberPrefix(%q) got = %q, want = %q", tt.name, tt.entity, got, tt.want)
		}
	}
}

func TestBillingEntityArchive(t *testing.T) {
	b := NewBillingEntity(core.OrganizationID{}, core.BillingEntityID{}, "acme", "Acme", testNow)
	b.IsDefault = true
	if err := b.Archive(testNow); err != nil {
		t.Fatalf("Archive() error got = %v, want = nil", err)
	}
	if !b.Archived() || b.IsDefault {
		t.Errorf("Archive() got = archived %v default %v, want = archived true default false", b.Archived(), b.IsDefault)
	}
	if err := b.Archive(testNow); err == nil {
		t.Errorf("second Archive() error got = nil, want = core.ErrInvalidTransition")
	}
}

func TestEUVATEligible(t *testing.T) {
	// spec/models/billing_entity_spec.rb:332
	for country, want := range map[string]bool{"": false, "US": false, "FR": true} {
		b := &BillingEntity{Address: core.Address{Country: country}}
		if got := b.EUVATEligible(); got != want {
			t.Errorf("EUVATEligible() with country %q got = %v, want = %v", country, got, want)
		}
	}
}

func TestOrganizationValidate(t *testing.T) {
	o := New(core.OrganizationID{}, "Acme, Inc.", testNow)
	if o.Slug != "acme-inc" {
		t.Errorf("Slugify got = %q, want = %q", o.Slug, "acme-inc")
	}
	if err := o.Validate(); err != nil {
		t.Errorf("Validate() got = %v, want = nil", err)
	}
	o.AuthenticationMethods = nil
	o.Name = ""
	want := []string{"name: value_is_mandatory", "authentication_methods: value_is_mandatory"}
	if got := fieldErrors(o.Validate()); !slices.Equal(got, want) {
		t.Errorf("Validate() errors got = %v, want = %v", got, want)
	}
}
