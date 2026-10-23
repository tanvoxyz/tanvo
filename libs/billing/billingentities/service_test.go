package billingentities_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingentities"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/dunning"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
	"github.com/tanvoxyz/tanvo/libs/tax"
)

var now = time.Date(2024, 3, 15, 10, 0, 0, 0, time.UTC)

// png is the start of a PNG file, enough for content sniffing.
var png = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

// blobs is an in-memory blob store.
type blobs struct {
	files map[core.BlobRef][]byte
	n     int
}

func (b *blobs) Put(_ context.Context, _ core.OrganizationID, name, _ string, body io.Reader) (core.BlobRef, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	b.n++
	ref := core.BlobRef(fmt.Sprintf("%d/%s", b.n, name))
	b.files[ref] = data
	return ref, nil
}

func (b *blobs) Get(_ context.Context, _ core.OrganizationID, ref core.BlobRef) (io.ReadCloser, error) {
	data, ok := b.files[ref]
	if !ok {
		return nil, core.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (b *blobs) Delete(_ context.Context, _ core.OrganizationID, ref core.BlobRef) error {
	delete(b.files, ref)
	return nil
}

func (b *blobs) URL(_ context.Context, _ core.OrganizationID, ref core.BlobRef) (string, error) {
	return "https://blobs.test/" + string(ref), nil
}

var _ port.BlobStore = (*blobs)(nil)

func setup(t *testing.T) (*billingtest.Harness, *billingentities.Service, *blobs) {
	t.Helper()
	h := billingtest.New(t, now)
	b := &blobs{files: map[core.BlobRef][]byte{}}
	s, err := billingentities.New(h.Deps, b)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return h, s, b
}

func ptr[T any](v T) *T { return &v }

// wantFieldError fails unless err carries the field error.
func wantFieldError(t *testing.T, err error, field, code string) {
	t.Helper()
	var ve *core.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a validation error", err)
	}
	if !ve.Has(field, code) {
		t.Fatalf("errors = %v, want %s %s", ve.Errors, field, code)
	}
}

// wantNotFound fails unless err is core.NotFound(resource).
func wantNotFound(t *testing.T, err error, resource string) {
	t.Helper()
	var nf *core.NotFoundError
	if !errors.As(err, &nf) || nf.Resource != resource {
		t.Fatalf("error = %v, want %s not found", err, resource)
	}
}

func reload(h *billingtest.Harness, id core.BillingEntityID) *organization.BillingEntity {
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) (*organization.BillingEntity, error) {
		return r.BillingEntities.Get(ctx, h.OrgID(), id)
	})
}

