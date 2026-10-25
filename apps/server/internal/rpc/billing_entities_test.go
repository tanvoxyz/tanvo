package rpc_test

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/billing/billingentities"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	organizationv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/organization/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/organization/v1/organizationv1connect"
)

// restBillingEntity is the JSON of a billing entity in a REST response.
type restBillingEntity struct {
	ID                      string `json:"id"`
	Code                    string `json:"code"`
	Name                    string `json:"name"`
	Email                   string `json:"email"`
	LegalName               string `json:"legal_name"`
	LegalNumber             string `json:"legal_number"`
	TaxIdentificationNumber string `json:"tax_identification_number"`
	Phone                   string `json:"phone"`
	Address                 struct {
		Line1   string `json:"line1"`
		Line2   string `json:"line2"`
		City    string `json:"city"`
		State   string `json:"state"`
		Zipcode string `json:"zipcode"`
		Country string `json:"country"`
	} `json:"address"`
	Timezone                  string   `json:"timezone"`
	DefaultCurrency           string   `json:"default_currency"`
	DocumentLocale            string   `json:"document_locale"`
	DocumentNumbering         string   `json:"document_numbering"`
	DocumentNumberPrefix      string   `json:"document_number_prefix"`
	InvoiceFooter             string   `json:"invoice_footer"`
	InvoiceGracePeriodDays    int      `json:"invoice_grace_period_days"`
	NetPaymentTermDays        int      `json:"net_payment_term_days"`
	FinalizeZeroAmountInvoice bool     `json:"finalize_zero_amount_invoice"`
	IssuingDateAnchor         string   `json:"issuing_date_anchor"`
	IssuingDateAdjustment     string   `json:"issuing_date_adjustment"`
	EmailSettings             []string `json:"email_settings"`
	EUTaxManagement           bool     `json:"eu_tax_management"`
	EInvoicing                bool     `json:"einvoicing"`
	IsDefault                 bool     `json:"is_default"`
	TaxIDs                    []string `json:"tax_ids"`
	InvoiceCustomSectionIDs   []string `json:"invoice_custom_section_ids"`
	ArchivedAt                string   `json:"archived_at"`
}

// beFullBody is the entity of the request spec's create and update.
const beFullBody = `{"name":"New Name","email":"new@email.com","legal_name":"New Legal Name","einvoicing":false,
	"legal_number":"1234567890","tax_identification_number":"Tax-1234",
	"address":{"line1":"Calle de la Princesa 1","line2":"Apt 1","city":"Barcelona","state":"Barcelona","zipcode":"08001","country":"ES"},
	"phone":"+49 30 1234567","default_currency":"EUR","timezone":"Europe/Madrid",
	"document_numbering":"DOCUMENT_NUMBERING_PER_BILLING_ENTITY","document_number_prefix":"NEW-0001",
	"finalize_zero_amount_invoice":true,"net_payment_term_days":10,"eu_tax_management":true,
	"email_settings":["EMAIL_SETTING_INVOICE_FINALIZED","EMAIL_SETTING_CREDIT_NOTE_CREATED"],
	"invoice_footer":"New Invoice Footer","document_locale":"es","invoice_grace_period_days":10,
	"issuing_date_anchor":"ISSUING_DATE_ANCHOR_CURRENT_PERIOD_END","issuing_date_adjustment":"ISSUING_DATE_ADJUSTMENT_KEEP_ANCHOR"`

// beCheckFull checks the attributes of beFullBody.
func beCheckFull(t *testing.T, e restBillingEntity) {
	t.Helper()
	if e.Name != "New Name" || e.Email != "new@email.com" || e.EInvoicing || e.LegalName != "New Legal Name" ||
		e.LegalNumber != "1234567890" || e.TaxIdentificationNumber != "Tax-1234" || e.Phone != "+49 30 1234567" {
		t.Errorf("got %+v, want the identity of the request", e)
	}
	if a := e.Address; a.Line1 != "Calle de la Princesa 1" || a.Line2 != "Apt 1" || a.City != "Barcelona" ||
		a.State != "Barcelona" || a.Zipcode != "08001" || a.Country != "ES" {
		t.Errorf("got address = %+v, want the request's", a)
	}
	if e.DefaultCurrency != "EUR" || e.Timezone != "Europe/Madrid" || e.DocumentNumbering != "DOCUMENT_NUMBERING_PER_BILLING_ENTITY" ||
		e.DocumentNumberPrefix != "NEW-0001" || !e.FinalizeZeroAmountInvoice || e.NetPaymentTermDays != 10 || !e.EUTaxManagement {
		t.Errorf("got %+v, want the settings of the request", e)
	}
	if len(e.EmailSettings) != 2 || e.EmailSettings[0] != "EMAIL_SETTING_INVOICE_FINALIZED" || e.EmailSettings[1] != "EMAIL_SETTING_CREDIT_NOTE_CREATED" {
		t.Errorf("got email_settings = %v, want invoice finalized, credit note created", e.EmailSettings)
	}
	if e.InvoiceFooter != "New Invoice Footer" || e.DocumentLocale != "es" || e.InvoiceGracePeriodDays != 10 ||
		e.IssuingDateAnchor != "ISSUING_DATE_ANCHOR_CURRENT_PERIOD_END" || e.IssuingDateAdjustment != "ISSUING_DATE_ADJUSTMENT_KEEP_ANCHOR" {
		t.Errorf("got %+v, want the billing configuration of the request", e)
	}
}

