package rpc

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
)

// Documents renders the API representations of invoices and credit notes
// the accounting providers receive (NetSuite's fullInvoicePayload and
// fullCreditNotePayload, lago V1::InvoiceSerializer and
// V1::CreditNoteSerializer): the JSON of the API's messages, with the
// proto field names. Without a service, its documents are left out.
type Documents struct {
	// Invoices loads the invoices with their fees and subscriptions.
	Invoices *invoicing.Service
	// CreditNotes loads the credit notes with their items.
	CreditNotes *creditnotes.Service
}

var _ integrations.Documents = Documents{}

// InvoiceDocument implements [integrations.Documents].
func (d Documents) InvoiceDocument(ctx context.Context, inv *invoice.Invoice) (json.RawMessage, error) {
	if d.Invoices == nil {
		return nil, nil
	}
	v, err := d.Invoices.View(ctx, inv.OrganizationID, inv.ID)
	if err != nil {
		return nil, fmt.Errorf("invoice document %s: %w", inv.ID, err)
	}
	return document(invoiceProto(v))
}

// CreditNoteDocument implements [integrations.Documents].
func (d Documents) CreditNoteDocument(ctx context.Context, cn *creditnote.CreditNote) (json.RawMessage, error) {
	if d.CreditNotes == nil {
		return nil, nil
	}
	full, err := d.CreditNotes.Get(ctx, cn.OrganizationID, cn.ID)
	if err != nil {
		return nil, fmt.Errorf("credit note document %s: %w", cn.ID, err)
	}
	return document(creditNote(full))
}

func document(m proto.Message) (json.RawMessage, error) {
	b, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode document: %w", err)
	}
	return b, nil
}