func section(h *billingtest.Harness, code string) *invoice.CustomSection {
	s := &invoice.CustomSection{
		ID: core.NewID[core.InvoiceCustomSectionID](h.IDs), OrganizationID: h.OrgID(), Code: code, Name: code,
		SectionType: invoice.CustomSectionTypeManual, CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	h.Do(func(ctx context.Context, r port.Repos) error { return r.InvoiceCustomSections.Create(ctx, s) })
	return s
}

func campaign(h *billingtest.Harness, code string) *dunning.Campaign {
	c := dunning.New(h.OrgID(), core.NewID[core.DunningCampaignID](h.IDs), code, code, h.Now())
	c.DaysBetweenAttempts, c.MaxAttempts = 1, 3
	h.Do(func(ctx context.Context, r port.Repos) error { return r.DunningCampaigns.Create(ctx, c) })
	return c
}

func draft(h *billingtest.Harness, c *customer.Customer, issuing core.Date, opts ...func(*invoice.Invoice)) *invoice.Invoice {
	return h.StoredInvoice(c, append([]func(*invoice.Invoice){func(inv *invoice.Invoice) {
		inv.Status = invoice.StatusDraft
		inv.IssuingDate, inv.ExpectedFinalizationDate, inv.PaymentDueDate = issuing, issuing, issuing
	}}, opts...)...)
}

func TestCreate(t *testing.T) {
	// source: spec/services/billing_entities/create_service_spec.rb:112
	t.Run("defaults", func(t *testing.T) {
		h, s, _ := setup(t)
		be, err := s.Create(h.Ctx, h.OrgID(), "second", billingentities.Input{Name: core.Some("Second Entity")})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if be.DocumentNumbering != organization.DocumentNumberingPerCustomer {
			t.Errorf("DocumentNumbering = %q, want per_customer", be.DocumentNumbering)
		}
		if want := organization.DefaultDocumentNumberPrefix("Second Entity", be.ID); be.DocumentNumberPrefix != want {
			t.Errorf("DocumentNumberPrefix = %q, want %q", be.DocumentNumberPrefix, want)
		}
		if be.IsDefault {
			t.Errorf("IsDefault = true, want false: the harness entity is the default")
		}
		if be.EUTaxManagement {
			t.Errorf("EUTaxManagement = true, want false when not provided")
		}
		if got := reload(h, be.ID); got.Name != "Second Entity" || got.Code != "second" {
			t.Errorf("stored = %q %q, want second Second Entity", got.Code, got.Name)
		}
	})

	// source: spec/services/billing_entities/create_service_spec.rb:162
	t.Run("full data", func(t *testing.T) {
		h, s, _ := setup(t)
		h.Tax("vat", "20")
		sec := section(h, "sec")
		be, err := s.Create(h.Ctx, h.OrgID(), "full", billingentities.Input{
			Name:                      core.Some("Full"),
			LegalName:                 core.Some(ptr("Full SAS")),
			LegalNumber:               core.Some(ptr("1234")),
			TaxIdentificationNumber:   core.Some(ptr("FR123")),
			Email:                     core.Some(ptr("billing@full.test")),
			Phone:                     core.Some(ptr("+33 1")),
			AddressLine1:              core.Some("1 rue"),
			City:                      core.Some("Paris"),
			Zipcode:                   core.Some("75001"),
			Country:                   core.Some("fr"),
			Timezone:                  core.Some(must(time.LoadLocation("Europe/Paris"))),
			DefaultCurrency:           core.Some(money.MustCurrency("EUR")),
			DocumentLocale:            core.Some("fr"),
			DocumentNumbering:         core.Some(organization.DocumentNumberingPerBillingEntity),
			DocumentNumberPrefix:      core.Some("full"),
			InvoiceFooter:             core.Some(ptr("footer")),
			InvoiceGracePeriodDays:    core.Some(3),
			NetPaymentTermDays:        core.Some(30),
			FinalizeZeroAmountInvoice: core.Some(false),
			IssuingDateAnchor:         core.Some(core.IssuingDateAnchorCurrentPeriodEnd),
			IssuingDateAdjustment:     core.Some(core.IssuingDateAdjustmentKeepAnchor),
			EmailSettings:             core.Some([]organization.EmailSetting{organization.EmailSettingInvoiceFinalized}),
			Taxes:                     core.Some([]refs.Ref{refs.ByCode("vat")}),
			InvoiceCustomSections:     core.Some([]refs.Ref{refs.ByID(sec.ID)}),
		})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		got := reload(h, be.ID)
		if got.Address.Country != "FR" || got.Timezone.String() != "Europe/Paris" || got.DefaultCurrency.Code() != "EUR" ||
			got.DocumentNumberPrefix != "FULL" || got.NetPaymentTermDays != 30 || got.InvoiceGracePeriodDays != 3 ||
			got.FinalizeZeroAmountInvoice || got.IssuingDateAnchor != core.IssuingDateAnchorCurrentPeriodEnd ||
			len(got.EmailSettings) != 1 || len(got.TaxIDs) != 1 || len(got.InvoiceCustomSectionIDs) != 1 {
			t.Errorf("stored = %+v, want the full data", got)
		}
	})

	// source: spec/services/billing_entities/create_service_spec.rb:198
	t.Run("prefix upper-cased", func(t *testing.T) {
		h, s, _ := setup(t)
		be, err := s.Create(h.Ctx, h.OrgID(), "lower", billingentities.Input{Name: core.Some("Lower"), DocumentNumberPrefix: core.Some("abc")})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if be.DocumentNumberPrefix != "ABC" {
			t.Errorf("DocumentNumberPrefix = %q, want ABC", be.DocumentNumberPrefix)
		}
	})

	// source: spec/services/billing_entities/create_service_spec.rb:55
	t.Run("eu tax management generates the EU taxes", func(t *testing.T) {
		h, s, _ := setup(t)
		be, err := s.Create(h.Ctx, h.OrgID(), "eu", billingentities.Input{Name: core.Some("EU"), Country: core.Some("FR"), EUTaxManagement: core.Some(true)})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if !be.EUTaxManagement {
			t.Errorf("EUTaxManagement = false, want true")
		}
		fr := billingtest.Read(h, func(ctx context.Context, r port.Repos) (*catalog.Tax, error) {
			return r.Taxes.GetByCode(ctx, h.OrgID(), tax.EUStandardTaxCode("FR"))
		})
		if !fr.AutoGenerated || fr.Rate.String() != "20" {
			t.Errorf("FR tax = %+v, want auto-generated at 20", fr)
		}
	})

	tests := []struct {
		name        string
		source      string
		code        string
		in          billingentities.Input
		field, want string
	}{
		{
			name: "prefix too long", source: "spec/services/billing_entities/create_service_spec.rb:209",
			code: "long", in: billingentities.Input{Name: core.Some("Long"), DocumentNumberPrefix: core.Some("aaaaaaaaaaaaaaa")},
			field: "document_number_prefix", want: core.CodeTooLong,
		},
		{
			name: "eu tax management outside the EU", source: "spec/services/billing_entities/create_service_spec.rb:227",
			code: "us", in: billingentities.Input{Name: core.Some("US"), Country: core.Some("US"), EUTaxManagement: core.Some(true)},
			field: "eu_tax_management", want: organization.CodeEUTaxManagementRequiresEUCountry,
		},
		{
			name: "missing name", source: "spec/services/billing_entities/create_service_spec.rb:240",
			code: "noname", in: billingentities.Input{},
			field: "name", want: core.CodeRequired,
		},
		{
			name: "code taken", source: "spec/requests/api/v1/billing_entities_controller_spec.rb:199",
			code: "org", in: billingentities.Input{Name: core.Some("Taken")},
			field: "code", want: core.CodeAlreadyExists,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, s, _ := setup(t)
			_, err := s.Create(h.Ctx, h.OrgID(), tt.code, tt.in)
			wantFieldError(t, err, tt.field, tt.want)
		})
	}
}

func TestCreateFirstEntityIsDefault(t *testing.T) {
	h, s, _ := setup(t)
	h.UpdateBillingEntity(func(be *organization.BillingEntity) { _ = be.Archive(now) })
	be, err := s.Create(h.Ctx, h.OrgID(), "first", billingentities.Input{Name: core.Some("First")})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !be.IsDefault {
		t.Errorf("IsDefault = false, want true for the only active entity")
	}
}

