package export

import (
	"github.com/tanvoxyz/tanvo/libs/core"
)

// Format is the file format of a data export (domain §20).
type Format string

// Format values.
const (
	// FormatCSV is "csv": comma-separated values.
	FormatCSV Format = "csv"
)

// FormatValues returns every Format, in declaration order.
func FormatValues() []Format {
	return []Format{FormatCSV}
}

// Valid reports whether v is one of the declared Format values.
func (v Format) Valid() bool {
	switch v {
	case FormatCSV:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v Format) String() string { return string(v) }

// ParseFormat parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseFormat(s string) (Format, error) {
	if v := Format(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("export.Format", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v Format) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *Format) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseFormat(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// Status is the lifecycle status of a data export (domain §20).
type Status string

// Status values.
const (
	// StatusPending is "pending": waiting to be processed.
	StatusPending Status = "pending"
	// StatusProcessing is "processing": parts are being built.
	StatusProcessing Status = "processing"
	// StatusCompleted is "completed": the file is ready.
	StatusCompleted Status = "completed"
	// StatusFailed is "failed": the export failed.
	StatusFailed Status = "failed"
)

// StatusValues returns every Status, in declaration order.
func StatusValues() []Status {
	return []Status{StatusPending, StatusProcessing, StatusCompleted, StatusFailed}
}

// Valid reports whether v is one of the declared Status values.
func (v Status) Valid() bool {
	switch v {
	case StatusPending, StatusProcessing, StatusCompleted, StatusFailed:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v Status) String() string { return string(v) }

// ParseStatus parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseStatus(s string) (Status, error) {
	if v := Status(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("export.Status", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v Status) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *Status) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseStatus(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// Resource is the resource a data export lists (domain §20).
type Resource string

// Resource values.
const (
	// ResourceInvoices is "invoices": invoices.
	ResourceInvoices Resource = "invoices"
	// ResourceInvoiceFees is "invoice_fees": the fees of invoices.
	ResourceInvoiceFees Resource = "invoice_fees"
	// ResourceCreditNotes is "credit_notes": credit notes.
	ResourceCreditNotes Resource = "credit_notes"
	// ResourceCreditNoteItems is "credit_note_items": the items of credit notes.
	ResourceCreditNoteItems Resource = "credit_note_items"
)

// ResourceValues returns every Resource, in declaration order.
func ResourceValues() []Resource {
	return []Resource{ResourceInvoices, ResourceInvoiceFees, ResourceCreditNotes, ResourceCreditNoteItems}
}

// Valid reports whether v is one of the declared Resource values.
func (v Resource) Valid() bool {
	switch v {
	case ResourceInvoices, ResourceInvoiceFees, ResourceCreditNotes, ResourceCreditNoteItems:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v Resource) String() string { return string(v) }

// ParseResource parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseResource(s string) (Resource, error) {
	if v := Resource(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("export.Resource", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v Resource) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *Resource) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseResource(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}