func beDecode(t *testing.T, body []byte) restBillingEntity {
	t.Helper()
	return decode[struct {
		BillingEntity restBillingEntity `json:"billing_entity"`
	}](t, body).BillingEntity
}

func billingEntitiesClient(s *rpctest.Server, c rpctest.Credential) organizationv1connect.BillingEntityServiceClient {
	return organizationv1connect.NewBillingEntityServiceClient(s.Connect(c))
}

// beArchive archives a billing entity in the store.
func beArchive(t *testing.T, s *rpctest.Server, id core.BillingEntityID) {
	t.Helper()
	s.Fixtures.Do(func(ctx context.Context, r port.Repos) error {
		be, err := r.BillingEntities.Get(ctx, s.Org.ID, id)
		if err != nil {
			return err
		}
		if err := be.Archive(rpctest.Now); err != nil {
			return err
		}
		return r.BillingEntities.Update(ctx, be)
	})
}

func TestCreateBillingEntity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		body   string
		status int
		check  func(t *testing.T, e restBillingEntity)
	}{
		{
			name:   "creates",
			source: "spec/requests/api/v1/billing_entities_controller_spec.rb:148",
			body:   `{"code":"NEW-0001",` + beFullBody[1:] + `}`,
			status: http.StatusOK,
			check: func(t *testing.T, e restBillingEntity) {
				if e.ID == "" || e.Code != "NEW-0001" || e.IsDefault {
					t.Errorf("got id = %q, code = %q, is_default = %v, want a new non-default NEW-0001", e.ID, e.Code, e.IsDefault)
				}
				beCheckFull(t, e)
			},
		},
		{
			name:   "code taken",
			source: "spec/requests/api/v1/billing_entities_controller_spec.rb:199",
			body:   `{"code":"org","name":"Taken"}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "eu tax management outside the EU",
			source: "spec/services/billing_entities/create_service_spec.rb:227",
			body:   `{"code":"us","name":"US","address":{"country":"US"},"eu_tax_management":true}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "invalid timezone",
			source: "app/models/billing_entity.rb:104",
			body:   `{"code":"tz","name":"TZ","timezone":"Mars/Olympus"}`,
			status: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			s := rpctest.New(t)
			status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/billing_entities", tt.body)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if tt.check != nil {
				tt.check(t, beDecode(t, body))
			}
		})
	}
}

func TestGetBillingEntity(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	h := s.Fixtures
	vat := h.Tax("vat", "20")
	h.UpdateBillingEntity(func(be *organization.BillingEntity) { be.TaxIDs = []core.TaxID{vat.ID} })
	other := beOtherOrg(t, s, "other_org")
	status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/billing_entities", `{"code":"old","name":"Old"}`)
	if status != http.StatusOK {
		t.Fatalf("create: status = %d (%s)", status, body)
	}
	old := beDecode(t, body)
	beArchive(t, s, core.MustParseID[core.BillingEntityID](old.ID))

	tests := []struct {
		name   string
		source string
		ref    string
		status int
		check  func(t *testing.T, e restBillingEntity)
	}{
		{
			name: "by code", source: "spec/requests/api/v1/billing_entities_controller_spec.rb:39",
			ref: "code%3Aorg", status: http.StatusOK,
			check: func(t *testing.T, e restBillingEntity) {
				if e.ID != h.BillingEntity.ID.String() || !e.IsDefault {
					t.Errorf("got id = %s, is_default = %v, want the default entity", e.ID, e.IsDefault)
				}
			},
		},
		{
			name: "with taxes", source: "spec/requests/api/v1/billing_entities_controller_spec.rb:52",
			ref: h.BillingEntity.ID.String(), status: http.StatusOK,
			check: func(t *testing.T, e restBillingEntity) {
				if len(e.TaxIDs) != 1 || e.TaxIDs[0] != vat.ID.String() {
					t.Errorf("got tax_ids = %v, want [%s]", e.TaxIDs, vat.ID)
				}
			},
		},
		{
			name: "another organization", source: "spec/requests/api/v1/billing_entities_controller_spec.rb:63",
			ref: "code%3Aother_org", status: http.StatusNotFound,
		},
		{
			name: "another organization by id", source: "spec/requests/api/v1/billing_entities_controller_spec.rb:63",
			ref: other.ID.String(), status: http.StatusNotFound,
		},
		{
			name: "archived", source: "spec/requests/api/v1/billing_entities_controller_spec.rb:74",
			ref: "code%3Aold", status: http.StatusOK,
			check: func(t *testing.T, e restBillingEntity) {
				if e.ID != old.ID || e.ArchivedAt == "" {
					t.Errorf("got id = %s, archived_at = %q, want the archived entity", e.ID, e.ArchivedAt)
				}
			},
		},
		{
			name: "unknown", source: "spec/requests/api/v1/billing_entities_controller_spec.rb:86",
			ref: "code%3Aunknown", status: http.StatusNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/billing_entities/"+tt.ref, nil)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if tt.check != nil {
				tt.check(t, beDecode(t, body))
			}
		})
	}
}