func update(t *testing.T, h *billingtest.Harness, s *billingentities.Service, in billingentities.Input) *organization.BillingEntity {
	t.Helper()
	be, err := s.Update(h.Ctx, h.OrgID(), h.BillingEntity.ID, in)
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	return be
}

func TestUpdate(t *testing.T) {
	// source: spec/services/billing_entities/update_service_spec.rb:52
	t.Run("attributes", func(t *testing.T) {
		h, s, _ := setup(t)
		update(t, h, s, billingentities.Input{
			Name:                    core.Some("New Name"),
			LegalName:               core.Some(ptr("Foobar")),
			LegalNumber:             core.Some(ptr("1234")),
			TaxIdentificationNumber: core.Some(ptr("2246")),
			Email:                   core.Some(ptr("foo@bar.com")),
			AddressLine1:            core.Some("Line 1"),
			AddressLine2:            core.Some("Line 2"),
			Phone:                   core.Some(ptr("+49 30 1234567")),
			State:                   core.Some("Foobar"),
			Zipcode:                 core.Some("FOO1234"),
			City:                    core.Some("Foobar"),
			Country:                 core.Some("fr"),
			EInvoicing:              core.Some(true),
			DefaultCurrency:         core.Some(money.MustCurrency("EUR")),
			InvoiceFooter:           core.Some(ptr("invoice footer")),
			DocumentLocale:          core.Some("fr"),
		})
		got := reload(h, h.BillingEntity.ID)
		want := core.Address{Line1: "Line 1", Line2: "Line 2", City: "Foobar", State: "Foobar", Zipcode: "FOO1234", Country: "FR"}
		if got.Name != "New Name" || *got.LegalName != "Foobar" || *got.LegalNumber != "1234" || *got.TaxID != "2246" ||
			*got.Email != "foo@bar.com" || *got.Phone != "+49 30 1234567" || got.Address != want || !got.EInvoicing ||
			got.DefaultCurrency.Code() != "EUR" || *got.InvoiceFooter != "invoice footer" || got.DocumentLocale != "fr" ||
			got.Code != "org" {
			t.Errorf("stored = %+v, want the new attributes", got)
		}
	})

	// source: spec/services/billing_entities/update_service_spec.rb:78
	t.Run("email sanitized", func(t *testing.T) {
		h, s, _ := setup(t)
		be := update(t, h, s, billingentities.Input{Email: core.Some(ptr("hello@something–other.com"))})
		if *be.Email != "hello@something-other.com" {
			t.Errorf("Email = %q, want hello@something-other.com", *be.Email)
		}
	})

	// source: spec/services/billing_entities/update_service_spec.rb:114
	t.Run("prefix upper-cased", func(t *testing.T) {
		h, s, _ := setup(t)
		if be := update(t, h, s, billingentities.Input{DocumentNumberPrefix: core.Some("abc")}); be.DocumentNumberPrefix != "ABC" {
			t.Errorf("DocumentNumberPrefix = %q, want ABC", be.DocumentNumberPrefix)
		}
	})

	// source: spec/services/billing_entities/update_service_spec.rb:124
	t.Run("finalize zero amount invoice", func(t *testing.T) {
		h, s, _ := setup(t)
		if be := update(t, h, s, billingentities.Input{FinalizeZeroAmountInvoice: core.Some(false)}); be.FinalizeZeroAmountInvoice {
			t.Errorf("FinalizeZeroAmountInvoice = true, want false")
		}
	})

	// source: spec/services/billing_entities/update_service_spec.rb:147
	t.Run("timezone and email settings", func(t *testing.T) {
		h, s, _ := setup(t)
		be := update(t, h, s, billingentities.Input{
			Timezone:      core.Some(must(time.LoadLocation("Europe/Paris"))),
			EmailSettings: core.Some([]organization.EmailSetting{organization.EmailSettingCreditNoteCreated, organization.EmailSettingInvoiceFinalized}),
		})
		if be.Timezone.String() != "Europe/Paris" || len(be.EmailSettings) != 2 {
			t.Errorf("Timezone, EmailSettings = %v, %v, want Europe/Paris and 2 settings", be.Timezone, be.EmailSettings)
		}
	})

	// source: spec/services/billing_entities/update_service_spec.rb:328
	t.Run("eu tax management disabled outside the EU", func(t *testing.T) {
		h, s, _ := setup(t)
		h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.Address.Country = "FR"; be.EUTaxManagement = true })
		be := update(t, h, s, billingentities.Input{Country: core.Some("US"), EUTaxManagement: core.Some(false)})
		if be.EUTaxManagement {
			t.Errorf("EUTaxManagement = true, want false")
		}
	})

	// source: spec/services/billing_entities/update_service_spec.rb:290
	// source: spec/services/billing_entities/change_eu_tax_management_service_spec.rb:23
	t.Run("eu tax management in the EU", func(t *testing.T) {
		h, s, _ := setup(t)
		update(t, h, s, billingentities.Input{Country: core.Some("fr"), EUTaxManagement: core.Some(true)})
		taxes := billingtest.Read(h, func(ctx context.Context, r port.Repos) (core.Page[*catalog.Tax], error) {
			return r.Taxes.List(ctx, h.OrgID(), catalog.TaxFilter{}, core.PageRequest{Size: core.MaxPageSize})
		})
		if want := len(tax.EUTaxes(now)); len(taxes.Items) != want {
			t.Errorf("generated %d taxes, want %d", len(taxes.Items), want)
		}
		for _, tx := range taxes.Items {
			if !tx.AutoGenerated || !strings.HasPrefix(tx.Code, tax.EUTaxCodePrefix) {
				t.Errorf("tax %q AutoGenerated = %v, want an auto-generated EU tax", tx.Code, tx.AutoGenerated)
			}
		}
	})

	// source: spec/services/billing_entities/update_service_spec.rb:209
	// source: spec/services/billing_entities/update_service_spec.rb:223
	t.Run("logo", func(t *testing.T) {
		h, s, b := setup(t)
		be := update(t, h, s, billingentities.Input{Logo: core.Some(png)})
		if be.Logo.IsZero() || !bytes.Equal(b.files[be.Logo], png) {
			t.Fatalf("Logo = %q, want the stored PNG", be.Logo)
		}
		if u, err := s.LogoURL(h.Ctx, be); err != nil || u != "https://blobs.test/"+string(be.Logo) {
			t.Errorf("LogoURL() = %q, %v, want the blob URL", u, err)
		}
		old := be.Logo
		be = update(t, h, s, billingentities.Input{Logo: core.Some([]byte{})})
		if !be.Logo.IsZero() {
			t.Errorf("Logo = %q, want removed", be.Logo)
		}
		if _, ok := b.files[old]; ok {
			t.Errorf("old logo %q still stored", old)
		}
	})

	tests := []struct {
		name        string
		source      string
		before      func(*organization.BillingEntity)
		in          billingentities.Input
		field, want string
	}{
		{
			name: "prefix too long", source: "spec/services/billing_entities/update_service_spec.rb:134",
			in:    billingentities.Input{DocumentNumberPrefix: core.Some("aaaaaaaaaaaaaaa")},
			field: "document_number_prefix", want: core.CodeTooLong,
		},
		{
			name: "invalid country", source: "spec/services/billing_entities/update_service_spec.rb:234",
			in:    billingentities.Input{Country: core.Some("---")},
			field: "address.country", want: core.CodeInvalidCountry,
		},
		{
			name: "einvoicing country not supported", source: "spec/services/billing_entities/update_service_spec.rb:250",
			before: func(be *organization.BillingEntity) { be.EInvoicing = true },
			in:     billingentities.Input{Country: core.Some("BR")},
			field:  "einvoicing", want: organization.CodeCountryNotSupported,
		},
		{
			name: "einvoicing without country", source: "spec/services/billing_entities/update_service_spec.rb:261",
			before: func(be *organization.BillingEntity) { be.EInvoicing = true },
			in:     billingentities.Input{Country: core.Some("")},
			field:  "einvoicing", want: organization.CodeCountryMustBePresent,
		},
		{
			name: "eu tax management outside the EU", source: "spec/services/billing_entities/update_service_spec.rb:307",
			in:    billingentities.Input{Country: core.Some("us"), EUTaxManagement: core.Some(true)},
			field: "eu_tax_management", want: organization.CodeEUTaxManagementRequiresEUCountry,
		},
		{
			name: "logo of another type", source: "app/models/billing_entity.rb:100-102",
			in:    billingentities.Input{Logo: core.Some([]byte("GIF89a......"))},
			field: "logo", want: billingentities.CodeInvalidContentType,
		},
		{
			name: "logo too large", source: "app/models/billing_entity.rb:100-102",
			in:    billingentities.Input{Logo: core.Some(append(bytes.Clone(png), make([]byte, billingentities.MaxLogoSize)...))},
			field: "logo", want: billingentities.CodeInvalidSize,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, s, _ := setup(t)
			if tt.before != nil {
				h.UpdateBillingEntity(tt.before)
			}
			_, err := s.Update(h.Ctx, h.OrgID(), h.BillingEntity.ID, tt.in)
			wantFieldError(t, err, tt.field, tt.want)
		})
	}

	// source: spec/services/billing_entities/update_service_spec.rb:272
	t.Run("einvoicing in a supported country", func(t *testing.T) {
		h, s, _ := setup(t)
		be := update(t, h, s, billingentities.Input{EInvoicing: core.Some(true), Country: core.Some("DE")})
		if !be.EInvoicing || be.Address.Country != "DE" {
			t.Errorf("EInvoicing, Country = %v, %q, want true, DE", be.EInvoicing, be.Address.Country)
		}
	})

	// source: spec/services/billing_entities/update_service_spec.rb:442
	t.Run("not found", func(t *testing.T) {
		h, s, _ := setup(t)
		_, err := s.Update(h.Ctx, h.OrgID(), core.NewID[core.BillingEntityID](h.IDs), billingentities.Input{Name: core.Some("x")})
		wantNotFound(t, err, billingentities.Resource)
	})

	t.Run("logo without a blob store", func(t *testing.T) {
		h := billingtest.New(t, now)
		s, err := billingentities.New(h.Deps, nil)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		_, err = s.Update(h.Ctx, h.OrgID(), h.BillingEntity.ID, billingentities.Input{Logo: core.Some(png)})
		var re *core.RuleError
		if !errors.As(err, &re) || re.Code != billingentities.CodeLogoStorageNotConfigured {
			t.Errorf("error = %v, want rule error %s", err, billingentities.CodeLogoStorageNotConfigured)
		}
	})
}

