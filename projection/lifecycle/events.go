package lifecycle

import (
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/projection"
	"github.com/gofrs/uuid/v5"
)

// RebuildInitiated records the decision that a rebuild is underway: admission
// to the projection's single in-flight attempt slot and allocation of the
// target version number, as one append. Optimistic concurrency on the
// lifecycle stream is the arbiter — Begin refuses an occupied slot, and two
// concurrent initiations conflict at the same stream version, so at most one
// is admitted. Previous carries the live version at initiation for ledger
// self-containment; the fold's own Live is the authority it is checked
// against. An admission with no attempt ID, into an occupied slot, under a
// different projection name, from a non-live previous, or outside the
// allocation sequence poisons the fold; the projection name is immutable
// once set, and the allocation high-water mark never lowers.
type RebuildInitiated struct {
	Attempt  uuid.UUID
	Target   projection.ID
	Previous projection.ID
	Reason   string
	At       time.Time
}

// EventType returns the type of event.
func (RebuildInitiated) EventType() string { return "rebuildinitiated" }

// New returns a new instance of the event.
func (RebuildInitiated) New() estoria.DomainEvent[State] { return &RebuildInitiated{} }

// ApplyTo applies the event to state, returning the new state.
func (e RebuildInitiated) ApplyTo(s State) State {
	switch {
	case e.Attempt.IsNil():
		s = s.poison("rebuild initiated with no attempt ID",
			"projection", e.Target.Name, "target", e.Target)
	case s.Attempt.Phase != PhaseNone:
		s = s.poison("rebuild initiated while the attempt slot is occupied",
			"projection", e.Target.Name, "attempt", e.Attempt, "displaced_attempt", s.Attempt.ID)
	case s.Name != "" && e.Target.Name != s.Name:
		s = s.poison("rebuild initiated for a different projection name",
			"projection", s.Name, "target", e.Target)
	case e.Previous != s.Live:
		s = s.poison("rebuild initiated with a previous version that was not live",
			"projection", e.Target.Name, "previous", e.Previous, "live", s.Live)
	case e.Target.Version != s.Allocated+1:
		s = s.poison("rebuild initiated with a target version outside the allocation sequence",
			"projection", e.Target.Name, "target", e.Target, "allocated", s.Allocated)
	}

	if s.Name == "" {
		s.Name = e.Target.Name
	}

	s.Allocated = max(s.Allocated, e.Target.Version)
	s.Attempt = AttemptState{
		ID:          e.Attempt,
		Target:      e.Target,
		Previous:    e.Previous,
		Phase:       PhaseCreated,
		Reason:      e.Reason,
		InitiatedAt: e.At,
	}

	return s
}

// BuildStarted records the first start of the target version's processor.
type BuildStarted struct{}

// EventType returns the type of event.
func (BuildStarted) EventType() string { return "buildstarted" }

// New returns a new instance of the event.
func (BuildStarted) New() estoria.DomainEvent[State] { return &BuildStarted{} }

// ApplyTo applies the event to state, returning the new state.
func (BuildStarted) ApplyTo(s State) State {
	if s.Attempt.Phase != PhaseCreated {
		s = s.poison("build started outside the created phase",
			"projection", s.Name, "phase", s.Attempt.Phase)
	}

	s.Attempt.Phase = PhaseBuilding

	return s
}

// BuildResumed records a processor restart after crash or stall
// reconciliation, carrying the checkpointed position it resumed from.
type BuildResumed struct {
	FromPosition int64
}

// EventType returns the type of event.
func (BuildResumed) EventType() string { return "buildresumed" }

// New returns a new instance of the event.
func (BuildResumed) New() estoria.DomainEvent[State] { return &BuildResumed{} }

// ApplyTo applies the event to state, returning the new state.
func (BuildResumed) ApplyTo(s State) State {
	if s.Attempt.Phase != PhaseBuilding {
		s = s.poison("build resumed outside the building phase",
			"projection", s.Name, "phase", s.Attempt.Phase)
	}

	s.Attempt.Phase = PhaseBuilding

	return s
}

// CaughtUp records that the target version first drained to the head of the
// event sequence: one event with the position and elapsed time as payload,
// not per-batch telemetry.
type CaughtUp struct {
	Position int64
	Duration time.Duration
	At       time.Time
}