// source: spec/requests/api/v1/billing_entities_controller_spec.rb:26
func TestListBillingEntities(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	beOtherOrg(t, s, "org")
	ids := map[string]string{}
	for _, code := range []string{"second", "old"} {
		status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/billing_entities", `{"code":"`+code+`","name":"`+code+`"}`)
		if status != http.StatusOK {
			t.Fatalf("create %s: status = %d (%s)", code, status, body)
		}
		ids[code] = beDecode(t, body).ID
	}
	beArchive(t, s, core.MustParseID[core.BillingEntityID](ids["old"]))

	tests := []struct {
		filter string
		want   []string
	}{
		{"", []string{s.Fixtures.BillingEntity.ID.String(), ids["second"]}},
		{"archived%20%3D%20true", []string{ids["old"]}},
		{"is_default%20%3D%20false", []string{ids["second"]}},
		{"code%20%3D%20%22second%22", []string{ids["second"]}},
	}
	for _, tt := range tests {
		status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/billing_entities?filter="+tt.filter, nil)
		if status != http.StatusOK {
			t.Fatalf("list %q: status = %d (%s)", tt.filter, status, body)
		}
		got := decode[struct {
			BillingEntities []restBillingEntity `json:"billing_entities"`
		}](t, body).BillingEntities
		if len(got) != len(tt.want) {
			t.Fatalf("list %q: got %d entities, want = %d", tt.filter, len(got), len(tt.want))
		}
		for i, e := range got {
			if e.ID != tt.want[i] {
				t.Errorf("list %q: got [%d] = %s, want = %s", tt.filter, i, e.ID, tt.want[i])
			}
		}
	}
}

func TestUpdateBillingEntity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		ref    string
		body   string
		status int
		check  func(t *testing.T, s *rpctest.Server, e restBillingEntity)
	}{
		{
			name: "updates", source: "spec/requests/api/v1/billing_entities_controller_spec.rb:273",
			body: beFullBody + `}`, status: http.StatusOK,
			check: func(t *testing.T, s *rpctest.Server, e restBillingEntity) {
				if e.Code != "org" {
					t.Errorf("got code = %q, want org", e.Code)
				}
				beCheckFull(t, e)
				if got := billingtest.Read(s.Fixtures, func(ctx context.Context, r port.Repos) (*organization.BillingEntity, error) {
					return r.BillingEntities.Get(ctx, s.Org.ID, s.Fixtures.BillingEntity.ID)
				}); got.Name != "New Name" {
					t.Errorf("stored name = %q, want New Name", got.Name)
				}
			},
		},
		{
			name: "invoice custom sections by code", source: "spec/requests/api/v1/billing_entities_controller_spec.rb:323",
			body: `{"invoice_custom_section_ids":["code:sec"]}`, status: http.StatusOK,
			check: func(t *testing.T, _ *rpctest.Server, e restBillingEntity) {
				if len(e.InvoiceCustomSectionIDs) != 1 {
					t.Errorf("got invoice_custom_section_ids = %v, want one", e.InvoiceCustomSectionIDs)
				}
			},
		},
		{
			name: "taxes", source: "spec/requests/api/v1/billing_entities_controller_spec.rb:348",
			body: `{"tax_ids":["code:TAX_CODE_1","code:TAX_CODE_2"]}`, status: http.StatusOK,
			check: func(t *testing.T, _ *rpctest.Server, e restBillingEntity) {
				if len(e.TaxIDs) != 2 {
					t.Errorf("got tax_ids = %v, want two", e.TaxIDs)
				}
			},
		},
		{
			name: "tax not found", source: "spec/requests/api/v1/billing_entities_controller_spec.rb:363",
			body: `{"tax_ids":["code:unknown"]}`, status: http.StatusNotFound,
		},
		{
			name: "entity not found", source: "spec/requests/api/v1/billing_entities_controller_spec.rb:373",
			ref: "code%3Aunknown", body: `{"name":"x"}`, status: http.StatusNotFound,
		},
		{
			name: "prefix too long", source: "spec/services/billing_entities/update_service_spec.rb:134",
			body: `{"document_number_prefix":"AAAAAAAAAAAAAAA"}`, status: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			s := rpctest.New(t)
			h := s.Fixtures
			h.Tax("TAX_CODE_1", "10")
			h.Tax("TAX_CODE_2", "20")
			h.Do(func(ctx context.Context, r port.Repos) error {
				return r.InvoiceCustomSections.Create(ctx, &invoice.CustomSection{
					ID: core.NewID[core.InvoiceCustomSectionID](h.IDs), OrganizationID: h.OrgID(), Code: "sec", Name: "Section",
					SectionType: invoice.CustomSectionTypeManual, CreatedAt: h.Now(), UpdatedAt: h.Now(),
				})
			})
			ref := tt.ref
			if ref == "" {
				ref = "code%3Aorg"
			}
			status, body := s.REST(t, s.APIKey, http.MethodPatch, "/v1/billing_entities/"+ref, tt.body)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if tt.check != nil {
				tt.check(t, s, beDecode(t, body))
			}
		})
	}
}

