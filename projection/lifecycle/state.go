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
// Only decisions and durable facts are events: initiated, claimed, started,
// caught up, promoted, rolled back, abandoned, retiring, retired. Progress is not —
// the advancing checkpoint lives in a checkpointstore.Store, and liveness is
// inferred from checkpoint recency, because a crashed process appends
// nothing on its way down. The test for what belongs in the stream: would a
// postmortem cite it?
package lifecycle

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/go-estoria/estoria"
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

	// CutoverRevision counts this projection's recorded cutovers — Promoted
	// and RolledBack events — monotonically: 1-based from the first
	// promotion, zero when reads have never been cut over. The revision is a
	// domain fact, incremented under the same stream CAS that arbitrates the
	// transition, so setters and retirement witnesses compare the exact
	// (Live, CutoverRevision) pair rather than inferring order from version
	// identity, which rollback makes non-monotonic.
	CutoverRevision int64

	// Allocated is the highest version number ever allocated to a rebuild of
	// this projection — not the live version. After v3 is rolled back to v2,
	// Live is v2 while Allocated remains 3, and the next rebuild targets v4:
	// version numbers are never reused.
	Allocated int

	// Attempt is the rebuild in flight, zero when there is none. Terminal
	// transitions — rolled back, abandoned, retired — vacate it; the stream
	// is the audit record of past attempts.
	Attempt AttemptState

	// RetirementPolicy is the durable witness policy governing retirements,
	// zero until the first audited RetirementPolicySet.
	RetirementPolicy RetirementPolicy

	// InvalidReason records the first fold inconsistency, permanently; empty
	// means none was observed. Final-state shape cannot prove historical
	// consistency — a malformed event can assign both sides of any equality
	// a validator would check — so the fold marks the state at the moment
	// the inconsistency is observed, and no later event clears the mark.
	// validate rejects a marked state. Exported so state codecs persist it:
	// State re-enters the aggregate through snapshots, and a snapshot round
	// trip must re-arm the refusal rather than launder a poisoned fold back
	// to valid.
	InvalidReason string
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

	// Runner identifies the runner that most recently claimed the attempt —
	// the process presumed to be building it. Claims are cooperative
	// supersession: a superseded runner observes its displacement and winds
	// itself down; nothing fences its data-plane writes, version isolation
	// contains them.
	Runner uuid.UUID

	// InitiatedAt is when the rebuild was admitted.
	InitiatedAt time.Time

	// ClaimedAt is when the attempt was most recently claimed.
	ClaimedAt time.Time

	// CaughtUpAt is when the target version's latest certified drain reached
	// the head; the first observation lives in event history.
	CaughtUpAt time.Time

	// CaughtUpPos is the global position of the latest certified drain: one
	// datapoint with audit value, not progress telemetry. Re-certification
	// overwrites it; the first observation lives in event history.
	CaughtUpPos int64

	// PromotedAt is when reads were cut over to the target version.
	PromotedAt time.Time

	// RetiringAt is when retirement of the previous version started.
	RetiringAt time.Time

	// RetiringWitnesses is the witness membership the retirement reservation
	// captured: a retry from PhaseRetiring resolves and re-attests exactly
	// these IDs, never current process configuration. Empty for an
	// unwitnessed or overridden reservation, and outside PhaseRetiring.
	RetiringWitnesses []string
}

// Vacant reports whether no attempt is in flight. AttemptState carries a
// slice, so the zero comparison lives here; TestAttemptStateVacant sweeps
// the fields reflectively so a new field cannot silently escape it.
func (a AttemptState) Vacant() bool {
	return a.ID == uuid.Nil &&
		a.Target == (projection.ID{}) &&
		a.Previous == (projection.ID{}) &&
		a.Phase == PhaseNone &&
		a.Reason == "" &&
		a.Runner == uuid.Nil &&
		a.InitiatedAt.IsZero() &&
		a.ClaimedAt.IsZero() &&
		a.CaughtUpAt.IsZero() &&
		a.CaughtUpPos == 0 &&
		a.PromotedAt.IsZero() &&
		a.RetiringAt.IsZero() &&
		len(a.RetiringWitnesses) == 0
}