func TestUpdateInvoiceCustomSections(t *testing.T) {
	tests := []struct {
		name   string
		source string
		before []int
		refs   func(s1, s2 *invoice.CustomSection) []refs.Ref
		want   []int
	}{
		{
			name: "by id", source: "spec/services/billing_entities/update_service_spec.rb:344",
			refs: func(s1, s2 *invoice.CustomSection) []refs.Ref { return []refs.Ref{refs.ByID(s1.ID), refs.ByID(s2.ID)} },
			want: []int{1, 2},
		},
		{
			name: "removing a section", source: "spec/services/billing_entities/update_service_spec.rb:350",
			before: []int{2},
			refs:   func(s1, _ *invoice.CustomSection) []refs.Ref { return []refs.Ref{refs.ByID(s1.ID)} },
			want:   []int{1},
		},
		{
			name: "adding a section", source: "spec/services/billing_entities/update_service_spec.rb:364",
			before: []int{2},
			refs:   func(s1, s2 *invoice.CustomSection) []refs.Ref { return []refs.Ref{refs.ByID(s1.ID), refs.ByID(s2.ID)} },
			want:   []int{1, 2},
		},
		{
			name: "removing all sections", source: "spec/services/billing_entities/update_service_spec.rb:378",
			before: []int{1, 2},
			refs:   func(_, _ *invoice.CustomSection) []refs.Ref { return []refs.Ref{} },
		},
		{
			name: "by code", source: "spec/services/billing_entities/update_service_spec.rb:388",
			before: []int{2},
			refs:   func(s1, _ *invoice.CustomSection) []refs.Ref { return []refs.Ref{refs.ByCode(s1.Code)} },
			want:   []int{1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, s, _ := setup(t)
			secs := []*invoice.CustomSection{nil, section(h, "s1"), section(h, "s2")}
			ids := func(ns []int) []core.InvoiceCustomSectionID {
				var out []core.InvoiceCustomSectionID
				for _, n := range ns {
					out = append(out, secs[n].ID)
				}
				return out
			}
			h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.InvoiceCustomSectionIDs = ids(tt.before) })
			update(t, h, s, billingentities.Input{InvoiceCustomSections: core.Some(tt.refs(secs[1], secs[2]))})
			got, want := reload(h, h.BillingEntity.ID).InvoiceCustomSectionIDs, ids(tt.want)
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("InvoiceCustomSectionIDs = %v, want %v", got, want)
			}
		})
	}

	t.Run("missing section", func(t *testing.T) {
		h, s, _ := setup(t)
		_, err := s.Update(h.Ctx, h.OrgID(), h.BillingEntity.ID, billingentities.Input{InvoiceCustomSections: core.Some([]refs.Ref{refs.ByCode("nope")})})
		wantNotFound(t, err, "invoice_custom_section")
	})
}

