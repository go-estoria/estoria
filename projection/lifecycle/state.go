// Package lifecycle models each named projection's lifecycle — the version
// serving reads, the version numbers ever allocated, and the single rebuild
// attempt allowed in flight — as an ordinary estoria aggregate, one per
// projection name.
//
// The name is the arbitration domain: every consequential transition
// (admitting a rebuild, promoting, rolling back, retiring) is an event
// appended to the name's stream under optimistic concurrency, so competing
// decisions about one projection conflict at the same stream and exactly one
// wins. A rebuild attempt is a child entity of the projection, identified by
// a correlation UUID that is never an address — handles are obtained by name.
//
// Version numbers are never reused: each rebuild targets the highest version
// ever allocated plus one, so the checkpoint and storage of an abandoned or
// rolled-back build belong to a permanently dead identity and cannot leak
// into any later build. Gaps in live version numbers are normal — the
// version is an allocation counter, not a count of successful rebuilds.
//
// Only decisions and durable facts are events: initiated, started, caught
// up, promoted, rolled back, abandoned, retiring, retired. Progress is not —
// the advancing checkpoint lives in a checkpointstore.Store, and liveness is
// inferred from checkpoint recency, because a crashed process appends
// nothing on its way down. The test for what belongs in the stream: would a
// postmortem cite it?
package lifecycle

import (
	"strconv"
	"time"

	"github.com/go-estoria/estoria/internal/reservedstream"
	"github.com/go-estoria/estoria/projection"
	"github.com/gofrs/uuid/v5"
)

// StreamType is the stream type under which projection lifecycle aggregates
// are stored. It uses the reserved "estoria." namespace, identifying
// lifecycle streams as library infrastructure wherever they interleave with
// domain streams.
const StreamType = reservedstream.ProjectionStreamType

// streamNamespace is the fixed UUIDv5 namespace from which lifecycle stream
// UUIDs are derived. It must never change: the derivation is the address of
// every projection's lifecycle stream in every store.
//
//nolint:gochecknoglobals // A fixed constant of the addressing scheme; Go cannot declare UUID constants.
var streamNamespace = uuid.Must(uuid.FromString("77572c00-7977-4ff6-a769-fc84e1523857"))

// StreamUUID returns the UUID of the named projection's lifecycle stream,
// derived deterministically from the name. The name is the address: no
// lookup or index resolves it, and two processes creating the same
// projection's lifecycle concurrently land on the same stream, where
// optimistic concurrency arbitrates them.
func StreamUUID(name string) uuid.UUID {
	return uuid.NewV5(streamNamespace, name)
}

// A Phase is a rebuild attempt's position in its lifecycle. Transitions are
// recorded by the domain events in this package; which transitions are legal
// is the command side's concern, not the fold's.
type Phase int

const (
	// PhaseNone: no rebuild is in flight. It is the zero AttemptState's phase.
	PhaseNone Phase = iota

	// PhaseCreated: the rebuild is admitted and its target version number
	// allocated; no processor has started.
	PhaseCreated

	// PhaseBuilding: a processor is (or was last known to be) replaying
	// history into the target version.
	PhaseBuilding

	// PhaseCaughtUp: the target version has drained to the head of the event
	// sequence and is eligible for promotion.
	PhaseCaughtUp

	// PhasePromoted: the target version serves reads; the previous version is
	// retained as the rollback target.
	PhasePromoted

	// PhaseRetiring: retirement of the previous version has started,
	// forfeiting the rollback target. Rolling back is illegal from here.
	PhaseRetiring
)

// String returns the phase's lowercase name.
func (p Phase) String() string {
	switch p {
	case PhaseNone:
		return "none"
	case PhaseCreated:
		return "created"
	case PhaseBuilding:
		return "building"
	case PhaseCaughtUp:
		return "caught_up"
	case PhasePromoted:
		return "promoted"
	case PhaseRetiring:
		return "retiring"
	default:
		return "unknown(" + strconv.Itoa(int(p)) + ")"
	}
}

// State is the fold of a projection lifecycle stream: which version serves
// reads, how many version numbers have ever been allocated, and the rebuild
// attempt in flight, if any. One aggregate exists per projection name, and it
// is the sole arbiter of that name's lifecycle.
type State struct {
	// Name is the projection whose lifecycle this is, e.g. "orders".
	Name string

	// Live is the version serving reads. It is zero until a first promotion.
	Live projection.ID

	// Allocated is the highest version number ever allocated to a rebuild of
	// this projection — not the live version. After v3 is rolled back to v2,
	// Live is v2 while Allocated remains 3, and the next rebuild targets v4:
	// version numbers are never reused.
	Allocated int

	// Attempt is the rebuild in flight, zero when there is none. Terminal
	// transitions — rolled back, abandoned, retired — vacate it; the stream
	// is the audit record of past attempts.
	Attempt AttemptState
}

// AttemptState is an in-flight rebuild attempt: a child entity of the
// projection, not a consistency boundary of its own.
type AttemptState struct {
	// ID correlates the attempt's transitions and its builder. It is not an
	// address — the projection name is.
	ID uuid.UUID

	// Target is the version being built.
	Target projection.ID

	// Previous is the version that was live when the rebuild was initiated:
	// the rollback target, and what retirement removes. Zero for a
	// projection that had never been live.
	Previous projection.ID

	// Phase is the attempt's position in its lifecycle.
	Phase Phase

	// Reason records why this rebuild exists.
	Reason string

	// InitiatedAt is when the rebuild was admitted.
	InitiatedAt time.Time

	// CaughtUpAt is when the target version first drained to the head.
	CaughtUpAt time.Time

	// CaughtUpPos is the global position the target version had reached when
	// it first caught up: one datapoint with audit value, not progress
	// telemetry.
	CaughtUpPos int64

	// PromotedAt is when reads were cut over to the target version.
	PromotedAt time.Time

	// RetiringAt is when retirement of the previous version started.
	RetiringAt time.Time
}

// NewState is the estoria.StateFactory for projection lifecycle aggregates.
// State carries no copy of the stream UUID: the projection name, recorded by
// the stream's first event, is the identity that matters.
func NewState(uuid.UUID) State { return State{} }
