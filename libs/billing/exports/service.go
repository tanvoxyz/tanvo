package exports

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/export"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Job kinds of the exports; they run on [port.QueueExports].
const (
	// JobExportResources lists the matching IDs of an export and splits
	// them in parts (lago DataExports::ExportResourcesJob).
	JobExportResources = "exports.export_resources"
	// JobProcessPart renders the rows of a part (lago
	// DataExports::ProcessPartJob).
	JobProcessPart = "exports.process_part"
	// JobCombineParts writes the file of an export once every part is
	// rendered (lago DataExports::CombinePartsJob).
	JobCombineParts = "exports.combine_parts"
)

// BatchSize is the number of exported documents of a part (lago
// ExportResourcesService::DEFAULT_BATCH_SIZE).
const BatchSize = 20

// Error codes of the export jobs (lago ExportResourcesService).
const (
	// CodeExpired: the export's file already expired.
	CodeExpired = "data_export_expired"
	// CodeProcessed: the export is no longer pending.
	CodeProcessed = "data_export_processed"
	// CodeMembershipRequired: exports are requested by a member, not an
	// API key.
	CodeMembershipRequired = "membership_required"
)

// lockScope serializes the completion of the parts of one export, so that
// exactly one part job sees every part completed.
const lockScope = "data_export"

// listPage is the page size the export job lists the matching IDs with.
const listPage = core.MaxPageSize

// ErrNoBlobStore reports that no blob store is wired: the parts are
// rendered but the file cannot be stored.
var ErrNoBlobStore = errors.New("exports: no blob store configured")

// Query is the list query of an export, stored as the export's
// resource_query: an AIP-160 filter and a search term, as the list RPCs
// take them.
type Query struct {
	// Filter is the AIP-160 filter.
	Filter string `json:"filter,omitempty"`
	// Search is the search term.
	Search string `json:"search,omitempty"`
}

// Filters parse a [Query] into the list filter of its resource; apps/server
// injects the parsers of the list RPCs so that an export lists exactly
// what the list shows.
type Filters struct {
	// Invoices parses the query of an invoices or invoice fees export.
	Invoices func(Query) (invoice.Filter, error)
	// CreditNotes parses the query of a credit notes or credit note items
	// export.
	CreditNotes func(Query) (creditnote.Filter, error)
}

// ExportArgs are the args of [JobExportResources] and [JobCombineParts].
type ExportArgs struct {
	// DataExportID is the export.
	DataExportID core.DataExportID `json:"data_export_id"`
}

// PartArgs are the args of [JobProcessPart].
type PartArgs struct {
	// DataExportPartID is the part.
	DataExportPartID core.DataExportPartID `json:"data_export_part_id"`
}

// Service builds data exports.
type Service struct {
	d       *kernel.Deps
	blobs   port.BlobStore
	filters Filters
}

// New returns the service. blobs stores the files and gives the links of
// the exported documents' files; nil leaves the exports processing once
// their parts are rendered ([ErrNoBlobStore]).
func New(d *kernel.Deps, blobs port.BlobStore, filters Filters) *Service {
	return &Service{d: d, blobs: blobs, filters: filters}
}