// clone returns a State sharing no mutable memory with the receiver: the
// slice-typed fields are deep-copied, so writing through a returned copy
// cannot alter the state the fold owns. Every State that leaves the package
// passes through here — retirement resolves its witnesses from the folded
// policy and captured membership, so an aliased copy would let a caller
// amend retirement authority without an event. TestStateClone sweeps State's
// reference fields reflectively so a new one cannot silently escape.
func (s State) clone() State {
	s.Attempt.RetiringWitnesses = slices.Clone(s.Attempt.RetiringWitnesses)
	s.RetirementPolicy.Witnesses = slices.Clone(s.RetirementPolicy.Witnesses)

	return s
}

// NewState is the estoria.StateFactory for projection lifecycle aggregates.
// State carries no copy of the stream UUID: the projection name, recorded by
// the stream's first event, is the identity that matters.
func NewState(uuid.UUID) State { return State{} }

// poison marks the state permanently invalid with the first observed
// inconsistency, reporting every observation via the ambient logger. The
// event that carried the inconsistency still applies — a persisted event won
// its append-time arbitration, and the stream is history — but no command
// acts on a poisoned fold, and no later event clears the mark: a malformed
// event can assign both sides of any equality a final-state validator would
// check, so the moment of observation is the only place the evidence exists.
func (s State) poison(msg string, args ...any) State {
	estoria.GetLogger().WithGroup("lifecycle").Warn(msg, args...)

	if s.InvalidReason == "" {
		s.InvalidReason = msg
	}

	return s
}

// ValidateSnapshotState implements aggregatestore.SnapshotStateValidator:
// it rejects snapshot payloads no legitimate fold could have produced, so
// the snapshotting store falls back to full hydration instead of installing
// them. Installed, a reset payload would seed the tail's fold with
// fabricated state — the events after the snapshot would fold as a fresh
// history and hand out already-used version numbers. A poisoned payload is
// accepted: it is valid testimony of a poisoned fold, and commands refuse
// it through validation as usual.
func (s State) ValidateSnapshotState() error {
	if s.InvalidReason != "" {
		return nil
	}

	if err := s.validate(); err != nil {
		return err
	}

	// Any fold a snapshot could legitimately capture has applied at least
	// one event, and a clean first event always records the name.
	if s.Name == "" {
		return errors.New("a lifecycle snapshot cannot hold uninitialized state")
	}

	return nil
}

