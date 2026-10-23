package creditnotes_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// storedNote returns a harness with a finalized credit note of 120 on a
// finalized invoice, as the lago credit_note factory.
func storedNote(t *testing.T, opts ...func(*creditnote.CreditNote)) (*billingtest.Harness, *creditnotes.Service, *creditnote.CreditNote) {
	t.Helper()
	h, svc := newHarness(t)
	c := h.Customer()
	inv := h.StoredInvoice(c, func(inv *invoice.Invoice) { inv.Status = invoice.StatusFinalized })
	return h, svc, h.StoredCreditNote(inv, 120, opts...)
}

func draft(cn *creditnote.CreditNote) { cn.Status = creditnote.StatusDraft }

func wantNotFound(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("got = %v, want = %v", err, core.ErrNotFound)
	}
}

func TestVoid(t *testing.T) {
	tests := []struct {
		source string
		opts   []func(*creditnote.CreditNote)
		// unknown voids an identifier that is not stored.
		unknown bool
		rule    string
		found   bool
	}{
		{source: "spec/services/credit_notes/void_service_spec.rb:11", found: true},
		{source: "spec/services/credit_notes/void_service_spec.rb:24", unknown: true},
		{source: "spec/services/credit_notes/void_service_spec.rb:37", opts: []func(*creditnote.CreditNote){draft}},
		{
			source: "spec/services/credit_notes/void_service_spec.rb:49", found: true, rule: creditnotes.CodeNoVoidableAmount,
			opts: []func(*creditnote.CreditNote){func(cn *creditnote.CreditNote) { cn.CreditStatus = creditnote.CreditStatusVoided }},
		},
	}
	for _, tt := range tests {
		t.Run(tt.source[len(tt.source)-3:], func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			h, svc, cn := storedNote(t, tt.opts...)
			id := cn.ID
			if tt.unknown {
				id = core.NewID[core.CreditNoteID](h.IDs)
			}
			got, err := svc.Void(h.Ctx, h.OrgID(), id)
			switch {
			case !tt.found:
				wantNotFound(t, err)
			case tt.rule != "":
				wantRule(t, err, tt.rule)
			default:
				must(t, err)
				eq(t, "credit status", got.CreditStatus, creditnote.CreditStatusVoided)
				eq(t, "voided at", got.VoidedAt != nil, true)
				eq(t, "balance", h.ReloadCreditNote(id).BalanceAmount, 0)
			}
		})
	}
}

func TestDelete(t *testing.T) {
	tests := []struct {
		source  string
		opts    []func(*creditnote.CreditNote)
		unknown bool
		rule    string
		found   bool
	}{
		{source: "spec/services/credit_notes/delete_service_spec.rb:14", opts: []func(*creditnote.CreditNote){draft}, found: true},
		{source: "spec/services/credit_notes/delete_service_spec.rb:25", unknown: true},
		{source: "spec/services/credit_notes/delete_service_spec.rb:37", found: true, rule: creditnotes.CodeNotDeletable},
	}
	for _, tt := range tests {
		t.Run(tt.source[len(tt.source)-3:], func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			h, svc, cn := storedNote(t, tt.opts...)
			id := cn.ID
			if tt.unknown {
				id = core.NewID[core.CreditNoteID](h.IDs)
			}
			err := svc.Delete(h.Ctx, h.OrgID(), id)
			switch {
			case !tt.found:
				wantNotFound(t, err)
			case tt.rule != "":
				wantRule(t, err, tt.rule)
				eq(t, "status", h.ReloadCreditNote(id).Status, creditnote.StatusFinalized)
			default:
				must(t, err)
				got, err := creditnotesGet(h, id)
				must(t, err)
				eq(t, "status", got.Status, creditnote.StatusDeleted)
				// A deleted credit note is no longer found, not even as a draft.
				wantNotFound(t, svc.Delete(h.Ctx, h.OrgID(), id))
			}
		})
	}
}