// Create starts an export of resource for the member requester (lago
// DataExports::CreateService): the export is stored pending and the export
// job is enqueued with it. The query is parsed first, so an invalid filter
// fails at once. onCreate, when not nil, runs in the same transaction once
// the export is stored, with the transaction's context: the caller records
// the export.created security log there (create_service.rb:28, 39-45).
func (s *Service) Create(ctx context.Context, org core.OrganizationID, requester core.MembershipID, resource export.Resource, q Query, onCreate func(context.Context, *export.DataExport) error) (*export.DataExport, error) {
	if requester.IsZero() {
		return nil, core.NewRuleError(CodeMembershipRequired, "data exports are requested by a member")
	}
	var v core.Validator
	v.Enum("resource_type", resource)
	if err := v.Err(); err != nil {
		return nil, err
	}
	if err := s.parse(resource, q); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(q)
	if err != nil {
		return nil, fmt.Errorf("encode export query: %w", err)
	}
	e := export.New(core.NewID[core.DataExportID](s.d.IDs), org, requester, resource, raw, s.d.Now())
	if err := e.Validate(); err != nil {
		return nil, err
	}
	err = s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := r.DataExports.Create(ctx, e); err != nil {
			return fmt.Errorf("create data export: %w", err)
		}
		if onCreate != nil {
			if err := onCreate(ctx, e); err != nil {
				return err
			}
		}
		return enqueue(ctx, r, org, JobExportResources, ExportArgs{DataExportID: e.ID}, "")
	})
	if err != nil {
		return nil, err
	}
	return e, nil
}

// parse checks that q is a valid query of resource.
func (s *Service) parse(resource export.Resource, q Query) error {
	switch resource {
	case export.ResourceInvoices, export.ResourceInvoiceFees:
		if s.filters.Invoices != nil {
			if _, err := s.filters.Invoices(q); err != nil {
				return err
			}
		}
	case export.ResourceCreditNotes, export.ResourceCreditNoteItems:
		if s.filters.CreditNotes != nil {
			if _, err := s.filters.CreditNotes(q); err != nil {
				return err
			}
		}
	}
	return nil
}

// Get returns the export id of org.
func (s *Service) Get(ctx context.Context, org core.OrganizationID, id core.DataExportID) (*export.DataExport, error) {
	var out *export.DataExport
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = r.DataExports.Get(ctx, org, id)
		return err
	})
	return out, err
}

// ExportResources starts building an export (lago
// DataExports::ExportResourcesService): an expired or non-pending export
// fails with [CodeExpired] or [CodeProcessed]; otherwise the export goes
// processing and the matching IDs are split in parts of batchSize (≤ 0
// means [BatchSize]), each with its part job. Any other failure marks the
// export failed.
func (s *Service) ExportResources(ctx context.Context, org core.OrganizationID, id core.DataExportID, batchSize int) ([]*export.Part, error) {
	if batchSize <= 0 {
		batchSize = BatchSize
	}
	var parts []*export.Part
	started := false
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		parts = nil
		e, err := r.DataExports.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("get data export %s: %w", id, err)
		}
		now := s.d.Now()
		if e.Expired(now) {
			return core.NewRuleError(CodeExpired, "Data Export already expired")
		}
		if e.Status != export.StatusPending {
			return core.NewRuleError(CodeProcessed, "Data Export already processed")
		}
		started = true
		if err := e.Start(now); err != nil {
			return err
		}
		if err := r.DataExports.Update(ctx, e); err != nil {
			return fmt.Errorf("update data export %s: %w", id, err)
		}
		ids, err := s.objectIDs(ctx, r, e)
		if err != nil {
			return err
		}
		for i, batch := range chunks(ids, batchSize) {
			p := &export.Part{
				ID: core.NewID[core.DataExportPartID](s.d.IDs), OrganizationID: org, DataExportID: e.ID,
				Index: i, ObjectIDs: batch, CreatedAt: now, UpdatedAt: now,
			}
			if err := r.DataExportParts.Create(ctx, p); err != nil {
				return fmt.Errorf("create data export part: %w", err)
			}
			if err := enqueue(ctx, r, org, JobProcessPart, PartArgs{DataExportPartID: p.ID}, ""); err != nil {
				return err
			}
			parts = append(parts, p)
		}
		return nil
	})
	if err != nil && started {
		if ferr := s.fail(ctx, org, id); ferr != nil {
			return nil, errors.Join(err, ferr)
		}
	}
	return parts, err
}