// validate reports whether the folded state satisfies the package's
// structural invariants. The fold is total — a persisted event always applies
// — so only tampering or a bug produces a state that violates them; commands
// check before acting, because acting on such a state can destroy the wrong
// version's storage. The reserved stream namespace is a guardrail, not a
// trust boundary, so infrastructure state is rejected here rather than
// assumed well-formed.
func (s State) validate() error {
	if s.InvalidReason != "" {
		return fmt.Errorf("the fold observed an inconsistent event: %s", s.InvalidReason)
	}

	if s.Name == "" {
		if s.Allocated != 0 || s.Live != (projection.ID{}) || s.CutoverRevision != 0 || !s.Attempt.Vacant() || !s.RetirementPolicy.zero() {
			return errors.New("state records no projection name but is not empty")
		}

		return nil
	}

	if err := s.RetirementPolicy.validate(); err != nil {
		return fmt.Errorf("retirement policy: %w", err)
	}

	if err := (projection.ID{Name: s.Name, Version: 1}).Validate(); err != nil {
		return fmt.Errorf("projection name %q: %w", s.Name, err)
	}

	// The admission that records the name allocates at least version 1, so
	// a named fold always carries a positive high-water mark. A named state
	// claiming none can only come from tampered or truncated persistence —
	// a snapshot resetting the fold — and trusting it would hand the next
	// admission an already-used version number.
	if s.Allocated < 1 {
		return errors.New("named state records no allocated versions")
	}

	if s.Live != (projection.ID{}) {
		if err := s.Live.Validate(); err != nil {
			return fmt.Errorf("live version %s: %w", s.Live, err)
		}

		switch {
		case s.Live.Name != s.Name:
			return fmt.Errorf("live version %s does not belong to projection %q", s.Live, s.Name)
		case s.Live.Version > s.Allocated:
			return fmt.Errorf("live version %s exceeds the allocation high-water mark %d", s.Live, s.Allocated)
		}
	}

	// Every cutover flips Live to a non-zero version — a first rebuild has no
	// rollback target, so no legitimate history returns Live to zero — and
	// the first promotion records revision 1: a live version and a positive
	// revision exist together or not at all.
	switch {
	case s.Live == (projection.ID{}) && s.CutoverRevision != 0:
		return fmt.Errorf("cutover revision %d recorded with no live version", s.CutoverRevision)
	case s.Live != (projection.ID{}) && s.CutoverRevision < 1:
		return errors.New("live version recorded with no cutover revision")
	}

	// The cutover history before the in-flight attempt is a rest state —
	// every prior attempt concluded, the slot vacant — and must itself be
	// reachable. The attempt's own allocation has completed nothing: it has
	// recorded no cutover before promotion, and exactly its promotion over
	// the rest state in the promoted and retiring phases, its rollback
	// having vacated the slot — so that promotion's previous version and
	// revision predecessor are the rest state to check.
	live := s.Live
	recorded := s.CutoverRevision
	completed := int64(s.Allocated)

	if !s.Attempt.Vacant() {
		completed--

		if s.Attempt.Phase == PhasePromoted || s.Attempt.Phase == PhaseRetiring {
			live = s.Attempt.Previous
			recorded--
		}
	}

	if err := unreachableRest(live, recorded, completed); err != nil {
		if s.Attempt.Vacant() {
			return fmt.Errorf("cutover history: %w", err)
		}

		return fmt.Errorf("cutover history before the attempt on %s: %w", s.Attempt.Target, err)
	}

	return s.validateAttempt()
}

// unreachableRest reports why no cutover history over the given completed
// allocations can come to rest — every attempt concluded, the slot vacant —
// with the given version live at the given revision, or nil if one can.
// Three structural facts bound rest states. Pairing: a version is live
// exactly when a cutover has been recorded. Counts: each allocation promotes
// at most once, version numbers are never reused, and only a promotion that
// retained a previous version rolls back — the first retained nothing — so
// k allocations record at most 2k-1 cutovers. Shape: version 1 is only ever
// promoted first, over nothing at revision 1, and history returns to it only
// in promote-rollback pairs, so it rests at odd revisions alone; the 2k-1
// maximum spends the unpaired promotion plus every pair resting at version
// 1, so any later version rests at 2k-2 or below. The division forms avoid
// overflowing the doubled bounds.
func unreachableRest(live projection.ID, recorded, completed int64) error {
	switch {
	case live == (projection.ID{}) && recorded != 0:
		return fmt.Errorf("%d cutovers recorded with no version left live", recorded)
	case live != (projection.ID{}) && recorded < 1:
		return fmt.Errorf("%s live with no cutover recorded", live)
	case recorded > 0 && recorded/2 >= completed:
		return fmt.Errorf("revision %d cannot arise from %d completed allocations", recorded, completed)
	case live.Version == 1 && recorded%2 == 0:
		return fmt.Errorf("revision %d cannot rest at %s: history returns to the first promotion only in promote-rollback pairs", recorded, live)
	case live.Version > 1 && (recorded-1)/2 >= completed-1:
		return fmt.Errorf("revision %d cannot rest at %s: only version 1 rests at the %d-allocation maximum", recorded, live, completed)
	}

	return nil
}

