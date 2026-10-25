package rpc_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// viewCreditNote is the JSON of the fields of a credit note taken from
// its invoice and integrations.
type viewCreditNote struct {
	ID                    string  `json:"id"`
	SequentialID          string  `json:"sequential_id"`
	Number                string  `json:"number"`
	InvoiceNumber         string  `json:"invoice_number"`
	PurchaseOrderNumber   string  `json:"purchase_order_number"`
	BillingEntityID       string  `json:"billing_entity_id"`
	SelfBilled            *bool   `json:"self_billed"`
	Voidable              *bool   `json:"voidable"`
	PreciseTotalAmount    string  `json:"precise_total_amount"`
	PreciseTaxesAmount    string  `json:"precise_taxes_amount"`
	IntegrationSyncable   *bool   `json:"integration_syncable"`
	TaxProviderSyncable   *bool   `json:"tax_provider_syncable"`
	ExternalIntegrationID string  `json:"external_integration_id"`
	TaxProviderID         string  `json:"tax_provider_id"`
	TotalAmount           cnMoney `json:"total_amount"`
	Items                 []struct {
		ID    string `json:"id"`
		FeeID string `json:"fee_id"`
	} `json:"items"`
	AppliedTaxes []struct {
		ID             string `json:"id"`
		TaxDescription string `json:"tax_description"`
	} `json:"applied_taxes"`
	Metadata []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"metadata"`
}

func viewDecode(t *testing.T, body []byte) viewCreditNote {
	t.Helper()
	return decode[struct {
		CreditNote viewCreditNote `json:"credit_note"`
	}](t, body).CreditNote
}

func boolOf(b *bool) bool { return b != nil && *b }

func TestCreditNoteRepresentation(t *testing.T) {
	t.Parallel()
	f := newCNFixture(t)
	h := f.s.Fixtures
	f.inv.SelfBilled, f.inv.PurchaseOrderNumber = true, new("PO-42")
	h.SaveInvoice(f.inv)
	vat := h.Tax("vat", "20")
	cn := h.StoredCreditNote(f.inv, 12, func(cn *creditnote.CreditNote) {
		cn.SequentialID = 3
		cn.TaxesAmount, cn.PreciseTaxesAmount = 2, money.MustParse("2")
		cn.Items = []creditnote.Item{{ID: core.NewID[core.CreditNoteItemID](h.IDs), FeeID: f.fees[0].ID, Amount: 10, PreciseAmount: money.MustParse("10")}}
		cn.AppliedTaxes = []creditnote.AppliedTax{{
			ID: core.NewID[core.CreditNoteAppliedTaxID](h.IDs), TaxID: vat.ID, TaxCode: "vat", TaxName: "VAT", TaxDescription: new("Value added"),
			TaxRate: money.MustParse("20"), BaseAmount: 10, Amount: 2, PreciseAmount: money.MustParse("2"),
		}}
	})

	// source: spec/requests/api/v1/credit_notes_controller_spec.rb:32
	// source: spec/graphql/resolvers/credit_note_resolver_spec.rb:59
	t.Run("returns a credit note", func(t *testing.T) {
		status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/credit_notes/"+cn.ID.String(), nil)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		got := viewDecode(t, body)
		if got.ID != cn.ID.String() || got.SequentialID != "3" || got.InvoiceNumber != f.inv.Number ||
			got.PurchaseOrderNumber != "PO-42" || !boolOf(got.SelfBilled) || got.BillingEntityID != h.BillingEntity.ID.String() {
			t.Errorf("got %+v, want sequential_id 3, invoice %s, PO-42, self billed, billing entity %s", got, f.inv.Number, h.BillingEntity.ID)
		}
		if len(got.Items) != 1 || got.Items[0].ID != cn.Items[0].ID.String() || got.Items[0].FeeID != f.fees[0].ID.String() {
			t.Errorf("got items = %+v, want the item %s", got.Items, cn.Items[0].ID)
		}
		if len(got.AppliedTaxes) != 1 || got.AppliedTaxes[0].ID != cn.AppliedTaxes[0].ID.String() || got.AppliedTaxes[0].TaxDescription != "Value added" {
			t.Errorf("got applied taxes = %+v, want the tax %s described", got.AppliedTaxes, cn.AppliedTaxes[0].ID)
		}
		// Items 0.10, with taxes 0.02.
		if got.PreciseTotalAmount != "0.12" || got.PreciseTaxesAmount != "0.02" || !boolOf(got.Voidable) {
			t.Errorf("got precise total = %s, taxes = %s, voidable = %v, want = 0.12, 0.02, true", got.PreciseTotalAmount, got.PreciseTaxesAmount, got.Voidable)
		}
		if boolOf(got.IntegrationSyncable) || boolOf(got.TaxProviderSyncable) || got.ExternalIntegrationID != "" || got.TaxProviderID != "" {
			t.Errorf("got %+v, want no integration state", got)
		}
	})

	// source: spec/requests/api/v1/credit_notes_controller_spec.rb:97
	t.Run("another organization", func(t *testing.T) {
		other := rpctest.New(t)
		if status, body := other.REST(t, other.APIKey, http.MethodGet, "/v1/credit_notes/"+cn.ID.String(), nil); status != http.StatusNotFound {
			t.Errorf("got status = %d (%s), want = 404", status, body)
		}
	})

	for _, tt := range []struct {
		name, source string
		md           core.Metadata
		want         int
	}{
		{"with metadata", "spec/requests/api/v1/credit_notes_controller_spec.rb:113", core.Metadata{{Key: "foo", Value: "bar"}}, 1},
		{"without metadata", "spec/requests/api/v1/credit_notes_controller_spec.rb:120", nil, 0},
		{"with empty metadata", "spec/requests/api/v1/credit_notes_controller_spec.rb:131", core.Metadata{}, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			cn := h.StoredCreditNote(f.inv, 10, func(cn *creditnote.CreditNote) { cn.Metadata = tt.md })
			status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/credit_notes/"+cn.ID.String(), nil)
			if status != http.StatusOK {
				t.Fatalf("got status = %d (%s), want = 200", status, body)
			}
			if got := viewDecode(t, body).Metadata; len(got) != tt.want || (tt.want == 1 && (got[0].Key != "foo" || got[0].Value != "bar")) {
				t.Errorf("got metadata = %+v, want %d entries", got, tt.want)
			}
		})
	}
}

func TestCreditNoteIntegrationState(t *testing.T) {
	t.Parallel()
	f := newCNFixture(t)
	h := f.s.Fixtures
	accounting := h.Integration("xero", integration.ProviderXero)
	accounting.SyncCreditNotes = true
	h.Do(func(ctx context.Context, r port.Repos) error { return r.Integrations.Update(ctx, accounting) })
	h.IntegrationConnection(f.cust, accounting, true)
	tax := h.Integration("anrok", integration.ProviderAnrok)
	h.IntegrationConnection(f.cust, tax, true)

	pending := h.StoredCreditNote(f.inv, 10)
	synced := h.StoredCreditNote(f.inv, 10)
	failed := h.StoredCreditNote(f.inv, 10)
	resource := func(i *integration.Integration, cn *creditnote.CreditNote, ext string) {
		h.Do(func(ctx context.Context, r port.Repos) error {
			return r.IntegrationResources.Create(ctx, &integration.Resource{
				ID: core.NewID[core.IntegrationResourceID](h.IDs), OrganizationID: h.OrgID(), IntegrationID: i.ID,
				ResourceType: integration.ResourceTypeCreditNote, SyncableID: cn.ID.UUID(), ExternalID: ext,
				CreatedAt: h.Now(), UpdatedAt: h.Now(),
			})
		})
	}
	resource(accounting, synced, "xero-cn-1")
	resource(tax, synced, "anrok-cn-1")
	h.Do(func(ctx context.Context, r port.Repos) error {
		return r.CreditNoteErrorDetails.Create(ctx, &creditnote.ErrorDetail{
			ID: core.NewID[core.ErrorDetailID](h.IDs), OrganizationID: h.OrgID(), CreditNoteID: failed.ID,
			Code: creditnote.ErrorCodeTaxError, Details: []byte(`{"message":"refused"}`), CreatedAt: h.Now(),
		})
	})

	tests := []struct {
		name, source string
		cn           *creditnote.CreditNote
		syncable     bool
		taxSyncable  bool
		external     string
		taxProvider  string
	}{
		{"not synced yet", "app/graphql/types/credit_notes/object.rb:67-73", pending, true, false, "", ""},
		{"synced", "app/graphql/types/credit_notes/object.rb:82-100", synced, false, false, "xero-cn-1", "anrok-cn-1"},
		{"tax report failed", "app/graphql/types/credit_notes/object.rb:75-80", failed, true, true, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/credit_notes/"+tt.cn.ID.String(), nil)
			if status != http.StatusOK {
				t.Fatalf("got status = %d (%s), want = 200", status, body)
			}
			got := viewDecode(t, body)
			if boolOf(got.IntegrationSyncable) != tt.syncable || boolOf(got.TaxProviderSyncable) != tt.taxSyncable ||
				got.ExternalIntegrationID != tt.external || got.TaxProviderID != tt.taxProvider {
				t.Errorf("got syncable = %v, tax syncable = %v, external = %q, tax provider = %q, want = %v, %v, %q, %q",
					got.IntegrationSyncable, got.TaxProviderSyncable, got.ExternalIntegrationID, got.TaxProviderID,
					tt.syncable, tt.taxSyncable, tt.external, tt.taxProvider)
			}
		})
	}

	t.Run("an accounting integration that does not sync credit notes", func(t *testing.T) {
		accounting.SyncCreditNotes = false
		h.Do(func(ctx context.Context, r port.Repos) error { return r.Integrations.Update(ctx, accounting) })
		status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/credit_notes/"+pending.ID.String(), nil)
		if status != http.StatusOK || boolOf(viewDecode(t, body).IntegrationSyncable) {
			t.Errorf("got = %d %s, want not syncable", status, body)
		}
	})
}

func TestListCreditNoteFilters(t *testing.T) {
	t.Parallel()
	f := newCNFixture(t)
	h := f.s.Fixtures
	second := organization.NewBillingEntity(h.OrgID(), core.NewID[core.BillingEntityID](h.IDs), "second", "Second", h.Now())
	second.DocumentNumberPrefix = "SEC"
	h.Do(func(ctx context.Context, r port.Repos) error { return r.BillingEntities.Create(ctx, second) })

	credit := h.StoredCreditNote(f.inv, 10)
	refund := h.StoredCreditNote(f.inv, 30, func(cn *creditnote.CreditNote) {
		cn.CreditAmount, cn.BalanceAmount, cn.RefundAmount, cn.RefundStatus = 0, 0, 30, core.RefundStatusPending
		cn.CreditStatus, cn.Reason = "", creditnote.ReasonOrderChange
	})
	offset := h.StoredCreditNote(f.inv, 20, func(cn *creditnote.CreditNote) {
		cn.CreditAmount, cn.BalanceAmount, cn.OffsetAmount, cn.CreditStatus = 0, 0, 20, ""
		cn.IssuingDate = core.DateOf(rpctest.Now.AddDate(0, -2, 0), rpctest.Now.Location())
	})
	eur, err := money.LookupCurrency("EUR")
	if err != nil {
		t.Fatal(err)
	}
	other := h.Customer()
	otherInv, _ := h.CreditableInvoice(other, []int64{5_000_000_000}, nil, []func(*invoice.Invoice){func(inv *invoice.Invoice) {
		inv.Currency, inv.SelfBilled, inv.BillingEntityID = eur, true, second.ID
		inv.Number, inv.PurchaseOrderNumber = "SEC-2025-001", new("PO-7")
	}})
	big := h.StoredCreditNote(otherInv, 3_000_000_000, func(cn *creditnote.CreditNote) {
		cn.Currency, cn.Reason, cn.CreditStatus = otherInv.Currency, creditnote.ReasonFraudulentCharge, creditnote.CreditStatusConsumed
		cn.BalanceAmount = 0
	})
	h.StoredCreditNote(f.inv, 10, func(cn *creditnote.CreditNote) { cn.Status = creditnote.StatusDraft })
	ids := func(cns ...*creditnote.CreditNote) []string {
		out := make([]string, len(cns))
		for i, cn := range cns {
			out[i] = cn.ID.String()
		}
		slices.Sort(out)
		return out
	}
	all := ids(credit, refund, offset, big)
	missingInv := core.NewID[core.InvoiceID](core.UUIDv7Generator{}).String()

	tests := []struct {
		name, source string
		filter       string
		path         string
		status       int
		want         []string
	}{
		{name: "no arguments", source: "spec/graphql/resolvers/credit_notes_resolver_spec.rb:43", want: all},
		{name: "currency", source: "spec/graphql/resolvers/credit_notes_resolver_spec.rb:57", filter: "currency=EUR", want: ids(big)},
		{name: "customer external id", source: "spec/graphql/resolvers/credit_notes_resolver_spec.rb:71", path: "/v1/customers/external_id%3A" + other.ExternalID + "/credit_notes", want: ids(big)},
		{name: "customer id", source: "spec/graphql/resolvers/credit_notes_resolver_spec.rb:85", path: "/v1/customers/" + f.cust.ID.String() + "/credit_notes", want: ids(credit, refund, offset)},
		{name: "reason", source: "spec/graphql/resolvers/credit_notes_resolver_spec.rb:106", filter: "reason=ORDER_CHANGE OR reason=FRAUDULENT_CHARGE", want: ids(refund, big)},
		{name: "credit status", source: "spec/graphql/resolvers/credit_notes_resolver_spec.rb:129", filter: "credit_status=AVAILABLE OR credit_status=CONSUMED", want: ids(credit, big)},
		{name: "refund status", source: "spec/graphql/resolvers/credit_notes_resolver_spec.rb:152", filter: "refund_status=PENDING", want: ids(refund)},
		{name: "type credit", source: "spec/graphql/resolvers/credit_notes_resolver_spec.rb:186", filter: "type=credit", want: ids(credit, big)},
		{name: "type refund", source: "spec/graphql/resolvers/credit_notes_resolver_spec.rb:194", filter: "type=refund", want: ids(refund)},
		{name: "type offset", source: "spec/graphql/resolvers/credit_notes_resolver_spec.rb:202", filter: "type=offset", want: ids(offset)},
		{name: "several types", source: "spec/graphql/resolvers/credit_notes_resolver_spec.rb:210", filter: "type=refund OR type=offset", want: ids(refund, offset)},
		{name: "invoice number", source: "spec/graphql/resolvers/credit_notes_resolver_spec.rb:225", filter: "invoice_number=SEC-2025-001", want: ids(big)},
		{name: "purchase order number", source: "app/controllers/concerns/credit_note_index.rb:49", filter: "purchase_order_number=PO-7", want: ids(big)},
		{
			name: "issuing dates", source: "spec/graphql/resolvers/credit_notes_resolver_spec.rb:244",
			filter: "issuing_date>=" + core.DateOf(rpctest.Now.AddDate(0, -3, 0), rpctest.Now.Location()).String() +
				" AND issuing_date<=" + core.DateOf(rpctest.Now.AddDate(0, -1, 0), rpctest.Now.Location()).String(),
			want: ids(offset),
		},
		{name: "amount range", source: "spec/graphql/resolvers/credit_notes_resolver_spec.rb:263", filter: "amount>=15 AND amount<=25", want: ids(offset)},
		{name: "amounts above 2^31", source: "spec/graphql/resolvers/credit_notes_resolver_spec.rb:272", filter: "amount>=2147483648 AND amount<=4000000000", want: ids(big)},
		{name: "self billed", source: "spec/graphql/resolvers/credit_notes_resolver_spec.rb:318", filter: "self_billed=true", want: ids(big)},
		{name: "billing entity id", source: "spec/graphql/resolvers/credit_notes_resolver_spec.rb:348", filter: "billing_entity_id=" + second.ID.String(), want: ids(big)},
		{name: "billing entity code", source: "app/controllers/concerns/credit_note_index.rb:8", filter: `billing_entity_id="code:second"`, want: ids(big)},
		{name: "unknown billing entity code", source: "app/controllers/concerns/credit_note_index.rb:9", filter: `billing_entity_id="code:second" OR billing_entity_id="code:nope"`, status: http.StatusNotFound},
		{name: "an invoice's credit notes", source: "spec/graphql/resolvers/invoice_credit_notes_resolver_spec.rb:35", filter: "invoice_id=" + otherInv.ID.String(), want: ids(big)},
		{name: "an invoice that does not exist", source: "spec/graphql/resolvers/invoice_credit_notes_resolver_spec.rb:56", filter: "invoice_id=" + missingInv, status: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			path := tt.path
			if path == "" {
				path = "/v1/credit_notes"
			}
			if tt.filter != "" {
				path += "?filter=" + url.QueryEscape(tt.filter)
			}
			want := tt.status
			if want == 0 {
				want = http.StatusOK
			}
			status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, path, nil)
			if status != want {
				t.Fatalf("got status = %d (%s), want = %d", status, body, want)
			}
			if status != http.StatusOK {
				return
			}
			var got []string
			for _, cn := range decode[struct {
				CreditNotes []viewCreditNote `json:"credit_notes"`
			}](t, body).CreditNotes {
				got = append(got, cn.ID)
			}
			slices.Sort(got)
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("got = %v, want = %v", got, tt.want)
			}
		})
	}

	// source: spec/graphql/resolvers/credit_notes_resolver_spec.rb:286
	t.Run("search term", func(t *testing.T) {
		status, body := f.s.REST(t, f.s.APIKey, http.MethodGet, "/v1/credit_notes?search="+url.QueryEscape(big.Number), nil)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		cns := decode[struct {
			CreditNotes []viewCreditNote `json:"credit_notes"`
		}](t, body).CreditNotes
		if len(cns) != 1 || cns[0].ID != big.ID.String() || cns[0].InvoiceNumber != "SEC-2025-001" || cns[0].BillingEntityID != second.ID.String() {
			t.Errorf("got = %+v, want only %s with its invoice", cns, big.ID)
		}
	})
}

// TestCreditNotePermissions ports the "requires API permission" examples
// of the credit note endpoints: a key without the action's level on
// credit_note is refused with 403, one with it is not.
func TestCreditNotePermissions(t *testing.T) {
	t.Parallel()
	f := newCNFixture(t)
	cn := f.s.Fixtures.StoredCreditNote(f.inv, 10)
	d := auth.Deps{Tx: f.s.Store, Clock: f.s.Clock, IDs: core.UUIDv7Generator{}, Rand: rand.Reader}
	keyWith := func(level permission.Level) rpctest.Credential {
		k, err := auth.NewAPIKeys(d, nil).Create(t.Context(), f.s.Org.ID, nil, map[permission.APIResource]permission.Level{permission.APIResourceCreditNote: level})
		if err != nil {
			t.Fatal(err)
		}
		return rpctest.Credential{Token: k.Secret}
	}
	read, write := keyWith(permission.LevelRead), keyWith(permission.LevelWrite)
	missing := core.NewID[core.CreditNoteID](core.UUIDv7Generator{}).String()
	tests := []struct {
		source, method, path, body string
		write                      bool
	}{
		{"spec/requests/api/v1/credit_notes_controller_spec.rb:30", http.MethodGet, "/v1/credit_notes/" + cn.ID.String(), "", false},
		{"spec/requests/api/v1/credit_notes_controller_spec.rb:150", http.MethodPatch, "/v1/credit_notes/" + missing, `{"refund_status":"REFUND_STATUS_SUCCEEDED"}`, true},
		{"spec/requests/api/v1/credit_notes_controller_spec.rb:248, 304", http.MethodPost, "/v1/credit_notes/" + missing + ":download", "{}", true},
		{"spec/requests/api/v1/credit_notes_controller_spec.rb:421", http.MethodPost, "/v1/credit_notes", `{"invoice_id":"` + missing + `","reason":"CREDIT_NOTE_REASON_OTHER"}`, true},
		{"spec/requests/api/v1/credit_notes_controller_spec.rb:694", http.MethodPost, "/v1/credit_notes/" + missing + ":void", "{}", true},
		{"spec/requests/api/v1/credit_notes_controller_spec.rb:754", http.MethodPost, "/v1/credit_notes:estimate", `{"invoice_id":"` + missing + `"}`, true},
		{"spec/requests/api/v1/credit_notes/metadata_controller_spec.rb:16", http.MethodPost, "/v1/credit_notes/" + missing + "/metadata", `{}`, true},
		{"spec/requests/api/v1/credit_notes/metadata_controller_spec.rb:103", http.MethodPatch, "/v1/credit_notes/" + missing + "/metadata", `{}`, true},
		{"spec/requests/api/v1/credit_notes/metadata_controller_spec.rb:180", http.MethodDelete, "/v1/credit_notes/" + missing + "/metadata", "", true},
		{"spec/requests/api/v1/credit_notes/metadata_controller_spec.rb:220", http.MethodDelete, "/v1/credit_notes/" + missing + "/metadata/foo", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			var body any
			if tt.body != "" {
				body = tt.body
			}
			if status, out := f.s.REST(t, write, tt.method, tt.path, body); status == http.StatusForbidden {
				t.Errorf("write key: got = 403 (%s), want another status", out)
			}
			status, out := f.s.REST(t, read, tt.method, tt.path, body)
			if tt.write != (status == http.StatusForbidden) {
				t.Errorf("read key: got = %d (%s), want forbidden = %v", status, out, tt.write)
			}
		})
	}
}

func TestCreditNoteUpdateAndVoidCases(t *testing.T) {
	t.Parallel()
	f := newCNFixture(t)
	h := f.s.Fixtures
	note := func(md core.Metadata, st creditnote.Status) *creditnote.CreditNote {
		return h.StoredCreditNote(f.inv, 10, func(cn *creditnote.CreditNote) {
			cn.RefundAmount, cn.RefundStatus, cn.Metadata, cn.Status = 5, core.RefundStatusPending, md, st
		})
	}
	existing := core.Metadata{{Key: "existing", Value: "value"}}
	missing := core.NewID[core.CreditNoteID](core.UUIDv7Generator{}).String()
	tests := []struct {
		name, source string
		method, path string
		body         string
		status       int
		want         []string // the metadata "key=value" after the call
	}{
		{
			name: "updates the refund status, keeping the metadata", source: "spec/graphql/mutations/credit_notes/update_spec.rb:28, 100",
			method: http.MethodPatch, path: "/v1/credit_notes/" + note(existing, creditnote.StatusFinalized).ID.String(),
			body: `{"refund_status":"REFUND_STATUS_SUCCEEDED"}`, status: http.StatusOK, want: []string{"existing=value"},
		},
		{
			name: "not found", source: "spec/requests/api/v1/credit_notes_controller_spec.rb:166 spec/graphql/mutations/credit_notes/update_spec.rb:44",
			method: http.MethodPatch, path: "/v1/credit_notes/" + missing, body: `{"refund_status":"REFUND_STATUS_SUCCEEDED"}`, status: http.StatusNotFound,
		},
		{
			name: "deleted", source: "spec/graphql/mutations/credit_notes/update_spec.rb:60",
			method: http.MethodPatch, path: "/v1/credit_notes/" + note(nil, creditnote.StatusDeleted).ID.String(),
			body: `{"refund_status":"REFUND_STATUS_SUCCEEDED"}`, status: http.StatusNotFound,
		},
		{
			name: "empty metadata keeps the existing one", source: "spec/requests/api/v1/credit_notes_controller_spec.rb:220",
			method: http.MethodPatch, path: "/v1/credit_notes/" + note(existing, creditnote.StatusFinalized).ID.String(),
			body: `{"metadata":[]}`, status: http.StatusOK, want: []string{"existing=value"},
		},
		{
			name: "creates metadata", source: "spec/requests/api/v1/credit_notes_controller_spec.rb:232",
			method: http.MethodPatch, path: "/v1/credit_notes/" + note(nil, creditnote.StatusFinalized).ID.String(),
			body: `{"metadata":[{"key":"new","value":"data"}]}`, status: http.StatusOK, want: []string{"new=data"},
		},
		{
			name: "the dashboard replaces the metadata", source: "spec/graphql/mutations/credit_notes/update_spec.rb:87",
			method: http.MethodPost, path: "/v1/credit_notes/" + note(existing, creditnote.StatusFinalized).ID.String() + "/metadata",
			body: `{"metadata":[{"key":"new","value":"data"}]}`, status: http.StatusOK, want: []string{"new=data"},
		},
		{
			name: "voids the credit note", source: "spec/graphql/mutations/credit_notes/void_spec.rb:29",
			method: http.MethodPost, path: "/v1/credit_notes/" + note(nil, creditnote.StatusFinalized).ID.String() + ":void", body: "{}", status: http.StatusOK,
		},
		{
			name: "void not found", source: "spec/graphql/mutations/credit_notes/void_spec.rb:42",
			method: http.MethodPost, path: "/v1/credit_notes/" + missing + ":void", body: "{}", status: http.StatusNotFound,
		},
		{
			name: "void deleted", source: "spec/requests/api/v1/credit_notes_controller_spec.rb:727 spec/graphql/mutations/credit_notes/void_spec.rb:55",
			method: http.MethodPost, path: "/v1/credit_notes/" + note(nil, creditnote.StatusDeleted).ID.String() + ":void", body: "{}", status: http.StatusNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			status, body := f.s.REST(t, f.s.APIKey, tt.method, tt.path, tt.body)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if tt.want == nil {
				return
			}
			md := decode[struct {
				CreditNote viewCreditNote `json:"credit_note"`
				Metadata   []struct {
					Key   string `json:"key"`
					Value string `json:"value"`
				} `json:"metadata"`
			}](t, body)
			items := md.CreditNote.Metadata
			if md.Metadata != nil {
				items = md.Metadata
			}
			var got []string
			for _, m := range items {
				got = append(got, m.Key+"="+m.Value)
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("got metadata = %v, want = %v", got, tt.want)
			}
		})
	}
}

func TestCreditNoteDocumentCases(t *testing.T) {
	t.Parallel()
	f := newCNFixture(t)
	h := f.s.Fixtures
	cn := h.StoredCreditNote(f.inv, 10)
	withXML := h.StoredCreditNote(f.inv, 10, func(cn *creditnote.CreditNote) {
		cn.File, cn.XMLFile = "credit_notes/cn.pdf", "credit_notes/cn.xml"
	})
	draft := h.StoredCreditNote(f.inv, 10, func(cn *creditnote.CreditNote) { cn.Status = creditnote.StatusDraft })
	other := rpctest.New(t)
	tests := []struct {
		name, source string
		s            *rpctest.Server
		id           string
		status       int
		xml          bool
	}{
		{"enqueues the document job", "spec/requests/api/v1/credit_notes_controller_spec.rb:306", f.s, cn.ID.String(), http.StatusOK, false},
		{"XML attached", "spec/requests/api/v1/credit_notes_controller_spec.rb:316", f.s, withXML.ID.String(), http.StatusOK, true},
		{"not found", "spec/requests/api/v1/credit_notes_controller_spec.rb:327", f.s, core.NewID[core.CreditNoteID](core.UUIDv7Generator{}).String(), http.StatusNotFound, false},
		{"draft", "spec/requests/api/v1/credit_notes_controller_spec.rb:336", f.s, draft.ID.String(), http.StatusNotFound, false},
		{"another organization", "spec/requests/api/v1/credit_notes_controller_spec.rb:290, 346", other, cn.ID.String(), http.StatusNotFound, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			status, body := tt.s.REST(t, tt.s.APIKey, http.MethodPost, "/v1/credit_notes/"+tt.id+":download", "{}")
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			got := decode[struct {
				XML *struct {
					ContentType string `json:"content_type"`
				} `json:"xml"`
			}](t, body)
			if (got.XML != nil) != tt.xml {
				t.Errorf("got xml = %+v (%s), want present = %v", got.XML, body, tt.xml)
			}
		})
	}
}

func TestCreateCreditNoteCases(t *testing.T) {
	t.Parallel()
	type check func(t *testing.T, f *cnFixture, cn viewCreditNote)
	creditInvoice := func(status core.PaymentStatus) func(f *cnFixture) (*invoice.Invoice, []*invoice.Fee) {
		return func(f *cnFixture) (*invoice.Invoice, []*invoice.Fee) {
			h := f.s.Fixtures
			w := h.Wallet(f.cust, "100", func(w *wallet.Wallet) { w.RateAmount = money.NewFromInt(1) })
			tx := h.InboundTransaction(w, wallet.TransactionStatusPurchased, "100")
			return h.CreditableInvoice(f.cust, []int64{100}, nil, []func(*invoice.Invoice){func(inv *invoice.Invoice) {
				inv.Type, inv.PaymentStatus = invoice.TypeCredit, status
			}}, billingtest.CreditFee(tx.ID))
		}
	}
	tests := []struct {
		name, source string
		invoice      func(f *cnFixture) (*invoice.Invoice, []*invoice.Fee)
		body         func(inv *invoice.Invoice, fees []*invoice.Fee) string
		status       int
		check        check
	}{
		{
			name: "creates a credit note", source: "spec/graphql/mutations/credit_notes/create_spec.rb:58",
			body: func(inv *invoice.Invoice, fees []*invoice.Fee) string {
				return fmt.Sprintf(`{"invoice_id":%q,"reason":"CREDIT_NOTE_REASON_DUPLICATED_CHARGE","credit_amount":{"currency":"USD","amount":"0.10"},
					"items":[{"fee_id":%q,"amount":"0.10"}]}`, inv.ID, fees[0].ID)
			},
			status: http.StatusOK,
			check: func(t *testing.T, f *cnFixture, cn viewCreditNote) {
				if cn.TotalAmount != (cnMoney{"USD", "0.10"}) || cn.InvoiceNumber != f.inv.Number || cn.SequentialID != "1" {
					t.Errorf("got %+v, want the first note of 0.10 on %s", cn, f.inv.Number)
				}
			},
		},
		{
			name: "invoice not found", source: "spec/graphql/mutations/credit_notes/create_spec.rb:112",
			body: func(_ *invoice.Invoice, fees []*invoice.Fee) string {
				return fmt.Sprintf(`{"invoice_id":%q,"reason":"CREDIT_NOTE_REASON_OTHER","items":[{"fee_id":%q,"amount":"0.10"}]}`,
					core.NewID[core.InvoiceID](core.UUIDv7Generator{}), fees[0].ID)
			},
			status: http.StatusNotFound,
		},
		{
			name: "zero total", source: "spec/requests/api/v1/credit_notes_controller_spec.rb:482 spec/graphql/mutations/credit_notes/create_spec.rb:139",
			body: func(inv *invoice.Invoice, fees []*invoice.Fee) string {
				return fmt.Sprintf(`{"invoice_id":%q,"reason":"CREDIT_NOTE_REASON_DUPLICATED_CHARGE",
					"items":[{"fee_id":%q,"amount":"0"},{"fee_id":%q,"amount":"0"}]}`, inv.ID, fees[0].ID, fees[1].ID)
			},
			status: http.StatusBadRequest,
		},
		{
			name: "with metadata", source: "spec/graphql/mutations/credit_notes/create_spec.rb:181",
			body: func(inv *invoice.Invoice, fees []*invoice.Fee) string {
				return fmt.Sprintf(`{"invoice_id":%q,"reason":"CREDIT_NOTE_REASON_OTHER","credit_amount":{"currency":"USD","amount":"0.10"},
					"items":[{"fee_id":%q,"amount":"0.10"}],"metadata":[{"key":"k","value":"v"}]}`, inv.ID, fees[0].ID)
			},
			status: http.StatusOK,
			check: func(t *testing.T, _ *cnFixture, cn viewCreditNote) {
				if len(cn.Metadata) != 1 || cn.Metadata[0].Key != "k" {
					t.Errorf("got metadata = %+v, want = [k: v]", cn.Metadata)
				}
			},
		},
		{
			name: "with empty metadata", source: "spec/requests/api/v1/credit_notes_controller_spec.rb:523",
			body: func(inv *invoice.Invoice, fees []*invoice.Fee) string {
				return fmt.Sprintf(`{"invoice_id":%q,"reason":"CREDIT_NOTE_REASON_OTHER","credit_amount":{"currency":"USD","amount":"0.10"},
					"items":[{"fee_id":%q,"amount":"0.10"}],"metadata":[]}`, inv.ID, fees[0].ID)
			},
			status: http.StatusOK,
			check: func(t *testing.T, _ *cnFixture, cn viewCreditNote) {
				if len(cn.Metadata) != 0 {
					t.Errorf("got metadata = %+v, want none", cn.Metadata)
				}
			},
		},
		{
			name: "an offset settles the invoice", source: "spec/requests/api/v1/credit_notes_controller_spec.rb:559, 584",
			body: func(inv *invoice.Invoice, fees []*invoice.Fee) string {
				return fmt.Sprintf(`{"invoice_id":%q,"reason":"CREDIT_NOTE_REASON_DUPLICATED_CHARGE","offset_amount":{"currency":"USD","amount":"0.08"},
					"items":[{"fee_id":%q,"amount":"0.08"}]}`, inv.ID, fees[0].ID)
			},
			status: http.StatusOK,
			check: func(t *testing.T, f *cnFixture, cn viewCreditNote) {
				s := f.s.Fixtures.Settlements(f.inv.ID)
				if cn.TotalAmount != (cnMoney{"USD", "0.08"}) || len(s) != 1 || s[0].Amount != 8 || s[0].Type != invoice.SettlementTypeCreditNote {
					t.Errorf("got total = %v, settlements = %+v, want 0.08 settled by the credit note", cn.TotalAmount, s)
				}
			},
		},
		{
			name: "an offset on a credit invoice pending payment", source: "spec/requests/api/v1/credit_notes_controller_spec.rb:615",
			invoice: creditInvoice(core.PaymentStatusPending),
			body: func(inv *invoice.Invoice, fees []*invoice.Fee) string {
				return fmt.Sprintf(`{"invoice_id":%q,"reason":"CREDIT_NOTE_REASON_OTHER","offset_amount":{"currency":"USD","amount":"1.00"},
					"items":[{"fee_id":%q,"amount":"1.00"}]}`, inv.ID, fees[0].ID)
			},
			status: http.StatusOK,
		},
		{
			name: "a credit on a credit invoice pending payment", source: "spec/requests/api/v1/credit_notes_controller_spec.rb:633",
			invoice: creditInvoice(core.PaymentStatusPending),
			body: func(inv *invoice.Invoice, fees []*invoice.Fee) string {
				return fmt.Sprintf(`{"invoice_id":%q,"reason":"CREDIT_NOTE_REASON_OTHER","credit_amount":{"currency":"USD","amount":"0.50"},
					"items":[{"fee_id":%q,"amount":"0.50"}]}`, inv.ID, fees[0].ID)
			},
			status: http.StatusBadRequest,
		},
		{
			name: "an offset on a credit invoice whose payment failed", source: "spec/requests/api/v1/credit_notes_controller_spec.rb:661",
			invoice: creditInvoice(core.PaymentStatusFailed),
			body: func(inv *invoice.Invoice, fees []*invoice.Fee) string {
				return fmt.Sprintf(`{"invoice_id":%q,"reason":"CREDIT_NOTE_REASON_OTHER","offset_amount":{"currency":"USD","amount":"1.00"},
					"items":[{"fee_id":%q,"amount":"1.00"}]}`, inv.ID, fees[0].ID)
			},
			status: http.StatusOK,
		},
		{
			name: "a credit on a credit invoice whose payment failed", source: "spec/requests/api/v1/credit_notes_controller_spec.rb:679",
			invoice: creditInvoice(core.PaymentStatusFailed),
			body: func(inv *invoice.Invoice, fees []*invoice.Fee) string {
				return fmt.Sprintf(`{"invoice_id":%q,"reason":"CREDIT_NOTE_REASON_OTHER","credit_amount":{"currency":"USD","amount":"0.50"},
					"items":[{"fee_id":%q,"amount":"0.50"}]}`, inv.ID, fees[0].ID)
			},
			status: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			f := newCNFixture(t)
			inv, fees := f.inv, f.fees
			if tt.invoice != nil {
				inv, fees = tt.invoice(f)
			}
			status, body := f.s.REST(t, f.s.APIKey, http.MethodPost, "/v1/credit_notes", tt.body(inv, fees))
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if tt.check != nil {
				tt.check(t, f, viewDecode(t, body))
			}
		})
	}
}

func TestEstimateCreditNoteCases(t *testing.T) {
	t.Parallel()
	f := newCNFixture(t)
	for _, tt := range []struct {
		source, invoice string
		status          int
	}{
		{"spec/graphql/resolvers/credit_notes/estimate_resolver_spec.rb:59", f.inv.ID.String(), http.StatusOK},
		{"spec/graphql/resolvers/credit_notes/estimate_resolver_spec.rb:83", core.NewID[core.InvoiceID](core.UUIDv7Generator{}).String(), http.StatusNotFound},
	} {
		t.Logf("source: %s", tt.source)
		body := fmt.Sprintf(`{"invoice_id":%q,"items":[{"fee_id":%q,"amount":"0.05"}]}`, tt.invoice, f.fees[0].ID)
		status, out := f.s.REST(t, f.s.APIKey, http.MethodPost, "/v1/credit_notes:estimate", body)
		if status != tt.status {
			t.Fatalf("got status = %d (%s), want = %d", status, out, tt.status)
		}
		if status != http.StatusOK {
			continue
		}
		// The estimate carries the invoice's number (credit_notes_controller_spec.rb:763).
		if cn := viewDecode(t, out); cn.InvoiceNumber != f.inv.Number || cn.TotalAmount != (cnMoney{"USD", "0.05"}) {
			t.Errorf("got %+v, want 0.05 on %s", cn, f.inv.Number)
		}
	}
}

func TestRetryCreditNoteTaxReportingMutation(t *testing.T) {
	t.Parallel()
	t.Log("source: spec/graphql/mutations/credit_notes/retry_tax_reporting_spec.rb:101")
	f := newCNFixture(t)
	cn := f.s.Fixtures.StoredCreditNote(f.inv, 10)
	status, body := f.s.REST(t, f.s.APIKey, http.MethodPost, "/v1/credit_notes/"+cn.ID.String()+":retry_tax_reporting", `{}`)
	if status != http.StatusOK || viewDecode(t, body).ID != cn.ID.String() {
		t.Errorf("got = %d %s, want the credit note %s", status, body, cn.ID)
	}
}