// fail marks the export id failed.
func (s *Service) fail(ctx context.Context, org core.OrganizationID, id core.DataExportID) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		e, err := r.DataExports.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("get data export %s: %w", id, err)
		}
		if !export.CanTransition(e.Status, export.StatusFailed) {
			return nil
		}
		if err := e.Fail(s.d.Now()); err != nil {
			return err
		}
		return r.DataExports.Update(ctx, e)
	})
}

// objectIDs lists every ID matching the export's query, in the list
// order, without duplicates (lago all_invoice_ids and credit_note_ids).
func (s *Service) objectIDs(ctx context.Context, r port.Repos, e *export.DataExport) ([]uuid.UUID, error) {
	var q Query
	if len(e.ResourceQuery) > 0 {
		if err := json.Unmarshal(e.ResourceQuery, &q); err != nil {
			return nil, fmt.Errorf("decode export query: %w", err)
		}
	}
	var ids []uuid.UUID
	switch e.ResourceType {
	case export.ResourceInvoices, export.ResourceInvoiceFees:
		var f invoice.Filter
		if s.filters.Invoices != nil {
			var err error
			if f, err = s.filters.Invoices(q); err != nil {
				return nil, err
			}
		}
		f.Search, f.Order = q.Search, core.OrderRecent
		for page := (core.PageRequest{Size: listPage}); ; {
			p, err := r.Invoices.List(ctx, e.OrganizationID, f, page)
			if err != nil {
				return nil, fmt.Errorf("list invoices: %w", err)
			}
			for _, inv := range p.Items {
				ids = append(ids, inv.ID.UUID())
			}
			if p.NextToken == "" {
				break
			}
			page.Token = p.NextToken
		}
	case export.ResourceCreditNotes, export.ResourceCreditNoteItems:
		var f creditnote.Filter
		if s.filters.CreditNotes != nil {
			var err error
			if f, err = s.filters.CreditNotes(q); err != nil {
				return nil, err
			}
		}
		// Only finalized credit notes are listed (credit_notes_query.rb:59).
		f.Statuses = []creditnote.Status{creditnote.StatusFinalized}
		f.Search, f.Order = q.Search, core.OrderRecent
		for page := (core.PageRequest{Size: listPage}); ; {
			p, err := r.CreditNotes.List(ctx, e.OrganizationID, f, page)
			if err != nil {
				return nil, fmt.Errorf("list credit notes: %w", err)
			}
			for _, cn := range p.Items {
				ids = append(ids, cn.ID.UUID())
			}
			if p.NextToken == "" {
				break
			}
			page.Token = p.NextToken
		}
	default:
		return nil, fmt.Errorf("'%s' resource not supported: %w", e.ResourceType, core.ErrInvalidValue)
	}
	seen := make(map[uuid.UUID]bool, len(ids))
	return slices.DeleteFunc(ids, func(id uuid.UUID) bool {
		dup := seen[id]
		seen[id] = true
		return dup
	}), nil
}

// chunks splits ids in slices of at most n.
func chunks(ids []uuid.UUID, n int) [][]uuid.UUID {
	var out [][]uuid.UUID
	for len(ids) > 0 {
		k := min(n, len(ids))
		out = append(out, slices.Clone(ids[:k]))
		ids = ids[k:]
	}
	return out
}