func TestUpdateTaxes(t *testing.T) {
	// source: spec/services/billing_entities/taxes/manage_taxes_service_spec.rb:22
	// source: spec/services/billing_entities/taxes/manage_taxes_service_spec.rb:45
	// source: spec/services/billing_entities/taxes/manage_taxes_service_spec.rb:52
	// source: spec/services/billing_entities/taxes/manage_taxes_service_spec.rb:60
	t.Run("replaces the taxes and refreshes the drafts", func(t *testing.T) {
		h, s, _ := setup(t)
		other, t1, t2 := h.Tax("other", "5"), h.Tax("tax_code_1", "10"), h.Tax("tax_code_2", "20")
		h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.TaxIDs = []core.TaxID{other.ID} })
		c := h.Customer()
		d := draft(h, c, core.DateOf(now, time.UTC))
		finalized := h.StoredInvoice(c, func(inv *invoice.Invoice) { inv.Status = invoice.StatusFinalized })
		update(t, h, s, billingentities.Input{Taxes: core.Some([]refs.Ref{refs.ByCode("tax_code_1"), refs.ByCode("tax_code_2"), refs.ByCode("tax_code_1")})})
		if got, want := fmt.Sprint(reload(h, h.BillingEntity.ID).TaxIDs), fmt.Sprint([]core.TaxID{t1.ID, t2.ID}); got != want {
			t.Errorf("TaxIDs = %v, want %v", got, want)
		}
		if !h.Invoice(d.ID).RefreshRequested {
			t.Errorf("draft RefreshRequested = false, want true")
		}
		if h.Invoice(finalized.ID).RefreshRequested {
			t.Errorf("finalized RefreshRequested = true, want false")
		}
	})

	// source: spec/services/billing_entities/taxes/manage_taxes_service_spec.rb:90
	t.Run("empty removes the taxes", func(t *testing.T) {
		h, s, _ := setup(t)
		t1 := h.Tax("tax_code_1", "10")
		h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.TaxIDs = []core.TaxID{t1.ID} })
		update(t, h, s, billingentities.Input{Taxes: core.Some([]refs.Ref{})})
		if got := reload(h, h.BillingEntity.ID).TaxIDs; len(got) != 0 {
			t.Errorf("TaxIDs = %v, want none", got)
		}
	})

	// source: spec/services/billing_entities/taxes/manage_taxes_service_spec.rb:31
	// source: spec/requests/api/v1/billing_entities_controller_spec.rb:363
	t.Run("missing tax", func(t *testing.T) {
		h, s, _ := setup(t)
		h.Tax("tax_code_1", "10")
		_, err := s.Update(h.Ctx, h.OrgID(), h.BillingEntity.ID, billingentities.Input{Taxes: core.Some([]refs.Ref{refs.ByCode("tax_code_1"), refs.ByCode("unknown")})})
		wantNotFound(t, err, "tax")
	})
}

