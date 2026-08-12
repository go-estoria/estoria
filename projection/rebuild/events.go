package rebuild

import (
	"time"

	"github.com/go-estoria/estoria"
	"github.com/go-estoria/estoria/projection"
)

// Each event's ApplyTo is total: a persisted event is a fact, and applying one
// cannot fail. Whether a transition was legal to append is validated on the
// command side, before the event exists.

// Created records the decision to build Next alongside the live
// Previous. The initiator and correlation ride in event metadata, not the
// payload.
type Created struct {
	Name     string
	Next     projection.ID
	Previous projection.ID
	Reason   string
	At       time.Time
}

// EventType returns the type of event.
func (Created) EventType() string { return "rebuildcreated" }

// New returns a new instance of the event.
func (Created) New() estoria.DomainEvent[State] { return &Created{} }

// ApplyTo applies the event to state, returning the new state.
func (e Created) ApplyTo(s State) State {
	s.Name = e.Name
	s.Next = e.Next
	s.Previous = e.Previous
	s.Reason = e.Reason
	s.CreatedAt = e.At
	s.Phase = PhaseCreated

	return s
}

// BuildStarted records the first start of the next version's processor.
type BuildStarted struct{}

// EventType returns the type of event.
func (BuildStarted) EventType() string { return "buildstarted" }

// New returns a new instance of the event.
func (BuildStarted) New() estoria.DomainEvent[State] { return &BuildStarted{} }

// ApplyTo applies the event to state, returning the new state.
func (BuildStarted) ApplyTo(s State) State {
	s.Phase = PhaseBuilding

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
	s.Phase = PhaseBuilding

	return s
}

// CaughtUp records that the next version first drained to the head of the
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
	s.Phase = PhaseCaughtUp
	s.CaughtUpAt = e.At
	s.CaughtUpPos = e.Position

	return s
}

// Promoted records the cutover of reads from Previous to Next. This event is
// the flip: routers derive or cache what it records. The payload carries both
// versions so the promotion history is self-contained.
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
	s.Phase = PhasePromoted
	s.PromotedAt = e.At

	return s
}

// RolledBack records the reversion of reads to the previous version.
type RolledBack struct {
	RevertedTo projection.ID
}

// EventType returns the type of event.
func (RolledBack) EventType() string { return "rolledback" }

// New returns a new instance of the event.
func (RolledBack) New() estoria.DomainEvent[State] { return &RolledBack{} }

// ApplyTo applies the event to state, returning the new state.
func (RolledBack) ApplyTo(s State) State {
	s.Phase = PhaseRolledBack

	return s
}

// Abandoned records that the rebuild was given up before promotion.
type Abandoned struct {
	Cause string
}

// EventType returns the type of event.
func (Abandoned) EventType() string { return "abandoned" }

// New returns a new instance of the event.
func (Abandoned) New() estoria.DomainEvent[State] { return &Abandoned{} }

// ApplyTo applies the event to state, returning the new state.
func (e Abandoned) ApplyTo(s State) State {
	s.Phase = PhaseAbandoned
	s.AbandonCause = e.Cause

	return s
}

// PreviousRetired records that the previous version's storage was torn down,
// after the teardown succeeded: the fact is recorded only once it is one.
type PreviousRetired struct {
	Retired projection.ID
}

// EventType returns the type of event.
func (PreviousRetired) EventType() string { return "previousretired" }

// New returns a new instance of the event.
func (PreviousRetired) New() estoria.DomainEvent[State] { return &PreviousRetired{} }

// ApplyTo applies the event to state, returning the new state.
func (PreviousRetired) ApplyTo(s State) State {
	s.Phase = PhaseRetired

	return s
}
