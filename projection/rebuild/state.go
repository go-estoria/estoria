// Package rebuild models a versioned projection rebuild — building the next
// version of a projection alongside the live one and cutting reads over — as
// an ordinary estoria aggregate: lifecycle transitions are domain events,
// folded into State.
//
// Only decisions and durable facts are events: created, started, caught up,
// promoted, rolled back, abandoned, retired. Progress is not — the advancing
// checkpoint lives in a checkpointstore.Store, and liveness is inferred from
// checkpoint recency, because a crashed process appends nothing on its way
// down. The test for what belongs in the stream: would a postmortem cite it?
package rebuild

import (
	"strconv"
	"time"

	"github.com/go-estoria/estoria/projection"
	"github.com/go-estoria/estoria/typeid"
	"github.com/gofrs/uuid/v5"
)

// StreamType is the stream type under which rebuild aggregates are stored. It
// uses the reserved "estoria." namespace, identifying rebuild streams as
// library infrastructure wherever they interleave with domain streams.
const StreamType = "estoria.rebuild"

// A Phase is a rebuild's position in its lifecycle. Transitions are recorded
// by the domain events in this package; which transitions are legal is the
// orchestrator's command-side concern, not the fold's.
type Phase int

const (
	// PhaseCreated: the rebuild exists; no processor has started.
	PhaseCreated Phase = iota

	// PhaseBuilding: a processor is (or was last known to be) replaying
	// history into the next version.
	PhaseBuilding

	// PhaseCaughtUp: the next version has drained to the head of the event
	// sequence and is eligible for promotion.
	PhaseCaughtUp

	// PhasePromoted: the next version serves reads; the previous version is
	// retained as the rollback target.
	PhasePromoted

	// PhaseRolledBack: reads were reverted to the previous version. Terminal
	// for the rebuild; a subsequent attempt is a new rebuild.
	PhaseRolledBack

	// PhaseAbandoned: the rebuild was given up before promotion. Terminal.
	PhaseAbandoned

	// PhaseRetired: the previous version has been torn down. Terminal for a
	// successful rebuild.
	PhaseRetired
)

// String returns the phase's lowercase name.
func (p Phase) String() string {
	switch p {
	case PhaseCreated:
		return "created"
	case PhaseBuilding:
		return "building"
	case PhaseCaughtUp:
		return "caught_up"
	case PhasePromoted:
		return "promoted"
	case PhaseRolledBack:
		return "rolled_back"
	case PhaseAbandoned:
		return "abandoned"
	case PhaseRetired:
		return "retired"
	default:
		return "unknown(" + strconv.Itoa(int(p)) + ")"
	}
}

// State is the fold of a rebuild aggregate's events: the rebuild's
// identity, targets, current phase, and the datapoints with audit value.
type State struct {
	// ID is the rebuild aggregate's typed ID, under StreamType.
	ID typeid.ID

	// Name is the projection being rebuilt, e.g. "orders".
	Name string

	// Next is the version being built.
	Next projection.ID

	// Previous is the version that was live when the rebuild was created; it
	// is the rollback target.
	Previous projection.ID

	// Phase is the rebuild's position in its lifecycle.
	Phase Phase

	// Reason records why this rebuild exists.
	Reason string

	// CreatedAt is when the rebuild was created.
	CreatedAt time.Time

	// CaughtUpAt is when the next version first drained to the head.
	CaughtUpAt time.Time

	// CaughtUpPos is the global position the next version had reached when it
	// first caught up: one datapoint with audit value, not progress telemetry.
	CaughtUpPos int64

	// PromotedAt is when reads were cut over to the next version.
	PromotedAt time.Time

	// AbandonCause records why the rebuild was abandoned, when it was.
	AbandonCause string
}

// NewState is the estoria.StateFactory for rebuild aggregates.
func NewState(id uuid.UUID) State {
	return State{ID: typeid.New(StreamType, id)}
}