// TestUpdateBillingEntityMask updates through Connect with an update_mask:
// only the masked fields change.
func TestUpdateBillingEntityMask(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	res, err := billingEntitiesClient(s, s.APIKey).UpdateBillingEntity(t.Context(), organizationv1.UpdateBillingEntityRequest_builder{
		BillingEntity: organizationv1.BillingEntity_builder{
			Id:                 new(s.Fixtures.BillingEntity.ID.String()),
			Name:               new("Renamed"),
			NetPaymentTermDays: new(int32(15)),
		}.Build(),
		UpdateMask: beMask("net_payment_term_days"),
	}.Build())
	if err != nil {
		t.Fatalf("UpdateBillingEntity() error = %v", err)
	}
	if be := res.GetBillingEntity(); be.GetName() == "Renamed" || be.GetNetPaymentTermDays() != 15 {
		t.Errorf("got name = %q, net_payment_term_days = %d, want the old name and 15", be.GetName(), be.GetNetPaymentTermDays())
	}
}

func beMask(paths ...string) *fieldmaskpb.FieldMask { return &fieldmaskpb.FieldMask{Paths: paths} }

// beOtherOrg creates another organization with a default billing entity
// coded code, with random identifiers so that they do not collide with
// those of s.Fixtures.
func beOtherOrg(t *testing.T, s *rpctest.Server, code string) *organization.BillingEntity {
	t.Helper()
	ids := core.UUIDv7Generator{}
	org := organization.New(core.NewID[core.OrganizationID](ids), "Other", rpctest.Now)
	be := organization.NewBillingEntity(org.ID, core.NewID[core.BillingEntityID](ids), code, "Other", rpctest.Now)
	be.IsDefault = true
	be.DocumentNumberPrefix = "OTH"
	err := s.Store.InTx(context.Background(), func(ctx context.Context, r port.Repos) error {
		if err := r.Organizations.Create(ctx, org); err != nil {
			return err
		}
		return r.BillingEntities.Create(ctx, be)
	})
	if err != nil {
		t.Fatalf("other organization: %v", err)
	}
	return be
}

func TestBillingEntitySecurityLogs(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	t.Log("source: spec/requests/api/v1/billing_entities_controller_spec.rb:182")
	if status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/billing_entities", `{"code":"second","name":"Second"}`); status != http.StatusOK {
		t.Fatalf("create: got = %d %s, want = 200", status, body)
	}
	t.Log("source: spec/requests/api/v1/billing_entities_controller_spec.rb:308")
	if status, body := s.REST(t, s.APIKey, http.MethodPatch, "/v1/billing_entities/code%3Asecond", `{"name":"Renamed"}`); status != http.StatusOK {
		t.Fatalf("update: got = %d %s, want = 200", status, body)
	}
	var logs []*auditlog.SecurityLog
	if err := s.Store.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
		p, err := r.SecurityLogs.List(ctx, s.Org.ID, auditlog.SecurityLogFilter{Types: []string{billingentities.SecurityLogType}}, core.PageRequest{})
		logs = p.Items
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got := map[string]map[string]any{}
	for _, l := range logs {
		got[l.Event] = l.Resources
	}
	want := map[string]map[string]any{
		billingentities.EventCreated: {"billing_entity_name": "Second", "billing_entity_code": "second"},
		billingentities.EventUpdated: {
			"billing_entity_name": "Renamed", "billing_entity_code": "second",
			"name": map[string]any{"deleted": "Second", "added": "Renamed"},
		},
	}
	if len(logs) != 2 || !reflect.DeepEqual(got, want) {
		t.Errorf("got = %v, want = %v", got, want)
	}
}
