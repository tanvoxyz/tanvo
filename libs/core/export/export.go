package export

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// ExpirationPeriod is how long a completed export's file is kept.
const ExpirationPeriod = 7 * 24 * time.Hour

// DataExport is one export requested by a member (domain §20).
type DataExport struct {
	// ID identifies the export.
	ID core.DataExportID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// MembershipID is the requester, who receives the link.
	MembershipID core.MembershipID
	// Format is the file format.
	Format Format
	// ResourceType is the exported list.
	ResourceType Resource
	// ResourceQuery holds the list filters of the resource as a JSON
	// object.
	ResourceQuery json.RawMessage
	// Status is the lifecycle state.
	Status Status
	// File references the built file once completed.
	File core.BlobRef
	// StartedAt is set when processing starts.
	StartedAt *time.Time
	// CompletedAt is set on completion.
	CompletedAt *time.Time
	// ExpiresAt is CompletedAt + [ExpirationPeriod].
	ExpiresAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// New returns a pending CSV export of resource.
func New(id core.DataExportID, org core.OrganizationID, requester core.MembershipID, resource Resource, query json.RawMessage, now time.Time) *DataExport {
	return &DataExport{
		ID:             id,
		OrganizationID: org,
		MembershipID:   requester,
		Format:         FormatCSV,
		ResourceType:   resource,
		ResourceQuery:  slices.Clone(query),
		Status:         StatusPending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

// Validate checks the invariants of domain §20.
func (e *DataExport) Validate() error {
	var v core.Validator
	v.Check(!e.MembershipID.IsZero(), "membership_id", core.CodeRequired)
	v.Enum("format", e.Format)
	v.Enum("resource_type", e.ResourceType)
	if len(e.ResourceQuery) > 0 {
		var m map[string]json.RawMessage
		v.Check(json.Unmarshal(e.ResourceQuery, &m) == nil && m != nil, "resource_query", core.CodeInvalidFormat)
	}
	v.Enum("status", e.Status)
	if e.Status == StatusCompleted {
		v.Check(!e.File.IsZero(), "file", core.CodeRequired)
	}
	return v.Err()
}

var transitions = map[Status][]Status{
	StatusPending:    {StatusProcessing, StatusFailed},
	StatusProcessing: {StatusCompleted, StatusFailed},
}

// CanTransition reports whether an export may go from one status to
// another: pending → processing → completed, and pending or processing →
// failed.
func CanTransition(from, to Status) bool { return slices.Contains(transitions[from], to) }

func (e *DataExport) to(s Status, now time.Time) error {
	if !CanTransition(e.Status, s) {
		return &core.TransitionError{Entity: "data_export", From: string(e.Status), To: string(s)}
	}
	e.Status = s
	e.UpdatedAt = now
	return nil
}

// Start marks the export processing at now (lago processing!).
func (e *DataExport) Start(now time.Time) error {
	if err := e.to(StatusProcessing, now); err != nil {
		return err
	}
	e.StartedAt = &now
	return nil
}

// Complete attaches file and marks the export completed at now, expiring
// after [ExpirationPeriod] (lago completed!).
func (e *DataExport) Complete(file core.BlobRef, now time.Time) error {
	if file.IsZero() {
		return core.NewRuleError(core.CodeRequired, "a completed export needs a file")
	}
	if err := e.to(StatusCompleted, now); err != nil {
		return err
	}
	exp := now.Add(ExpirationPeriod)
	e.File, e.CompletedAt, e.ExpiresAt = file, &now, &exp
	return nil
}

// Fail marks the export failed at now.
func (e *DataExport) Fail(now time.Time) error { return e.to(StatusFailed, now) }

// Expired reports whether the file has expired at now.
func (e *DataExport) Expired(now time.Time) bool {
	return e.ExpiresAt != nil && e.ExpiresAt.Before(now)
}

// Filename returns the name of the exported file: the creation instant in
// UTC as YYYYMMDDhhmmss, "_", the resource type and the format extension.
func (e *DataExport) Filename() string {
	return e.CreatedAt.UTC().Format("20060102150405") + "_" + string(e.ResourceType) + "." + string(e.Format)
}

// Part is one batch of rows of an export.
type Part struct {
	// ID identifies the part.
	ID core.DataExportPartID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// DataExportID is the export.
	DataExportID core.DataExportID
	// Index orders the parts of an export from 0.
	Index int
	// ObjectIDs are the ids of the exported documents of the batch.
	ObjectIDs []uuid.UUID
	// Completed is set once CSVLines is built.
	Completed bool
	// CSVLines are the rows of the batch, without header.
	CSVLines string
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Validate checks that the part is complete.
func (p *Part) Validate() error {
	var v core.Validator
	v.Check(!p.DataExportID.IsZero(), "data_export_id", core.CodeRequired)
	v.Check(p.Index >= 0, "index", core.CodeOutOfRange)
	v.Check(len(p.ObjectIDs) > 0, "object_ids", core.CodeRequired)
	return v.Err()
}

// Finish stores the batch's rows and marks the part completed at now.
func (p *Part) Finish(lines string, now time.Time) {
	p.CSVLines, p.Completed, p.UpdatedAt = lines, true, now
}
