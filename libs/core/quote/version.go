package quote

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Version is one version of a quote (domain §21.1). A quote has at most
// one non-voided (draft or approved) version.
type Version struct {
	// ID identifies the version.
	ID core.QuoteVersionID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// QuoteID is the quote.
	QuoteID core.QuoteID
	// Version numbers the versions of a quote from 1.
	Version int
	// Status is the lifecycle state.
	Status VersionStatus
	// VoidReason is set once voided.
	VoidReason VersionVoidReason
	// Content is the document of the proposal (rich text with mention
	// placeholders); nil when unset.
	Content *string
	// BillingItems are the quoted plans, coupons, wallet credits and
	// add-ons, a JSON object decoded and validated by libs/billing; nil
	// when unset.
	BillingItems json.RawMessage
	// MentionVariables are the values of the content's placeholders frozen
	// at approval, a JSON object; nil until approved.
	MentionVariables json.RawMessage
	// Currency is the currency of the deal; unset until known.
	Currency money.Currency
	// BillingEntityID names the issuer; zero follows the amended
	// subscription or the customer.
	BillingEntityID core.BillingEntityID
	// ApprovedAt is set while approved.
	ApprovedAt *time.Time
	// VoidedAt is set once voided.
	VoidedAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Validate checks the invariants of domain §21.1.
func (v *Version) Validate() error {
	var val core.Validator
	val.Check(!v.QuoteID.IsZero(), "quote_id", core.CodeRequired)
	val.Check(v.Version >= 1, "version", core.CodeOutOfRange)
	val.Enum("status", v.Status)
	val.OptionalEnum("void_reason", v.VoidReason)
	if len(v.BillingItems) > 0 {
		val.Check(json.Valid(v.BillingItems), "billing_items", core.CodeInvalidFormat)
	}
	if len(v.MentionVariables) > 0 {
		val.Check(json.Valid(v.MentionVariables), "mention_variables", core.CodeInvalidFormat)
	}
	switch v.Status {
	case VersionStatusVoided:
		val.Check(v.VoidReason != "", "void_reason", core.CodeRequired)
		val.Check(v.VoidedAt != nil, "voided_at", core.CodeRequired)
	case VersionStatusApproved:
		val.Check(v.ApprovedAt != nil, "approved_at", core.CodeRequired)
	}
	if v.Status != VersionStatusVoided {
		val.Check(v.VoidReason == "", "void_reason", core.CodeForbidden)
	}
	return val.Err()
}

// Active reports whether the version is draft or approved.
func (v *Version) Active() bool { return v.Status != VersionStatusVoided }

var versionTransitions = map[VersionStatus][]VersionStatus{
	VersionStatusDraft:    {VersionStatusApproved, VersionStatusVoided},
	VersionStatusApproved: {VersionStatusVoided},
}

// CanTransitionVersion reports whether a version may go from one status
// to another: draft → approved | voided, approved → voided (by cascade
// only, see [Version.Void]).
func CanTransitionVersion(from, to VersionStatus) bool {
	return slices.Contains(versionTransitions[from], to)
}

func (v *Version) to(s VersionStatus, now time.Time) error {
	if !CanTransitionVersion(v.Status, s) {
		return &core.TransitionError{Entity: "quote_version", From: string(v.Status), To: string(s)}
	}
	v.Status = s
	v.UpdatedAt = now
	return nil
}

// Approve approves a draft version at now.
func (v *Version) Approve(now time.Time) error {
	if err := v.to(VersionStatusApproved, now); err != nil {
		return err
	}
	v.ApprovedAt = &now
	return nil
}

// Void voids the version at now for reason. A draft can be voided for any
// reason; an approved version only by the cascade of its order form
// (cascade_of_expired, cascade_of_voided), which fails otherwise with
// [CodeNotVoidable].
func (v *Version) Void(reason VersionVoidReason, now time.Time) error {
	if !reason.Valid() {
		return core.InvalidEnum("quote version void reason", string(reason))
	}
	cascade := reason == VersionVoidReasonCascadeOfExpired || reason == VersionVoidReasonCascadeOfVoided
	if v.Status == VersionStatusApproved && !cascade {
		return core.NewRuleError(CodeNotVoidable, "an approved quote version is voided only through its order form")
	}
	if err := v.to(VersionStatusVoided, now); err != nil {
		return err
	}
	v.VoidReason, v.VoidedAt, v.ApprovedAt = reason, &now, nil
	return nil
}

// FirstVersion returns version 1 of a quote, a draft with the given
// billing items.
func FirstVersion(id core.QuoteVersionID, q *Quote, billingItems json.RawMessage, now time.Time) *Version {
	return &Version{
		ID:             id,
		OrganizationID: q.OrganizationID,
		QuoteID:        q.ID,
		Version:        1,
		Status:         VersionStatusDraft,
		BillingItems:   slices.Clone(billingItems),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

// NewVersion clones source into the next draft version of its quote
// (lago QuoteVersions::CloneService) given every version of the quote. It
// fails with [CodeNotClonable] while a version is approved; the active
// draft, if any, is voided as superseded and returned so the caller saves
// both in one transaction. The mention variables, frozen at approval, are
// not carried over (clone_service.rb:57-66).
func NewVersion(id core.QuoteVersionID, source *Version, versions []*Version, now time.Time) (next, superseded *Version, err error) {
	last := 0
	for _, v := range versions {
		if v.Status == VersionStatusApproved {
			return nil, nil, core.NewRuleError(CodeNotClonable, "the quote has an approved version")
		}
		last = max(last, v.Version)
	}
	for _, v := range versions {
		if v.Status == VersionStatusDraft {
			if err := v.Void(VersionVoidReasonSuperseded, now); err != nil {
				return nil, nil, err
			}
			superseded = v
			break
		}
	}
	next = &Version{
		ID:              id,
		OrganizationID:  source.OrganizationID,
		QuoteID:         source.QuoteID,
		Version:         last + 1,
		Status:          VersionStatusDraft,
		Content:         cloneString(source.Content),
		BillingItems:    slices.Clone(source.BillingItems),
		Currency:        source.Currency,
		BillingEntityID: source.BillingEntityID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	return next, superseded, nil
}

// CheckSingleActive refuses candidate when another version of its quote
// among versions is draft or approved ([CodeActiveVersionExists]).
func CheckSingleActive(candidate *Version, versions []*Version) error {
	if !candidate.Active() {
		return nil
	}
	if slices.ContainsFunc(versions, func(v *Version) bool {
		return v.ID != candidate.ID && v.QuoteID == candidate.QuoteID && v.Active()
	}) {
		return core.NewRuleError(CodeActiveVersionExists, "a quote has at most one draft or approved version")
	}
	return nil
}

func cloneString(s *string) *string {
	if s == nil {
		return nil
	}
	c := *s
	return &c
}