// ProcessPart renders the rows of a part (lago
// DataExports::ProcessPartService); a completed part is left as is. The
// last part of the export to complete enqueues the combine job.
func (s *Service) ProcessPart(ctx context.Context, org core.OrganizationID, id core.DataExportPartID) (*export.Part, error) {
	var out *export.Part
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := r.DataExportParts.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("get data export part %s: %w", id, err)
		}
		out = p
		if p.Completed {
			return nil
		}
		e, err := r.DataExports.Get(ctx, org, p.DataExportID)
		if err != nil {
			return fmt.Errorf("get data export %s: %w", p.DataExportID, err)
		}
		lines, err := s.render(ctx, r, e, p)
		if err != nil {
			return err
		}
		if err := r.Locks.Lock(ctx, org, port.LockKey{Scope: lockScope, ID: e.ID.UUID()}); err != nil {
			return fmt.Errorf("lock data export %s: %w", e.ID, err)
		}
		p.Finish(lines, s.d.Now())
		if err := r.DataExportParts.Update(ctx, p); err != nil {
			return fmt.Errorf("update data export part %s: %w", p.ID, err)
		}
		_, err = r.DataExportParts.NextUnprocessed(ctx, org, e.ID)
		switch {
		case errors.Is(err, core.ErrNotFound):
			return enqueue(ctx, r, org, JobCombineParts, ExportArgs{DataExportID: e.ID}, e.ID.String())
		case err != nil:
			return fmt.Errorf("next unprocessed part of %s: %w", e.ID, err)
		}
		return nil
	})
	return out, err
}

// File returns the file of an export: the header and the lines of its
// parts in index order (lago CombinePartsService), with the file name.
func (s *Service) File(ctx context.Context, org core.OrganizationID, id core.DataExportID) ([]byte, string, error) {
	var (
		buf  bytes.Buffer
		name string
	)
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		buf.Reset()
		e, err := r.DataExports.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("get data export %s: %w", id, err)
		}
		name = e.Filename()
		parts, err := r.DataExportParts.ListByExport(ctx, org, id)
		if err != nil {
			return fmt.Errorf("list parts of data export %s: %w", id, err)
		}
		header, err := s.header(ctx, r, e)
		if err != nil {
			return err
		}
		buf.WriteString(header)
		buf.WriteByte('\n')
		for _, p := range parts {
			buf.WriteString(p.CSVLines)
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return buf.Bytes(), name, nil
}

// CombineParts writes the file of an export to the blob store and
// completes the export (lago DataExports::CombinePartsService). It fails
// with [ErrNoBlobStore] when none is wired. The completion email is not
// sent here.
func (s *Service) CombineParts(ctx context.Context, org core.OrganizationID, id core.DataExportID) (*export.DataExport, error) {
	if s.blobs == nil {
		return nil, ErrNoBlobStore
	}
	body, _, err := s.File(ctx, org, id)
	if err != nil {
		return nil, err
	}
	var suffix [5]byte
	u := s.d.IDs.NewUUID()
	copy(suffix[:], u[len(u)-5:])
	key := fmt.Sprintf("data_exports/%s-%x.%s", id, suffix, export.FormatCSV)
	ref, err := s.blobs.Put(ctx, org, key, "text/csv", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("store data export %s: %w", id, err)
	}
	var out *export.DataExport
	err = s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		e, err := r.DataExports.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("get data export %s: %w", id, err)
		}
		if err := e.Complete(ref, s.d.Now()); err != nil {
			return err
		}
		if err := r.DataExports.Update(ctx, e); err != nil {
			return fmt.Errorf("update data export %s: %w", id, err)
		}
		out = e
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// FileURL returns the link of a completed export's file, or "" when it has
// none or no blob store is wired.
func (s *Service) FileURL(ctx context.Context, e *export.DataExport) (string, error) {
	if s.blobs == nil || e.File.IsZero() || e.Expired(s.d.Now()) {
		return "", nil
	}
	return s.blobs.URL(ctx, e.OrganizationID, e.File)
}

// enqueue enqueues a job of kind on the exports queue.
func enqueue(ctx context.Context, r port.Repos, org core.OrganizationID, kind string, args any, uniqueKey string) error {
	raw, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("encode %s args: %w", kind, err)
	}
	job := port.Job{Kind: kind, Queue: port.QueueExports, OrganizationID: org, Args: raw, UniqueKey: uniqueKey, RunAt: time.Time{}}
	if err := r.Jobs.Enqueue(ctx, job); err != nil {
		return fmt.Errorf("enqueue %s: %w", kind, err)
	}
	return nil
}
