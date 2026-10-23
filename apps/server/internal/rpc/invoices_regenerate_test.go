package rpc_test

import (
	"net/http"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/invoice"
)

func TestRegenerateInvoice(t *testing.T) {
	t.Parallel()
	f := newInvoiceFixture(t)
	voided := func() string { return f.stored(invoice.StatusVoided).ID.String() }
	// source: spec/services/invoices/build_regeneration_preview_service_spec.rb:38
	t.Run("validate only previews the voided invoice", func(t *testing.T) {
		id := voided()
		out := f.call(t, http.MethodPost, "/v1/invoices/"+id+":regenerate", `{"validate_only":true,"fees":[{"fee_id":"`+missingInvoice()+`"}]}`, http.StatusOK, "")
		if got := invoiceDecode(t, out); got.ID != id || got.Status != "INVOICE_STATUS_VOIDED" {
			t.Errorf("preview got = %+v, want = the voided invoice %s", got, id)
		}
	})
	t.Run("purchase order number", func(t *testing.T) {
		f.call(t, http.MethodPost, "/v1/invoices/"+voided()+":regenerate", `{"purchase_order_number":"PO-42","fees":[]}`, http.StatusOK, `"purchase_order_number":"PO-42"`)
	})
	t.Run("invalid fee reference", func(t *testing.T) {
		f.call(t, http.MethodPost, "/v1/invoices/"+voided()+":regenerate", `{"fees":[{"charge_id":"nope"}]}`, http.StatusBadRequest, "charge_id")
	})
}

func TestRetryInvoicePaymentMethod(t *testing.T) {
	t.Parallel()
	f := newInvoiceFixture(t)
	inv := f.stored(invoice.StatusFinalized, func(inv *invoice.Invoice) { inv.ReadyForPaymentProcessing, inv.TotalAmount = true, 1000 })
	f.call(t, http.MethodPost, "/v1/invoices/"+inv.ID.String()+":retry_payment", `{"payment_method_type":"PAYMENT_METHOD_TYPE_MANUAL"}`, http.StatusOK, "")
	f.call(t, http.MethodPost, "/v1/invoices/"+inv.ID.String()+":retry_payment", `{"payment_method_id":"nope"}`, http.StatusBadRequest, "payment_method_id")
}

func TestCreateInvoicePurchaseOrderNumber(t *testing.T) {
	t.Parallel()
	f := newInvoiceFixture(t)
	a := f.s.Fixtures.AddOn(1000)
	body := `{"customer_id":"` + f.cust.ID.String() + `","purchase_order_number":" PO-7 ","fees":[{"add_on_id":"code:` + a.Code + `"}]}`
	f.call(t, http.MethodPost, "/v1/invoices", body, http.StatusOK, `"purchase_order_number":"PO-7"`)
}