// EventType returns the type of event.
func (CaughtUp) EventType() string { return "caughtup" }

// New returns a new instance of the event.
func (CaughtUp) New() estoria.DomainEvent[State] { return &CaughtUp{} }

// ApplyTo applies the event to state, returning the new state.
func (e CaughtUp) ApplyTo(s State) State {
	if s.Attempt.Phase != PhaseBuilding {
		s = s.poison("catch-up recorded outside the building phase",
			"projection", s.Name, "phase", s.Attempt.Phase)
	}

	s.Attempt.Phase = PhaseCaughtUp
	s.Attempt.CaughtUpAt = e.At
	s.Attempt.CaughtUpPos = e.Position

	return s
}

// Promoted records the cutover of reads from Previous to Next. This event is
// the flip: routers and the effect worker derive or cache what it records.
// The payload carries both versions so the promotion history is
// self-contained; Previous is defense in depth, checked against the fold's
// own Live, not the arbiter — same-stream optimistic concurrency is.
type Promoted struct {
	Previous projection.ID
	Next     projection.ID
	At       time.Time
}

// EventType returns the type of event.
func (Promoted) EventType() string { return "promoted" }

// New returns a new instance of the event.
func (Promoted) New() estoria.DomainEvent[State] { return &Promoted{} }

// ApplyTo applies the event to state, returning the new state.
func (e Promoted) ApplyTo(s State) State {
	switch {
	case s.Attempt.Phase != PhaseCaughtUp:
		s = s.poison("promotion recorded outside the caught-up phase",
			"projection", s.Name, "phase", s.Attempt.Phase)
	case e.Previous != s.Live:
		s = s.poison("promotion recorded from a version that was not live",
			"projection", s.Name, "recorded_previous", e.Previous, "live", s.Live)
	case e.Next != s.Attempt.Target:
		s = s.poison("promotion recorded for a version that was not the attempt's target",
			"projection", s.Name, "recorded_next", e.Next, "target", s.Attempt.Target)
	}

	s.Live = e.Next
	s.Attempt.Phase = PhasePromoted
	s.Attempt.PromotedAt = e.At

	return s
}

// RolledBack records the reversion of reads to the previous version.
// Terminal for the attempt: the slot is vacated, and a subsequent rebuild is
// a new attempt targeting a new version number. From is defense in depth,
// checked against the fold's own Live. A first rebuild has no previous
// version and so no rollback target: a rollback recorded for an attempt with
// no previous poisons the fold rather than passing the lineage check on two
// zero values.
type RolledBack struct {
	From       projection.ID
	RevertedTo projection.ID
	At         time.Time
}

// EventType returns the type of event.
func (RolledBack) EventType() string { return "rolledback" }

// New returns a new instance of the event.
func (RolledBack) New() estoria.DomainEvent[State] { return &RolledBack{} }

// ApplyTo applies the event to state, returning the new state.
func (e RolledBack) ApplyTo(s State) State {
	switch {
	case s.Attempt.Phase != PhasePromoted:
		s = s.poison("rollback recorded outside the promoted phase",
			"projection", s.Name, "phase", s.Attempt.Phase)
	case e.From != s.Live:
		s = s.poison("rollback recorded from a version that was not live",
			"projection", s.Name, "recorded_from", e.From, "live", s.Live)
	case s.Attempt.Previous == (projection.ID{}):
		s = s.poison("rollback recorded for an attempt with no previous version",
			"projection", s.Name, "attempt", s.Attempt.ID)
	case e.RevertedTo != s.Attempt.Previous:
		s = s.poison("rollback recorded to a version that was not the attempt's previous",
			"projection", s.Name, "recorded_reverted_to", e.RevertedTo, "previous", s.Attempt.Previous)
	}

	s.Live = e.RevertedTo
	s.Attempt = AttemptState{}

	return s
}

// Abandoned records that the rebuild was given up before promotion. Terminal
// for the attempt: the slot is vacated. The abandoned build's checkpoint and
// storage belong to a version number that is never reused, so any residue is
// inert until explicitly collected.
type Abandoned struct {
	Cause string
}

// EventType returns the type of event.
func (Abandoned) EventType() string { return "abandoned" }

// New returns a new instance of the event.
func (Abandoned) New() estoria.DomainEvent[State] { return &Abandoned{} }

