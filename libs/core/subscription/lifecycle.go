package subscription

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// CanTransition reports whether a subscription may go from one status to
// another (domain §9.2):
//
//	pending    → incomplete | active | canceled
//	incomplete → active | canceled
//	active     → terminated
//
// terminated and canceled are terminal.
func CanTransition(from, to Status) bool {
	switch from {
	case StatusPending:
		return to == StatusIncomplete || to == StatusActive || to == StatusCanceled
	case StatusIncomplete:
		return to == StatusActive || to == StatusCanceled
	case StatusActive:
		return to == StatusTerminated
	default:
		return false
	}
}

func (s *Subscription) transition(to Status, at time.Time) error {
	if !CanTransition(s.Status, to) {
		return &core.TransitionError{Entity: "subscription", From: string(s.Status), To: string(to)}
	}
	s.Status = to
	s.UpdatedAt = at
	return nil
}

// Activate makes a pending or incomplete subscription active. started_at
// is kept when already set (an incomplete subscription started when it
// became incomplete) and otherwise set to startedAt, which may be in the
// past for a back-dated creation (domain §9.2).
func (s *Subscription) Activate(startedAt, now time.Time) error {
	if err := s.transition(StatusActive, now); err != nil {
		return err
	}
	if s.StartedAt == nil {
		t := startedAt.UTC()
		s.StartedAt = &t
	}
	return nil
}

// MarkIncomplete makes a pending subscription incomplete: it has started
// at at but waits for an activation rule (domain §9.3).
func (s *Subscription) MarkIncomplete(at time.Time) error {
	if err := s.transition(StatusIncomplete, at); err != nil {
		return err
	}
	t := at.UTC()
	s.StartedAt = &t
	return nil
}

// Cancel cancels a pending or incomplete subscription at at for reason,
// which is empty when none is recorded (lago mark_as_canceled!,
// app/models/subscription.rb:139-142).
func (s *Subscription) Cancel(at time.Time, reason CancellationReason) error {
	if reason != "" && !reason.Valid() {
		return core.InvalidEnum("subscription.CancellationReason", string(reason))
	}
	if err := s.transition(StatusCanceled, at); err != nil {
		return err
	}
	t := at.UTC()
	s.CanceledAt = &t
	s.CancellationReason = reason
	s.NextBillingAt = nil
	return nil
}

// Terminate terminates an active subscription at at (now, or the ending_at
// instant, domain §22.14).
func (s *Subscription) Terminate(at time.Time) error {
	if err := s.transition(StatusTerminated, at); err != nil {
		return err
	}
	t := at.UTC()
	s.TerminatedAt = &t
	return nil
}

// TrialEnded records the end of the trial at at (domain §9.4). It fails
// with [core.ErrInvalidTransition] unless the subscription is active and
// its trial end was not recorded yet.
func (s *Subscription) TrialEnded(at time.Time) error {
	if s.Status != StatusActive || s.TrialEndedAt != nil {
		return &core.TransitionError{Entity: "subscription", From: string(s.Status), To: "trial_ended"}
	}
	t := at.UTC()
	s.TrialEndedAt = &t
	s.UpdatedAt = at
	return nil
}

// TerminationAction is what a termination request does to a subscription
// in a given status (domain §22.14).
type TerminationAction int

// Termination actions.
const (
	// TerminationRefused: the subscription is already canceled or
	// terminated.
	TerminationRefused TerminationAction = iota
	// TerminationCancel: a pending subscription is canceled.
	TerminationCancel
	// TerminationDecline: an incomplete subscription is canceled (manual)
	// and its activation rules declined (domain §9.3).
	TerminationDecline
	// TerminationTerminate: an active subscription is terminated.
	TerminationTerminate
)

// TerminationFor returns what terminating a subscription in status st does.
func TerminationFor(st Status) TerminationAction {
	switch st {
	case StatusPending:
		return TerminationCancel
	case StatusIncomplete:
		return TerminationDecline
	case StatusActive:
		return TerminationTerminate
	default:
		return TerminationRefused
	}
}
