package rpc_test

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/export"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	exportv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/export/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/export/v1/exportv1connect"
)

func exportsClient(s *rpctest.Server, c rpctest.Credential) exportv1connect.DataExportServiceClient {
	return exportv1connect.NewDataExportServiceClient(s.Connect(c))
}

func storedExport(t *testing.T, s *rpctest.Server, id string) *export.DataExport {
	t.Helper()
	var out *export.DataExport
	if err := s.Store.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = r.DataExports.Get(ctx, s.Org.ID, core.MustParseID[core.DataExportID](id))
		return err
	}); err != nil {
		t.Fatalf("get export: %v", err)
	}
	return out
}

func TestExportInvoices(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	c := exportsClient(s, s.Session)
	t.Run("creates", func(t *testing.T) {
		t.Log("source: spec/graphql/mutations/data_exports/invoices/create_spec.rb:59")
		res, err := c.ExportInvoices(t.Context(), exportv1.ExportInvoicesRequest_builder{
			Format: new(exportv1.DataExportFormat_DATA_EXPORT_FORMAT_CSV),
			Filter: new(`currency = "USD" AND payment_overdue = true`), Search: new("abc"),
		}.Build())
		if err != nil {
			t.Fatalf("ExportInvoices() error got = %v, want = nil", err)
		}
		x := res.GetDataExport()
		if x.GetId() == "" || x.GetStatus() != exportv1.DataExportStatus_DATA_EXPORT_STATUS_PENDING {
			t.Errorf("got = %q %s, want = an ID, pending", x.GetId(), x.GetStatus())
		}
		got := storedExport(t, s, x.GetId())
		if got.ResourceType != export.ResourceInvoices || got.MembershipID.IsZero() {
			t.Errorf("stored got = %s %s, want = invoices of the member", got.ResourceType, got.MembershipID)
		}
		t.Log("source: spec/services/data_exports/create_service_spec.rb:50")
		var logs []*auditlog.SecurityLog
		if err := s.Store.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
			p, err := r.SecurityLogs.List(ctx, s.Org.ID, auditlog.SecurityLogFilter{Events: []string{"export.created"}}, core.PageRequest{})
			logs = p.Items
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if len(logs) != 1 || logs[0].UserID != s.User.ID || logs[0].Resources["export_type"] != "invoices" {
			t.Errorf("security logs got = %+v, want = one export.created of invoices by the member", logs)
		}
	})
	t.Run("invalid filter", func(t *testing.T) {
		_, err := c.ExportInvoices(t.Context(), exportv1.ExportInvoicesRequest_builder{Filter: new(`partially_paid = true`)}.Build())
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("got = %v, want = InvalidArgument", err)
		}
	})
	t.Run("api key", func(t *testing.T) {
		_, err := exportsClient(s, s.APIKey).ExportInvoiceFees(t.Context(), exportv1.ExportInvoiceFeesRequest_builder{}.Build())
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("got = %v, want = PermissionDenied", err)
		}
	})
}

func TestExportCreditNotes(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	c := exportsClient(s, s.Session)
	cust := s.Fixtures.Customer()
	t.Log("source: spec/graphql/mutations/data_exports/credit_notes/create_spec.rb:60")
	res, err := c.ExportCreditNotes(t.Context(), exportv1.ExportCreditNotesRequest_builder{
		Filter: new(`customer_id = "` + cust.ID.String() + `" AND type = "credit"`),
	}.Build())
	if err != nil {
		t.Fatalf("ExportCreditNotes() error got = %v, want = nil", err)
	}
	if got := res.GetDataExport().GetStatus(); got != exportv1.DataExportStatus_DATA_EXPORT_STATUS_PENDING {
		t.Errorf("status got = %s, want = pending", got)
	}
	if got := storedExport(t, s, res.GetDataExport().GetId()).ResourceType; got != export.ResourceCreditNotes {
		t.Errorf("resource got = %s, want = credit_notes", got)
	}
	items, err := c.ExportCreditNoteItems(t.Context(), exportv1.ExportCreditNoteItemsRequest_builder{}.Build())
	if err != nil {
		t.Fatalf("ExportCreditNoteItems() error got = %v, want = nil", err)
	}
	if got := storedExport(t, s, items.GetDataExport().GetId()).ResourceType; got != export.ResourceCreditNoteItems {
		t.Errorf("resource got = %s, want = credit_note_items", got)
	}
}