// source: spec/services/billing_entities/update_invoice_payment_due_date_service_spec.rb:14-63
func TestUpdateNetPaymentTerm(t *testing.T) {
	h, s, _ := setup(t)
	issuing := core.NewDate(2024, 3, 15)
	c := h.Customer()
	own := h.Customer(func(c *customer.Customer) { c.NetPaymentTermDays = ptr(5) })
	d := draft(h, c, issuing)
	ownDraft := draft(h, own, issuing)
	finalized := h.StoredInvoice(c, func(inv *invoice.Invoice) {
		inv.Status, inv.IssuingDate, inv.PaymentDueDate = invoice.StatusFinalized, issuing, issuing
	})
	other, err := s.Create(h.Ctx, h.OrgID(), "other", billingentities.Input{Name: core.Some("Other")})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	otherDraft := draft(h, c, issuing, func(inv *invoice.Invoice) { inv.BillingEntityID = other.ID })

	update(t, h, s, billingentities.Input{NetPaymentTermDays: core.Some(30)})

	tests := []struct {
		name    string
		id      core.InvoiceID
		term    int
		dueDate core.Date
	}{
		// source: spec/services/billing_entities/update_invoice_payment_due_date_service_spec.rb:25
		{"draft", d.ID, 30, issuing.AddDays(30)},
		// source: spec/services/billing_entities/update_invoice_payment_due_date_service_spec.rb:30
		{"finalized", finalized.ID, 0, issuing},
		// source: spec/services/billing_entities/update_invoice_payment_due_date_service_spec.rb:45
		{"customer with own term", ownDraft.ID, 0, issuing},
		// source: spec/services/billing_entities/update_invoice_payment_due_date_service_spec.rb:57
		{"another billing entity", otherDraft.ID, 0, issuing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv := h.Invoice(tt.id)
			if inv.NetPaymentTermDays != tt.term || inv.PaymentDueDate != tt.dueDate {
				t.Errorf("term, due date = %d, %v, want %d, %v", inv.NetPaymentTermDays, inv.PaymentDueDate, tt.term, tt.dueDate)
			}
		})
	}
}