// validateAttempt checks the in-flight attempt against the projection-level
// facts: identity, target/allocation relation, lineage, and phase-dependent
// consistency between the live version and the attempt's versions.
func (s State) validateAttempt() error {
	a := s.Attempt

	switch a.Phase {
	case PhaseNone:
		if !a.Vacant() {
			return errors.New("attempt slot records no phase but is not vacant")
		}

		return nil
	case PhaseCreated, PhaseBuilding, PhaseCaughtUp, PhasePromoted, PhaseRetiring:
	default:
		return fmt.Errorf("unknown attempt phase %s", a.Phase)
	}

	if a.Phase != PhaseRetiring && len(a.RetiringWitnesses) != 0 {
		return errors.New("captured retirement witnesses outside the retiring phase")
	}

	if err := invalidWitnessSet(a.RetiringWitnesses); err != nil {
		return fmt.Errorf("captured retirement witnesses: %w", err)
	}

	// A capture testifies that a witnessed policy required exactly that set
	// when the retirement was reserved. Generations only rise, so a capture
	// with no policy ever recorded is unreachable, and under a sole recorded
	// generation the active policy is the reservation's: it must be witnessed
	// with the captured membership.
	if len(a.RetiringWitnesses) != 0 {
		switch {
		case s.RetirementPolicy.Generation == 0:
			return errors.New("captured retirement witnesses with no recorded policy")
		case s.RetirementPolicy.Generation == 1 && s.RetirementPolicy.Unwitnessed:
			return errors.New("captured retirement witnesses under a sole unwitnessed policy generation")
		case s.RetirementPolicy.Generation == 1 && !slices.Equal(a.RetiringWitnesses, s.RetirementPolicy.Witnesses):
			return errors.New("captured retirement witnesses diverge from the sole policy generation")
		}
	}

	if a.ID.IsNil() {
		return errors.New("in-flight attempt has no ID")
	}

	if err := a.Target.Validate(); err != nil {
		return fmt.Errorf("attempt target %s: %w", a.Target, err)
	}

	switch {
	case a.Target.Name != s.Name:
		return fmt.Errorf("attempt target %s does not belong to projection %q", a.Target, s.Name)
	case a.Target.Version != s.Allocated:
		return fmt.Errorf("attempt target %s is not the latest allocation %d", a.Target, s.Allocated)
	}

	// A runner claim precedes every processor start, so any attempt past the
	// created phase records the runner that last claimed it.
	if a.Phase != PhaseCreated && a.Runner.IsNil() {
		return fmt.Errorf("attempt in phase %s records no claimed runner", a.Phase)
	}

	if a.Previous != (projection.ID{}) {
		if err := a.Previous.Validate(); err != nil {
			return fmt.Errorf("attempt previous %s: %w", a.Previous, err)
		}

		switch {
		case a.Previous.Name != s.Name:
			return fmt.Errorf("attempt previous %s does not belong to projection %q", a.Previous, s.Name)
		case a.Previous.Version >= a.Target.Version:
			return fmt.Errorf("attempt previous %s is not older than the target %s", a.Previous, a.Target)
		}
	}

	switch a.Phase {
	case PhaseCreated, PhaseBuilding, PhaseCaughtUp:
		if s.Live != a.Previous {
			return fmt.Errorf("live version %s diverges from the attempt's previous version %s", s.Live, a.Previous)
		}
	case PhasePromoted, PhaseRetiring:
		if s.Live != a.Target {
			return fmt.Errorf("live version %s diverges from the promoted target %s", s.Live, a.Target)
		}

		if a.Phase == PhaseRetiring && a.Previous == (projection.ID{}) {
			return errors.New("retiring with no previous version recorded")
		}
	case PhaseNone:
		// Vacant slots returned above; enumerated for exhaustiveness.
	}

	return nil
}