func creditnotesGet(h *billingtest.Harness, id core.CreditNoteID) (*creditnote.CreditNote, error) {
	var out *creditnote.CreditNote
	err := h.Deps.Tx.InTx(h.Ctx, func(ctx context.Context, r port.Repos) error {
		cn, err := r.CreditNotes.Get(ctx, h.OrgID(), id)
		out = cn
		return err
	})
	return out, err
}

func TestUpdate(t *testing.T) {
	succeeded := core.RefundStatusSucceeded
	foo := core.RefundStatus("foo_bar")
	md := func(kv ...string) *core.Metadata {
		m := core.Metadata{}
		for i := 0; i < len(kv); i += 2 {
			m = append(m, core.MetadataItem{Key: kv[i], Value: kv[i+1]})
		}
		return &m
	}
	withFoo := func(cn *creditnote.CreditNote) { cn.Metadata = *md("foo", "bar") }
	tests := []struct {
		source  string
		opts    []func(*creditnote.CreditNote)
		in      creditnotes.UpdateInput
		fields  []string
		found   bool
		want    core.Metadata
		touched bool
		check   func(t *testing.T, cn *creditnote.CreditNote)
	}{
		{
			source: "spec/services/credit_notes/update_service_spec.rb:15", found: true, touched: true,
			in: creditnotes.UpdateInput{RefundStatus: &succeeded},
			check: func(t *testing.T, cn *creditnote.CreditNote) {
				eq(t, "refund status", cn.RefundStatus, core.RefundStatusSucceeded)
				eq(t, "refunded at", cn.RefundedAt != nil, true)
			},
		},
		{
			source: "spec/services/credit_notes/update_service_spec.rb:42", found: true,
			in: creditnotes.UpdateInput{RefundStatus: &foo}, fields: []string{"refund_status:" + core.CodeInvalid},
		},
		{
			source: "spec/services/credit_notes/update_service_spec.rb:55",
			opts:   []func(*creditnote.CreditNote){draft}, in: creditnotes.UpdateInput{RefundStatus: &succeeded},
		},
		{
			// and :79
			source: "spec/services/credit_notes/update_service_spec.rb:72", found: true, touched: true,
			opts: []func(*creditnote.CreditNote){withFoo}, in: creditnotes.UpdateInput{Metadata: md()},
		},
		{
			source: "spec/services/credit_notes/update_service_spec.rb:89", found: true, touched: true,
			in: creditnotes.UpdateInput{Metadata: md("foo", "bar")}, want: *md("foo", "bar"),
		},
		{
			// and :107
			source: "spec/services/credit_notes/update_service_spec.rb:100", found: true, touched: true,
			opts: []func(*creditnote.CreditNote){withFoo}, in: creditnotes.UpdateInput{Metadata: md("baz", "qux")},
			want: *md("baz", "qux"),
		},
		{
			source: "spec/services/credit_notes/update_service_spec.rb:120", found: true, touched: true,
			opts: []func(*creditnote.CreditNote){withFoo}, in: creditnotes.UpdateInput{Metadata: md("baz", "qux"), MergeMetadata: true},
			want: *md("foo", "bar", "baz", "qux"),
		},
		{
			// and :137
			source: "spec/services/credit_notes/update_service_spec.rb:133", found: true,
			opts: []func(*creditnote.CreditNote){withFoo}, want: *md("foo", "bar"),
		},
		{
			source: "spec/services/credit_notes/update_service_spec.rb:149", found: true, touched: true,
			opts: []func(*creditnote.CreditNote){withFoo}, in: creditnotes.UpdateInput{RefundStatus: &succeeded},
			want: *md("foo", "bar"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.source[len(tt.source)-3:], func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			h, svc, cn := storedNote(t, tt.opts...)
			before := h.ReloadCreditNote(cn.ID).UpdatedAt
			h.Clock.Set(now.Add(time.Minute))
			_, err := svc.Update(h.Ctx, h.OrgID(), cn.ID, tt.in)
			switch {
			case !tt.found:
				wantNotFound(t, err)
				return
			case tt.fields != nil:
				wantFieldIncluded(t, err, tt.fields[0])
				return
			}
			must(t, err)
			got := h.ReloadCreditNote(cn.ID)
			eq(t, "touched", !got.UpdatedAt.Equal(before), tt.touched)
			eq(t, "metadata", len(got.Metadata), len(tt.want))
			for i := range min(len(got.Metadata), len(tt.want)) {
				eq(t, "metadata item", got.Metadata[i], tt.want[i])
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func TestRecredit(t *testing.T) {
	tests := []struct {
		source string
		status creditnote.CreditStatus
		rule   string
	}{
		{source: "spec/services/credit_notes/recredit_service_spec.rb:29", status: creditnote.CreditStatusVoided, rule: creditnotes.CodeCreditNoteVoided},
		// and :55
		{source: "spec/services/credit_notes/recredit_service_spec.rb:47 (also :55)", status: creditnote.CreditStatusConsumed},
	}
	for _, tt := range tests {
		t.Run(tt.source[len(tt.source)-3:], func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			h, svc, cn := storedNote(t, func(cn *creditnote.CreditNote) { cn.CreditStatus = tt.status })
			var err error
			h.Do(func(ctx context.Context, r port.Repos) error {
				err = svc.Recredit(ctx, r, cn, 200)
				return nil
			})
			if tt.rule != "" {
				wantRule(t, err, tt.rule)
				return
			}
			must(t, err)
			got := h.ReloadCreditNote(cn.ID)
			eq(t, "balance", got.BalanceAmount, 320)
			eq(t, "credit status", got.CreditStatus, creditnote.CreditStatusAvailable)
		})
	}
}

func TestRefreshDraft(t *testing.T) {
	tests := []struct {
		source string
		status creditnote.Status
	}{
		{source: "spec/services/credit_notes/refresh_draft_service_spec.rb:49", status: creditnote.StatusFinalized},
		// and :58
		{source: "spec/services/credit_notes/refresh_draft_service_spec.rb:54 (also :58)", status: creditnote.StatusDraft},
	}
	for _, tt := range tests {
		t.Run(tt.source[len(tt.source)-3:], func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			h, svc := newHarness(t)
			c := h.Customer()
			tax := h.Tax("vat", "20")
			inv := h.StoredInvoice(c, func(inv *invoice.Invoice) {
				inv.Status, inv.FeesAmount, inv.CouponsAmount = invoice.StatusDraft, 100, 20
			})
			old := h.StoredFee(inv, 200)
			fee := h.StoredFee(inv, 100, billingtest.Coupons("20"), h.FeeTax(tax))
			h.InvoiceTaxes(inv, []*invoice.Fee{fee}, tax)
			itemID := core.NewID[core.CreditNoteItemID](h.IDs)
			cn := h.StoredCreditNote(inv, 100, func(cn *creditnote.CreditNote) {
				cn.Status = tt.status
				cn.Items = []creditnote.Item{{ID: itemID, FeeID: old.ID, Amount: 100, PreciseAmount: money.NewFromInt(100)}}
			})
			h.Clock.Set(now.Add(time.Minute))
			h.Do(func(ctx context.Context, r port.Repos) error {
				return svc.RefreshDraft(ctx, r, cn, fee, map[core.CreditNoteItemID]int64{itemID: old.Amount})
			})
			got := h.ReloadCreditNote(cn.ID)
			if tt.status == creditnote.StatusFinalized {
				eq(t, "updated at", got.UpdatedAt, now)
				eq(t, "item fee", got.Items[0].FeeID, old.ID)
				return
			}
			eq(t, "item fee", got.Items[0].FeeID, fee.ID)
			eq(t, "taxes", got.TaxesAmount, 8)
			eq(t, "coupons adjustment", got.CouponsAdjustmentAmount, 10)
			eq(t, "credit", got.CreditAmount, 48)
			eq(t, "balance", got.BalanceAmount, 48)
			eq(t, "total", got.TotalAmount, 48)
		})
	}
}