// source: spec/services/invoices/update_issuing_date_from_billing_entity_service_spec.rb:5-200
func TestUpdateIssuingDates(t *testing.T) {
	type settings struct {
		anchor     core.IssuingDateAnchor
		adjustment core.IssuingDateAdjustment
		grace      int
	}
	start, align := core.IssuingDateAnchorNextPeriodStart, core.IssuingDateAdjustmentAlignWithFinalizationDate
	end, keep := core.IssuingDateAnchorCurrentPeriodEnd, core.IssuingDateAdjustmentKeepAnchor
	tests := []struct {
		name         string
		source       string
		recurring    *bool
		customer     func(*customer.Customer)
		status       invoice.Status
		old, new     settings
		issuingShift int
		finalShift   int
		wantGrace    int
	}{
		{
			name: "customer has a grace period", source: "spec/services/invoices/update_issuing_date_from_billing_entity_service_spec.rb:53",
			customer: func(c *customer.Customer) { c.InvoiceGracePeriodDays = ptr(12) },
			old:      settings{start, align, 12}, new: settings{start, align, 1}, wantGrace: 12,
		},
		{
			name: "not draft", source: "spec/services/invoices/update_issuing_date_from_billing_entity_service_spec.rb:61",
			status: invoice.StatusFinalized,
			old:    settings{start, align, 12}, new: settings{start, align, 1}, wantGrace: 12,
		},
		{
			name: "12 to 15 days", source: "spec/services/invoices/update_issuing_date_from_billing_entity_service_spec.rb:69",
			old: settings{start, align, 12}, new: settings{start, align, 15}, issuingShift: 3, finalShift: 3, wantGrace: 15,
		},
		{
			name: "12 to 9 days", source: "spec/services/invoices/update_issuing_date_from_billing_entity_service_spec.rb:89",
			old: settings{start, align, 12}, new: settings{start, align, 9}, issuingShift: -3, finalShift: -3, wantGrace: 9,
		},
		{
			name: "current_period_end + keep_anchor", source: "spec/services/invoices/update_issuing_date_from_billing_entity_service_spec.rb:116",
			recurring: ptr(true),
			old:       settings{start, align, 12}, new: settings{end, keep, 2}, issuingShift: -13, finalShift: -10, wantGrace: 2,
		},
		{
			name: "current_period_end + align_with_finalization_date", source: "spec/services/invoices/update_issuing_date_from_billing_entity_service_spec.rb:127",
			recurring: ptr(true),
			old:       settings{start, align, 12}, new: settings{end, align, 2}, issuingShift: -10, finalShift: -10, wantGrace: 2,
		},
		{
			name: "next_period_start + keep_anchor", source: "spec/services/invoices/update_issuing_date_from_billing_entity_service_spec.rb:138",
			recurring: ptr(true),
			old:       settings{start, align, 12}, new: settings{start, keep, 2}, issuingShift: -12, finalShift: -10, wantGrace: 2,
		},
		{
			name: "next_period_start + align_with_finalization_date", source: "spec/services/invoices/update_issuing_date_from_billing_entity_service_spec.rb:149",
			recurring: ptr(true),
			old:       settings{start, align, 12}, new: settings{start, align, 2}, issuingShift: -10, finalShift: -10, wantGrace: 2,
		},
		{
			name: "preferences set on the customer", source: "spec/services/invoices/update_issuing_date_from_billing_entity_service_spec.rb:160",
			recurring: ptr(true),
			customer: func(c *customer.Customer) {
				c.IssuingDateAnchor, c.IssuingDateAdjustment, c.InvoiceGracePeriodDays = end, align, ptr(12)
			},
			old: settings{start, align, 12}, new: settings{start, keep, 2}, wantGrace: 12,
		},
		{
			name: "not recurring", source: "spec/services/invoices/update_issuing_date_from_billing_entity_service_spec.rb:183",
			recurring: ptr(false),
			old:       settings{start, align, 12}, new: settings{end, keep, 2}, issuingShift: -10, finalShift: -10, wantGrace: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, s, _ := setup(t)
			h.UpdateBillingEntity(func(be *organization.BillingEntity) {
				be.IssuingDateAnchor, be.IssuingDateAdjustment, be.InvoiceGracePeriodDays = tt.old.anchor, tt.old.adjustment, tt.old.grace
			})
			var opts []func(*customer.Customer)
			if tt.customer != nil {
				opts = append(opts, tt.customer)
			}
			c := h.Customer(opts...)
			issuing := core.DateOf(now, time.UTC).AddDays(tt.old.grace)
			inv := draft(h, c, issuing, func(inv *invoice.Invoice) {
				inv.AppliedGracePeriodDays = 12
				if tt.status != "" {
					inv.Status = tt.status
				}
			})
			if tt.recurring != nil {
				reason := invoice.InvoicingReasonSubscriptionStarting
				if *tt.recurring {
					reason = invoice.InvoicingReasonSubscriptionPeriodic
				}
				sub := h.Subscription(c, h.Plan(period.Monthly, 1000))
				h.LinkSubscription(inv, sub, reason, now.AddDate(0, -1, 0), now)
			}

			update(t, h, s, billingentities.Input{
				IssuingDateAnchor:      core.Some(tt.new.anchor),
				IssuingDateAdjustment:  core.Some(tt.new.adjustment),
				InvoiceGracePeriodDays: core.Some(tt.new.grace),
			})

			got := h.Invoice(inv.ID)
			if want := issuing.AddDays(tt.issuingShift); got.IssuingDate != want {
				t.Errorf("IssuingDate = %v, want %v", got.IssuingDate, want)
			}
			if want := issuing.AddDays(tt.finalShift); got.ExpectedFinalizationDate != want {
				t.Errorf("ExpectedFinalizationDate = %v, want %v", got.ExpectedFinalizationDate, want)
			}
			if got.AppliedGracePeriodDays != tt.wantGrace {
				t.Errorf("AppliedGracePeriodDays = %d, want %d", got.AppliedGracePeriodDays, tt.wantGrace)
			}
			// The payment due date is the issuing date plus the net
			// payment term (0): it follows the issuing date.
			if got.PaymentDueDate != got.IssuingDate {
				t.Errorf("PaymentDueDate = %v, want %v", got.PaymentDueDate, got.IssuingDate)
			}
		})
	}

	// source: spec/services/billing_entities/update_invoice_issuing_date_settings_service_spec.rb:73
	t.Run("same settings leave the drafts", func(t *testing.T) {
		h, s, _ := setup(t)
		h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.InvoiceGracePeriodDays = 3 })
		issuing := core.NewDate(2024, 3, 20)
		inv := draft(h, h.Customer(), issuing, func(inv *invoice.Invoice) { inv.PaymentDueDate = issuing.AddDays(7) })
		update(t, h, s, billingentities.Input{InvoiceGracePeriodDays: core.Some(3)})
		if got := h.Invoice(inv.ID); got.IssuingDate != issuing || got.PaymentDueDate != issuing.AddDays(7) {
			t.Errorf("issuing, due = %v, %v, want unchanged", got.IssuingDate, got.PaymentDueDate)
		}
	})
}