// ApplyTo applies the event to state, returning the new state.
func (Abandoned) ApplyTo(s State) State {
	switch s.Attempt.Phase {
	case PhaseCreated, PhaseBuilding, PhaseCaughtUp:
	case PhaseNone, PhasePromoted, PhaseRetiring:
		s = s.poison("abandonment recorded outside the pre-promotion phases",
			"projection", s.Name, "phase", s.Attempt.Phase)
	default:
		s = s.poison("abandonment recorded in an unknown phase",
			"projection", s.Name, "phase", s.Attempt.Phase)
	}

	s.Attempt = AttemptState{}

	return s
}

// RetireStarted reserves the retirement of the previous version. It contends
// directly with rollback on the lifecycle stream — one wins, the other is
// refused — and nothing is destroyed before that arbitration: teardown runs
// only after this event is durable, and rolling back is illegal from
// PhaseRetiring, so retirement can never destroy a version that is about to
// serve reads again. There is deliberately no un-retire. A first rebuild has
// no previous version and nothing to reserve: its completion is recorded
// directly by PreviousRetired, and a reservation for an attempt with no
// previous poisons the fold rather than passing the lineage check on two
// zero values.
type RetireStarted struct {
	Retiring projection.ID
	At       time.Time
}

// EventType returns the type of event.
func (RetireStarted) EventType() string { return "retirestarted" }

// New returns a new instance of the event.
func (RetireStarted) New() estoria.DomainEvent[State] { return &RetireStarted{} }

// ApplyTo applies the event to state, returning the new state.
func (e RetireStarted) ApplyTo(s State) State {
	switch {
	case s.Attempt.Phase != PhasePromoted:
		s = s.poison("retirement reserved outside the promoted phase",
			"projection", s.Name, "phase", s.Attempt.Phase)
	case s.Attempt.Previous == (projection.ID{}):
		s = s.poison("retirement reserved for an attempt with no previous version",
			"projection", s.Name, "attempt", s.Attempt.ID)
	case e.Retiring != s.Attempt.Previous:
		s = s.poison("retirement reserved for a version that was not the attempt's previous",
			"projection", s.Name, "recorded_retiring", e.Retiring, "previous", s.Attempt.Previous)
	}

	s.Attempt.Phase = PhaseRetiring
	s.Attempt.RetiringAt = e.At

	return s
}

// PreviousRetired records that the previous version was retired: its storage
// was torn down (when the handler implements projection.Teardowner) and its
// checkpoint deleted. Terminal for the attempt: the slot is vacated and the
// rebuild is complete; Live is unchanged. A first rebuild has no previous
// version; its completion carries a zero Retired ID, recording that there
// was nothing to retire, and is recorded directly from PhasePromoted. The
// two completion forms are exclusive: a reserved retirement completes only
// from PhaseRetiring with a previous version recorded, so a completion
// cannot vacate the evidence of a reservation that had nothing to reserve.
type PreviousRetired struct {
	Retired projection.ID
}

// EventType returns the type of event.
func (PreviousRetired) EventType() string { return "previousretired" }

// New returns a new instance of the event.
func (PreviousRetired) New() estoria.DomainEvent[State] { return &PreviousRetired{} }

// ApplyTo applies the event to state, returning the new state.
func (e PreviousRetired) ApplyTo(s State) State {
	reserved := s.Attempt.Phase == PhaseRetiring && s.Attempt.Previous != (projection.ID{})
	firstVersion := s.Attempt.Phase == PhasePromoted && s.Attempt.Previous == (projection.ID{})

	switch {
	case s.Attempt.Phase == PhaseRetiring && s.Attempt.Previous == (projection.ID{}):
		s = s.poison("retirement completed for a reservation with no previous version",
			"projection", s.Name, "attempt", s.Attempt.ID)
	case !reserved && !firstVersion:
		s = s.poison("retirement completed outside the retiring phase",
			"projection", s.Name, "phase", s.Attempt.Phase)
	case e.Retired != s.Attempt.Previous:
		s = s.poison("retirement completed for a version that was not the attempt's previous",
			"projection", s.Name, "recorded_retired", e.Retired, "previous", s.Attempt.Previous)
	}

	s.Attempt = AttemptState{}

	return s
}