// source: spec/services/billing_entities/update_applied_dunning_campaign_service_spec.rb:20-170
func TestUpdateAppliedDunningCampaign(t *testing.T) {
	attempted := func(c *customer.Customer) {
		c.DunningCampaignAttempts = map[string]int{"USD": 2}
		c.LastDunningCampaignAttemptAt = ptr(now.Add(-time.Hour))
	}
	tests := []struct {
		name      string
		source    string
		applied   bool
		to        func(applied, other *dunning.Campaign) refs.Ref
		want      func(applied, other *dunning.Campaign) core.DunningCampaignID
		wantReset bool
	}{
		{
			name: "nil to nil", source: "spec/services/billing_entities/update_applied_dunning_campaign_service_spec.rb:32",
			to:   func(_, _ *dunning.Campaign) refs.Ref { return refs.Ref{} },
			want: func(_, _ *dunning.Campaign) core.DunningCampaignID { return core.DunningCampaignID{} },
		},
		{
			name: "removed", source: "spec/services/billing_entities/update_applied_dunning_campaign_service_spec.rb:45",
			applied: true, wantReset: true,
			to:   func(_, _ *dunning.Campaign) refs.Ref { return refs.Ref{} },
			want: func(_, _ *dunning.Campaign) core.DunningCampaignID { return core.DunningCampaignID{} },
		},
		{
			name: "set", source: "spec/services/billing_entities/update_applied_dunning_campaign_service_spec.rb:80",
			wantReset: true,
			to:        func(_, o *dunning.Campaign) refs.Ref { return refs.ByID(o.ID) },
			want:      func(_, o *dunning.Campaign) core.DunningCampaignID { return o.ID },
		},
		{
			name: "changed", source: "spec/services/billing_entities/update_applied_dunning_campaign_service_spec.rb:103",
			applied: true, wantReset: true,
			to:   func(_, o *dunning.Campaign) refs.Ref { return refs.ByCode(o.Code) },
			want: func(_, o *dunning.Campaign) core.DunningCampaignID { return o.ID },
		},
		{
			name: "same", source: "spec/services/billing_entities/update_applied_dunning_campaign_service_spec.rb:126",
			applied: true,
			to:      func(a, _ *dunning.Campaign) refs.Ref { return refs.ByID(a.ID) },
			want:    func(a, _ *dunning.Campaign) core.DunningCampaignID { return a.ID },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, s, _ := setup(t)
			applied, other := campaign(h, "applied"), campaign(h, "other")
			if tt.applied {
				h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.AppliedDunningCampaignID = applied.ID })
			}
			second, err := s.Create(h.Ctx, h.OrgID(), "second", billingentities.Input{Name: core.Some("Second")})
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			fallback := h.Customer(attempted)
			own := h.Customer(attempted, func(c *customer.Customer) { c.AppliedDunningCampaignID = applied.ID })
			elsewhere := h.Customer(attempted, func(c *customer.Customer) { c.BillingEntityID = second.ID })

			be := update(t, h, s, billingentities.Input{AppliedDunningCampaign: core.Some(tt.to(applied, other))})
			if want := tt.want(applied, other); be.AppliedDunningCampaignID != want {
				t.Errorf("AppliedDunningCampaignID = %v, want %v", be.AppliedDunningCampaignID, want)
			}
			reset := func(id core.CustomerID) bool {
				c := h.ReloadCustomer(id)
				return len(c.DunningCampaignAttempts) == 0 && c.LastDunningCampaignAttemptAt == nil
			}
			if got := reset(fallback.ID); got != tt.wantReset {
				t.Errorf("fallback customer reset = %v, want %v", got, tt.wantReset)
			}
			if reset(own.ID) || reset(elsewhere.ID) {
				t.Errorf("customers with their own campaign or of another entity reset, want kept")
			}
		})
	}

	// source: spec/services/billing_entities/update_applied_dunning_campaign_service_spec.rb:163
	t.Run("campaign not found", func(t *testing.T) {
		h, s, _ := setup(t)
		_, err := s.Update(h.Ctx, h.OrgID(), h.BillingEntity.ID, billingentities.Input{AppliedDunningCampaign: core.Some(refs.ByCode("nope"))})
		wantNotFound(t, err, "dunning_campaign")
	})
}

func TestGetAndList(t *testing.T) {
	h, s, _ := setup(t)
	second, err := s.Create(h.Ctx, h.OrgID(), "second", billingentities.Input{Name: core.Some("Second")})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	archived, err := s.Create(h.Ctx, h.OrgID(), "archived", billingentities.Input{Name: core.Some("Archived")})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	h.Do(func(ctx context.Context, r port.Repos) error {
		if err := archived.Archive(now); err != nil {
			return err
		}
		return r.BillingEntities.Update(ctx, archived)
	})

	// source: spec/requests/api/v1/billing_entities_controller_spec.rb:26
	t.Run("list active", func(t *testing.T) {
		p, err := s.List(h.Ctx, h.OrgID(), billingentities.Filter{}, core.PageRequest{})
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(p.Items) != 2 || p.Items[0].ID != h.BillingEntity.ID || p.Items[1].ID != second.ID {
			t.Errorf("List() = %d entities, want the default and second", len(p.Items))
		}
	})

	t.Run("list filters", func(t *testing.T) {
		tests := []struct {
			f    billingentities.Filter
			want core.BillingEntityID
		}{
			{billingentities.Filter{Archived: ptr(true)}, archived.ID},
			{billingentities.Filter{IsDefault: ptr(true)}, h.BillingEntity.ID},
			{billingentities.Filter{Code: "second"}, second.ID},
			{billingentities.Filter{Name: "Second"}, second.ID},
		}
		for _, tt := range tests {
			p, err := s.List(h.Ctx, h.OrgID(), tt.f, core.PageRequest{})
			if err != nil || len(p.Items) != 1 || p.Items[0].ID != tt.want {
				t.Errorf("List(%+v) = %v, %v, want only %v", tt.f, p.Items, err, tt.want)
			}
		}
	})

	// source: spec/requests/api/v1/billing_entities_controller_spec.rb:39
	// source: spec/requests/api/v1/billing_entities_controller_spec.rb:74
	t.Run("get by code, archived included", func(t *testing.T) {
		for _, want := range []*organization.BillingEntity{second, archived} {
			got, err := s.GetByCode(h.Ctx, h.OrgID(), want.Code)
			if err != nil || got.ID != want.ID {
				t.Errorf("GetByCode(%q) = %v, %v, want %v", want.Code, got, err, want.ID)
			}
		}
	})

	// source: spec/requests/api/v1/billing_entities_controller_spec.rb:86
	t.Run("get missing", func(t *testing.T) {
		_, err := s.GetByCode(h.Ctx, h.OrgID(), "missing")
		wantNotFound(t, err, billingentities.Resource)
		_, err = s.Get(h.Ctx, h.OrgID(), core.NewID[core.BillingEntityID](h.IDs))
		wantNotFound(t, err, billingentities.Resource)
	})
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
